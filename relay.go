package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed web
var webFS embed.FS

// 中继（隧道）服务器：被控端和控制端都主动"连出去"到这里，
// 所以两边都不需要公网 IP、不需要开端口。服务器只按设备 ID 配对并原样转发密文。

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 << 10,
	WriteBufferSize: 64 << 10,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type wsConn struct {
	c  *websocket.Conn
	mu sync.Mutex
}

func (w *wsConn) write(mt int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.c.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return w.c.WriteMessage(mt, data)
}

func (w *wsConn) writeJSON(v any) error {
	b, _ := json.Marshal(v)
	return w.write(websocket.TextMessage, b)
}

type hostEntry struct {
	conn     *wsConn
	viewer   *wsConn
	viewerIP string
}

func ipOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

type relay struct {
	mu     sync.Mutex
	hosts  map[string]*hostEntry
	key    string // 可选：被控端注册需要的中继密钥，防止陌生人蹭你的服务器
	failed map[string][]time.Time
}

func newRelay(key string) *relay {
	return &relay{hosts: map[string]*hostEntry{}, key: key, failed: map[string][]time.Time{}}
}

func (r *relay) handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/ws/host", r.serveHost)
	mux.HandleFunc("/ws/view", r.serveView)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return mux
}

func keepAlive(w *wsConn, stop <-chan struct{}) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			w.mu.Lock()
			err := w.c.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			w.mu.Unlock()
			if err != nil {
				return
			}
		case <-stop:
			return
		}
	}
}

func setReadTimeout(c *websocket.Conn) {
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.SetPongHandler(func(string) error { c.SetReadDeadline(time.Now().Add(60 * time.Second)); return nil })
}

func (r *relay) serveHost(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get("id")
	if !validID(id) {
		http.Error(w, "bad id", 400)
		return
	}
	if r.key != "" && req.URL.Query().Get("key") != r.key {
		http.Error(w, "bad relay key", 403)
		return
	}
	c, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(64 << 20)
	hc := &wsConn{c: c}
	r.mu.Lock()
	if old, ok := r.hosts[id]; ok {
		// 同一 ID 重连：踢掉旧连接
		old.conn.c.Close()
		if old.viewer != nil {
			old.viewer.c.Close()
		}
	}
	he := &hostEntry{conn: hc}
	r.hosts[id] = he
	r.mu.Unlock()
	log.Printf(L("[relay] 被控端上线 id=%s from=%s"), id, clientIP(req))

	stop := make(chan struct{})
	go keepAlive(hc, stop)
	setReadTimeout(c)
	defer func() {
		close(stop)
		r.mu.Lock()
		if r.hosts[id] == he {
			delete(r.hosts, id)
			if he.viewer != nil {
				he.viewer.writeJSON(map[string]string{"t": "err", "m": "对方已下线"})
				he.viewer.c.Close()
			}
		}
		r.mu.Unlock()
		c.Close()
		log.Printf(L("[relay] 被控端下线 id=%s"), id)
	}()
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		r.mu.Lock()
		v := he.viewer
		r.mu.Unlock()
		if mt == websocket.TextMessage {
			var m struct{ T string }
			json.Unmarshal(data, &m)
			if m.T == "kick" && v != nil {
				v.c.Close()
			}
			continue
		}
		if v != nil {
			if err := v.write(websocket.BinaryMessage, data); err != nil {
				v.c.Close()
			}
		}
	}
}

func (r *relay) serveView(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get("id")
	c, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(4 << 20)
	vc := &wsConn{c: c}
	defer c.Close()
	ip := clientIP(req)

	r.mu.Lock()
	if r.tooManyFails(ip) {
		r.mu.Unlock()
		vc.writeJSON(map[string]string{"t": "err", "m": "尝试过于频繁，请稍后再试"})
		return
	}
	he, ok := r.hosts[id]
	if !ok {
		r.failed[ip] = append(r.failed[ip], time.Now())
		r.mu.Unlock()
		vc.writeJSON(map[string]string{"t": "err", "m": "设备不在线或 ID 错误"})
		return
	}
	if he.viewer != nil {
		if ipOnly(he.viewerIP) != ipOnly(ip) {
			r.mu.Unlock()
			vc.writeJSON(map[string]string{"t": "err", "m": "该设备正在被其他人控制"})
			return
		}
		// 同一台手机重连（旧连接还没断干净，比如网络切换时卡住）：直接顶掉旧的
		he.viewer.c.Close()
	}
	he.viewer = vc
	he.viewerIP = ip
	r.mu.Unlock()
	log.Printf(L("[relay] 控制端接入 id=%s from=%s"), id, ip)

	he.conn.writeJSON(map[string]string{"t": "open", "ip": ip})
	vc.writeJSON(map[string]string{"t": "paired"})

	stop := make(chan struct{})
	go keepAlive(vc, stop)
	setReadTimeout(c)
	defer func() {
		close(stop)
		r.mu.Lock()
		current := he.viewer == vc
		if current {
			he.viewer = nil
		}
		r.mu.Unlock()
		if current { // 被新连接顶掉的旧连接不通知被控端，免得把新会话也关了
			he.conn.writeJSON(map[string]string{"t": "close"})
		}
		log.Printf(L("[relay] 控制端断开 id=%s"), id)
	}()
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		if err := he.conn.write(websocket.BinaryMessage, data); err != nil {
			return
		}
	}
}

// 同一 IP 1 分钟内连错 ID 超过 20 次就暂时拒绝（防扫号）
func (r *relay) tooManyFails(ip string) bool {
	cut := time.Now().Add(-time.Minute)
	l := r.failed[ip][:0]
	for _, t := range r.failed[ip] {
		if t.After(cut) {
			l = append(l, t)
		}
	}
	r.failed[ip] = l
	return len(l) >= 20
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("Cf-Connecting-Ip"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return v
	}
	return r.RemoteAddr
}

func validID(id string) bool {
	if len(id) < 6 || len(id) > 32 {
		return false
	}
	for _, ch := range id {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}
