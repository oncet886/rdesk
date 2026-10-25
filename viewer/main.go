// RDesk 电脑控制端（Windows / Mac / Linux）：
// 在本机 127.0.0.1 起一个网页服务，把网页控制端（web/）用浏览器的「应用模式」打开成独立窗口；
// 网页里的 WebSocket 连到本机，这里原样转发给远程被控端。
//
// 为什么要本地转发：浏览器只在 https 或 127.0.0.1 这样的「安全环境」里提供硬件解码（WebCodecs）和加密接口，
// 而被控端是 ws://100.x.x.x 明文地址（内容本身已端到端加密）。画面、声音、键鼠的加解密都在网页里完成，
// 这个程序只搬运密文，看不到任何内容。
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed web
var webFS embed.FS

const version = "0.1.0"

var (
	token    string       // 本次运行的随机令牌：别的网页拿不到，也就借用不了这个转发
	lastSeen atomic.Int64 // 网页最后一次心跳（Unix 秒）
	active   atomic.Int32 // 正在转发的连接数
)

func main() {
	port := flag.Int("port", 21140, "本机端口（固定端口：浏览器按端口保存记住的设备，换端口就丢了）")
	noOpen := flag.Bool("no-open", false, "不自动打开窗口（调试用）")
	flag.Parse()
	b := make([]byte, 16)
	rand.Read(b)
	token = hex.EncodeToString(b)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil { // 端口被占（比如已经开着一个）：换随机端口
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			log.Fatal(err)
		}
	}
	origin := "http://" + ln.Addr().String()
	sub, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	static := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		static.ServeHTTP(w, r)
	})
	mux.HandleFunc("/alive", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		lastSeen.Store(time.Now().Unix())
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/proxy", func(w http.ResponseWriter, r *http.Request) { proxy(w, r, origin) })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)

	page := origin + "/?t=" + token
	log.Printf("RDesk 控制端 %s：%s", version, page)
	lastSeen.Store(time.Now().Unix())
	if !*noOpen {
		if err := openAppWindow(page); err != nil {
			log.Printf("打不开浏览器窗口：%v。请手动打开 %s", err, page)
		}
	}
	// 窗口关掉后（没有心跳、也没有连接）自动退出
	for {
		time.Sleep(5 * time.Second)
		if !*noOpen && active.Load() == 0 && time.Now().Unix()-lastSeen.Load() > 30 {
			log.Print("窗口已关闭，退出")
			os.Exit(0)
		}
	}
}

var up = websocket.Upgrader{ReadBufferSize: 64 << 10, WriteBufferSize: 64 << 10}

// /proxy?t=令牌&target=被控端地址&id=设备ID → 转发到 ws://被控端/ws/view?id=…
func proxy(w http.ResponseWriter, r *http.Request, origin string) {
	q := r.URL.Query()
	if q.Get("t") != token || r.Header.Get("Origin") != origin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	target, err := remoteURL(q.Get("target"), q.Get("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d := websocket.Dialer{HandshakeTimeout: 8 * time.Second, NetDialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext}
	remote, _, err := d.Dial(target, nil)
	if err != nil {
		http.Error(w, "连不上被控端："+err.Error(), http.StatusBadGateway)
		return
	}
	if tc, ok := remote.UnderlyingConn().(*net.TCPConn); ok {
		tc.SetNoDelay(true) // 键鼠小包立刻发出去
	}
	local, err := up.Upgrade(w, r, nil)
	if err != nil {
		remote.Close()
		return
	}
	active.Add(1)
	defer active.Add(-1)
	var once sync.Once
	done := make(chan struct{})
	stop := func() { once.Do(func() { close(done); local.Close(); remote.Close() }) }
	pipe := func(dst, src *websocket.Conn) {
		defer stop()
		for {
			mt, data, err := src.ReadMessage()
			if err != nil {
				return
			}
			if dst.WriteMessage(mt, data) != nil {
				return
			}
		}
	}
	go pipe(remote, local)
	go pipe(local, remote)
	<-done
}

// 用户填的地址 → ws://主机:端口/ws/view?id=…（规则和手机端一致：IP / *.ts.net / 带端口 = 直连 ws，默认端口 21130；其他域名 = wss）
func remoteURL(server, id string) (string, error) {
	s := strings.TrimSpace(server)
	if s == "" || id == "" {
		return "", fmt.Errorf("缺少地址或设备 ID")
	}
	if !strings.Contains(s, "://") {
		hostPart := strings.SplitN(s, "/", 2)[0]
		host := strings.SplitN(hostPart, ":", 2)[0]
		direct := net.ParseIP(host) != nil || strings.HasSuffix(host, ".ts.net") || strings.Contains(hostPart, ":")
		if direct {
			if !strings.Contains(hostPart, ":") {
				s = strings.Replace(s, hostPart, hostPart+":21130", 1)
			}
			s = "ws://" + s
		} else {
			s = "wss://" + s
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("地址格式不对")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("地址格式不对")
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/ws/view") + "/ws/view"
	u.RawQuery = url.Values{"id": {id}}.Encode()
	return u.String(), nil
}
