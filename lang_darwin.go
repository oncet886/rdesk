//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
static void firstLang(char *buf, int n) {
	buf[0] = 0;
	CFArrayRef a = CFLocaleCopyPreferredLanguages();
	if (!a) return;
	if (CFArrayGetCount(a) > 0) CFStringGetCString((CFStringRef)CFArrayGetValueAtIndex(a, 0), buf, n, kCFStringEncodingUTF8);
	CFRelease(a);
}
*/
import "C"

// 系统首选语言（如 zh-Hans-US、en-US、ja-JP）
func systemLang() string {
	var b [64]C.char
	C.firstLang(&b[0], 64)
	return C.GoString(&b[0])
}
