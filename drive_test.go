package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 远程操作被控端（自动化测试用）：
//
//	RDESK_E2E=ws://100.x.x.x:21130 RDESK_ID=… RDESK_PW=… RDESK_SCRIPT=脚本文件 go test -count=1 -run TestDrive -v .
//
// 脚本每行一条命令，坐标是被控端屏幕像素：
//
//	shot 文件.png | wait 毫秒 | move x y | click x y | rclick x y | dclick x y | drag x1 y1 x2 y2
//	wheel dy | key Code | combo ControlLeft+KeyC | text 文字 | cursor 文件.png
func TestDrive(t *testing.T) {
	base, script := os.Getenv("RDESK_E2E"), os.Getenv("RDESK_SCRIPT")
	if base == "" || script == "" {
		t.Skip("RDESK_E2E / RDESK_SCRIPT 未设置")
	}
	c, _, err := websocket.DefaultDialer.Dial(base+"/ws/view?id="+envOr("RDESK_ID", "123456789"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b, _ := newBox(envOr("RDESK_PW", "testpass1"), envOr("RDESK_ID", "123456789"))
	var wmu sync.Mutex
	send := func(p []byte) {
		wmu.Lock()
		c.WriteMessage(websocket.BinaryMessage, b.seal(p, aadV2H))
		wmu.Unlock()
	}

	var mu sync.Mutex
	var canvas *image.RGBA
	var frameN, shapeN int
	var lastShape []byte
	ready := make(chan struct{})
	go func() {
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.TextMessage {
				if string(data) == `{"t":"paired"}` {
					send(append([]byte{msgHello}, []byte(`{"q":80,"maxw":1920,"fps":15}`)...))
				}
				continue
			}
			p, err := b.open(data, aadH2V)
			if err != nil || len(p) == 0 {
				continue
			}
			switch p[0] {
			case msgHelloOK:
				t.Logf("握手成功: %s", p[1:])
				close(ready)
			case msgFrame: // [type][seq u32][W u16][H u16][n] + n×(x y w h u16, len u32, jpeg)
				W, H, n := int(binary.BigEndian.Uint16(p[5:])), int(binary.BigEndian.Uint16(p[7:])), int(p[9])
				mu.Lock()
				if canvas == nil || canvas.Rect.Dx() != W || canvas.Rect.Dy() != H {
					canvas = image.NewRGBA(image.Rect(0, 0, W, H))
				}
				o := 10
				for i := 0; i < n && o+12 <= len(p); i++ {
					x, y := int(binary.BigEndian.Uint16(p[o:])), int(binary.BigEndian.Uint16(p[o+2:]))
					l := int(binary.BigEndian.Uint32(p[o+8:]))
					o += 12
					if img, err := jpeg.Decode(bytes.NewReader(p[o : o+l])); err == nil {
						draw.Draw(canvas, img.Bounds().Add(image.Pt(x, y)), img, image.Point{}, draw.Src)
					}
					o += l
				}
				frameN++
				mu.Unlock()
				send([]byte{msgAck})
			case msgCursorShape:
				mu.Lock()
				shapeN++
				lastShape = append([]byte(nil), p[9:]...)
				mu.Unlock()
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("连不上被控端")
	}

	// 屏幕像素 → 0...65535
	xy := func(x, y int) (uint16, uint16) {
		mu.Lock()
		defer mu.Unlock()
		if canvas == nil {
			return 0, 0
		}
		w, h := canvas.Rect.Dx(), canvas.Rect.Dy()
		return uint16(x * 65535 / max(w-1, 1)), uint16(y * 65535 / max(h-1, 1))
	}
	move := func(x, y int) {
		X, Y := xy(x, y)
		p := []byte{msgMove, 0, 0, 0, 0}
		binary.BigEndian.PutUint16(p[1:], X)
		binary.BigEndian.PutUint16(p[3:], Y)
		send(p)
	}
	button := func(btn byte, down bool, x, y int) {
		X, Y := xy(x, y)
		p := []byte{msgButton, btn, 0, 0, 0, 0, 0}
		if down {
			p[2] = 1
		}
		binary.BigEndian.PutUint16(p[3:], X)
		binary.BigEndian.PutUint16(p[5:], Y)
		send(p)
	}
	key := func(code string, down bool) {
		p := []byte{msgKey, 0}
		if down {
			p[1] = 1
		}
		send(append(p, code...))
	}
	// 第一帧到了再开始
	for i := 0; i < 100; i++ {
		mu.Lock()
		got := canvas != nil
		mu.Unlock()
		if got {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	f, err := os.Open(script)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cmd, rest, _ := strings.Cut(line, " ")
		a := strings.Fields(rest)
		n := func(i int) int { v, _ := strconv.Atoi(a[i]); return v }
		switch cmd {
		case "wait":
			time.Sleep(time.Duration(n(0)) * time.Millisecond)
		case "shot": // 等一帧新画面再存
			mu.Lock()
			start := frameN
			mu.Unlock()
			for i := 0; i < 20; i++ {
				mu.Lock()
				fresh := frameN > start
				mu.Unlock()
				if fresh {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			mu.Lock()
			if canvas != nil {
				out, _ := os.Create(a[0])
				png.Encode(out, canvas)
				out.Close()
			}
			mu.Unlock()
			t.Logf("截图 %s", a[0])
		case "move":
			move(n(0), n(1))
		case "click", "rclick":
			btn := byte(0)
			if cmd == "rclick" {
				btn = 2
			}
			move(n(0), n(1))
			time.Sleep(30 * time.Millisecond)
			button(btn, true, n(0), n(1))
			time.Sleep(40 * time.Millisecond)
			button(btn, false, n(0), n(1))
		case "dclick":
			move(n(0), n(1))
			for i := 0; i < 2; i++ {
				button(0, true, n(0), n(1))
				time.Sleep(30 * time.Millisecond)
				button(0, false, n(0), n(1))
				time.Sleep(60 * time.Millisecond)
			}
		case "drag":
			move(n(0), n(1))
			button(0, true, n(0), n(1))
			for s := 1; s <= 10; s++ {
				time.Sleep(30 * time.Millisecond)
				move(n(0)+(n(2)-n(0))*s/10, n(1)+(n(3)-n(1))*s/10)
			}
			button(0, false, n(2), n(3))
		case "wheel":
			p := []byte{msgWheel, 0, 0, 0, 0}
			binary.BigEndian.PutUint16(p[3:], uint16(int16(n(0))))
			send(p)
		case "key":
			key(a[0], true)
			time.Sleep(30 * time.Millisecond)
			key(a[0], false)
		case "combo":
			codes := strings.Split(a[0], "+")
			for _, k := range codes {
				key(k, true)
				time.Sleep(30 * time.Millisecond)
			}
			for i := len(codes) - 1; i >= 0; i-- {
				key(codes[i], false)
				time.Sleep(30 * time.Millisecond)
			}
		case "text":
			send(append([]byte{msgText}, rest...))
		case "cursor":
			mu.Lock()
			t.Logf("光标样子消息 %d 条", shapeN)
			if len(lastShape) > 0 {
				os.WriteFile(a[0], lastShape, 0o644)
			}
			mu.Unlock()
		default:
			t.Fatalf("不认识的命令: %s", line)
		}
	}
	mu.Lock()
	t.Logf("共收到 %d 帧", frameN)
	mu.Unlock()
}
