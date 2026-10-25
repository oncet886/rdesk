package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

const version = "0.2.10"

func usage() {
	fmt.Print(`rdesk ` + version + ` —— 走隧道的远程桌面（类 RustDesk）

用法:
  rdesk                      被控端一键模式：有 Tailscale 就走 Tailscale 直连（像 RustDesk 的 IP 直连），
                             没有就自动开 Cloudflare 隧道
  rdesk host -relay URL      被控端，连到你自己部署的中继（如 https://relay.example.com）
  rdesk relay [-listen :8080] [-tunnel] [-key 密钥]
                             只跑中继服务器（放 VPS 上，或配合 -tunnel 用 Cloudflare 隧道暴露）

被控端可选参数:
  -id ID          指定设备 ID（默认自动生成并保存）
  -password 密码  指定连接密码（默认自动生成并保存）
  -new-password   重新生成随机密码
  -key 密钥       中继服务器要求的注册密钥
  -tunnel         强制走 Cloudflare 隧道（即使装了 Tailscale）
  -no-tunnel      一键模式下不开隧道（只能本机 http://localhost 访问，用于测试）
  -port 端口      本地监听端口（直连默认 21130，其他默认随机）

控制端：用浏览器（电脑/手机都行）打开中继网址，输入 ID 和密码即可。
`)
}

func main() {
	log.SetFlags(log.Ltime)
	waitOldInstance() // 刚升级完的新进程：等旧进程退出
	if os.Getenv("RDESK_BENCH") == "1" {
		benchAndExit()
	}
	if os.Getenv("RDESK_APP") == "1" {
		appMode()
	}
	args := os.Args[1:]
	cmd := "host"
	if len(args) > 0 && (args[0] == "host" || args[0] == "relay") {
		cmd, args = args[0], args[1:]
	} else if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		usage()
		return
	}
	switch cmd {
	case "relay":
		fs := flag.NewFlagSet("relay", flag.ExitOnError)
		listen := fs.String("listen", ":8080", "监听地址")
		key := fs.String("key", "", "被控端注册密钥（可选）")
		tunnel := fs.Bool("tunnel", false, "用 cloudflared 开公网隧道")
		cert := fs.String("tls-cert", "", "TLS 证书（可选）")
		tkey := fs.String("tls-key", "", "TLS 私钥（可选）")
		fs.Parse(args)
		r := newRelay(*key)
		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("中继服务器已启动 %s", ln.Addr())
		if *tunnel {
			go runTunnel(ln.Addr().(*net.TCPAddr).Port, func(u string) {
				log.Printf("🌐 公网地址: %s", u)
			})
		}
		srv := &http.Server{Handler: r.handler(), ReadHeaderTimeout: 10 * time.Second}
		if *cert != "" {
			log.Fatal(srv.ServeTLS(ln, *cert, *tkey))
		}
		log.Fatal(srv.Serve(ln))

	case "host":
		fs := flag.NewFlagSet("host", flag.ExitOnError)
		fs.Usage = usage
		relayURL := fs.String("relay", "", "中继地址")
		id := fs.String("id", "", "设备 ID")
		pw := fs.String("password", "", "连接密码")
		newPw := fs.Bool("new-password", false, "重新生成密码")
		key := fs.String("key", "", "中继注册密钥")
		noTunnel := fs.Bool("no-tunnel", false, "不开隧道")
		forceTunnel := fs.Bool("tunnel", false, "强制 Cloudflare 隧道")
		port := fs.Int("port", 0, "本地中继端口")
		noMenubar := fs.Bool("no-menubar", false, "不显示菜单栏图标")
		fs.Parse(args)

		cfg := loadConfig()
		firstRun := cfg.ID == ""
		if cfg.Diag {
			diagEnabled = true
		}
		if *id != "" {
			cfg.ID = *id
		}
		if *pw != "" {
			cfg.Password = *pw
		}
		if *newPw {
			cfg.Password = ""
		}
		if cfg.ID == "" {
			cfg.ID = randDigits(9)
		}
		if cfg.Password == "" {
			cfg.Password = randPassword(8)
		}
		if !validID(cfg.ID) {
			log.Fatal("ID 只能是 6-32 位字母数字")
		}
		saveConfig(cfg)

		o := hostOpts{id: cfg.ID, password: cfg.Password, key: *key}
		diagURL = startDiag()
		// 第一次打开才自动弹出控制面板；之后从菜单栏图标进
		if os.Getenv("RDESK_APP") == "1" && diagURL != "" && firstRun {
			go func() { time.Sleep(800 * time.Millisecond); exec.Command("open", diagURL).Run() }()
		}
		run := func() { startHostCmd(cfg, o, *relayURL, *forceTunnel, *noTunnel, *port) }
		// 菜单栏图标要占主线程：远程逻辑放到后台
		if !*noMenubar && menubarAvailable() {
			go run()
			statusRun()
		} else {
			run()
		}
	}
}

func startHostCmd(cfg config, o hostOpts, relayURL string, forceTunnel, noTunnel bool, port int) {
	go updateLoop()
	autostartInit()
	{
		if relayURL != "" {
			o.relayURL = normalizeRelay(relayURL, "/ws/host")
			panel = panelState{Addr: httpURL(relayURL), Mode: "relay"}
			banner(cfg, httpURL(relayURL))
		} else {
			// 没有 Tailscale 也没装 cloudflared：等 Tailscale 打开（控制面板会提示下载/打开），打开后自动直连
			if tailscaleIP() == "" && !forceTunnel && !noTunnel && findCloudflared() == "" {
				panel = panelState{Addr: "等待 Tailscale 连接…", Mode: "waiting"}
				log.Print(L("没检测到 Tailscale，请安装并登录 Tailscale：https://tailscale.com/download （打开后会自动继续）"))
				for tailscaleIP() == "" {
					time.Sleep(3 * time.Second)
				}
			}
			// Tailscale 直连：只监听 Tailscale 网卡，控制端在同一个 tailnet 里点对点直连，没有中转
			if ts := tailscaleIP(); ts != "" && !forceTunnel && !noTunnel {
				pt := port
				if pt == 0 {
					pt = 21130 // 避开 RustDesk 的 21115-21119
				}
				// 端口已被别的程序（比如 RustDesk）占着就顺延，绝不抢别人的连接
				for i := 0; i < 20 && portBusy(ts, pt); i++ {
					log.Printf(L("端口 %d 已被其他程序使用，换下一个"), pt)
					pt++
				}
				addr := net.JoinHostPort(ts, fmt.Sprint(pt))
				ln, err := net.Listen("tcp", addr)
				if err != nil {
					log.Fatalf("监听 %s 失败: %v（端口被占用可加 -port 换一个）", addr, err)
				}
				serveTailscale(ln, newRelay("").handler(), pt)
				o.relayURL = "ws://" + addr + "/ws/host"
				log.Print(L("Tailscale 直连模式（不经过任何中转服务器）"))
				panel = panelState{Addr: addr, Mode: "tailscale"}
				banner(cfg, addr)
				runHost(o)
				return
			}
			// 一键模式：内置中继 + 隧道
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				log.Fatal(err)
			}
			p := ln.Addr().(*net.TCPAddr).Port
			srv := &http.Server{Handler: newRelay("").handler(), ReadHeaderTimeout: 10 * time.Second}
			go srv.Serve(ln)
			o.relayURL = fmt.Sprintf("ws://127.0.0.1:%d/ws/host", p)
			local := fmt.Sprintf("http://localhost:%d", p)
			if noTunnel {
				panel = panelState{Addr: local, Mode: "local"}
				banner(cfg, local)
			} else {
				log.Printf(L("正在打开 Cloudflare 隧道…（本机测试地址 %s）"), local)
				panel = panelState{Addr: "正在打开 Cloudflare 隧道…", Mode: "cloudflare"}
				go runTunnel(p, func(u string) { panel.Addr = u; banner(cfg, u) })
			}
		}
		runHost(o)
	}
}

var diagURL string

func banner(cfg config, u string) {
	if langCol < 0 {
		fmt.Printf(`
╔══════════════════════════════════════════════════╗
  远程地址:  %s
  设备 ID :  %s
  密    码:  %s
  控制面板:  %s （只能在这台电脑上打开）
╚══════════════════════════════════════════════════╝

`, u, cfg.ID, cfg.Password, diagURL)
		return
	}
	fmt.Printf("\n══════════════════════════════════════════════════\n  %s:  %s\n  %s:  %s\n  %s:  %s\n  %s:  %s %s\n══════════════════════════════════════════════════\n\n",
		L("远程地址"), u, L("设备 ID"), cfg.ID, L("密码"), cfg.Password, L("控制面板"), diagURL, L("（只能在这台电脑上打开）"))
}

func httpURL(s string) string {
	u := normalizeRelay(s, "")
	if len(u) > 6 && u[:6] == "wss://" {
		return "https://" + u[6:]
	}
	return "http://" + u[5:]
}

// ---------------- 配置 ----------------

type config struct {
	ID       string `json:"id"`
	Password string `json:"password"`
	Diag     bool   `json:"diag,omitempty"` // 开发者自用：打开连接诊断
	// 关掉自动更新（默认开）
	NoAutoUpdate bool `json:"noAutoUpdate,omitempty"`
	// Windows：已经按默认设置过登录时自动启动（之后以用户的选择为准）
	AutostartAsked bool `json:"autostartAsked,omitempty"`
}

func configPath() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "rdesk", "config.json")
}

func loadConfig() config {
	var c config
	b, err := os.ReadFile(configPath())
	if err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

func saveConfig(c config) {
	p := configPath()
	os.MkdirAll(filepath.Dir(p), 0o700)
	b, _ := json.MarshalIndent(c, "", "  ")
	os.WriteFile(p, b, 0o600)
}

func randFrom(alpha string, n int) string {
	out := make([]byte, n)
	for i := range out {
		v, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alpha))))
		out[i] = alpha[v.Int64()]
	}
	return string(out)
}

func randDigits(n int) string {
	s := randFrom("0123456789", n)
	if s[0] == '0' {
		s = "1" + s[1:]
	}
	return s
}

// 去掉容易看错的 0/O/1/l/I
func randPassword(n int) string { return randFrom("abcdefghijkmnpqrstuvwxyz23456789", n) }

// ---------------- Cloudflare 隧道 ----------------

var tunnelRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

func findCloudflared() string {
	if exe, err := os.Executable(); err == nil {
		for _, n := range []string{"cloudflared", "cloudflared.exe"} {
			p := filepath.Join(filepath.Dir(exe), n)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	if p := cloudflaredPath(); fileExists(p) {
		return p
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/cloudflared", "/usr/local/bin/cloudflared"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// 用 cloudflared 的免费快速隧道把本地中继暴露到公网（自带 https，无需域名、无需 VPS）
// 隧道掉了会自动重开（网址会变，会重新打印）
func runTunnel(port int, onURL func(string)) {
	bin := findCloudflared()
	if bin == "" {
		log.Print(L("❌ 没找到 cloudflared。Mac 运行: brew install cloudflared ；Windows 把 cloudflared.exe 放到本程序同一目录"))
		return
	}
	for {
		cmd := exec.Command(bin, "tunnel", "--no-autoupdate", "--url", fmt.Sprintf("http://127.0.0.1:%d", port))
		stderr, _ := cmd.StderrPipe()
		if err := cmd.Start(); err != nil {
			log.Printf(L("启动 cloudflared 失败: %v"), err)
			return
		}
		sc := bufio.NewScanner(stderr)
		found := false
		for sc.Scan() {
			if !found {
				if u := tunnelRe.FindString(sc.Text()); u != "" {
					found = true
					onURL(u)
				}
			}
		}
		cmd.Wait()
		log.Print(L("隧道断开，5 秒后重开…"))
		time.Sleep(5 * time.Second)
	}
}

// 找本机的 Tailscale 地址（100.64.0.0/10）
func tailscaleIP() string {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && cgnat.Contains(n.IP) {
				return n.IP.String()
			}
		}
	}
	return ""
}

// 有程序在这个端口上应答 = 被占用（含监听 0.0.0.0 的程序，单纯 bind 检测不出来）
func portBusy(ip string, port int) bool {
	for _, h := range []string{ip, "127.0.0.1"} {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(h, fmt.Sprint(port)), 300*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
	}
	return false
}

// 双击「RDesk Host.app」启动：已经在运行就只打开控制面板；否则日志写到 ~/Library/Logs/RDesk/
func appMode() {
	c := http.Client{Timeout: 500 * time.Millisecond}
	if r, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/ping", diagPort)); err == nil {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if string(b) == "rdesk" {
			exec.Command("open", fmt.Sprintf("http://127.0.0.1:%d", diagPort)).Run()
			os.Exit(0)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, "Library", "Logs", "RDesk")
		os.MkdirAll(dir, 0o755)
		if f, err := os.OpenFile(filepath.Join(dir, "rdesk.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			log.SetOutput(f)
			os.Stdout = f
			os.Stderr = f
		}
	}
}
