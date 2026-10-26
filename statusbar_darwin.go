//go:build darwin

package main

/*
#cgo LDFLAGS: -framework AppKit -framework ServiceManagement
#include <stdlib.h>
#include "statusbar_darwin.h"
*/
import "C"

import (
	"os"
	"runtime"
	"unsafe"
)

// AppKit 必须跑在进程的主线程上：把 main goroutine 固定在主线程
func init() { runtime.LockOSThread() }

// 有图形界面（不是 ssh 远程登录）时才显示菜单栏图标
func menubarAvailable() bool { return os.Getenv("SSH_CONNECTION") == "" }

func statusRun() {
	onConnectedChange = func(on bool) { C.rdStatusConnected(cbool(on)) }
	C.rdStatusRun()
}

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

func setItem(tag int, title string, enabled, checked, hidden bool) {
	cs := C.CString(title)
	defer C.free(unsafe.Pointer(cs))
	C.rdStatusSet(C.int(tag), cs, cbool(enabled), cbool(checked), cbool(hidden))
}

func cstr(s string, f func(*C.char)) {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	f(cs)
}

func copyText(s string) { cstr(s, func(c *C.char) { C.rdPasteboardCopy(c) }) }
func openURL(u string)  { cstr(u, func(c *C.char) { C.rdOpenURL(c) }) }

func confirm(title, msg, ok string) bool {
	t, m, o := C.CString(title), C.CString(msg), C.CString(ok)
	defer C.free(unsafe.Pointer(t))
	defer C.free(unsafe.Pointer(m))
	defer C.free(unsafe.Pointer(o))
	return C.rdConfirm(t, m, o) == 1
}

// 每次点开菜单时刷新内容
//
//export goMenuWillOpen
func goMenuWillOpen() { menuWillOpen() }

//export goMenuAction
func goMenuAction(tag C.int) { menuAction(int(tag)) }

const isMac = true

func loginItemSupported() bool { return C.rdIsBundle() == 1 }
func loginItemOn() bool        { return C.rdLoginItem() == 1 }
func setLoginItem(on bool)     { C.rdSetLoginItem(cbool(on)) }

func openTailscale() {
	if _, err := os.Stat("/Applications/Tailscale.app"); err == nil {
		openURL("file:///Applications/Tailscale.app")
	} else {
		openURL("https://tailscale.com/download/mac")
	}
}

func beforeExit() {}

func trayLogInit() {}
