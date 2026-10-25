//go:build !windows

package main

import "errors"

// Mac 用菜单栏「登录时自动启动」（系统登录项），这里只给 Windows 用
func autostartSupported() bool { return false }
func autostartEnabled() bool   { return false }
func setAutostart(bool) error  { return errors.New("not supported") }
func autostartInit()           {}
