//go:build !windows && !darwin

package main

import "os/exec"

// Linux：有 Chrome / Chromium / Edge 就用应用模式开独立窗口，否则用默认浏览器
func openAppWindow(u string) error {
	for _, b := range []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge"} {
		if p, err := exec.LookPath(b); err == nil {
			return exec.Command(p, "--app="+u, "--window-size=1280,860").Start()
		}
	}
	return exec.Command("xdg-open", u).Start()
}
