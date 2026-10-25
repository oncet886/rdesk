package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 连接诊断：被控端本机 http://127.0.0.1:21131 打开，实时看两端的帧率、延迟、带宽和 Tailscale 路径，并给出结论。
// 只监听 127.0.0.1，外部访问不到。

//go:embed diagui/index.html
var diagHTML []byte

const diagPort = 21131

type diagPoint struct {
	T        int64   `json:"t"`
	CapFPS   float64 `json:"cap"`   // 截屏帧率（屏幕实际变化的频率）
	EncFPS   float64 `json:"enc"`   // 编码帧率
	SentFPS  float64 `json:"sent"`  // 发出帧率
	ViewFPS  float64 `json:"view"`  // 手机显示帧率
	Kbps     float64 `json:"kbps"`  // 发送码率
	AckMs    float64 `json:"ack"`   // 帧往返：发出 → 手机显示后确认
	PingMs   float64 `json:"ping"`  // 手机测的心跳往返
	Pauses   int64   `json:"pause"` // 因对方没确认而暂停编码的次数
	InputHz  float64 `json:"input"` // 收到的键鼠事件/秒
	EncLatMs float64 `json:"encLat"`
	AudioPS  float64 `json:"audio"`   // 声音包/秒（正常约 47）
	AudioPk  float64 `json:"audioPk"` // 声音电平 0~1（0 = 没声音）
}

type tsInfo struct {
	Path   string  `json:"path"` // "direct 1.2.3.4:5" / "DERP(lax)" / ""
	PingMs float64 `json:"pingMs"`
	Err    string  `json:"err,omitempty"`
	At     int64   `json:"at"`
}

type diagState struct {
	mu       sync.Mutex
	active   bool
	viewerIP string
	codec    string
	since    time.Time
	set      settings
	kbpsNow  int

	sentFrames, sentBytes, keyFrames, pauses int64
	inputs                                   int64
	sendTimes                                [256]time.Time
	ackSamples                               []float64 // 最近 1 秒
	viewer                                   map[string]any
	viewerAt                                 time.Time
	ts                                       tsInfo
	hist                                     []diagPoint

	lastSent, lastBytes, lastPauses, lastInputs int64
	lastCap, lastEnc, lastAudio                 uint64
	lastEncLatSum, lastEncLatN                  uint64
	lastAt                                      time.Time
}

var diag = &diagState{}

// 手机连上 / 断开时通知（菜单栏图标用）
var onConnectedChange = func(bool) {}

// 诊断只给自己用：公开版默认关闭（面板只显示连接信息），启动时设 RDESK_DIAG=1 才打开
var diagEnabled = os.Getenv("RDESK_DIAG") == "1"

func (d *diagState) open(ip string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active, d.viewerIP, d.since = true, ip, time.Now()
	d.sentFrames, d.sentBytes, d.keyFrames, d.pauses, d.inputs = 0, 0, 0, 0, 0
	d.lastSent, d.lastBytes, d.lastPauses, d.lastInputs = 0, 0, 0, 0
	d.viewer = nil
	d.hist = nil
	d.ts = tsInfo{}
	d.codec = ""
}

func (d *diagState) authed(codec string, set settings) {
	d.mu.Lock()
	d.codec, d.set = codec, set
	d.mu.Unlock()
	onConnectedChange(true)
}

func (d *diagState) close() {
	d.mu.Lock()
	was := d.active
	d.active = false
	d.mu.Unlock()
	if was {
		onConnectedChange(false)
	}
}

func (d *diagState) frameSent(seq uint32, n int, key bool) {
	d.mu.Lock()
	d.sentFrames++
	d.sentBytes += int64(n)
	if key {
		d.keyFrames++
	}
	d.sendTimes[seq%256] = time.Now()
	d.mu.Unlock()
}

func (d *diagState) ack(seq uint32) {
	d.mu.Lock()
	if t := d.sendTimes[seq%256]; !t.IsZero() {
		d.ackSamples = append(d.ackSamples, float64(time.Since(t).Microseconds())/1000)
	}
	d.mu.Unlock()
}

func (d *diagState) setKbps(k int) { d.mu.Lock(); d.kbpsNow = k; d.mu.Unlock() }
func (d *diagState) pause()        { d.mu.Lock(); d.pauses++; d.mu.Unlock() }
func (d *diagState) input()        { d.mu.Lock(); d.inputs++; d.mu.Unlock() }

func (d *diagState) viewerStats(b []byte) {
	var m map[string]any
	if json.Unmarshal(b, &m) == nil {
		d.mu.Lock()
		d.viewer, d.viewerAt = m, time.Now()
		d.mu.Unlock()
	}
}

// 每秒采样一次
func (d *diagState) run() {
	t := time.NewTicker(time.Second)
	for range t.C {
		capN, encN, latSum, latN := videoCounters()
		audN, audPk := audioStats()
		d.mu.Lock()
		now := time.Now()
		dt := now.Sub(d.lastAt).Seconds()
		if dt <= 0 || dt > 5 {
			dt = 1
		}
		p := diagPoint{T: now.UnixMilli()}
		if d.active {
			p.CapFPS = float64(capN-d.lastCap) / dt
			p.EncFPS = float64(encN-d.lastEnc) / dt
			p.SentFPS = float64(d.sentFrames-d.lastSent) / dt
			p.Kbps = float64(d.sentBytes-d.lastBytes) * 8 / 1000 / dt
			p.Pauses = d.pauses - d.lastPauses
			p.InputHz = float64(d.inputs-d.lastInputs) / dt
			p.AudioPS = float64(audN-d.lastAudio) / dt
			p.AudioPk = audPk
			if n := latN - d.lastEncLatN; n > 0 {
				p.EncLatMs = float64(latSum-d.lastEncLatSum) / float64(n)
			}
			if len(d.ackSamples) > 0 {
				p.AckMs = median(d.ackSamples)
			}
			if d.viewer != nil && time.Since(d.viewerAt) < 3*time.Second {
				p.ViewFPS = num(d.viewer["fps"])
				p.PingMs = num(d.viewer["rtt"])
			}
			d.hist = append(d.hist, p)
			if len(d.hist) > 180 {
				d.hist = d.hist[len(d.hist)-180:]
			}
		}
		d.ackSamples = d.ackSamples[:0]
		d.lastSent, d.lastBytes, d.lastPauses, d.lastInputs = d.sentFrames, d.sentBytes, d.pauses, d.inputs
		d.lastCap, d.lastEnc, d.lastEncLatSum, d.lastEncLatN, d.lastAudio = capN, encN, latSum, latN, audN
		d.lastAt = now
		active, ip := d.active, d.viewerIP
		d.mu.Unlock()

		if diagEnabled && active && now.Unix()%5 == 0 {
			go d.probeTailscale(ip)
			log.Printf("[诊断] %s", d.oneLine())
		}
	}
}

var pingRe = regexp.MustCompile(`via (\S+(?: \S+)?) in ([\d.]+)ms`)

func tailscaleBin() string {
	for _, p := range []string{"/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale",
		`C:\Program Files\Tailscale\tailscale.exe`, "tailscale"} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return ""
}

// 用 tailscale ping 看到手机这条路是点对点直连还是走 DERP 中继
func (d *diagState) probeTailscale(ip string) {
	host, _, err := net.SplitHostPort(ip)
	if err != nil {
		host = ip
	}
	info := tsInfo{At: time.Now().UnixMilli()}
	pip := net.ParseIP(host)
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	if pip == nil || !cgnat.Contains(pip) {
		info.Err = "控制端不是通过 Tailscale 连进来的"
	} else if bin := tailscaleBin(); bin == "" {
		info.Err = "找不到 tailscale 命令"
	} else {
		out, _ := exec.Command(bin, "ping", "-c", "1", "--timeout", "3s", host).CombinedOutput()
		if m := pingRe.FindStringSubmatch(string(out)); m != nil {
			info.Path = m[1]
			info.PingMs, _ = strconv.ParseFloat(m[2], 64)
		} else {
			info.Err = strings.TrimSpace(string(out))
		}
	}
	d.mu.Lock()
	d.ts = info
	d.mu.Unlock()
}

func (d *diagState) snapshot() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	var last diagPoint
	if len(d.hist) > 0 {
		last = d.hist[len(d.hist)-1]
	}
	return map[string]any{
		"active": d.active, "viewerIP": d.viewerIP, "codec": d.codec,
		"since": d.since.UnixMilli(), "settings": d.set, "kbpsTarget": kbpsFor(d.set), "kbpsNow": d.kbpsNow,
		"sentFrames": d.sentFrames, "keyFrames": d.keyFrames, "pauses": d.pauses,
		"viewer": d.viewer, "tailscale": d.ts, "now": last, "hist": d.hist,
		"verdicts": d.verdictsLocked(),
	}
}

func (d *diagState) oneLine() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.hist) == 0 {
		return "暂无数据"
	}
	p := d.hist[len(d.hist)-1]
	return fmt.Sprintf("截屏%.0f 编码%.0f 发送%.0f 手机%.0f fps | %.0f kbps(目标%d) | 帧往返%.0fms 心跳%.0fms | 暂停%d | 输入%.0f/s | 声音%.0f包/s 电平%.2f | TS %s %.0fms",
		p.CapFPS, p.EncFPS, p.SentFPS, p.ViewFPS, p.Kbps, d.kbpsNow, p.AckMs, p.PingMs, p.Pauses, p.InputHz, p.AudioPS, p.AudioPk, d.ts.Path, d.ts.PingMs)
}

// 结论：按严重程度给出人话
func (d *diagState) verdictsLocked() []map[string]string {
	var v []map[string]string
	add := func(level, msg string) { v = append(v, map[string]string{"level": level, "msg": msg}) }
	if !d.active {
		add("info", "目前没有控制端连接。用手机连上后这里会实时显示。")
		return v
	}
	if d.codec == "jpeg" {
		add("warn", "这次连接用的是 JPEG 兼容模式（浏览器或旧版 App），比 H.264 卡。请把 iPhone 上的「RDesk 远控」更新到最新版。")
	}
	if d.ts.Err != "" {
		add("info", "Tailscale 检测："+d.ts.Err)
	} else if d.ts.Path != "" {
		if strings.HasPrefix(d.ts.Path, "DERP") {
			add("bad", fmt.Sprintf("Tailscale 现在走的是中继 %s，不是点对点直连，延迟 %.0fms。手机换个网络（Wi-Fi/蜂窝）或等几秒可能会转成直连。", d.ts.Path, d.ts.PingMs))
		} else {
			add("ok", fmt.Sprintf("Tailscale 是点对点直连（%s），网络往返 %.0fms。", d.ts.Path, d.ts.PingMs))
		}
	}
	n := len(d.hist)
	if n < 3 {
		add("info", "正在收集数据…")
		return v
	}
	recent := d.hist[max(0, n-5):]
	var pauses int64
	var ack, ping, view, sent, cap_, enc, encLat float64
	for _, p := range recent {
		pauses += p.Pauses
		ack += p.AckMs
		ping += p.PingMs
		view += p.ViewFPS
		sent += p.SentFPS
		cap_ += p.CapFPS
		enc += p.EncFPS
		encLat += p.EncLatMs
	}
	k := float64(len(recent))
	ack, ping, view, sent, cap_, enc, encLat = ack/k, ping/k, view/k, sent/k, cap_/k, enc/k, encLat/k
	if ping > 120 {
		add("bad", fmt.Sprintf("网络往返 %.0fms 偏高（流畅操作一般要 <60ms）。", ping))
	} else if ping > 60 {
		add("warn", fmt.Sprintf("网络往返 %.0fms，操作会有可感知的延迟。", ping))
	}
	if pauses > 0 {
		add("bad", fmt.Sprintf("网络在排队：最近 5 秒有帧迟迟没被手机确认（比平时往返多出 150ms 以上），暂停编码 %d 次，画面会跳。偶尔出现是网络抖动；一直出现说明带宽不够，把画质调到「流畅」。", pauses))
	}
	if ack > 0 && ping > 0 && ack-ping > 60 {
		add("warn", fmt.Sprintf("手机端处理慢：一帧从发出到手机显示确认要 %.0fms，比网络往返多 %.0fms（解码/显示/排队）。", ack, ack-ping))
	}
	if sent > 5 && view > 0 && view < sent*0.8 {
		add("warn", fmt.Sprintf("手机显示 %.0f fps，少于发出的 %.0f fps，有帧在路上或手机端被丢掉。", view, sent))
	}
	if encLat > 30 {
		add("warn", fmt.Sprintf("编码偏慢：平均每帧 %.0fms。", encLat))
	}
	if cap_ > 0 && enc < cap_*0.7 && pauses == 0 {
		add("info", fmt.Sprintf("截屏 %.0f fps，编码 %.0f fps。", cap_, enc))
	}
	problem := false
	for _, x := range v {
		if x["level"] == "warn" || x["level"] == "bad" {
			problem = true
		}
	}
	if !problem {
		add("ok", fmt.Sprintf("链路正常：发出 %.0f fps，手机显示 %.0f fps，帧往返 %.0fms。", sent, view, ack))
	}
	return v
}

func median(a []float64) float64 {
	b := append([]float64(nil), a...)
	sort.Float64s(b)
	return b[len(b)/2]
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	}
	return 0
}

func startDiag() string {
	go diag.run()
	port := diagPort
	for i := 0; i < 10 && portBusy("127.0.0.1", port); i++ {
		port++
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(diagHTML)
	})
	registerPanel(mux)
	mux.HandleFunc("/diag.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !diagEnabled {
			diag.mu.Lock()
			active := diag.active
			diag.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"active": active, "diag": false})
			return
		}
		json.NewEncoder(w).Encode(diag.snapshot())
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("诊断页面启动失败: %v", err)
		return ""
	}
	go http.Serve(ln, mux)
	return "http://" + addr
}
