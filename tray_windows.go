//go:build windows

package main

/*
#cgo LDFLAGS: -lshell32 -luser32
#include <stdlib.h>
#include "tray_windows.h"
*/
import "C"

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// 托盘的消息循环必须一直在同一个系统线程上：把 main goroutine 固定在主线程
func init() { runtime.LockOSThread() }

const isMac = false

func menubarAvailable() bool { return true }

func statusRun() {
	onConnectedChange = func(on bool) { updateTip(on) }
	updateTip(false)
	go func() {
		time.Sleep(3 * time.Second)
		var ok, e, w C.int
		C.rdTrayDiag(&ok, &e, &w)
		log.Printf("tray: add=%d err=%d wndErr=%d", int(ok), int(e), int(w))
	}()
	C.rdTrayRun()
}

func updateTip(on bool) {
	tip := fmt.Sprintf(L("RDesk Host %s · 等待控制端连接"), version)
	if on {
		diag.mu.Lock()
		ip := diag.viewerIP
		diag.mu.Unlock()
		if h := strings.LastIndex(ip, ":"); h > 0 {
			ip = strings.Trim(ip[:h], "[]")
		}
		tip = fmt.Sprintf(L("● 控制端已连接（%s）"), ip)
	}
	cstr(tip, func(c *C.char) { C.rdTraySetTip(c) })
}

//export goMenuWillOpen
func goMenuWillOpen() { menuWillOpen() }

//export goMenuAction
func goMenuAction(tag C.int) { menuAction(int(tag)) }

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

func cstr(s string, f func(*C.char)) {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	f(cs)
}

func setItem(tag int, title string, enabled, checked, hidden bool) {
	cstr(title, func(c *C.char) { C.rdTraySet(C.int(tag), c, cbool(enabled), cbool(checked), cbool(hidden)) })
}

func copyText(s string) { cstr(s, func(c *C.char) { C.rdCopyText(c) }) }
func openURL(u string)  { cstr(u, func(c *C.char) { C.rdOpenURL(c) }) }

// Windows 消息框的按钮文字由系统决定（确定 / 取消），ok 参数不用
func confirm(title, msg, ok string) bool {
	t, m := C.CString(title), C.CString(msg)
	defer C.free(unsafe.Pointer(t))
	defer C.free(unsafe.Pointer(m))
	okCancel := C.int(1)
	if ok == L("好") {
		okCancel = 0
	}
	return C.rdConfirm(t, m, okCancel) == 1
}

func loginItemSupported() bool { return true }
func loginItemOn() bool        { return autostartEnabled() }
func setLoginItem(on bool)     { setAutostart(on) }

func openTailscale() {
	for _, p := range []string{os.Getenv("ProgramFiles") + `\Tailscale\tailscale-ipn.exe`, `C:\Program Files\Tailscale\tailscale-ipn.exe`} {
		if _, err := os.Stat(p); err == nil {
			openURL(p)
			return
		}
	}
	openURL("https://tailscale.com/download/windows")
}

// 没有控制台窗口（托盘程序）：日志写到 %LOCALAPPDATA%\RDesk\rdesk.log
func trayLogInit() {
	// 被旧版（0.2.12 及以前）升级启动时会带着一个新开的控制台窗口：脱离它。
	// 否则那个窗口一直挂着，用户关掉它（或关掉终端）时被控端也跟着退出
	syscall.NewLazyDLL("kernel32.dll").NewProc("FreeConsole").Call()

	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "RDesk")
	if os.Getenv("LOCALAPPDATA") == "" {
		return
	}
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "rdesk.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 5<<20 { // 超过 5 MB 换新
		os.Rename(p, p+".old")
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(f)
		os.Stdout = f
		os.Stderr = f
	}
}

func trayExit() { C.rdTrayRemove() }

func beforeExit() { trayExit() }
