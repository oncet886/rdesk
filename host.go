package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kbinani/screenshot"
)

// 控制端 -> 被控端 消息类型（解密后第一个字节）
const (
	msgHello    = 0x01
	msgAck      = 0x02
	msgPing     = 0x03
	msgKeyReq   = 0x04 // 控制端解码出错/刚切回前台，要一个关键帧
	msgMove     = 0x10
	msgButton   = 0x11
	msgWheel    = 0x12
	msgKey      = 0x20
	msgText     = 0x21
	msgSettings = 0x30
	msgStats    = 0x31 // 控制端每秒上报：帧率、心跳延迟等（诊断用）
)

// 被控端 -> 控制端
const (
	msgHelloOK     = 0x81
	msgFrame       = 0x82
	msgPong        = 0x83
	msgInfo        = 0x84
	msgVideo       = 0x85 // H.264 Annex-B: [u32 seq][u16 W][u16 H][u8 关键帧][数据]
	msgAudio       = 0x87 // 系统声音：一个裸 AAC-LC 包（48kHz 立体声）
	msgCursorShape = 0x88 // 光标样子 [u16 宽][u16 高][u16 热点x][u16 热点y]（单位 0.01 点）[PNG]
	msgCursor      = 0x86 // 鼠标位置 [u16 x][u16 y]（0..65535，65535,65535 = 不在本屏）
)

type hostOpts struct {
	relayURL string // ws(s)://.../ws/host
	id       string
	password string
	key      string
}

type settings struct {
	Quality int  `json:"q"`
	MaxW    int  `json:"maxw"`
	FPS     int  `json:"fps"`
	Display int  `json:"display"`
	KBPS    int  `json:"kbps"`
	H264    bool `json:"h264"`  // 控制端能解 H.264
	Audio   int  `json:"audio"` // 声音：1 开 2 关（0 = 没说，旧版控制端，不发）
}

type host struct {
	opts hostOpts
	box  atomic.Pointer[box] // 换密码时整体替换
	conn *wsConn

	mu       sync.Mutex
	sess     *session
	failures []time.Time
}

type session struct {
	authed    bool
	cancel    context.CancelFunc
	inflight  chan struct{}
	acks      chan ackEvt // 视频模式：控制端确认的帧序号和收到确认的时刻
	set       settings
	setCh     chan settings
	bounds    image.Rectangle // 当前显示器在全局坐标中的范围（输入映射用）
	btnDown   int
	video     bool        // 本次会话用 H.264
	audio     atomic.Bool // 现在要不要发声音（控制端可随时静音）
	wantAudio bool        // 控制端支持声音（握手时说的），才去录
}

func runHost(o hostOpts) {
	b, err := newBox(o.password, o.id)
	if err != nil {
		log.Fatal(err)
	}
	if err := inputInit(); err != nil {
		log.Printf("⚠️  %v", err)
	}
	h := &host{opts: o}
	h.box.Store(b)
	curHost.Store(h)
	backoff := time.Second
	for {
		start := time.Now()
		err := h.connectOnce()
		h.endSession()
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		log.Printf(L("与中继断开 (%v)，%v 后重连…"), err, backoff)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (h *host) connectOnce() error {
	u, _ := url.Parse(h.opts.relayURL)
	q := u.Query()
	q.Set("id", h.opts.id)
	if h.opts.key != "" {
		q.Set("key", h.opts.key)
	}
	u.RawQuery = q.Encode()
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	c, resp, err := d.Dial(u.String(), nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("%v (HTTP %d)", err, resp.StatusCode)
		}
		return err
	}
	defer c.Close()
	c.SetReadLimit(4 << 20)
	h.conn = &wsConn{c: c}
	log.Print(L("✅ 已连接中继，等待控制端…"))
	stop := make(chan struct{})
	defer close(stop)
	go keepAlive(h.conn, stop)
	setReadTimeout(c)
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			return err
		}
		if mt == websocket.TextMessage {
			var m struct{ T, IP string }
			json.Unmarshal(data, &m)
			switch m.T {
			case "open":
				log.Printf(L("📥 有控制端请求连接 (来自 %s)"), m.IP)
				h.newSession()
				diag.open(m.IP)
			case "close":
				h.endSession()
			}
			continue
		}
		h.handleBinary(data)
	}
}

func (h *host) newSession() {
	h.endSession()
	h.mu.Lock()
	h.sess = &session{inflight: make(chan struct{}, 2), acks: make(chan ackEvt, 512), setCh: make(chan settings, 4),
		set: settings{Quality: 65, MaxW: 1920, FPS: 20}}
	h.mu.Unlock()
}

func (h *host) endSession() {
	h.mu.Lock()
	s := h.sess
	h.sess = nil
	h.mu.Unlock()
	if s != nil {
		if s.cancel != nil {
			s.cancel()
		}
		if s.authed {
			releaseAll()
			log.Print(L("👋 控制端已断开"))
		}
		diag.close()
	}
}

func (h *host) send(plain []byte) error {
	return h.conn.write(websocket.BinaryMessage, h.box.Load().seal(plain, aadH2V))
}

func (h *host) handleBinary(data []byte) {
	h.mu.Lock()
	s := h.sess
	h.mu.Unlock()
	if s == nil {
		return
	}
	plain, err := h.box.Load().open(data, aadV2H)
	if err != nil {
		if !s.authed {
			h.rejectAuth()
		}
		return
	}
	if len(plain) == 0 {
		return
	}
	if !s.authed {
		if plain[0] != msgHello {
			return
		}
		if h.locked() {
			h.reject("密码错误次数过多，已锁定 5 分钟")
			return
		}
		s.authed = true
		var set settings
		if json.Unmarshal(plain[1:], &set) == nil {
			s.set = mergeSettings(s.set, set)
		}
		s.video = set.H264 && videoAvailable()
		codec := "jpeg"
		if s.video {
			codec = "h264"
		}
		hn, _ := os.Hostname()
		info, _ := json.Marshal(map[string]any{"name": hn, "os": runtime.GOOS,
			"displays": screenshot.NumActiveDisplays(), "input": inputAvailable(), "codec": codec})
		h.send(append([]byte{msgHelloOK}, info...))
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		diag.authed(codec, s.set)
		if s.video {
			s.wantAudio = set.Audio != 0
			s.audio.Store(set.Audio == 1)
		}
		go h.stream(ctx, s)
		go h.cursorLoop(ctx, s)
		if s.wantAudio {
			go h.audioLoop(ctx, s)
		}
		log.Printf(L("🔓 控制端验证通过，开始远程（%s）"), codec)
		return
	}
	h.handleInput(s, plain)
}

func (h *host) locked() bool {
	cut := time.Now().Add(-5 * time.Minute)
	n := 0
	for _, t := range h.failures {
		if t.After(cut) {
			n++
		}
	}
	return n >= 5
}

func (h *host) rejectAuth() {
	h.failures = append(h.failures, time.Now())
	if len(h.failures) > 20 {
		h.failures = h.failures[len(h.failures)-20:]
	}
	log.Print(L("⛔ 控制端密码错误"))
	if h.locked() {
		h.reject("密码错误次数过多，已锁定 5 分钟")
	} else {
		h.reject("密码错误")
	}
}

func (h *host) reject(msg string) {
	h.conn.write(websocket.BinaryMessage, append([]byte{framePlain}, msg...))
	h.conn.writeJSON(map[string]string{"t": "kick"})
	h.mu.Lock()
	h.sess = nil
	h.mu.Unlock()
}

func mergeSettings(cur, n settings) settings {
	if n.Quality >= 10 && n.Quality <= 95 {
		cur.Quality = n.Quality
	}
	if n.MaxW >= 320 && n.MaxW <= 8192 {
		cur.MaxW = n.MaxW
	}
	if n.FPS >= 1 && n.FPS <= 60 {
		cur.FPS = n.FPS
	}
	if n.Display >= 0 && n.Display < screenshot.NumActiveDisplays() {
		cur.Display = n.Display
	}
	if n.Audio == 1 || n.Audio == 2 {
		cur.Audio = n.Audio
	}
	if n.KBPS >= 300 && n.KBPS <= 50000 {
		cur.KBPS = n.KBPS
	}
	return cur
}

func (h *host) handleInput(s *session, p []byte) {
	switch p[0] {
	case msgAck:
		var seq uint32 // 旧版控制端不带序号，记 0 = 确认最早一帧
		if len(p) >= 5 {
			seq = binary.BigEndian.Uint32(p[1:])
			diag.ack(seq)
		}
		if s.video {
			select {
			case s.acks <- ackEvt{seq, time.Now()}: // 在读取线程打时间戳，处理晚了也不影响测量
			default:
			}
		} else {
			select {
			case <-s.inflight:
			default:
			}
		}
	case msgStats:
		diag.viewerStats(p[1:])
	case msgPing:
		h.send(append([]byte{msgPong}, p[1:]...))
	case msgKeyReq:
		if s.video {
			videoKeyframe()
		}
	case msgSettings:
		var set settings
		if json.Unmarshal(p[1:], &set) == nil {
			if set.Audio != 0 {
				s.audio.Store(set.Audio == 1)
			}
			select {
			case s.setCh <- set:
			default:
			}
		}
	case msgMove, msgButton, msgWheel, msgKey, msgText:
		diag.input()
		h.handleControl(s, p)
	}
}

func (h *host) handleControl(s *session, p []byte) {
	be := binary.BigEndian
	switch p[0] {
	case msgMove:
		if len(p) >= 5 {
			x, y := h.mapXY(s, be.Uint16(p[1:]), be.Uint16(p[3:]))
			mouseMove(x, y)
		}
	case msgButton:
		if len(p) >= 7 {
			x, y := h.mapXY(s, be.Uint16(p[3:]), be.Uint16(p[5:]))
			mouseButton(int(p[1]), p[2] == 1, x, y)
		}
	case msgWheel:
		if len(p) >= 5 {
			mouseWheel(int(int16(be.Uint16(p[1:]))), int(int16(be.Uint16(p[3:]))))
		}
	case msgKey:
		if len(p) >= 3 {
			keyEvent(string(p[2:]), p[1] == 1)
		}
	case msgText:
		typeText(string(p[1:]))
	}
}

// 控制端发来的是 0..65535 的归一化坐标，映射到当前显示器的全局坐标
func (h *host) mapXY(s *session, nx, ny uint16) (float64, float64) {
	h.mu.Lock()
	b := s.bounds
	h.mu.Unlock()
	if b.Empty() {
		b = screenshot.GetDisplayBounds(0)
	}
	return float64(b.Min.X) + float64(nx)/65535*float64(b.Dx()-1),
		float64(b.Min.Y) + float64(ny)/65535*float64(b.Dy()-1)
}

// ---------------- 画面推流 ----------------

// 硬件 H.264 推流：编码在 C 侧完成，这里只负责取包、流量控制、发送
func (h *host) streamVideo(ctx context.Context, s *session) {
	set := s.set
	fc := &flowCtl{}
	abr := &abrCtl{fc: fc}
	start := func() bool {
		h.mu.Lock()
		s.bounds = screenshot.GetDisplayBounds(set.Display)
		h.mu.Unlock()
		// 手机屏幕显示不了更宽的画面，宽了只会让编码变慢（2560 宽每帧 25ms，1920 宽 8ms）
		abr.reset(kbpsFor(set))
		if err := videoStart(set.Display, min(set.MaxW, 1920), set.FPS, abr.kbps, s.wantAudio); err != nil {
			log.Printf(L("硬件编码启动失败: %v"), err)
			h.send(append([]byte{msgInfo}, "被控端截屏失败：请在被控电脑上授予「屏幕录制」权限"...))
			return false
		}
		return true
	}
	defer videoStop()
	ok := start()
	var seq uint32
	for {
		select {
		case <-ctx.Done():
			return
		case n := <-s.setCh:
			set = mergeSettings(set, n)
			ok = start()
			continue
		default:
		}
		if !ok || videoStopped() {
			sleepCtx(ctx, 2*time.Second)
			ok = start()
			continue
		}
		fc.drain(s.acks) // 没有新帧时也要及时处理确认，否则帧稀时测出的往返会把空闲时间也算进去
		data, key, w, hh, got := videoNext(50 * time.Millisecond)
		if !got {
			continue
		}
		abr.tick()
		// 流量控制（按延迟）：路上的帧只要没有"越积越久"就照发；最早那帧迟迟没确认（超过最小往返+150ms）
		// 说明网络在排队，才暂停编码，等恢复后直接编最新画面 —— 延迟不会越积越多
		fc.drain(s.acks)
		if !fc.canSend() {
			videoPause(true)
			diag.pause()
			abr.paused++
			deadline := time.After(2 * time.Second)
		wait:
			for !fc.canSend() {
				select {
				case a := <-s.acks:
					fc.ack(a.seq, a.t)
				case <-ctx.Done():
					return
				case <-deadline:
					fc.reset()
					break wait
				}
			}
			videoPause(false)
		}
		seq++
		fc.sent(seq, len(data))
		msg := make([]byte, 10, 10+len(data))
		msg[0] = msgVideo
		binary.BigEndian.PutUint32(msg[1:], seq)
		binary.BigEndian.PutUint16(msg[5:], uint16(w))
		binary.BigEndian.PutUint16(msg[7:], uint16(hh))
		if key {
			msg[9] = 1
		}
		if err := h.send(append(msg, data...)); err != nil {
			return
		}
		diag.frameSent(seq, len(data), key)
	}
}

// 码率：控制端指定优先，否则按画质档位
func kbpsFor(set settings) int {
	if set.KBPS > 0 {
		return set.KBPS
	}
	switch {
	case set.Quality <= 50:
		return 2500
	case set.Quality <= 70:
		return 6000
	default:
		return 12000
	}
}

func (h *host) stream(ctx context.Context, s *session) {
	if s.video {
		h.streamVideo(ctx, s)
		return
	}
	var prev, last *image.RGBA // prev = 上次发出去的画面，last = 最新截到的画面
	var seq uint32
	set := s.set
	lastErr := ""
	cap := newCapturer()
	defer cap.close()
	configured := false
	report := func(err error) {
		if e := fmt.Sprint(err); e != lastErr {
			lastErr = e
			log.Printf(L("截屏失败: %v（Mac 请在 系统设置→隐私与安全性→屏幕录制 中允许本程序）"), err)
			h.send(append([]byte{msgInfo}, "被控端截屏失败：请在被控电脑上授予「屏幕录制」权限"...))
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case n := <-s.setCh:
			set = mergeSettings(set, n)
			prev, last, configured = nil, nil, false // 设置变了，重新开始推流并发全屏
			continue
		default:
		}
		t0 := time.Now()

		// 流量控制：最多 2 帧在路上，控制端画完一帧回 ACK 才发下一帧 —— 网速慢时自动降帧而不是堆积延迟
		select {
		case s.inflight <- struct{}{}:
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
			for len(s.inflight) > 0 {
				<-s.inflight
			}
			continue
		}

		h.mu.Lock()
		s.bounds = screenshot.GetDisplayBounds(set.Display)
		h.mu.Unlock()
		if !configured {
			if err := cap.configure(set.Display, set.MaxW, set.FPS); err != nil {
				<-s.inflight
				report(err)
				sleepCtx(ctx, 2*time.Second)
				continue
			}
			configured = true
		}
		img, fresh, err := cap.grab()
		if err != nil {
			<-s.inflight
			report(err)
			sleepCtx(ctx, time.Second)
			continue
		}
		if fresh {
			last = img
		}
		if last == nil || (!fresh && prev != nil) {
			<-s.inflight // 屏幕没变：不发
			sleepCtx(ctx, 8*time.Millisecond)
			continue
		}
		img = last

		var rects []image.Rectangle
		if prev == nil || prev.Bounds() != img.Bounds() {
			rects = []image.Rectangle{img.Bounds()}
		} else {
			rects = diffRects(prev, img)
		}
		if len(rects) == 0 {
			<-s.inflight
			sleepCtx(ctx, frameGap(set.FPS, t0))
			continue
		}
		prev = img

		seq++
		var buf bytes.Buffer
		buf.WriteByte(msgFrame)
		hdr := make([]byte, 9)
		binary.BigEndian.PutUint32(hdr, seq)
		binary.BigEndian.PutUint16(hdr[4:], uint16(img.Bounds().Dx()))
		binary.BigEndian.PutUint16(hdr[6:], uint16(img.Bounds().Dy()))
		hdr[8] = byte(len(rects))
		buf.Write(hdr)
		var jb bytes.Buffer
		for _, r := range rects {
			jb.Reset()
			jpeg.Encode(&jb, img.SubImage(r), &jpeg.Options{Quality: set.Quality})
			rh := make([]byte, 12)
			binary.BigEndian.PutUint16(rh, uint16(r.Min.X))
			binary.BigEndian.PutUint16(rh[2:], uint16(r.Min.Y))
			binary.BigEndian.PutUint16(rh[4:], uint16(r.Dx()))
			binary.BigEndian.PutUint16(rh[6:], uint16(r.Dy()))
			binary.BigEndian.PutUint32(rh[8:], uint32(jb.Len()))
			buf.Write(rh)
			buf.Write(jb.Bytes())
		}
		if err := h.send(buf.Bytes()); err != nil {
			return
		}
		diag.frameSent(seq, buf.Len(), true)
		sleepCtx(ctx, frameGap(set.FPS, t0))
	}
}

func frameGap(fps int, t0 time.Time) time.Duration {
	if fps <= 0 {
		fps = 20
	}
	return time.Second/time.Duration(fps) - time.Since(t0)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// 整数倍缩小（框滤波），Retina 屏会缩到逻辑分辨率左右
func downscale(src *image.RGBA, maxW int) *image.RGBA {
	w, hgt := src.Bounds().Dx(), src.Bounds().Dy()
	f := (w + maxW - 1) / maxW
	if f <= 1 {
		if src.Bounds().Min != (image.Point{}) {
			dst := image.NewRGBA(image.Rect(0, 0, w, hgt))
			for y := 0; y < hgt; y++ {
				copy(dst.Pix[y*dst.Stride:y*dst.Stride+w*4], src.Pix[y*src.Stride:y*src.Stride+w*4])
			}
			return dst
		}
		return src
	}
	dw, dh := w/f, hgt/f
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	n := uint32(f * f)
	for y := 0; y < dh; y++ {
		drow := dst.Pix[y*dst.Stride:]
		for x := 0; x < dw; x++ {
			var r, g, b uint32
			for yy := 0; yy < f; yy++ {
				srow := src.Pix[(y*f+yy)*src.Stride+x*f*4:]
				for xx := 0; xx < f; xx++ {
					r += uint32(srow[xx*4])
					g += uint32(srow[xx*4+1])
					b += uint32(srow[xx*4+2])
				}
			}
			drow[x*4], drow[x*4+1], drow[x*4+2], drow[x*4+3] = byte(r/n), byte(g/n), byte(b/n), 255
		}
	}
	return dst
}

const tile = 64

// 把画面切成 64×64 的格子，只把变化的区域合并成若干矩形发送（省流量的关键）
func diffRects(a, b *image.RGBA) []image.Rectangle {
	w, hgt := b.Bounds().Dx(), b.Bounds().Dy()
	tw, th := (w+tile-1)/tile, (hgt+tile-1)/tile
	var rects []image.Rectangle
	var prevRow []image.Rectangle
	dirtyArea := 0
	for ty := 0; ty < th; ty++ {
		y0, y1 := ty*tile, min((ty+1)*tile, hgt)
		var row []image.Rectangle
		runStart := -1
		for tx := 0; tx <= tw; tx++ {
			dirty := false
			if tx < tw {
				x0, x1 := tx*tile*4, min((tx+1)*tile, w)*4
				for y := y0; y < y1; y++ {
					if !bytes.Equal(a.Pix[y*a.Stride+x0:y*a.Stride+x1], b.Pix[y*b.Stride+x0:y*b.Stride+x1]) {
						dirty = true
						break
					}
				}
			}
			if dirty && runStart < 0 {
				runStart = tx
			} else if !dirty && runStart >= 0 {
				row = append(row, image.Rect(runStart*tile, y0, min(tx*tile, w), y1))
				runStart = -1
			}
		}
		// 与上一行相同横向范围的矩形向下合并
		var next []image.Rectangle
		for _, r := range row {
			dirtyArea += r.Dx() * r.Dy()
			merged := false
			for i, p := range prevRow {
				if p.Min.X == r.Min.X && p.Max.X == r.Max.X && p.Max.Y == r.Min.Y {
					prevRow[i].Max.Y = r.Max.Y
					next = append(next, prevRow[i])
					prevRow[i] = image.Rectangle{}
					merged = true
					break
				}
			}
			if !merged {
				next = append(next, r)
			}
		}
		for _, p := range prevRow {
			if !p.Empty() {
				rects = append(rects, p)
			}
		}
		prevRow = next
	}
	rects = append(rects, prevRow...)
	if len(rects) == 0 {
		return nil
	}
	// 变化太多或太碎：直接发一个外接矩形
	if len(rects) > 24 || dirtyArea*10 > w*hgt*6 {
		u := rects[0]
		for _, r := range rects[1:] {
			u = u.Union(r)
		}
		return []image.Rectangle{u}
	}
	return rects
}

func normalizeRelay(s string, path string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	switch {
	case strings.HasPrefix(s, "https://"):
		s = "wss://" + s[8:]
	case strings.HasPrefix(s, "http://"):
		s = "ws://" + s[7:]
	case !strings.HasPrefix(s, "ws://") && !strings.HasPrefix(s, "wss://"):
		s = "wss://" + s
	}
	if !strings.HasSuffix(s, path) {
		s += path
	}
	return s
}

// 把被控端鼠标位置实时发给控制端，由控制端画一个大光标（手机上才看得清、跟手）
func (h *host) cursorLoop(ctx context.Context, s *session) {
	t := time.NewTicker(time.Second / 60)
	defer t.Stop()
	var lx, ly uint16 = 1, 1
	cursorShapeReset()
	tick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if tick++; tick%6 == 0 { // 每秒 10 次看光标样子有没有变
			if png, w, hh, hx, hy, ok := cursorShape(); ok {
				m := make([]byte, 9, 9+len(png))
				m[0] = msgCursorShape
				for i, v := range []float64{w, hh, hx, hy} {
					binary.BigEndian.PutUint16(m[1+i*2:], uint16(min(v*100, 65535)))
				}
				h.send(append(m, png...))
			}
		}
		x, y, ok := cursorPos()
		if !ok {
			return
		}
		h.mu.Lock()
		b := s.bounds
		h.mu.Unlock()
		if b.Empty() {
			continue
		}
		nx, ny := uint16(65535), uint16(65535)
		if x >= float64(b.Min.X) && x < float64(b.Max.X) && y >= float64(b.Min.Y) && y < float64(b.Max.Y) {
			nx = uint16((x - float64(b.Min.X)) / float64(b.Dx()-1) * 65535)
			ny = uint16((y - float64(b.Min.Y)) / float64(b.Dy()-1) * 65535)
		}
		if nx == lx && ny == ly {
			continue
		}
		lx, ly = nx, ny
		m := []byte{msgCursor, 0, 0, 0, 0}
		binary.BigEndian.PutUint16(m[1:], nx)
		binary.BigEndian.PutUint16(m[3:], ny)
		h.send(m)
	}
}

// 基于延迟的流量控制
type sentFrame struct {
	seq   uint32
	t     time.Time
	bytes int
}

type rttSample struct {
	t     time.Time
	rtt   time.Duration
	bytes int
}

type flowCtl struct {
	unacked []sentFrame
	samples []rttSample
	now     func() time.Time // 测试时注入假时钟
}

func (f *flowCtl) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

func (f *flowCtl) sent(seq uint32, bytes int) {
	f.unacked = append(f.unacked, sentFrame{seq, f.clock(), bytes})
}

type ackEvt struct {
	seq uint32
	t   time.Time
}

func (f *flowCtl) ack(seq uint32, now time.Time) {
	for len(f.unacked) > 0 && (seq == 0 || f.unacked[0].seq <= seq) {
		u := f.unacked[0]
		f.unacked = f.unacked[1:]
		if seq == 0 || u.seq == seq {
			f.samples = append(f.samples, rttSample{now, now.Sub(u.t), u.bytes})
			break
		}
	}
	cut := now.Add(-10 * time.Second)
	for len(f.samples) > 0 && f.samples[0].t.Before(cut) {
		f.samples = f.samples[1:]
	}
}

func (f *flowCtl) drain(ch chan ackEvt) {
	for {
		select {
		case a := <-ch:
			f.ack(a.seq, a.t)
		default:
			return
		}
	}
}

// 最近 10 秒的最小往返（≈ 不排队时的真实网络延迟）
func (f *flowCtl) minRTT() time.Duration {
	if len(f.samples) == 0 {
		return 300 * time.Millisecond
	}
	m := f.samples[0].rtt
	for _, x := range f.samples {
		m = min(m, x.rtt)
	}
	return m
}

func (f *flowCtl) canSend() bool {
	if len(f.unacked) == 0 {
		return true
	}
	if len(f.unacked) >= 60 {
		return false
	}
	return f.clock().Sub(f.unacked[0].t) < f.minRTT()+150*time.Millisecond
}

func (f *flowCtl) reset() { f.unacked = nil }

// 排队延迟：每帧往返 − 最小往返 − 按当前码率传完这帧本来就要的时间（大帧传得久不算堵）。
// 取最近 1 秒的中位数；样本不到 3 个时不下结论（画面静止、帧很稀时不乱调码率）。
func (f *flowCtl) queueDelay(kbps int) (time.Duration, bool) {
	cut := f.clock().Add(-time.Second)
	base := f.minRTT()
	var q []time.Duration
	for _, x := range f.samples {
		if x.t.After(cut) {
			tx := time.Duration(float64(x.bytes*8) / float64(max(kbps, 1)*1000) * float64(time.Second))
			q = append(q, x.rtt-base-tx)
		}
	}
	if len(q) < 3 {
		return 0, false
	}
	sort.Slice(q, func(i, j int) bool { return q[i] < q[j] })
	return q[len(q)/2], true
}

// 自适应码率（类似 RustDesk / WebRTC）：网络开始排队（扣掉传输时间后仍多出 120ms 以上，或被迫暂停）
// 就降到 0.75 倍；连续 2 秒顺畅再往上加，最高不超过画质档位的上限。宁可稍糊也不卡。
type abrCtl struct {
	fc            *flowCtl
	kbps, maxKbps int
	paused        int
	last, goodAt  time.Time
}

const minKbps = 800

func (a *abrCtl) reset(max int) {
	a.maxKbps = max
	a.kbps = min(max, 3000) // 从保守值起步，网络好再往上加
	a.last, a.goodAt = a.fc.clock(), a.fc.clock()
	diag.setKbps(a.kbps)
}

func (a *abrCtl) tick() {
	now := a.fc.clock()
	if now.Sub(a.last) < 500*time.Millisecond {
		return
	}
	a.last = now
	q, ok := a.fc.queueDelay(a.kbps)
	nk := a.kbps
	switch {
	case a.paused > 0 || (ok && q > 120*time.Millisecond):
		nk = max(minKbps, a.kbps*3/4)
		a.goodAt = now
	case ok && q < 40*time.Millisecond && now.Sub(a.goodAt) > 2*time.Second:
		nk = min(a.maxKbps, a.kbps*115/100+200)
	}
	if os.Getenv("RDESK_CDEBUG") != "" && nk != a.kbps {
		log.Printf("[abr] %d→%d q=%v ok=%v paused=%d unacked=%d minRTT=%v", a.kbps, nk, q, ok, a.paused, len(a.fc.unacked), a.fc.minRTT())
	}
	a.paused = 0
	if nk != a.kbps {
		a.kbps = nk
		videoSetBitrate(nk)
		diag.setKbps(nk)
	}
}

// 系统声音：编码好的 AAC 包直接发；控制端关了声音就丢掉（不占流量）
func (h *host) audioLoop(ctx context.Context, s *session) {
	for ctx.Err() == nil {
		pkt, ok := audioNext(100 * time.Millisecond)
		if !ok || !s.audio.Load() {
			continue
		}
		h.send(append([]byte{msgAudio}, pkt...))
	}
}

var curHost atomic.Pointer[host]

// 手动断开正在连着的控制端（菜单栏 / 控制面板）。提示用各控制端都已翻译好的「连接已断开」
func (h *host) kickViewer() bool {
	h.mu.Lock()
	s := h.sess
	h.mu.Unlock()
	if s == nil || !s.authed {
		return false
	}
	h.conn.write(websocket.BinaryMessage, append([]byte{framePlain}, "连接已断开"...))
	h.conn.writeJSON(map[string]string{"t": "kick"})
	log.Print(L("✋ 已手动断开控制端"))
	h.endSession() // 停掉推流、松开按着的键
	return true
}

// 控制面板「换新密码」：立刻生效，正在连着的控制端会被断开
func (h *host) setPassword(pw string) error {
	b, err := newBox(pw, h.opts.id)
	if err != nil {
		return err
	}
	h.box.Store(b)
	h.opts.password = pw
	if c := h.conn; c != nil {
		c.writeJSON(map[string]string{"t": "kick"})
	}
	h.endSession()
	return nil
}
