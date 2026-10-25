//go:build windows

package main

/*
#cgo LDFLAGS: -luser32 -lgdi32
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

static HCURSOR gLastCur = (HCURSOR)-1;

static void cursorShapeReset(void) { gLastCur = (HCURSOR)-1; }

// 光标位置（虚拟桌面物理像素，和截屏同一坐标系）
static int cursorPos(double *x, double *y) {
	POINT p;
	if (!GetCursorPos(&p)) return 0;
	*x = p.x; *y = p.y;
	return 1;
}

// 反色光标（输入竖线）画成黑色后，再给它描一圈白边，深色背景上也看得清
static void outline(uint8_t *px, int W, int H) {
	uint8_t *o = (uint8_t *)malloc((size_t)W * H * 4);
	memcpy(o, px, (size_t)W * H * 4);
	for (int y = 0; y < H; y++) for (int x = 0; x < W; x++) {
		if (px[(y * W + x) * 4 + 3]) continue;
		for (int dy = -1; dy <= 1; dy++) for (int dx = -1; dx <= 1; dx++) {
			int nx = x + dx, ny = y + dy;
			if (nx < 0 || ny < 0 || nx >= W || ny >= H) continue;
			uint8_t *q = px + (ny * W + nx) * 4;
			if (q[3] && q[0] == 0) { uint8_t *t = o + (y * W + x) * 4; t[0] = t[1] = t[2] = 255; t[3] = 255; }
		}
	}
	memcpy(px, o, (size_t)W * H * 4);
	free(o);
}

// 系统缩放比例（100% = 1.0），光标尺寸要换算成「点」
static double dpiScale(void) {
	typedef UINT(WINAPI * F)(void);
	static F f;
	if (!f) f = (F)GetProcAddress(GetModuleHandleA("user32.dll"), "GetDpiForSystem");
	UINT d = f ? f() : 96;
	return d ? d / 96.0 : 1.0;
}

// 光标样子变了：返回 RGBA（调用方 free）、尺寸和热点（像素）；没变返回 0
static int cursorShape(uint8_t **out, int *w, int *h, int *hx, int *hy) {
	CURSORINFO ci = {sizeof(ci)};
	if (!GetCursorInfo(&ci) || !(ci.flags & CURSOR_SHOWING) || !ci.hCursor) return 0;
	if (ci.hCursor == gLastCur) return 0;
	gLastCur = ci.hCursor;
	ICONINFO ii;
	if (!GetIconInfo(ci.hCursor, &ii)) return 0;
	BITMAP bm = {0};
	int ok = 0;
	HDC dc = GetDC(NULL);
	if (ii.hbmColor && GetObject(ii.hbmColor, sizeof(bm), &bm)) {
		// 彩色光标：直接取 32 位像素；没有 alpha 时用遮罩补
		int W = bm.bmWidth, H = bm.bmHeight;
		BITMAPINFO bi = {0};
		bi.bmiHeader.biSize = sizeof(bi.bmiHeader); bi.bmiHeader.biWidth = W; bi.bmiHeader.biHeight = -H;
		bi.bmiHeader.biPlanes = 1; bi.bmiHeader.biBitCount = 32; bi.bmiHeader.biCompression = BI_RGB;
		uint8_t *px = (uint8_t *)malloc((size_t)W * H * 4), *mask = (uint8_t *)malloc((size_t)W * H * 4);
		if (GetDIBits(dc, ii.hbmColor, 0, H, px, &bi, DIB_RGB_COLORS) == H) {
			int hasAlpha = 0;
			for (int i = 0; i < W * H; i++) if (px[i * 4 + 3]) { hasAlpha = 1; break; }
			int haveMask = ii.hbmMask && GetDIBits(dc, ii.hbmMask, 0, H, mask, &bi, DIB_RGB_COLORS) == H;
			int inverted = 0;
			for (int i = 0; i < W * H; i++) {
				uint8_t b = px[i * 4], g = px[i * 4 + 1], r = px[i * 4 + 2], a;
				if (hasAlpha) a = px[i * 4 + 3];
				else if (haveMask && mask[i * 4]) { // 遮罩为 1：颜色为 0 是透明，非 0 是反色（如输入竖线）→ 画成黑色
					if (r | g | b) { r = g = b = 0; a = 255; inverted = 1; } else a = 0;
				} else a = 255;
				px[i * 4] = r; px[i * 4 + 1] = g; px[i * 4 + 2] = b; px[i * 4 + 3] = a;
			}
			if (inverted) outline(px, W, H);
			*out = px; *w = W; *h = H; ok = 1;
		} else free(px);
		free(mask);
	} else if (ii.hbmMask && GetObject(ii.hbmMask, sizeof(bm), &bm)) {
		// 黑白光标（如输入竖线）：遮罩上半是 AND、下半是 XOR
		int W = bm.bmWidth, H = bm.bmHeight / 2;
		BITMAPINFO bi = {0};
		bi.bmiHeader.biSize = sizeof(bi.bmiHeader); bi.bmiHeader.biWidth = W; bi.bmiHeader.biHeight = -H * 2;
		bi.bmiHeader.biPlanes = 1; bi.bmiHeader.biBitCount = 32; bi.bmiHeader.biCompression = BI_RGB;
		uint8_t *m = (uint8_t *)malloc((size_t)W * H * 2 * 4), *px = (uint8_t *)calloc((size_t)W * H, 4);
		if (GetDIBits(dc, ii.hbmMask, 0, H * 2, m, &bi, DIB_RGB_COLORS) == H * 2) {
			for (int i = 0; i < W * H; i++) {
				int and = m[i * 4] != 0, xor = m[(W * H + i) * 4] != 0;
				uint8_t *p = px + i * 4;
				if (!and) { uint8_t v = xor ? 255 : 0; p[0] = p[1] = p[2] = v; p[3] = 255; } // 黑或白
				else if (xor) { p[0] = p[1] = p[2] = 0; p[3] = 255; }                           // 反色：画成黑色
			}
			outline(px, W, H);
			*out = px; *w = W; *h = H; ok = 1;
		} else free(px);
		free(m);
	}
	ReleaseDC(NULL, dc);
	*hx = ii.xHotspot; *hy = ii.yHotspot;
	if (ii.hbmColor) DeleteObject(ii.hbmColor);
	if (ii.hbmMask) DeleteObject(ii.hbmMask);
	return ok;
}
*/
import "C"

import (
	"bytes"
	"image"
	"image/png"
	"unsafe"
)

// 当前鼠标位置（虚拟桌面物理像素，和显示器 bounds 同一坐标系）
func cursorPos() (float64, float64, bool) {
	var x, y C.double
	if C.cursorPos(&x, &y) == 0 {
		return 0, 0, false
	}
	return float64(x), float64(y), true
}

// 光标样子变了就返回 PNG 和尺寸/热点（单位：点 = 像素 ÷ 系统缩放比例，和 Mac 版一致）
func cursorShape() (pngData []byte, w, h, hx, hy float64, ok bool) {
	k := float64(C.dpiScale())
	var p *C.uint8_t
	var cw, ch, chx, chy C.int
	if C.cursorShape(&p, &cw, &ch, &chx, &chy) == 0 {
		return nil, 0, 0, 0, 0, false
	}
	defer C.free(unsafe.Pointer(p))
	img := image.NewNRGBA(image.Rect(0, 0, int(cw), int(ch)))
	copy(img.Pix, unsafe.Slice((*byte)(unsafe.Pointer(p)), int(cw)*int(ch)*4))
	var b bytes.Buffer
	if png.Encode(&b, img) != nil {
		return nil, 0, 0, 0, 0, false
	}
	return b.Bytes(), float64(cw) / k, float64(ch) / k, float64(chx) / k, float64(chy) / k, true
}

func cursorShapeReset() { C.cursorShapeReset() }
