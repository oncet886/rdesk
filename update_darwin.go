//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

func updateKey() string { return "darwin" }

// 正在运行的 .app（命令行版不自动替换）
func appBundle() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	if !strings.HasSuffix(filepath.Dir(exe), ".app/Contents/MacOS") {
		return ""
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(exe)))
}

func updateSupported() bool {
	app := appBundle()
	return app != "" && syscall.Access(filepath.Dir(app), 2 /* W_OK */) == nil
}

var sigRe = regexp.MustCompile(`(?m)^(Identifier|TeamIdentifier)=(.+)$`)

// codesign 给出的程序标识和开发者团队
func signer(app string) (string, error) {
	out, err := exec.Command("codesign", "-dv", app).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("codesign: %s", strings.TrimSpace(string(out)))
	}
	var id, team string
	for _, m := range sigRe.FindAllStringSubmatch(string(out), -1) {
		if m[1] == "Identifier" {
			id = m[2]
		} else {
			team = m[2]
		}
	}
	if id == "" || team == "" || team == "not set" {
		return "", errors.New("app is not signed")
	}
	return id + " / " + team, nil
}

func applyUpdate(zip string) error {
	app := appBundle()
	if app == "" {
		return errors.New("not running from an app bundle")
	}
	if err := replaceApp(app, zip); err != nil {
		return err
	}
	// 等这个进程退出后再打开新版（同时只能有一个被控端在跑）
	sh := fmt.Sprintf(`while kill -0 %d 2>/dev/null; do sleep 0.2; done; open %q`, os.Getpid(), app)
	cmd := exec.Command("/bin/sh", "-c", sh)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exitApp(0)
	return nil
}

// 用 zip 里的新 App 替换 app（核对签名后一次改名换掉）
func replaceApp(app, zip string) error {
	// 临时目录放在 App 旁边：同一个磁盘，替换是一次改名
	tmp, err := os.MkdirTemp(filepath.Dir(app), ".rdesk-update-")
	if err != nil {
		return err
	}
	if out, err := exec.Command("ditto", "-x", "-k", zip, tmp).CombinedOutput(); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("unzip: %v %s", err, out)
	}
	apps, _ := filepath.Glob(filepath.Join(tmp, "*.app"))
	if len(apps) != 1 {
		os.RemoveAll(tmp)
		return errors.New("no app in update")
	}
	newApp := apps[0]
	// 新程序必须签名完好，而且和正在运行的是同一个程序、同一个开发者
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", newApp).CombinedOutput(); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("codesign verify: %s", strings.TrimSpace(string(out)))
	}
	want, err1 := signer(app)
	got, err2 := signer(newApp)
	if err1 != nil || err2 != nil || want != got {
		os.RemoveAll(tmp)
		return fmt.Errorf("signer mismatch: %q vs %q", got, want)
	}
	old := filepath.Join(tmp, "old.app")
	if err := os.Rename(app, old); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(newApp, app); err != nil {
		os.Rename(old, app) // 放回去
		os.RemoveAll(tmp)
		return err
	}
	os.RemoveAll(tmp)
	return nil
}

// 上次升级失败留下的临时目录
func updateCleanup() {
	if app := appBundle(); app != "" {
		old, _ := filepath.Glob(filepath.Join(filepath.Dir(app), ".rdesk-update-*"))
		for _, d := range old {
			os.RemoveAll(d)
		}
	}
}

func waitOldInstance() {}
