//go:build windows

package main

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func updateKey() string { return "windows-" + runtime.GOARCH }

func selfExe() string {
	exe, _ := os.Executable()
	return exe
}

// 程序所在文件夹能写才能自动替换
func updateSupported() bool {
	f, err := os.CreateTemp(filepath.Dir(selfExe()), ".rdesk-write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

func applyUpdate(zipPath string) error {
	exe := selfExe()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	var src *zip.File
	for _, f := range zr.File {
		if strings.EqualFold(filepath.Base(f.Name), "rdesk.exe") {
			src = f
		}
	}
	if src == nil {
		return errors.New("rdesk.exe not in update")
	}
	rc, err := src.Open()
	if err != nil {
		return err
	}
	newExe := exe + ".new"
	out, err := os.Create(newExe)
	if err == nil {
		_, err = io.Copy(out, io.LimitReader(rc, 200<<20))
		out.Close()
	}
	rc.Close()
	if err != nil {
		os.Remove(newExe)
		return err
	}
	// 正在运行的 exe 不能覆盖，但可以改名：旧的改成 .old（下次启动删掉），新的放到原名
	os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		os.Remove(newExe)
		return err
	}
	if err := os.Rename(newExe, exe); err != nil {
		os.Rename(exe+".old", exe)
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Dir, _ = os.Getwd()
	cmd.Env = append(os.Environ(), "RDESK_WAIT_PID="+strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010} // CREATE_NEW_CONSOLE：新版自己的窗口
	if err := cmd.Start(); err != nil {
		os.Rename(exe, newExe)
		os.Rename(exe+".old", exe)
		return err
	}
	exitApp(0)
	return nil
}

func updateCleanup() { os.Remove(selfExe() + ".old") }

// 升级后的新进程：等旧进程退出（让出端口）再继续，最多等 15 秒
func waitOldInstance() {
	pid, _ := strconv.Atoi(os.Getenv("RDESK_WAIT_PID"))
	os.Unsetenv("RDESK_WAIT_PID")
	if pid <= 0 {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		done := make(chan struct{})
		go func() { p.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	}
	time.Sleep(300 * time.Millisecond)
}
