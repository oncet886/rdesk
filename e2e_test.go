package main

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kbinani/screenshot"
)

// 端到端自测：RDESK_E2E=ws://127.0.0.1:18080 go test -run E2E
// 需要一个正在运行的被控端（ID 123456789 / 密码 testpass1），只收画面，不动键鼠
func TestE2E(t *testing.T) {
	base := os.Getenv("RDESK_E2E")
	if base == "" {
		t.Skip("RDESK_E2E 未设置")
	}
	c, _, err := websocket.DefaultDialer.Dial(base+"/ws/view?id="+envOr("RDESK_ID", "123456789"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b, _ := newBox(envOr("RDESK_PW", "testpass1"), envOr("RDESK_ID", "123456789"))
	send := func(p []byte) { c.WriteMessage(websocket.BinaryMessage, b.seal(p, aadV2H)) }
	frames, start := 0, time.Time{}
	var out, aout *os.File
	audioN := 0
	if f := os.Getenv("RDESK_ADUMP"); f != "" {
		aout, _ = os.Create(f)
		defer aout.Close()
	}
	if f := os.Getenv("RDESK_DUMP"); f != "" {
		out, _ = os.Create(f)
		defer out.Close()
	}
	var bytesIn int
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		c.SetReadDeadline(deadline)
		mt, data, err := c.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.TextMessage {
			if string(data) == `{"t":"paired"}` {
				hello := []byte(`{}`)
				if os.Getenv("RDESK_H264") != "" {
					hello = []byte(`{"h264":true,"q":65,"maxw":1920,"fps":30` + os.Getenv("RDESK_HELLO_EXTRA") + `}`)
				}
				send(append([]byte{msgHello}, hello...))
			}
			continue
		}
		p, err := b.open(data, aadH2V)
		if err != nil {
			t.Fatalf("解密失败: %v", err)
		}
		switch p[0] {
		case msgHelloOK:
			t.Logf("握手成功: %s", p[1:])
			start = time.Now()
		case msgAudio:
			audioN++
			if aout != nil {
				binary.Write(aout, binary.BigEndian, uint32(len(p)-1))
				aout.Write(p[1:])
			}
		case msgVideo:
			if frames == 0 && p[9] != 1 {
				t.Fatal("第一帧不是关键帧")
			}
			if out != nil {
				out.Write(p[10:])
			}
			frames++
			bytesIn += len(p)
			send(append([]byte{msgAck}, p[1:5]...)) // 带序号确认
		case msgFrame:
			frames++
			bytesIn += len(p)
			send([]byte{msgAck})
		}
	}
	el := time.Since(start).Seconds()
	t.Logf("收到 %d 帧 / %.1fs = %.1f fps, 平均 %d KB/帧", frames, el, float64(frames)/el, bytesIn/max(frames, 1)/1024)
	t.Logf("声音包 %d 个（%.1f 个/秒，每包 1024 采样 ≈ 46.9 个/秒）", audioN, float64(audioN)/el)
	bd := screenshot.GetDisplayBounds(0)
	t.Logf("屏幕 %v", bd)
	if frames == 0 {
		t.Fatal("没有收到画面")
	}
}

func BenchmarkCapture(b *testing.B) {
	bd := screenshot.GetDisplayBounds(0)
	for i := 0; i < b.N; i++ {
		screenshot.CaptureRect(bd)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func TestCursorShape(t *testing.T) {
	png, w, h, hx, hy, ok := cursorShape()
	if !ok {
		t.Skip("当前平台取不到光标样子")
	}
	os.WriteFile(os.Getenv("TMPDIR")+"/cursor.png", png, 0o644)
	t.Logf("光标 PNG %d 字节, 尺寸 %.1fx%.1f 点, 热点 (%.1f,%.1f)", len(png), w, h, hx, hy)
	if _, _, _, _, _, again := cursorShape(); again {
		t.Fatal("没变化时不应重复返回")
	}
}

// 同一台手机重连会顶掉旧连接，且不会给被控端发 close（否则新会话也会被关）
func TestRelayReplaceSameIP(t *testing.T) {
	srv := httptest.NewServer(newRelay("").handler())
	defer srv.Close()
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")
	hostC, _, err := websocket.DefaultDialer.Dial(ws+"/ws/host?id=123456789", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hostC.Close()
	time.Sleep(100 * time.Millisecond)
	read := func(c *websocket.Conn) string {
		c.SetReadDeadline(time.Now().Add(time.Second))
		_, b, err := c.ReadMessage()
		if err != nil {
			return "ERR"
		}
		return string(b)
	}
	a, _, _ := websocket.DefaultDialer.Dial(ws+"/ws/view?id=123456789", nil)
	if m := read(a); m != `{"t":"paired"}` {
		t.Fatalf("A 应配对成功: %s", m)
	}
	read(hostC) // open
	b, _, _ := websocket.DefaultDialer.Dial(ws+"/ws/view?id=123456789", nil)
	if m := read(b); m != `{"t":"paired"}` {
		t.Fatalf("同 IP 的 B 应顶掉 A 并配对成功: %s", m)
	}
	if m := read(hostC); !strings.Contains(m, `"open"`) {
		t.Fatalf("被控端应收到新的 open: %s", m)
	}
	if m := read(a); m != "ERR" {
		t.Fatalf("A 应被关闭: %s", m)
	}
	b.WriteMessage(websocket.BinaryMessage, []byte("from-b"))
	if m := read(hostC); m != "from-b" {
		t.Fatalf("旧连接被顶掉后不应给被控端发 close，被控端先收到: %s", m)
	}
	b.Close()
	if m := read(hostC); !strings.Contains(m, `"close"`) {
		t.Fatalf("B 断开时被控端应收到 close: %s", m)
	}
}

// 生成给安卓端对照用的测试向量
func TestVectors(t *testing.T) {
	if os.Getenv("RDESK_VEC") == "" {
		t.Skip()
	}
	key, _ := pbkdf2.Key(sha256.New, "测试pw1", []byte("rdesk:123456789"), kdfIter, 32)
	b, _ := newBox("测试pw1", "123456789")
	ct := b.seal([]byte("hello rdesk"), aadH2V)
	fmt.Printf("KEY=%x\nCT=%x\n", key, ct)
}

// 自适应码率：大帧传得久不算堵；真正排队（往返持续变长）才降码率
func TestABR(t *testing.T) {
	now := time.Unix(0, 0)
	fc := &flowCtl{now: func() time.Time { return now }}
	a := &abrCtl{fc: fc}
	a.reset(6000)
	seq := uint32(0)
	// 发一帧：链路 8Mbps、基础往返 90ms、额外排队 extra
	frame := func(bytes int, extra time.Duration) {
		seq++
		fc.sent(seq, bytes)
		tx := time.Duration(float64(bytes*8) / 8e6 * float64(time.Second))
		now = now.Add(90*time.Millisecond + tx + extra)
		fc.ack(seq, now)
		now = now.Add(50 * time.Millisecond)
		a.tick()
	}
	// 1. 正常：小帧大帧混合（有 120KB 的大帧），网络不堵 → 码率应该往上涨，不能降
	for i := 0; i < 80; i++ {
		b := 3000
		if i%5 == 0 {
			b = 120000
		}
		frame(b, 0)
	}
	if a.kbps <= 3000 {
		t.Fatalf("网络正常时码率没涨上去: %d", a.kbps)
	}
	t.Logf("正常网络（含大帧）码率涨到 %d kbps", a.kbps)
	high := a.kbps
	// 2. 真堵了：每帧额外排队 300ms → 必须降
	for i := 0; i < 20; i++ {
		frame(5000, 300*time.Millisecond)
	}
	if a.kbps >= high {
		t.Fatalf("网络排队时没有降码率: %d", a.kbps)
	}
	t.Logf("排队 300ms 后码率降到 %d kbps", a.kbps)
	if a.kbps < minKbps {
		t.Fatalf("码率低于下限: %d", a.kbps)
	}
}
