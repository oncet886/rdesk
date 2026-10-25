//go:build darwin

package main

import (
	"os"
	"os/exec"
)

// 有 Chrome / Edge 就用应用模式开独立窗口，否则用默认浏览器
func openAppWindow(u string) error {
	for _, app := range []string{"Google Chrome", "Microsoft Edge", "Chromium"} {
		if _, err := os.Stat("/Applications/" + app + ".app"); err == nil {
			return exec.Command("open", "-na", app, "--args", "--app="+u, "--window-size=1280,860").Start()
		}
	}
	return exec.Command("open", u).Start()
}
