package main

// 浏览器连接（网页版控制端 https://rdeskapp.com/web）。浏览器的 https 页面只能连带正规证书的 wss://，所以有两条路：
//  1. Tailscale HTTPS 证书：在 Tailscale 直连端口上同时接受 TLS（浏览器连 wss://电脑名.xxx.ts.net:端口）
//     和明文（已发布的 App 照旧连 ws://100.x.x.x:端口）。证书用 `tailscale cert` 申请，每天检查续期。
//     需要用户在 Tailscale 后台开启 HTTPS；控制端设备也要装 Tailscale。
//  2. 临时分享：Cloudflare 快速隧道给出公网 https 地址，对方什么都不用装；到时自动关闭。
//     安全靠端到端加密 + 密码（连续错 5 次全局锁 5 分钟）。

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	webViewer   = "https://rdeskapp.com/web/"
	shareFor    = time.Hour
	tsAdminDNS  = "https://login.tailscale.com/admin/dns"
	certErrOff  = "https-off" // Tailscale 后台没开 HTTPS
	certPending = "pending"
)

type webState struct {
	mu         sync.Mutex
	local      string // Tailscale 直连监听地址 100.x.x.x:端口（隧道也转发到这里）
	tlsAddr    string // 电脑名.xxx.ts.net:端口；证书可用时才有
	certErr    string
	cert       *tls.Certificate
	shareURL   string // https://xxx.trycloudflare.com
	shareUntil time.Time
	shareBusy  bool // 正在下载 cloudflared / 开隧道
	shareErr   string
	shareCmd   *exec.Cmd
}

var web webState

// 浏览器打开就能连的链接（地址和 ID 放在 # 后面，不会发给网站；不带密码）
func viewerLink(server string) string {
	return webViewer + "#s=" + url.QueryEscape(server) + "&id=" + url.QueryEscape(loadConfig().ID)
}

type webInfo struct {
	Link       string `json:"link,omitempty"`    // Tailscale 证书版链接
	CertErr    string `json:"certErr,omitempty"` // https-off / pending / 失败原因
	Share      string `json:"share,omitempty"`   // 临时分享链接
	ShareUntil int64  `json:"shareUntil,omitempty"`
	ShareBusy  bool   `json:"shareBusy,omitempty"`
	ShareErr   string `json:"shareErr,omitempty"`
	CanShare   bool   `json:"canShare"`
}

func webSnapshot() webInfo {
	web.mu.Lock()
	defer web.mu.Unlock()
	w := webInfo{CertErr: web.certErr, ShareBusy: web.shareBusy, ShareErr: web.shareErr, CanShare: web.local != ""}
	if web.tlsAddr != "" {
		w.Link = "https://" + web.tlsAddr + "/#id=" + url.QueryEscape(loadConfig().ID) // 被控端自己提供网页版
	}
	if web.shareURL != "" {
		w.Share = viewerLink(strings.TrimPrefix(web.shareURL, "https://"))
		w.ShareUntil = web.shareUntil.UnixMilli()
	}
	return w
}

// ---------------- Tailscale 证书 ----------------

// 在 Tailscale 直连端口上启动 http 服务：TLS 和明文共用一个端口
func serveTailscale(ln net.Listener, h http.Handler, port int) {
	web.mu.Lock()
	web.local = ln.Addr().String()
	web.certErr = certPending
	web.mu.Unlock()
	tc := &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		web.mu.Lock()
		defer web.mu.Unlock()
		if web.cert == nil {
			return nil, errors.New("no certificate")
		}
		return web.cert, nil
	}}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(&sniffListener{Listener: ln, tls: tc, ch: make(chan net.Conn)})
	go certLoop(port)
}

func certLoop(port int) {
	announced := ""
	for {
		wait := 24 * time.Hour
		domain, err := tsCertDomain()
		switch {
		case err != nil:
			setCertErr(err.Error())
			wait = time.Minute
		case domain == "":
			setCertErr(certErrOff)
			wait = time.Minute // 用户在后台开了 HTTPS 后一分钟内自动生效
		default:
			if err := fetchCert(domain); err != nil {
				log.Printf(L("申请 Tailscale 证书失败: %v"), err)
				setCertErr(err.Error())
				wait = 10 * time.Minute
				break
			}
			addr := net.JoinHostPort(domain, fmt.Sprint(port))
			web.mu.Lock()
			web.tlsAddr, web.certErr = addr, ""
			web.mu.Unlock()
			if announced != addr {
				announced = addr
				log.Printf(L("🌐 浏览器连接：%s"), "https://"+addr+"/#id="+url.QueryEscape(loadConfig().ID))
			}
		}
		time.Sleep(wait)
	}
}

func setCertErr(e string) {
	web.mu.Lock()
	web.certErr = e
	web.mu.Unlock()
}

// Tailscale 后台开了 HTTPS 才有 CertDomains
func tsCertDomain() (string, error) {
	bin := tailscaleBin()
	if bin == "" {
		return "", errors.New("tailscale not found")
	}
	out, err := exec.Command(bin, "status", "--json").Output()
	if err != nil {
		return "", err
	}
	var st struct{ CertDomains []string }
	if err := json.Unmarshal(out, &st); err != nil {
		return "", err
	}
	if len(st.CertDomains) == 0 {
		return "", nil
	}
	return st.CertDomains[0], nil
}

func fetchCert(domain string) error {
	dir := filepath.Join(filepath.Dir(configPath()), "tls")
	os.MkdirAll(dir, 0o700)
	crt, key := filepath.Join(dir, domain+".crt"), filepath.Join(dir, domain+".key")
	cmd := exec.Command(tailscaleBin(), "cert", "--cert-file", crt, "--key-file", key, domain)
	if out, err := runTimeout(cmd, 3*time.Minute); err != nil {
		return fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
	}
	c, err := tls.LoadX509KeyPair(crt, key)
	if err != nil {
		return err
	}
	web.mu.Lock()
	web.cert = &c
	web.mu.Unlock()
	return nil
}

func runTimeout(cmd *exec.Cmd, d time.Duration) ([]byte, error) {
	var buf strings.Builder
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return []byte(buf.String()), err
	case <-time.After(d):
		cmd.Process.Kill()
		return []byte(buf.String()), errors.New("timeout")
	}
}

// 看连接的第一个字节：0x16 是 TLS 握手，交给 tls.Server；否则当明文 HTTP
type sniffListener struct {
	net.Listener
	tls  *tls.Config
	ch   chan net.Conn
	once sync.Once
	err  error
}

type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekConn) Read(b []byte) (int, error) { return c.r.Read(b) }

func (l *sniffListener) Accept() (net.Conn, error) {
	l.once.Do(func() {
		go func() {
			for {
				c, err := l.Listener.Accept()
				if err != nil {
					l.err = err
					close(l.ch)
					return
				}
				go l.sniff(c) // 不在 Accept 里等对方发数据，免得一个慢连接卡住所有人
			}
		}()
	})
	c, ok := <-l.ch
	if !ok {
		return nil, l.err
	}
	return c, nil
}

func (l *sniffListener) sniff(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	b, err := r.Peek(1)
	c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	pc := &peekConn{Conn: c, r: r}
	if b[0] == 0x16 {
		l.ch <- tls.Server(pc, l.tls)
	} else {
		l.ch <- pc
	}
}

// ---------------- 临时分享（Cloudflare 快速隧道） ----------------

func startShare() {
	web.mu.Lock()
	if web.local == "" || web.shareBusy || web.shareURL != "" {
		web.mu.Unlock()
		return
	}
	web.shareBusy, web.shareErr = true, ""
	local := web.local
	web.mu.Unlock()

	fail := func(err error) {
		log.Printf(L("临时分享失败: %v"), err)
		web.mu.Lock()
		web.shareBusy, web.shareErr = false, err.Error()
		web.mu.Unlock()
	}
	bin := findCloudflared()
	if bin == "" {
		log.Print(L("正在下载 cloudflared（只需一次）…"))
		var err error
		if bin, err = downloadCloudflared(); err != nil {
			fail(err)
			return
		}
	}
	exitOnSignal.Do(func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM) // Ctrl+C、关掉 Windows 控制台窗口
		go func() { <-ch; exitApp(0) }()
	})
	t, err := openTunnel(bin, local, nil)
	if err != nil && strings.Contains(err.Error(), "SRV") {
		// 有些路由器 / 虚拟机的 DNS 会截断 SRV 记录，cloudflared 找不到两个节点就退出：直接告诉它节点地址
		t, err = openTunnel(bin, local, []string{"--edge", "region1.v2.argotunnel.com:7844", "--edge", "region2.v2.argotunnel.com:7844"})
	}
	if err != nil {
		fail(err)
		return
	}
	until := time.Now().Add(shareFor)
	web.mu.Lock()
	web.shareURL, web.shareUntil, web.shareCmd, web.shareBusy = t.url, until, t.cmd, false
	web.mu.Unlock()
	log.Printf(L("🌍 临时分享已开启，%d 分钟后自动关闭：%s"), int(shareFor.Minutes()), viewerLink(strings.TrimPrefix(t.url, "https://")))
	go func() {
		select {
		case <-time.After(time.Until(until)):
		case <-t.exited:
			log.Printf("cloudflared exited: %s", t.lastLines())
		}
		stopShareCmd(t.cmd)
	}()
}

type tunnel struct {
	cmd    *exec.Cmd
	url    string
	exited chan struct{}
	mu     sync.Mutex
	tail   []string // cloudflared 最后几行输出，出错时记到日志里
}

func (t *tunnel) lastLines() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.tail, "\n")
}

// 启动 cloudflared 快速隧道，等到隧道真正连上 Cloudflare（不只是拿到链接）才返回
func openTunnel(bin, local string, extra []string) (*tunnel, error) {
	args := append([]string{"tunnel", "--no-autoupdate"}, extra...)
	cmd := exec.Command(bin, append(args, "--url", "http://"+local)...)
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t := &tunnel{cmd: cmd, exited: make(chan struct{})}
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderr)
		sent := false
		for sc.Scan() {
			line := sc.Text()
			t.mu.Lock()
			if t.tail = append(t.tail, line); len(t.tail) > 6 {
				t.tail = t.tail[1:]
			}
			if u := tunnelRe.FindString(line); u != "" && t.url == "" {
				t.url = u
			}
			ok := t.url != "" && strings.Contains(line, "Registered tunnel connection")
			t.mu.Unlock()
			if ok && !sent {
				sent = true
				close(ready)
			}
		}
		cmd.Wait() // 输出读完再 Wait（Wait 会关掉管道）
		close(t.exited)
	}()
	select {
	case <-ready:
		return t, nil
	case <-t.exited:
		return nil, fmt.Errorf("cloudflared: %s", t.lastLines())
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		<-t.exited
		return nil, errors.New("cloudflared timeout")
	}
}

func stopShare() {
	web.mu.Lock()
	cmd := web.shareCmd
	web.mu.Unlock()
	if cmd != nil {
		stopShareCmd(cmd)
	}
}

func stopShareCmd(cmd *exec.Cmd) {
	web.mu.Lock()
	if web.shareCmd != cmd {
		web.mu.Unlock()
		return
	}
	web.shareURL, web.shareCmd = "", nil
	web.mu.Unlock()
	cmd.Process.Kill()
	log.Print(L("临时分享已关闭"))
}

var exitOnSignal sync.Once

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// 退出前关掉隧道（cloudflared 是子进程，不关会留在后台）
func exitApp(code int) {
	stopShare()
	beforeExit()
	os.Exit(code)
}

// 从 Cloudflare 官方 GitHub 下载 cloudflared，放到配置目录（只下载一次）
func cloudflaredPath() string {
	n := "cloudflared"
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return filepath.Join(filepath.Dir(configPath()), "bin", n)
}

func downloadCloudflared() (string, error) {
	const base = "https://github.com/cloudflare/cloudflared/releases/latest/download/"
	var asset string
	switch runtime.GOOS {
	case "darwin":
		asset = "cloudflared-darwin-" + runtime.GOARCH + ".tgz"
	case "windows":
		asset = "cloudflared-windows-amd64.exe" // 没有 arm64 版；Windows on ARM 会自动转译运行
		if runtime.GOARCH == "386" {
			asset = "cloudflared-windows-386.exe"
		}
	default:
		asset = "cloudflared-linux-" + runtime.GOARCH
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Get(base + asset)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download %s: %s", asset, resp.Status)
	}
	var src io.Reader = resp.Body
	if strings.HasSuffix(asset, ".tgz") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return "", err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err != nil {
				return "", fmt.Errorf("cloudflared not in %s: %v", asset, err)
			}
			if filepath.Base(h.Name) == "cloudflared" && h.Typeflag == tar.TypeReg {
				break
			}
		}
		src = tr
	}
	dst := cloudflaredPath()
	os.MkdirAll(filepath.Dir(dst), 0o755)
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, io.LimitReader(src, 200<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	f.Close()
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}
