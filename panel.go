package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// 本机控制面板（http://127.0.0.1:21131）：显示远程地址 / ID / 密码、权限状态，可换密码、退出。
// 只监听 127.0.0.1；另外校验 Host 头（防 DNS 重绑定）和自定义请求头（防其他网站跨站调用）。

type panelState struct {
	Addr string `json:"addr"`
	Mode string `json:"mode"` // tailscale / cloudflare / relay / local
}

var panel panelState

func localOnly(r *http.Request) bool {
	h := r.Host
	return strings.HasPrefix(h, "127.0.0.1:") || strings.HasPrefix(h, "localhost:")
}

func registerPanel(mux *http.ServeMux) {
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("rdesk")) })
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		if !localOnly(r) || r.Header.Get("X-RDesk") != "1" {
			http.Error(w, "forbidden", 403)
			return
		}
		cfg := loadConfig()
		screen, access := permStatus()
		_, appErr := os.Stat("/Applications/Tailscale.app")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]any{
			"addr": L(panel.Addr), "mode": panel.Mode, "id": cfg.ID, "password": cfg.Password,
			"lang": langCode(), "os": runtime.GOOS,
			"web":     webSnapshot(),
			"version": version, "screen": screen, "access": access,
			"tsIP": tailscaleIP(), "tsInstalled": appErr == nil || tailscaleBin() != "",
		})
	})
	mux.HandleFunc("/api/newpw", func(w http.ResponseWriter, r *http.Request) {
		if !localOnly(r) || r.Method != "POST" || r.Header.Get("X-RDesk") != "1" {
			http.Error(w, "forbidden", 403)
			return
		}
		if _, err := newPassword(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/share", func(w http.ResponseWriter, r *http.Request) {
		if !localOnly(r) || r.Method != "POST" || r.Header.Get("X-RDesk") != "1" {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.URL.Query().Get("on") == "1" {
			go startShare()
		} else {
			stopShare()
		}
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if !localOnly(r) || r.Method != "POST" || r.Header.Get("X-RDesk") != "1" {
			http.Error(w, "forbidden", 403)
			return
		}
		w.Write([]byte("bye"))
		go func() { time.Sleep(300 * time.Millisecond); exitApp(0) }()
	})
}

// 生成新密码：立刻生效并保存，正在连接的控制端会被断开
func newPassword() (string, error) {
	h := curHost.Load()
	if h == nil {
		return "", errors.New("被控端还没启动好")
	}
	cfg := loadConfig()
	cfg.Password = randPassword(8)
	if err := h.setPassword(cfg.Password); err != nil {
		return "", err
	}
	saveConfig(cfg)
	return cfg.Password, nil
}
