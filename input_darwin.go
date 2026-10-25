//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreGraphics -framework ApplicationServices -framework CoreFoundation -framework AppKit -framework Carbon
#include <CoreGraphics/CoreGraphics.h>
#include <ApplicationServices/ApplicationServices.h>
#import <AppKit/AppKit.h>
#include <Carbon/Carbon.h>
#include <stdlib.h>
#include <math.h>

static int axTrusted(int prompt) {
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *vals[] = { prompt ? kCFBooleanTrue : kCFBooleanFalse };
	CFDictionaryRef opts = CFDictionaryCreate(NULL, keys, vals, 1,
		&kCFCopyStringDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	int ok = AXIsProcessTrustedWithOptions(opts);
	CFRelease(opts);
	return ok;
}

static int screenAccess(int request) {
	if (CGPreflightScreenCaptureAccess()) return 1;
	if (request) return CGRequestScreenCaptureAccess();
	return 0;
}

// 事件源用「硬件状态」，模拟的事件更像真实鼠标键盘（有的程序会区分）
static CGEventSourceRef gSrc = NULL;
static CGEventSourceRef src(void) {
	if (!gSrc) gSrc = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
	return gSrc;
}

// dx/dy：和上一次位置的差。VMware、Parallels 等虚拟机按位移量移动虚拟机里的光标，
// 不带位移的话虚拟机里光标不动、点击落不到想点的地方
static void postMouse(int type, double x, double y, int button, int clicks, double dx, double dy) {
	CGEventRef e = CGEventCreateMouseEvent(src(), (CGEventType)type, CGPointMake(x, y), (CGMouseButton)button);
	if (clicks > 0) CGEventSetIntegerValueField(e, kCGMouseEventClickState, clicks);
	CGEventSetIntegerValueField(e, kCGMouseEventDeltaX, (int64_t)lround(dx));
	CGEventSetIntegerValueField(e, kCGMouseEventDeltaY, (int64_t)lround(dy));
	CGEventSetDoubleValueField(e, kCGMouseEventDeltaX, dx);
	CGEventSetDoubleValueField(e, kCGMouseEventDeltaY, dy);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void postScroll(int dy, int dx) {
	CGEventRef e = CGEventCreateScrollWheelEvent2(src(), kCGScrollEventUnitPixel, 2, dy, dx, 0);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void postKey(int code, int down, unsigned long long flags) {
	CGEventRef e = CGEventCreateKeyboardEvent(src(), (CGKeyCode)code, down ? true : false);
	CGEventSetFlags(e, (CGEventFlags)flags);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void cursorPos(double *x, double *y) {
	CGEventRef e = CGEventCreate(NULL);
	CGPoint p = CGEventGetLocation(e);
	CFRelease(e);
	*x = p.x; *y = p.y;
}

// 当前系统光标的样子（箭头 / 输入竖线 / 手形 / 拖拽 / 缩放箭头…）。没变化返回 0。
static NSData *gLastCursorPNG = nil;
// 光标图片几乎全透明 = 被程序隐藏了
static int cursorInvisible(NSCursor *c) {
	CGImageRef cg = [c.image CGImageForProposedRect:NULL context:nil hints:nil];
	if (!cg) return 1;
	NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithCGImage:cg];
	NSInteger w = rep.pixelsWide, h = rep.pixelsHigh;
	for (NSInteger y = 0; y < h; y += 2)
		for (NSInteger x = 0; x < w; x += 2)
			if ([rep colorAtX:x y:y].alphaComponent > 0.05) return 0;
	return 1;
}
static void cursorShapeReset(void) { gLastCursorPNG = nil; }
static int cursorShape(uint8_t **out, double *w, double *h, double *hx, double *hy) {
	@autoreleasepool {
		NSCursor *c = [NSCursor currentSystemCursor];
		if (!c || cursorInvisible(c)) c = [NSCursor arrowCursor]; // 被程序藏起来的光标（如 VMware 捕获鼠标）：发普通箭头，手机上始终看得到
		NSImage *img = c.image;
		NSSize sz = img.size;
		if (sz.width <= 0 || sz.height <= 0) return 0;
		NSRect r = NSMakeRect(0, 0, sz.width * 2, sz.height * 2); // 取 2x 清晰版本
		CGImageRef cg = [img CGImageForProposedRect:&r context:nil hints:nil];
		if (!cg) return 0;
		NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithCGImage:cg];
		NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
		if (!png || [png isEqualToData:gLastCursorPNG]) return 0;
		gLastCursorPNG = png;
		*w = sz.width; *h = sz.height; *hx = c.hotSpot.x; *hy = c.hotSpot.y;
		*out = malloc(png.length);
		memcpy(*out, png.bytes, png.length);
		return (int)png.length;
	}
}

// 最前面是虚拟机 / 远程桌面程序吗？它们只认按键编号，不认附带的文字
static int frontWantsKeycodes(void) {
	@autoreleasepool {
		NSString *b = NSWorkspace.sharedWorkspace.frontmostApplication.bundleIdentifier;
		if (!b) return 0;
		for (NSString *p in @[@"com.vmware.fusion", @"com.parallels.", @"com.utmapp.", @"org.virtualbox.",
		                      @"com.microsoft.rdc", @"com.carriez.rustdesk", @"com.citrix."]) {
			if ([b hasPrefix:p]) return 1;
		}
		return 0;
	}
}

// 字符 → 当前键盘布局下的按键编号（是否要按 Shift）。找不到返回 -1
static int keyForChar(UniChar ch, int *shift) {
	TISInputSourceRef ref = TISCopyCurrentKeyboardLayoutInputSource();
	if (!ref) return -1;
	CFDataRef data = (CFDataRef)TISGetInputSourceProperty(ref, kTISPropertyUnicodeKeyLayoutData);
	int found = -1;
	if (data) {
		const UCKeyboardLayout *layout = (const UCKeyboardLayout *)CFDataGetBytePtr(data);
		for (int mod = 0; mod < 2 && found < 0; mod++) {
			for (int code = 0; code < 128; code++) {
				UInt32 dead = 0; UniChar out[4]; UniCharCount n = 0;
				if (UCKeyTranslate(layout, code, kUCKeyActionDown, mod ? (shiftKey >> 8) & 0xff : 0, LMGetKbdType(),
				                   kUCKeyTranslateNoDeadKeysMask, &dead, 4, &n, out) == noErr && n == 1 && out[0] == ch) {
					found = code; *shift = mod; break;
				}
			}
		}
	}
	CFRelease(ref);
	return found;
}

static void postUnicode(const UniChar *s, int n) {
	CGEventRef d = CGEventCreateKeyboardEvent(src(), 0, true);
	CGEventRef u = CGEventCreateKeyboardEvent(src(), 0, false);
	CGEventSetFlags(d, 0);
	CGEventSetFlags(u, 0);
	CGEventKeyboardSetUnicodeString(d, n, s);
	CGEventKeyboardSetUnicodeString(u, n, s);
	CGEventPost(kCGHIDEventTap, d);
	CGEventPost(kCGHIDEventTap, u);
	CFRelease(d);
	CFRelease(u);
}
*/
import "C"

import (
	"errors"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"
)

var in struct {
	sync.Mutex
	x, y      float64
	buttons   [3]bool
	keys      map[string]bool
	lastDown  time.Time
	lastBtn   int
	lastX     float64
	lastY     float64
	clicks    int
	trustedOK bool
	px, py    float64 // 上一次发出去的光标位置（算位移量用）
	posted    bool
}

// 当前按着的键决定移动事件的类型（普通移动 / 拖拽）
func moveType() (t, b int) {
	switch {
	case in.buttons[0]:
		return evLeftDrag, 0
	case in.buttons[2]:
		return evRightDrag, 1
	case in.buttons[1]:
		return evOtherDrag, 2
	}
	return evMoved, 0
}

// 发鼠标事件并带上相对上一次位置的位移量（调用方持有 in 锁）
func post(t int, x, y float64, b, clicks int) {
	if !in.posted {
		in.px, in.py, _ = cursorPos()
		in.posted = true
	}
	dx, dy := x-in.px, y-in.py
	if t != evMoved && t != evLeftDrag && t != evRightDrag && t != evOtherDrag {
		dx, dy = 0, 0
	}
	in.px, in.py = x, y
	C.postMouse(C.int(t), C.double(x), C.double(y), C.int(b), C.int(clicks), C.double(dx), C.double(dy))
}

func inputInit() error {
	in.keys = map[string]bool{}
	var msg []string
	if C.screenAccess(1) == 0 {
		msg = append(msg, "「屏幕录制」")
	}
	if C.axTrusted(1) == 0 {
		msg = append(msg, "「辅助功能」")
	} else {
		in.trustedOK = true
	}
	if len(msg) > 0 {
		return errors.New("需要在 系统设置 → 隐私与安全性 中给运行本程序的 App（终端 / 本程序）打开 " +
			strings.Join(msg, "和") + " 权限，打开后重启本程序。没有「辅助功能」只能看不能控。")
	}
	return nil
}

func inputAvailable() bool { return C.axTrusted(0) == 1 }

// 权限状态：屏幕录制、辅助功能
func permStatus() (screen, access bool) { return C.screenAccess(0) == 1, C.axTrusted(0) == 1 }

// 光标样子变了就返回 PNG（2x）和尺寸/热点（单位：点）
func cursorShape() (png []byte, w, h, hx, hy float64, ok bool) {
	var p *C.uint8_t
	var cw, ch, chx, chy C.double
	n := C.cursorShape(&p, &cw, &ch, &chx, &chy)
	if n == 0 {
		return nil, 0, 0, 0, 0, false
	}
	png = C.GoBytes(unsafe.Pointer(p), n)
	C.free(unsafe.Pointer(p))
	return png, float64(cw), float64(ch), float64(chx), float64(chy), true
}

func cursorShapeReset() { C.cursorShapeReset() }

// 当前鼠标位置（全局坐标，和显示器 bounds 同一坐标系）
func cursorPos() (float64, float64, bool) {
	var x, y C.double
	C.cursorPos(&x, &y)
	return float64(x), float64(y), true
}

// CGEvent 类型常量
const (
	evLeftDown, evLeftUp    = 1, 2
	evRightDown, evRightUp  = 3, 4
	evMoved                 = 5
	evLeftDrag, evRightDrag = 6, 7
	evOtherDown, evOtherUp  = 25, 26
	evOtherDrag             = 27
)

// 浏览器按钮编号: 0 左 1 中 2 右 ；CGMouseButton: 0 左 1 右 2 中
func cgButton(b int) int {
	switch b {
	case 1:
		return 2
	case 2:
		return 1
	}
	return 0
}

func mouseMove(x, y float64) {
	in.Lock()
	defer in.Unlock()
	in.x, in.y = x, y
	t, b := moveType()
	post(t, x, y, b, 0)
}

func mouseButton(btn int, down bool, x, y float64) {
	if btn < 0 || btn > 2 {
		return
	}
	in.Lock()
	defer in.Unlock()
	in.x, in.y = x, y
	if down {
		// 双击/三击：macOS 需要在事件里带上点击次数
		if btn == in.lastBtn && time.Since(in.lastDown) < 450*time.Millisecond &&
			math.Abs(x-in.lastX) < 6 && math.Abs(y-in.lastY) < 6 {
			in.clicks++
		} else {
			in.clicks = 1
		}
		in.lastDown, in.lastBtn, in.lastX, in.lastY = time.Now(), btn, x, y
	}
	// 按下前光标不在这里：先带位移移过去（虚拟机才知道光标到了哪）
	if in.posted && (x != in.px || y != in.py) {
		t, b := moveType()
		post(t, x, y, b, 0)
	}
	in.buttons[btn] = down
	var t int
	switch btn {
	case 0:
		t = map[bool]int{true: evLeftDown, false: evLeftUp}[down]
	case 2:
		t = map[bool]int{true: evRightDown, false: evRightUp}[down]
	default:
		t = map[bool]int{true: evOtherDown, false: evOtherUp}[down]
	}
	post(t, x, y, cgButton(btn), max(in.clicks, 1))
}

func mouseWheel(dx, dy int) {
	// 浏览器 deltaY>0 是向下滚；CG 的 wheel1>0 是向上
	C.postScroll(C.int(-dy), C.int(-dx))
}

const (
	flagShift   = 0x00020000
	flagControl = 0x00040000
	flagAlt     = 0x00080000
	flagCommand = 0x00100000
)

func modFlags() uint64 {
	var f uint64
	if in.keys["ShiftLeft"] || in.keys["ShiftRight"] {
		f |= flagShift
	}
	if in.keys["ControlLeft"] || in.keys["ControlRight"] {
		f |= flagControl
	}
	if in.keys["AltLeft"] || in.keys["AltRight"] {
		f |= flagAlt
	}
	if in.keys["MetaLeft"] || in.keys["MetaRight"] || in.keys["OSLeft"] || in.keys["OSRight"] {
		f |= flagCommand
	}
	return f
}

func keyEvent(code string, down bool) {
	vk, ok := macKeys[code]
	if !ok {
		return
	}
	in.Lock()
	defer in.Unlock()
	if down {
		in.keys[code] = true
	} else {
		delete(in.keys, code)
	}
	C.postKey(C.int(vk), boolInt(down), C.ulonglong(modFlags()))
}

func typeText(s string) {
	in.Lock()
	defer in.Unlock()
	keycodes := C.frontWantsKeycodes() == 1
	for i, line := range strings.Split(s, "\n") {
		if keycodes { // 虚拟机里：逐个字符按真实的键（中文等找不到按键的字符照旧发文字）
			if i > 0 {
				C.postKey(0x24, 1, 0)
				C.postKey(0x24, 0, 0)
			}
			for _, r := range line {
				var shift C.int
				code := C.int(-1)
				if r < 0x10000 {
					code = C.keyForChar(C.UniChar(r), &shift)
				}
				if code < 0 {
					u := utf16.Encode([]rune{r})
					C.postUnicode((*C.UniChar)(unsafe.Pointer(&u[0])), C.int(len(u)))
					continue
				}
				var flags C.ulonglong
				if shift == 1 {
					flags = flagShift
					C.postKey(0x38, 1, flags)
				}
				C.postKey(code, 1, flags)
				time.Sleep(8 * time.Millisecond) // 虚拟机来不及处理太快的按键，连续相同的键（如 0000）会被吞掉
				C.postKey(code, 0, flags)
				if shift == 1 {
					C.postKey(0x38, 0, 0)
				}
				time.Sleep(8 * time.Millisecond)
			}
			continue
		}
		if i > 0 {
			C.postKey(0x24, 1, 0)
			C.postKey(0x24, 0, 0)
		}
		u := utf16.Encode([]rune(line))
		for len(u) > 0 {
			n := min(len(u), 20)
			C.postUnicode((*C.UniChar)(unsafe.Pointer(&u[0])), C.int(n))
			u = u[n:]
		}
	}
}

// 断开时把按着的键和鼠标都松开，避免被控端"卡键"
func releaseAll() {
	in.Lock()
	keys := in.keys
	in.keys = map[string]bool{}
	btns := in.buttons
	x, y := in.x, in.y
	in.Unlock()
	for code := range keys {
		if vk, ok := macKeys[code]; ok {
			C.postKey(C.int(vk), 0, 0)
		}
	}
	for b, d := range btns {
		if d {
			mouseButton(b, false, x, y)
		}
	}
}

func boolInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// KeyboardEvent.code -> macOS 虚拟键码 (kVK_*)
var macKeys = map[string]int{
	"KeyA": 0x00, "KeyS": 0x01, "KeyD": 0x02, "KeyF": 0x03, "KeyH": 0x04, "KeyG": 0x05, "KeyZ": 0x06,
	"KeyX": 0x07, "KeyC": 0x08, "KeyV": 0x09, "IntlBackslash": 0x0A, "KeyB": 0x0B, "KeyQ": 0x0C,
	"KeyW": 0x0D, "KeyE": 0x0E, "KeyR": 0x0F, "KeyY": 0x10, "KeyT": 0x11, "Digit1": 0x12,
	"Digit2": 0x13, "Digit3": 0x14, "Digit4": 0x15, "Digit6": 0x16, "Digit5": 0x17, "Equal": 0x18,
	"Digit9": 0x19, "Digit7": 0x1A, "Minus": 0x1B, "Digit8": 0x1C, "Digit0": 0x1D,
	"BracketRight": 0x1E, "KeyO": 0x1F, "KeyU": 0x20, "BracketLeft": 0x21, "KeyI": 0x22,
	"KeyP": 0x23, "Enter": 0x24, "KeyL": 0x25, "KeyJ": 0x26, "Quote": 0x27, "KeyK": 0x28,
	"Semicolon": 0x29, "Backslash": 0x2A, "Comma": 0x2B, "Slash": 0x2C, "KeyN": 0x2D, "KeyM": 0x2E,
	"Period": 0x2F, "Tab": 0x30, "Space": 0x31, "Backquote": 0x32, "Backspace": 0x33,
	"Escape": 0x35, "MetaRight": 0x36, "MetaLeft": 0x37, "OSRight": 0x36, "OSLeft": 0x37,
	"ShiftLeft": 0x38, "CapsLock": 0x39, "AltLeft": 0x3A, "ControlLeft": 0x3B, "ShiftRight": 0x3C,
	"AltRight": 0x3D, "ControlRight": 0x3E, "F17": 0x40, "NumpadDecimal": 0x41,
	"NumpadMultiply": 0x43, "NumpadAdd": 0x45, "NumLock": 0x47, "NumpadDivide": 0x4B,
	"NumpadEnter": 0x4C, "NumpadSubtract": 0x4E, "F18": 0x4F, "F19": 0x50, "NumpadEqual": 0x51,
	"Numpad0": 0x52, "Numpad1": 0x53, "Numpad2": 0x54, "Numpad3": 0x55, "Numpad4": 0x56,
	"Numpad5": 0x57, "Numpad6": 0x58, "Numpad7": 0x59, "F20": 0x5A, "Numpad8": 0x5B,
	"Numpad9": 0x5C, "IntlYen": 0x5D, "IntlRo": 0x5E, "NumpadComma": 0x5F, "F5": 0x60, "F6": 0x61,
	"F7": 0x62, "F3": 0x63, "F8": 0x64, "F9": 0x65, "Lang2": 0x66, "F11": 0x67, "Lang1": 0x68,
	"F13": 0x69, "F16": 0x6A, "F14": 0x6B, "F10": 0x6D, "ContextMenu": 0x6E, "F12": 0x6F,
	"F15": 0x71, "Insert": 0x72, "Help": 0x72, "Home": 0x73, "PageUp": 0x74, "Delete": 0x75,
	"F4": 0x76, "End": 0x77, "F2": 0x78, "PageDown": 0x79, "F1": 0x7A, "ArrowLeft": 0x7B,
	"ArrowRight": 0x7C, "ArrowDown": 0x7D, "ArrowUp": 0x7E,
}

// 字符在当前键盘布局下的按键编号和是否要 Shift（测试用）
func keyFor(r rune) (code int, shift bool, ok bool) {
	var sh C.int
	c := C.keyForChar(C.UniChar(r), &sh)
	return int(c), sh == 1, c >= 0
}
