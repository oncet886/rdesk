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
	"time"
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
	tagWeb       // 浏览器连接（Tailscale 证书）
	tagShare     // 临时分享
	tagShareStop // 停止临时分享
	tagKick      // 断开当前控制端
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
		setItem(tagStatus, fmt.Sprintf(L("● 控制端已连接（%s）"), ip), false, false, false)
	} else {
		setItem(tagStatus, fmt.Sprintf(L("RDesk Host %s · 等待控制端连接"), version), false, false, false)
	}
	setItem(tagKick, L("断开当前控制端"), true, false, !active)
	setItem(tagAddr, fmt.Sprintf(L("地址　　%s　（点击复制）"), L(panel.Addr)), true, false, false)
	setItem(tagID, fmt.Sprintf(L("设备 ID　%s　（点击复制）"), cfg.ID), true, false, false)
	setItem(tagPassword, L("密码　　••••••••　（点击复制，按住 ⌥ 显示）"), true, false, false)
	setItem(tagPasswordShown, fmt.Sprintf(L("密码　　%s　（点击复制）"), cfg.Password), true, false, false)
	setItem(tagCopyAll, L("复制全部连接信息"), true, false, false)

	if ts := tailscaleIP(); ts != "" {
		setItem(tagTailscale, fmt.Sprintf(L("✓ Tailscale 已连接（%s）"), ts), false, false, false)
	} else {
		setItem(tagTailscale, L("⚠️ Tailscale 没连接 — 点击下载或打开"), true, false, false)
	}
	screen, access := permStatus()
	switch {
	case !screen:
		setItem(tagPerms, L("⚠️ 缺少「屏幕录制」权限 — 点击去设置"), true, false, false)
	case !access:
		setItem(tagPerms, L("⚠️ 缺少「辅助功能」权限（只能看不能控）— 点击去设置"), true, false, false)
	default:
		setItem(tagPerms, L("✓ 屏幕录制、辅助功能权限正常"), false, false, false)
	}
	w := webSnapshot()
	switch {
	case w.Link != "":
		setItem(tagWeb, L("浏览器连接（点击复制链接）"), true, false, false)
	case w.CertErr == certErrOff:
		setItem(tagWeb, L("开启浏览器连接…（需在 Tailscale 后台打开 HTTPS）"), true, false, false)
	default:
		setItem(tagWeb, "", false, false, true)
	}
	switch {
	case !w.CanShare:
		setItem(tagShare, "", false, false, true)
	case w.Share != "":
		mins := int(time.Until(time.UnixMilli(w.ShareUntil)).Minutes()) + 1
		setItem(tagShare, fmt.Sprintf(L("● 临时分享中，剩 %d 分钟 — 点击复制链接"), mins), true, false, false)
	case w.ShareBusy:
		setItem(tagShare, L("临时分享：正在准备…"), false, false, false)
	default:
		setItem(tagShare, L("临时分享给别人（1 小时）…"), true, false, false)
	}
	setItem(tagShareStop, L("停止临时分享"), true, false, w.Share == "")
	setItem(tagPanel, L("打开控制面板…"), diagURL != "", false, false)
	setItem(tagNewPassword, L("换新密码…"), true, false, false)
	setItem(tagLogin, L("登录时自动启动"), true, C.rdLoginItem() == 1, C.rdIsBundle() == 0)
	setItem(tagQuit, L("退出 RDesk Host"), true, false, false)
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
		copyText(fmt.Sprintf(L("地址：%s\n设备 ID：%s\n密码：%s"), panel.Addr, cfg.ID, cfg.Password))
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
	case tagWeb:
		if w := webSnapshot(); w.Link != "" {
			copyText(w.Link)
		} else {
			openURL(tsAdminDNS)
		}
	case tagShare:
		if w := webSnapshot(); w.Share != "" {
			copyText(w.Share)
		} else if confirm(L("临时分享给别人？"), L("会生成一个公网链接：对方用浏览器打开、输入密码就能控制这台电脑，不用装任何软件。1 小时后自动关闭，只发给你信任的人。"), L("开始分享")) {
			go startShare()
			openURL(diagURL) // 链接准备好后显示在控制面板里
		}
	case tagShareStop:
		stopShare()
	case tagKick:
		if h := curHost.Load(); h != nil {
			h.kickViewer()
		}
	case tagPanel:
		openURL(diagURL)
	case tagNewPassword:
		if confirm(L("换一个新密码？"), L("正在连接的控制端会断开，需要用新密码重新连接。"), L("换新密码")) {
			if pw, err := newPassword(); err == nil {
				copyText(pw)
				confirm(L("新密码已复制"), fmt.Sprintf(L("新密码：%s\n已复制到剪贴板。"), pw), L("好"))
			}
		}
	case tagLogin:
		C.rdSetLoginItem(cbool(C.rdLoginItem() == 0))
	case tagQuit:
		exitApp(0)
	}
}
