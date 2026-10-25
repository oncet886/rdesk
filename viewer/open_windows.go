//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// 用 Edge 的应用模式打开成独立窗口（Windows 10 / 11 自带 Edge）；找不到就用默认浏览器
func openAppWindow(u string) error {
	var cands []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if d := os.Getenv(env); d != "" {
			cands = append(cands, filepath.Join(d, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(d, "Google", "Chrome", "Application", "chrome.exe"))
		}
	}
	for _, p := range cands {
		if _, err := os.Stat(p); err == nil {
			return exec.Command(p, "--app="+u, "--window-size=1280,860").Start()
		}
	}
	c := exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return c.Start()
}
