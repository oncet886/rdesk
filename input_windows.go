//go:build windows

package main

import (
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procSendInput   = user32.NewProc("SendInput")
	procGetMetrics  = user32.NewProc("GetSystemMetrics")
	procSetDPIAware = user32.NewProc("SetProcessDPIAware")
	procSetDPICtx   = user32.NewProc("SetProcessDpiAwarenessContext")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseMoveF     = 0x0001
	mouseLeftDown  = 0x0002
	mouseLeftUp    = 0x0004
	mouseRightDown = 0x0008
	mouseRightUp   = 0x0010
	mouseMidDown   = 0x0020
	mouseMidUp     = 0x0040
	mouseWheelF    = 0x0800
	mouseHWheel    = 0x1000
	mouseVirtDesk  = 0x4000
	mouseAbsolute  = 0x8000

	keyExtended = 0x0001
	keyUp       = 0x0002
	keyUnicode  = 0x0004
)

// INPUT 结构（64 位下 40 字节）
type mouseInput struct {
	typ       uint32
	_         uint32
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	_         uint32
	extra     uintptr
}

type keybdInput struct {
	typ   uint32
	_     uint32
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	_     uint32
	extra uintptr
	_     [8]byte
}

func sendInputs(p unsafe.Pointer, n int, size uintptr) {
	procSendInput.Call(uintptr(n), uintptr(p), size)
}

var win struct {
	sync.Mutex
	keys    map[string]bool
	buttons [3]bool
	x, y    float64
	wheelX  int
	wheelY  int
}

func init() {
	// 让截屏拿到真实像素，坐标也按真实像素算
	if procSetDPICtx.Find() == nil {
		procSetDPICtx.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	} else {
		procSetDPIAware.Call()
	}
}

func inputInit() error     { win.keys = map[string]bool{}; return nil }
func inputAvailable() bool { return true }

func metric(i int) int32 { r, _, _ := procGetMetrics.Call(uintptr(i)); return int32(r) }

func absXY(x, y float64) (int32, int32) {
	vx, vy, vw, vh := float64(metric(76)), float64(metric(77)), float64(metric(78)), float64(metric(79))
	if vw <= 1 || vh <= 1 {
		return 0, 0
	}
	return int32((x - vx) * 65535 / (vw - 1)), int32((y - vy) * 65535 / (vh - 1))
}

func sendMouse(flags uint32, x, y float64, data uint32) {
	ax, ay := absXY(x, y)
	m := mouseInput{typ: inputMouse, dx: ax, dy: ay, mouseData: data, flags: flags | mouseAbsolute | mouseVirtDesk | mouseMoveF}
	sendInputs(unsafe.Pointer(&m), 1, unsafe.Sizeof(m))
}

func mouseMove(x, y float64) {
	win.Lock()
	defer win.Unlock()
	win.x, win.y = x, y
	sendMouse(0, x, y, 0)
}

func mouseButton(btn int, down bool, x, y float64) {
	if btn < 0 || btn > 2 {
		return
	}
	win.Lock()
	defer win.Unlock()
	win.x, win.y = x, y
	win.buttons[btn] = down
	f := [3][2]uint32{{mouseLeftUp, mouseLeftDown}, {mouseMidUp, mouseMidDown}, {mouseRightUp, mouseRightDown}}
	i := 0
	if down {
		i = 1
	}
	sendMouse(f[btn][i], x, y, 0)
}

func mouseWheel(dx, dy int) {
	win.Lock()
	defer win.Unlock()
	// 浏览器 100 像素 ≈ Windows 一格 (120)
	win.wheelY += -dy * 120 / 100
	win.wheelX += dx * 120 / 100
	if win.wheelY != 0 {
		m := mouseInput{typ: inputMouse, mouseData: uint32(int32(win.wheelY)), flags: mouseWheelF}
		sendInputs(unsafe.Pointer(&m), 1, unsafe.Sizeof(m))
		win.wheelY = 0
	}
	if win.wheelX != 0 {
		m := mouseInput{typ: inputMouse, mouseData: uint32(int32(win.wheelX)), flags: mouseHWheel}
		sendInputs(unsafe.Pointer(&m), 1, unsafe.Sizeof(m))
		win.wheelX = 0
	}
}

func sendKey(vk uint16, down bool) {
	k := keybdInput{typ: inputKeyboard, vk: vk}
	if !down {
		k.flags |= keyUp
	}
	if extendedKeys[vk] {
		k.flags |= keyExtended
	}
	sendInputs(unsafe.Pointer(&k), 1, unsafe.Sizeof(k))
}

func keyEvent(code string, down bool) {
	vk, ok := winKeys[code]
	if !ok {
		return
	}
	win.Lock()
	defer win.Unlock()
	if down {
		win.keys[code] = true
	} else {
		delete(win.keys, code)
	}
	sendKey(vk, down)
}

func typeText(s string) {
	win.Lock()
	defer win.Unlock()
	for _, r := range s {
		if r == '\n' {
			sendKey(0x0D, true)
			sendKey(0x0D, false)
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			for _, up := range []bool{false, true} {
				k := keybdInput{typ: inputKeyboard, scan: u, flags: keyUnicode}
				if up {
					k.flags |= keyUp
				}
				sendInputs(unsafe.Pointer(&k), 1, unsafe.Sizeof(k))
			}
		}
	}
}

func releaseAll() {
	win.Lock()
	keys := win.keys
	win.keys = map[string]bool{}
	btns := win.buttons
	x, y := win.x, win.y
	win.Unlock()
	for c := range keys {
		if vk, ok := winKeys[c]; ok {
			sendKey(vk, false)
		}
	}
	for b, d := range btns {
		if d {
			mouseButton(b, false, x, y)
		}
	}
}

var extendedKeys = map[uint16]bool{0x21: true, 0x22: true, 0x23: true, 0x24: true, 0x25: true, 0x26: true,
	0x27: true, 0x28: true, 0x2D: true, 0x2E: true, 0x5B: true, 0x5C: true, 0x5D: true, 0x6F: true,
	0xA3: true, 0xA5: true, 0x90: true}

// KeyboardEvent.code -> Windows 虚拟键码
var winKeys = func() map[string]uint16 {
	m := map[string]uint16{
		"Backspace": 0x08, "Tab": 0x09, "Enter": 0x0D, "NumpadEnter": 0x0D, "ShiftLeft": 0xA0, "ShiftRight": 0xA1,
		"ControlLeft": 0xA2, "ControlRight": 0xA3, "AltLeft": 0xA4, "AltRight": 0xA5, "Pause": 0x13,
		"CapsLock": 0x14, "Escape": 0x1B, "Space": 0x20, "PageUp": 0x21, "PageDown": 0x22, "End": 0x23,
		"Home": 0x24, "ArrowLeft": 0x25, "ArrowUp": 0x26, "ArrowRight": 0x27, "ArrowDown": 0x28,
		"PrintScreen": 0x2C, "Insert": 0x2D, "Delete": 0x2E, "MetaLeft": 0x5B, "MetaRight": 0x5C,
		"OSLeft": 0x5B, "OSRight": 0x5C, "ContextMenu": 0x5D, "NumpadMultiply": 0x6A, "NumpadAdd": 0x6B,
		"NumpadSubtract": 0x6D, "NumpadDecimal": 0x6E, "NumpadDivide": 0x6F, "NumLock": 0x90,
		"ScrollLock": 0x91, "Semicolon": 0xBA, "Equal": 0xBB, "Comma": 0xBC, "Minus": 0xBD,
		"Period": 0xBE, "Slash": 0xBF, "Backquote": 0xC0, "BracketLeft": 0xDB, "Backslash": 0xDC,
		"BracketRight": 0xDD, "Quote": 0xDE, "IntlBackslash": 0xE2,
	}
	for c := 'A'; c <= 'Z'; c++ {
		m["Key"+string(c)] = uint16(c)
	}
	for d := '0'; d <= '9'; d++ {
		m["Digit"+string(d)] = uint16(d)
		m["Numpad"+string(d)] = uint16(0x60 + d - '0')
	}
	for i := 1; i <= 24; i++ {
		m["F"+itoa(i)] = uint16(0x70 + i - 1)
	}
	return m
}()

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func permStatus() (bool, bool) { return true, true }
