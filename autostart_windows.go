//go:build windows

package main

import (
	"log"
	"os"

	"golang.org/x/sys/windows/registry"
)

// 登录时自动启动：当前用户的「启动」项（HKCU\...\Run），不需要管理员权限
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runName = "RDesk Host"

func autostartSupported() bool { return true }

func autostartCmd() string {
	exe, _ := os.Executable()
	return `"` + exe + `"`
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runName)
	return err == nil && v != ""
}

func setAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(runName, autostartCmd())
	}
	if err := k.DeleteValue(runName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

// 第一次运行默认打开；之后如果程序被挪了位置，启动项跟着改到新路径
func autostartInit() {
	cfg := loadConfig()
	if !cfg.AutostartAsked {
		if err := setAutostart(true); err == nil {
			log.Print(L("已设置登录时自动启动（可在控制面板关闭）"))
		}
		cfg.AutostartAsked = true
		saveConfig(cfg)
		return
	}
	if autostartEnabled() {
		setAutostart(true)
	}
}
