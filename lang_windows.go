//go:build windows

package main

import "syscall"

// 系统界面语言：GetUserDefaultUILanguage 的 LANGID → 语言代码
func systemLang() string {
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage").Call()
	id := uint16(r)
	primary, sub := id&0x3ff, id>>10
	switch primary {
	case 0x04: // 中文：台湾 / 香港 / 澳门用繁体
		if sub == 0x01 || sub == 0x03 || sub == 0x05 {
			return "zh-Hant"
		}
		return "zh-Hans"
	case 0x11:
		return "ja"
	case 0x12:
		return "ko"
	case 0x0a:
		return "es"
	case 0x0c:
		return "fr"
	case 0x07:
		return "de"
	case 0x16:
		return "pt"
	case 0x19:
		return "ru"
	}
	return "en"
}
