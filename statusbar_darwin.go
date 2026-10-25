//go:build darwin

package main

/*
#cgo LDFLAGS: -framework AppKit -framework ServiceManagement
#include <stdlib.h>
#include "statusbar_darwin.h"
*/
import "C"

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"
)

const (
	tagStatus = iota
	tagAddr
	tagID
	tagPassword
	tagCopyAll
	tagTailscale
	tagPerms
	tagPanel
	tagNewPassword
	tagLogin
	tagQuit
	_
	tagPasswordShown
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
func goMenuWillOpen() {
	cfg := loadConfig()
	diag.mu.Lock()
	active, ip := diag.active, diag.viewerIP
	diag.mu.Unlock()
	if active {
		if h := strings.LastIndex(ip, ":"); h > 0 {
			ip = strings.Trim(ip[:h], "[]")
		}
		setItem(tagStatus, "● 手机已连接（"+ip+"）", false, false, false)
	} else {
		setItem(tagStatus, "RDesk Host "+version+" · 等待手机连接", false, false, false)
	}
	setItem(tagAddr, "地址　　"+panel.Addr+"　（点击复制）", true, false, false)
	setItem(tagID, "设备 ID　"+cfg.ID+"　（点击复制）", true, false, false)
	setItem(tagPassword, "密码　　••••••••　（点击复制，按住 ⌥ 显示）", true, false, false)
	setItem(tagPasswordShown, "密码　　"+cfg.Password+"　（点击复制）", true, false, false)
	setItem(tagCopyAll, "复制全部连接信息", true, false, false)

	if ts := tailscaleIP(); ts != "" {
		setItem(tagTailscale, "✓ Tailscale 已连接（"+ts+"）", false, false, false)
	} else {
		setItem(tagTailscale, "⚠️ Tailscale 没连接 — 点击下载或打开", true, false, false)
	}
	screen, access := permStatus()
	switch {
	case !screen:
		setItem(tagPerms, "⚠️ 缺少「屏幕录制」权限 — 点击去设置", true, false, false)
	case !access:
		setItem(tagPerms, "⚠️ 缺少「辅助功能」权限（只能看不能控）— 点击去设置", true, false, false)
	default:
		setItem(tagPerms, "✓ 屏幕录制、辅助功能权限正常", false, false, false)
	}
	setItem(tagPanel, "打开控制面板…", diagURL != "", false, false)
	setItem(tagNewPassword, "换新密码…", true, false, false)
	setItem(tagLogin, "登录时自动启动", true, C.rdLoginItem() == 1, C.rdIsBundle() == 0)
	setItem(tagQuit, "退出 RDesk Host", true, false, false)
}

//export goMenuAction
func goMenuAction(tag C.int) {
	cfg := loadConfig()
	switch int(tag) {
	case tagAddr:
		copyText(panel.Addr)
	case tagID:
		copyText(cfg.ID)
	case tagPassword, tagPasswordShown:
		copyText(cfg.Password)
	case tagCopyAll:
		copyText(fmt.Sprintf("地址：%s\n设备 ID：%s\n密码：%s", panel.Addr, cfg.ID, cfg.Password))
	case tagTailscale:
		if _, err := os.Stat("/Applications/Tailscale.app"); err == nil {
			openURL("file:///Applications/Tailscale.app")
		} else {
			openURL("https://tailscale.com/download/mac")
		}
	case tagPerms:
		screen, _ := permStatus()
		if !screen {
			openURL("x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture")
		} else {
			openURL("x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility")
		}
	case tagPanel:
		openURL(diagURL)
	case tagNewPassword:
		if confirm("换一个新密码？", "正在连接的手机会断开，需要用新密码重新连接。", "换新密码") {
			if pw, err := newPassword(); err == nil {
				copyText(pw)
				confirm("新密码已复制", "新密码："+pw+"\n已复制到剪贴板。", "好")
			}
		}
	case tagLogin:
		C.rdSetLoginItem(cbool(C.rdLoginItem() == 0))
	case tagQuit:
		os.Exit(0)
	}
}
