//go:build windows

package main

/*
#cgo LDFLAGS: -lole32 -loleaut32 -lmfplat -lmfuuid -ld3d11 -ldxgi -luuid -lwmcodecdspuuid -lgdi32 -luser32
#define COBJMACROS
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <d3d11.h>
#include <dxgi1_2.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mftransform.h>
#include <mferror.h>
#include <codecapi.h>
#include <wmcodecdsp.h>
#include <mmdeviceapi.h>
#include <audioclient.h>
#include <ksmedia.h>

// ---------------- 公共状态 ----------------
// 截屏线程：DXGI 桌面复制（失败退回 GDI），按 fps 取帧。
//   JPEG 模式：把最新画面转成 RGBA 放进 gRGBA，Go 端 grab() 取走。
//   H.264 模式：缩放成 NV12 交给 Media Foundation 的 H.264 编码器，输出 Annex-B 包进队列。
// 声音线程：WASAPI 回环录系统声音 → 48kHz 立体声 16 位 → AAC-LC 编码器 → 每包 1024 采样。

typedef struct { uint8_t *data; int len; int key; int w, h; } rdPkt;
typedef struct { uint8_t *data; int len; } rdAPkt;

#define VQ 8
#define AQ 96
static SRWLOCK gMu = SRWLOCK_INIT;
static CONDITION_VARIABLE gVCv = CONDITION_VARIABLE_INIT, gACv = CONDITION_VARIABLE_INIT;
static rdPkt gVQ[VQ]; static int gVHead, gVLen;
static rdAPkt gAQ[AQ]; static int gAHead, gALen;

static volatile LONG gRun, gStopped, gPaused, gWantKey, gKbps, gNewKbps, gEncode;
static int gDisplay, gMaxW, gFps;
static HANDLE gThread, gAThread;
static volatile LONG gARun;
static uint64_t gCapN, gEncN, gLatSum, gLatN, gAPkts;
static float gAPeak;

// JPEG 模式的最新画面
static uint8_t *gRGBA; static int gRW, gRH; static uint64_t gSeq;

static uint64_t nowUs(void) {
	static LARGE_INTEGER f; LARGE_INTEGER c;
	if (!f.QuadPart) QueryPerformanceFrequency(&f);
	QueryPerformanceCounter(&c);
	return (uint64_t)(c.QuadPart * 1000000 / f.QuadPart);
}

static void vpush(uint8_t *d, int n, int key, int w, int h) {
	AcquireSRWLockExclusive(&gMu);
	if (gVLen == VQ) { // 满了：丢最旧的（流量控制会让这里很少发生）
		free(gVQ[gVHead].data);
		gVHead = (gVHead + 1) % VQ; gVLen--;
	}
	gVQ[(gVHead + gVLen) % VQ] = (rdPkt){d, n, key, w, h};
	gVLen++;
	ReleaseSRWLockExclusive(&gMu);
	WakeAllConditionVariable(&gVCv);
}

static void apush(uint8_t *d, int n) {
	AcquireSRWLockExclusive(&gMu);
	if (gALen == AQ) { free(gAQ[gAHead].data); gAHead = (gAHead + 1) % AQ; gALen--; }
	gAQ[(gAHead + gALen) % AQ] = (rdAPkt){d, n};
	gALen++; gAPkts++;
	ReleaseSRWLockExclusive(&gMu);
	WakeAllConditionVariable(&gACv);
}

static int rdvNext(rdPkt *out, int ms) {
	AcquireSRWLockExclusive(&gMu);
	if (!gVLen) SleepConditionVariableSRW(&gVCv, &gMu, ms, 0);
	int ok = 0;
	if (gVLen) { *out = gVQ[gVHead]; gVHead = (gVHead + 1) % VQ; gVLen--; ok = 1; }
	ReleaseSRWLockExclusive(&gMu);
	return ok;
}

static int rdaNext(rdAPkt *out, int ms) {
	AcquireSRWLockExclusive(&gMu);
	if (!gALen) SleepConditionVariableSRW(&gACv, &gMu, ms, 0);
	int ok = 0;
	if (gALen) { *out = gAQ[gAHead]; gAHead = (gAHead + 1) % AQ; gALen--; ok = 1; }
	ReleaseSRWLockExclusive(&gMu);
	return ok;
}

static void rdvFree(void *p) { free(p); }

static void rdCounters(uint64_t *c, uint64_t *e, uint64_t *ls, uint64_t *ln) {
	AcquireSRWLockExclusive(&gMu);
	*c = gCapN; *e = gEncN; *ls = gLatSum; *ln = gLatN;
	gCapN = gEncN = gLatSum = gLatN = 0;
	ReleaseSRWLockExclusive(&gMu);
}

static void rdAudioStats(uint64_t *n, float *peak) {
	AcquireSRWLockExclusive(&gMu);
	*n = gAPkts; *peak = gAPeak; gAPeak = 0;
	ReleaseSRWLockExclusive(&gMu);
}

// ---------------- 截屏：DXGI 桌面复制，失败退回 GDI ----------------

typedef struct {
	ID3D11Device *dev; ID3D11DeviceContext *ctx;
	IDXGIOutputDuplication *dup; ID3D11Texture2D *stage;
	int x, y, w, h;        // 显示器在虚拟桌面里的位置（物理像素）
	int gdi;               // 1 = 用 GDI 截图
	HDC sdc, mdc; HBITMAP bmp; uint8_t *bits;
	uint8_t *cpu; int stride; // 最近一帧 BGRA
} Cap;

static void capRelease(Cap *c) {
	if (c->stage) { ID3D11Texture2D_Release(c->stage); c->stage = NULL; }
	if (c->dup) { IDXGIOutputDuplication_Release(c->dup); c->dup = NULL; }
	if (c->ctx) { ID3D11DeviceContext_Release(c->ctx); c->ctx = NULL; }
	if (c->dev) { ID3D11Device_Release(c->dev); c->dev = NULL; }
	if (c->bmp) { DeleteObject(c->bmp); c->bmp = NULL; }
	if (c->mdc) { DeleteDC(c->mdc); c->mdc = NULL; }
	if (c->sdc) { ReleaseDC(NULL, c->sdc); c->sdc = NULL; }
}

// 第 index 个显示器的 DXGI 输出（顺序：适配器 → 输出，主屏通常在最前）
static int capInitDXGI(Cap *c, int index) {
	IDXGIFactory1 *f = NULL;
	if (FAILED(CreateDXGIFactory1(&IID_IDXGIFactory1, (void **)&f))) return -1;
	IDXGIAdapter1 *ad = NULL; IDXGIOutput *out = NULL; int n = 0, found = 0;
	for (UINT a = 0; !found && IDXGIFactory1_EnumAdapters1(f, a, &ad) == S_OK; a++) {
		for (UINT o = 0; IDXGIAdapter1_EnumOutputs(ad, o, &out) == S_OK; o++) {
			if (n++ == index) { found = 1; break; }
			IDXGIOutput_Release(out); out = NULL;
		}
		if (!found) { IDXGIAdapter1_Release(ad); ad = NULL; }
	}
	IDXGIFactory1_Release(f);
	if (!found) return -1;
	DXGI_OUTPUT_DESC od; IDXGIOutput_GetDesc(out, &od);
	c->x = od.DesktopCoordinates.left; c->y = od.DesktopCoordinates.top;
	c->w = od.DesktopCoordinates.right - od.DesktopCoordinates.left;
	c->h = od.DesktopCoordinates.bottom - od.DesktopCoordinates.top;
	HRESULT hr = D3D11CreateDevice((IDXGIAdapter *)ad, D3D_DRIVER_TYPE_UNKNOWN, NULL, D3D11_CREATE_DEVICE_BGRA_SUPPORT,
	                               NULL, 0, D3D11_SDK_VERSION, &c->dev, NULL, &c->ctx);
	IDXGIAdapter1_Release(ad);
	if (FAILED(hr)) { IDXGIOutput_Release(out); return -1; }
	IDXGIOutput1 *o1 = NULL;
	hr = IDXGIOutput_QueryInterface(out, &IID_IDXGIOutput1, (void **)&o1);
	IDXGIOutput_Release(out);
	if (FAILED(hr)) return -1;
	hr = IDXGIOutput1_DuplicateOutput(o1, (IUnknown *)c->dev, &c->dup);
	IDXGIOutput1_Release(o1);
	return FAILED(hr) ? -1 : 0;
}

static BOOL CALLBACK monEnum(HMONITOR m, HDC dc, LPRECT r, LPARAM p) {
	int *a = (int *)p; // a[0]=目标序号 a[1]=计数 a[2..5]=矩形
	if (a[1]++ == a[0]) { a[2] = r->left; a[3] = r->top; a[4] = r->right - r->left; a[5] = r->bottom - r->top; return FALSE; }
	return TRUE;
}

static int capInitGDI(Cap *c, int index) {
	int a[6] = {index, 0, 0, 0, 0, 0};
	EnumDisplayMonitors(NULL, NULL, monEnum, (LPARAM)a);
	if (a[4] == 0) { a[2] = 0; a[3] = 0; a[4] = GetSystemMetrics(SM_CXSCREEN); a[5] = GetSystemMetrics(SM_CYSCREEN); }
	c->x = a[2]; c->y = a[3]; c->w = a[4]; c->h = a[5];
	c->sdc = GetDC(NULL); c->mdc = CreateCompatibleDC(c->sdc);
	BITMAPINFO bi = {0};
	bi.bmiHeader.biSize = sizeof(bi.bmiHeader); bi.bmiHeader.biWidth = c->w; bi.bmiHeader.biHeight = -c->h;
	bi.bmiHeader.biPlanes = 1; bi.bmiHeader.biBitCount = 32; bi.bmiHeader.biCompression = BI_RGB;
	c->bmp = CreateDIBSection(c->mdc, &bi, DIB_RGB_COLORS, (void **)&c->bits, NULL, 0);
	if (!c->bmp) return -1;
	SelectObject(c->mdc, c->bmp);
	c->gdi = 1; c->cpu = c->bits; c->stride = c->w * 4;
	return 0;
}

static int capInit(Cap *c, int index) {
	memset(c, 0, sizeof(*c));
	if (capInitDXGI(c, index) == 0) return 0;
	capRelease(c);
	memset(c, 0, sizeof(*c));
	return capInitGDI(c, index);
}

// 取一帧：DXGI 模式把画面拷到 staging 纹理后直接映射，c->cpu 指向映射内存（不再整帧拷贝），
// 用完必须 capUnmap。返回 1 = 有新画面，0 = 没变化，-1 = 要重建（分辨率变了 / 切到安全桌面）
static int capGrab(Cap *c, int timeoutMs) {
	if (c->gdi) {
		Sleep(timeoutMs);
		BitBlt(c->mdc, 0, 0, c->w, c->h, c->sdc, c->x, c->y, SRCCOPY | CAPTUREBLT);
		return 1;
	}
	DXGI_OUTDUPL_FRAME_INFO fi; IDXGIResource *res = NULL;
	HRESULT hr = IDXGIOutputDuplication_AcquireNextFrame(c->dup, timeoutMs, &fi, &res);
	if (hr == DXGI_ERROR_WAIT_TIMEOUT) return 0;
	if (FAILED(hr)) return -1;
	int copied = 0;
	if (fi.LastPresentTime.QuadPart != 0) {
		ID3D11Texture2D *tex = NULL;
		if (SUCCEEDED(IDXGIResource_QueryInterface(res, &IID_ID3D11Texture2D, (void **)&tex))) {
			if (!c->stage) {
				D3D11_TEXTURE2D_DESC d; ID3D11Texture2D_GetDesc(tex, &d);
				d.Usage = D3D11_USAGE_STAGING; d.BindFlags = 0; d.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
				d.MiscFlags = 0; d.MipLevels = 1; d.ArraySize = 1; d.SampleDesc.Count = 1;
				ID3D11Device_CreateTexture2D(c->dev, &d, NULL, &c->stage);
				c->w = d.Width; c->h = d.Height;
			}
			if (c->stage) { ID3D11DeviceContext_CopyResource(c->ctx, (ID3D11Resource *)c->stage, (ID3D11Resource *)tex); copied = 1; }
			ID3D11Texture2D_Release(tex);
		}
	}
	IDXGIResource_Release(res);
	IDXGIOutputDuplication_ReleaseFrame(c->dup); // 先还给系统，桌面合成不用等我们转换
	if (!copied) return 0;
	D3D11_MAPPED_SUBRESOURCE m;
	if (FAILED(ID3D11DeviceContext_Map(c->ctx, (ID3D11Resource *)c->stage, 0, D3D11_MAP_READ, 0, &m))) return 0;
	c->cpu = (uint8_t *)m.pData; c->stride = (int)m.RowPitch;
	return 1;
}

static void capUnmap(Cap *c) {
	if (!c->gdi && c->stage && c->cpu) { ID3D11DeviceContext_Unmap(c->ctx, (ID3D11Resource *)c->stage, 0); c->cpu = NULL; }
}

static void capFree(Cap *c) {
	capUnmap(c);
	c->cpu = NULL;
	capRelease(c);
}

// 输出尺寸：最宽 maxW，保持比例，宽高都取偶数
static void outSize(int sw, int sh, int maxW, int *ow, int *oh) {
	int w = sw, h = sh;
	if (maxW > 0 && w > maxW) { h = (int)((int64_t)h * maxW / w); w = maxW; }
	*ow = w & ~1; *oh = h & ~1;
}

// BGRA → NV12（顺便缩放：亮度双线性，色度 2x2 平均）
static void toNV12(const uint8_t *src, int sw, int sh, int stride, uint8_t *dst, int ow, int oh) {
	uint8_t *Y = dst, *UV = dst + (size_t)ow * oh;
	int fx = (sw << 16) / ow, fy = (sh << 16) / oh;
	for (int y = 0; y < oh; y++) {
		int sy = (y * fy) >> 16; if (sy >= sh) sy = sh - 1;
		const uint8_t *row = src + (size_t)sy * stride;
		uint8_t *yo = Y + (size_t)y * ow;
		for (int x = 0; x < ow; x++) {
			int sx = (x * fx) >> 16; if (sx >= sw) sx = sw - 1;
			const uint8_t *p = row + sx * 4;
			yo[x] = (uint8_t)(((66 * p[2] + 129 * p[1] + 25 * p[0] + 128) >> 8) + 16);
		}
	}
	for (int y = 0; y < oh / 2; y++) {
		uint8_t *uo = UV + (size_t)y * ow;
		for (int x = 0; x < ow / 2; x++) {
			int r = 0, g = 0, b = 0;
			for (int dy = 0; dy < 2; dy++) for (int dx = 0; dx < 2; dx++) {
				int sx = ((x * 2 + dx) * fx) >> 16, sy = ((y * 2 + dy) * fy) >> 16;
				if (sx >= sw) sx = sw - 1;
				if (sy >= sh) sy = sh - 1;
				const uint8_t *p = src + (size_t)sy * stride + sx * 4;
				b += p[0]; g += p[1]; r += p[2];
			}
			r >>= 2; g >>= 2; b >>= 2;
			uo[x * 2] = (uint8_t)(((-38 * r - 74 * g + 112 * b + 128) >> 8) + 128);
			uo[x * 2 + 1] = (uint8_t)(((112 * r - 94 * g - 18 * b + 128) >> 8) + 128);
		}
	}
}

// ---------------- H.264：Media Foundation 自带编码器 ----------------

// codecapi.h 在 C 模式下不提供这些 GUID 常量，自己定义（值取自 codecapi.h）
static const GUID kRateControl = {0x1c0608e9, 0x370c, 0x4710, {0x8a, 0x58, 0xcb, 0x61, 0x81, 0xc4, 0x24, 0x23}};
static const GUID kMeanBitRate = {0xf7222374, 0x2144, 0x4815, {0xb5, 0x50, 0xa3, 0x7f, 0x8e, 0x12, 0xee, 0x52}};
static const GUID kLowLatency = {0x9c27891a, 0xed7a, 0x40e1, {0x88, 0xe8, 0xb2, 0x27, 0x27, 0xa0, 0x24, 0xee}};
static const GUID kBPictures = {0x8d390aac, 0xdc5c, 0x4200, {0xb5, 0x7f, 0x81, 0x4d, 0x04, 0xba, 0xba, 0xb2}};
static const GUID kGOPSize = {0x95f31b26, 0x95a4, 0x41aa, {0x93, 0x03, 0x24, 0x6a, 0x7f, 0xc6, 0xee, 0xf1}};
static const GUID kQualityVsSpeed = {0x98332df8, 0x03cd, 0x476b, {0x89, 0xfa, 0x3f, 0x9e, 0x44, 0x2d, 0xec, 0x9f}};
static const GUID kForceKeyFrame = {0x398c1b98, 0x8353, 0x475a, {0x9e, 0xf2, 0x8f, 0x26, 0x5d, 0x26, 0x03, 0x45}};

typedef struct { IMFTransform *t; ICodecAPI *api; int w, h, fps; LONGLONG ts; uint8_t *seqHdr; int seqLen; DWORD outSize; } Enc;

static void encFree(Enc *e) {
	if (e->api) { ICodecAPI_Release(e->api); e->api = NULL; }
	if (e->t) { IMFTransform_Release(e->t); e->t = NULL; }
	free(e->seqHdr); e->seqHdr = NULL; e->seqLen = 0;
}

static void apiU32(ICodecAPI *api, const GUID *k, ULONG v) {
	VARIANT var; VariantInit(&var); var.vt = VT_UI4; var.ulVal = v;
	ICodecAPI_SetValue(api, k, &var);
}

static void apiBool(ICodecAPI *api, const GUID *k, int v) {
	VARIANT var; VariantInit(&var); var.vt = VT_BOOL; var.boolVal = v ? VARIANT_TRUE : VARIANT_FALSE;
	ICodecAPI_SetValue(api, k, &var);
}

static int encInit(Enc *e, int w, int h, int fps, int kbps) {
	memset(e, 0, sizeof(*e));
	e->w = w; e->h = h; e->fps = fps;
	if (FAILED(CoCreateInstance(&CLSID_CMSH264EncoderMFT, NULL, CLSCTX_INPROC_SERVER, &IID_IMFTransform, (void **)&e->t))) return -1;
	if (SUCCEEDED(IMFTransform_QueryInterface(e->t, &IID_ICodecAPI, (void **)&e->api))) {
		apiU32(e->api, &kRateControl, eAVEncCommonRateControlMode_CBR);
		apiU32(e->api, &kMeanBitRate, (ULONG)kbps * 1000);
		apiBool(e->api, &kLowLatency, 1);
		apiU32(e->api, &kBPictures, 0);
		apiU32(e->api, &kGOPSize, (ULONG)fps * 30); // 长 GOP，丢包/新连接时按需要关键帧
		apiU32(e->api, &kQualityVsSpeed, 30);    // 偏速度
	}
	IMFMediaType *ot = NULL, *it = NULL;
	MFCreateMediaType(&ot);
	IMFMediaType_SetGUID(ot, &MF_MT_MAJOR_TYPE, &MFMediaType_Video);
	IMFMediaType_SetGUID(ot, &MF_MT_SUBTYPE, &MFVideoFormat_H264);
	IMFMediaType_SetUINT32(ot, &MF_MT_AVG_BITRATE, (UINT32)kbps * 1000);
	IMFMediaType_SetUINT64(ot, &MF_MT_FRAME_SIZE, ((UINT64)w << 32) | (UINT32)h);
	IMFMediaType_SetUINT64(ot, &MF_MT_FRAME_RATE, ((UINT64)fps << 32) | 1);
	IMFMediaType_SetUINT64(ot, &MF_MT_PIXEL_ASPECT_RATIO, ((UINT64)1 << 32) | 1);
	IMFMediaType_SetUINT32(ot, &MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
	IMFMediaType_SetUINT32(ot, &MF_MT_MPEG2_PROFILE, eAVEncH264VProfile_Main);
	HRESULT hr = IMFTransform_SetOutputType(e->t, 0, ot, 0);
	IMFMediaType_Release(ot);
	if (FAILED(hr)) { encFree(e); return -2; }
	MFCreateMediaType(&it);
	IMFMediaType_SetGUID(it, &MF_MT_MAJOR_TYPE, &MFMediaType_Video);
	IMFMediaType_SetGUID(it, &MF_MT_SUBTYPE, &MFVideoFormat_NV12);
	IMFMediaType_SetUINT64(it, &MF_MT_FRAME_SIZE, ((UINT64)w << 32) | (UINT32)h);
	IMFMediaType_SetUINT64(it, &MF_MT_FRAME_RATE, ((UINT64)fps << 32) | 1);
	IMFMediaType_SetUINT64(it, &MF_MT_PIXEL_ASPECT_RATIO, ((UINT64)1 << 32) | 1);
	IMFMediaType_SetUINT32(it, &MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
	hr = IMFTransform_SetInputType(e->t, 0, it, 0);
	IMFMediaType_Release(it);
	if (FAILED(hr)) { encFree(e); return -3; }
	// SPS/PPS（关键帧里没带时补上）
	IMFMediaType *cur = NULL;
	if (SUCCEEDED(IMFTransform_GetOutputCurrentType(e->t, 0, &cur))) {
		UINT32 n = 0;
		if (SUCCEEDED(IMFMediaType_GetBlobSize(cur, &MF_MT_MPEG_SEQUENCE_HEADER, &n)) && n > 0) {
			e->seqHdr = (uint8_t *)malloc(n);
			if (SUCCEEDED(IMFMediaType_GetBlob(cur, &MF_MT_MPEG_SEQUENCE_HEADER, e->seqHdr, n, &n))) e->seqLen = (int)n;
		}
		IMFMediaType_Release(cur);
	}
	MFT_OUTPUT_STREAM_INFO si = {0};
	IMFTransform_GetOutputStreamInfo(e->t, 0, &si);
	e->outSize = si.cbSize > 0 ? si.cbSize : (DWORD)(w * h * 2);
	IMFTransform_ProcessMessage(e->t, MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
	IMFTransform_ProcessMessage(e->t, MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
	return 0;
}

static int hasSPS(const uint8_t *b, int n) {
	for (int i = 0; i + 4 < n; i++)
		if (b[i] == 0 && b[i + 1] == 0 && b[i + 2] == 1 && (b[i + 3] & 0x1f) == 7) return 1;
	return 0;
}

// 收走编码器输出
static void encDrain(Enc *e, uint64_t t0) {
	for (;;) {
		IMFSample *s = NULL; IMFMediaBuffer *b = NULL;
		MFCreateSample(&s); MFCreateMemoryBuffer(e->outSize, &b); IMFSample_AddBuffer(s, b);
		MFT_OUTPUT_DATA_BUFFER ob = {0}; ob.pSample = s;
		DWORD st = 0;
		HRESULT hr = IMFTransform_ProcessOutput(e->t, 0, 1, &ob, &st);
		if (ob.pEvents) IMFCollection_Release(ob.pEvents);
		if (hr == MF_E_TRANSFORM_STREAM_CHANGE) { // 编码器自己改了输出格式：照单全收
			IMFMediaType *nt = NULL;
			if (SUCCEEDED(IMFTransform_GetOutputAvailableType(e->t, 0, 0, &nt))) { IMFTransform_SetOutputType(e->t, 0, nt, 0); IMFMediaType_Release(nt); }
			IMFMediaBuffer_Release(b); IMFSample_Release(s);
			continue;
		}
		if (FAILED(hr)) { IMFMediaBuffer_Release(b); IMFSample_Release(s); return; }
		BYTE *p = NULL; DWORD n = 0;
		if (SUCCEEDED(IMFMediaBuffer_Lock(b, &p, NULL, &n)) && n > 0) {
			UINT32 clean = 0;
			IMFSample_GetUINT32(s, &MFSampleExtension_CleanPoint, &clean);
			int key = clean != 0, extra = (key && e->seqLen && !hasSPS(p, (int)n)) ? e->seqLen : 0;
			uint8_t *d = (uint8_t *)malloc(n + extra);
			if (extra) memcpy(d, e->seqHdr, extra);
			memcpy(d + extra, p, n);
			IMFMediaBuffer_Unlock(b);
			AcquireSRWLockExclusive(&gMu);
			gEncN++; gLatSum += nowUs() - t0; gLatN++;
			ReleaseSRWLockExclusive(&gMu);
			vpush(d, (int)(n + extra), key, e->w, e->h);
		}
		IMFMediaBuffer_Release(b); IMFSample_Release(s);
	}
}

static void encFrame(Enc *e, const uint8_t *nv12, int key, uint64_t t0) {
	DWORD n = (DWORD)(e->w * e->h * 3 / 2);
	IMFSample *s = NULL; IMFMediaBuffer *b = NULL; BYTE *p = NULL;
	MFCreateSample(&s); MFCreateMemoryBuffer(n, &b);
	IMFMediaBuffer_Lock(b, &p, NULL, NULL); memcpy(p, nv12, n); IMFMediaBuffer_Unlock(b);
	IMFMediaBuffer_SetCurrentLength(b, n);
	IMFSample_AddBuffer(s, b);
	LONGLONG dur = 10000000LL / e->fps;
	IMFSample_SetSampleTime(s, e->ts); IMFSample_SetSampleDuration(s, dur); e->ts += dur;
	if (key && e->api) apiU32(e->api, &kForceKeyFrame, 1);
	if (SUCCEEDED(IMFTransform_ProcessInput(e->t, 0, s, 0))) encDrain(e, t0);
	IMFMediaBuffer_Release(b); IMFSample_Release(s);
}


// 测速（RDESK_BENCH=1）：合成画面，分别测 BGRA→NV12 转换和 H.264 编码每帧耗时
static void rdBench(int sw, int sh, int ow, int oh, int frames, double *convMs, double *encMs) {
	CoInitializeEx(NULL, COINIT_MULTITHREADED);
	MFStartup(MF_VERSION, MFSTARTUP_LITE);
	uint8_t *src = (uint8_t *)malloc((size_t)sw * sh * 4), *nv = (uint8_t *)malloc((size_t)ow * oh * 3 / 2);
	Enc e; *convMs = *encMs = -1;
	if (encInit(&e, ow, oh, 30, 6000) == 0) {
		uint64_t tc = 0, te = 0;
		for (int f = 0; f < frames; f++) {
			for (int y = 0; y < sh; y++) for (int x = 0; x < sw; x++) {
				uint8_t *p = src + ((size_t)y * sw + x) * 4;
				p[0] = (uint8_t)(x + f * 7); p[1] = (uint8_t)(y * 3 + f); p[2] = (uint8_t)((x ^ y) + f * 5); p[3] = 255;
			}
			uint64_t t0 = nowUs(); toNV12(src, sw, sh, sw * 4, nv, ow, oh);
			uint64_t t1 = nowUs(); encFrame(&e, nv, f == 0, t1);
			uint64_t t2 = nowUs(); tc += t1 - t0; te += t2 - t1;
		}
		*convMs = tc / 1000.0 / frames; *encMs = te / 1000.0 / frames;
		encFree(&e);
	}
	AcquireSRWLockExclusive(&gMu);
	while (gVLen) { free(gVQ[gVHead].data); gVHead = (gVHead + 1) % VQ; gVLen--; }
	ReleaseSRWLockExclusive(&gMu);
	free(src); free(nv);
}

// ---------------- 截屏线程 + 编码线程（并行：编码这一帧时已经在截下一帧）----------------

// 截屏线程交给编码线程的「信箱」：只放最新一帧，编码跟不上时旧帧直接丢掉
static SRWLOCK gBoxMu = SRWLOCK_INIT;
static CONDITION_VARIABLE gBoxCv = CONDITION_VARIABLE_INIT;
static uint8_t *gBox; static int gBoxW, gBoxH, gBoxKey; static uint64_t gBoxT0;
static HANDLE gEThread;

static void boxPut(uint8_t *nv, int w, int h, int key, uint64_t t0) {
	AcquireSRWLockExclusive(&gBoxMu);
	if (gBox) { key |= gBoxKey; free(gBox); } // 上一帧还没编：丢掉，但关键帧请求要保留
	gBox = nv; gBoxW = w; gBoxH = h; gBoxKey = key; gBoxT0 = t0;
	ReleaseSRWLockExclusive(&gBoxMu);
	WakeConditionVariable(&gBoxCv);
}

static DWORD WINAPI encThread(LPVOID arg) {
	CoInitializeEx(NULL, COINIT_MULTITHREADED);
	MFStartup(MF_VERSION, MFSTARTUP_LITE);
	Enc e = {0}; int kbps = gKbps, fps = gFps > 0 ? gFps : 30;
	while (gRun) {
		AcquireSRWLockExclusive(&gBoxMu);
		while (gRun && !gBox) SleepConditionVariableSRW(&gBoxCv, &gBoxMu, 200, 0);
		uint8_t *nv = gBox; int w = gBoxW, h = gBoxH, key = gBoxKey; uint64_t t0 = gBoxT0;
		gBox = NULL;
		ReleaseSRWLockExclusive(&gBoxMu);
		if (!nv) continue;
		if (!e.t || w != e.w || h != e.h) {
			encFree(&e);
			kbps = gKbps;
			if (encInit(&e, w, h, fps, kbps) != 0) { free(nv); InterlockedExchange(&gStopped, 1); break; }
			key = 1;
		}
		LONG nk = InterlockedExchange(&gNewKbps, 0);
		if (nk > 0 && nk != kbps && e.api) { kbps = nk; apiU32(e.api, &kMeanBitRate, (ULONG)kbps * 1000); }
		encFrame(&e, nv, key, t0);
		free(nv);
	}
	encFree(&e);
	MFShutdown(); CoUninitialize();
	return 0;
}

static DWORD WINAPI capThread(LPVOID arg) {
	CoInitializeEx(NULL, COINIT_MULTITHREADED);
	Cap c; uint8_t *last = NULL; int lw = 0, lh = 0; // 最近一帧 NV12：画面静止时补关键帧用
	int fps = gFps > 0 ? gFps : 30;
	uint64_t interval = 1000000 / fps, next = nowUs(), lastTry = nowUs();
	if (capInit(&c, gDisplay) != 0) { InterlockedExchange(&gStopped, 1); CoUninitialize(); return 0; }
	while (gRun) {
		// 不超过设定帧率：按固定节拍走，落后了就不等
		uint64_t now = nowUs();
		if (now < next) Sleep((DWORD)((next - now) / 1000));
		next = (next + interval > nowUs()) ? next + interval : nowUs();
		// GDI 是退路（比如刚从锁屏/UAC 安全桌面回来时桌面复制还用不了）：每 3 秒试着切回桌面复制
		if (c.gdi && nowUs() - lastTry > 3000000) {
			lastTry = nowUs();
			Cap t; memset(&t, 0, sizeof(t));
			if (capInitDXGI(&t, gDisplay) == 0) { capFree(&c); c = t; } else capRelease(&t);
		}
		int r = capGrab(&c, (int)(interval / 1000));
		if (r < 0) { // 分辨率变了、切到 UAC/锁屏等安全桌面：重建
			capFree(&c); Sleep(200);
			if (capInit(&c, gDisplay) != 0) Sleep(500);
			continue;
		}
		uint64_t t0 = nowUs();
		if (r > 0) { AcquireSRWLockExclusive(&gMu); gCapN++; ReleaseSRWLockExclusive(&gMu); }
		int w, h; outSize(c.w, c.h, gMaxW, &w, &h);
		if (!gEncode) { // JPEG 模式：转 RGBA 给 Go
			if (r > 0) {
				AcquireSRWLockExclusive(&gMu);
				if (gRW != w || gRH != h) { free(gRGBA); gRGBA = (uint8_t *)malloc((size_t)w * h * 4); gRW = w; gRH = h; }
				int fx = (c.w << 16) / w, fy = (c.h << 16) / h;
				for (int y = 0; y < h; y++) {
					const uint8_t *row = c.cpu + (size_t)((y * fy) >> 16) * c.stride;
					uint8_t *o = gRGBA + (size_t)y * w * 4;
					for (int x = 0; x < w; x++) { const uint8_t *p = row + ((x * fx) >> 16) * 4; o[x*4] = p[2]; o[x*4+1] = p[1]; o[x*4+2] = p[0]; o[x*4+3] = 255; }
				}
				gSeq++;
				ReleaseSRWLockExclusive(&gMu);
				capUnmap(&c);
			}
			continue;
		}
		int key = InterlockedExchange(&gWantKey, 0);
		if (gPaused) { if (r > 0) capUnmap(&c); if (key) InterlockedExchange(&gWantKey, 1); continue; }
		size_t n = (size_t)w * h * 3 / 2;
		uint8_t *nv = (uint8_t *)malloc(n);
		if (r > 0) {
			toNV12(c.cpu, c.w, c.h, c.stride, nv, w, h);
			capUnmap(&c);
			if (lw != w || lh != h) { free(last); last = (uint8_t *)malloc(n); lw = w; lh = h; }
			memcpy(last, nv, n);
		} else if (key && last && lw == w && lh == h) {
			memcpy(nv, last, n); // 屏幕没变但要关键帧：重发上一帧
		} else {
			free(nv);
			if (key) InterlockedExchange(&gWantKey, 1); // 还没有画面：下一帧再给关键帧
			continue;
		}
		boxPut(nv, w, h, key, t0);
	}
	free(last); capFree(&c);
	CoUninitialize();
	return 0;
}

// ---------------- 系统声音：WASAPI 回环 → AAC ----------------
// mmdeviceapi / audioclient 的 GUID（mingw 库里没有导出，自己定义）
static const GUID kCLSID_MMDeviceEnumerator = {0xBCDE0395, 0xE52F, 0x467C, {0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}};
static const GUID kIID_IMMDeviceEnumerator = {0xA95664D2, 0x9614, 0x4F35, {0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}};
static const GUID kIID_IAudioClient = {0x1CB9AD4C, 0xDBFA, 0x4C32, {0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}};
static const GUID kIID_IAudioCaptureClient = {0xC8ADBD64, 0xE71E, 0x48A0, {0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}};


static IMFTransform *aacInit(void) {
	IMFTransform *t = NULL;
	if (FAILED(CoCreateInstance(&CLSID_AACMFTEncoder, NULL, CLSCTX_INPROC_SERVER, &IID_IMFTransform, (void **)&t))) return NULL;
	IMFMediaType *it = NULL, *ot = NULL;
	MFCreateMediaType(&it);
	IMFMediaType_SetGUID(it, &MF_MT_MAJOR_TYPE, &MFMediaType_Audio);
	IMFMediaType_SetGUID(it, &MF_MT_SUBTYPE, &MFAudioFormat_PCM);
	IMFMediaType_SetUINT32(it, &MF_MT_AUDIO_BITS_PER_SAMPLE, 16);
	IMFMediaType_SetUINT32(it, &MF_MT_AUDIO_SAMPLES_PER_SECOND, 48000);
	IMFMediaType_SetUINT32(it, &MF_MT_AUDIO_NUM_CHANNELS, 2);
	IMFMediaType_SetUINT32(it, &MF_MT_AUDIO_BLOCK_ALIGNMENT, 4);
	IMFMediaType_SetUINT32(it, &MF_MT_AUDIO_AVG_BYTES_PER_SECOND, 192000);
	MFCreateMediaType(&ot);
	IMFMediaType_SetGUID(ot, &MF_MT_MAJOR_TYPE, &MFMediaType_Audio);
	IMFMediaType_SetGUID(ot, &MF_MT_SUBTYPE, &MFAudioFormat_AAC);
	IMFMediaType_SetUINT32(ot, &MF_MT_AUDIO_BITS_PER_SAMPLE, 16);
	IMFMediaType_SetUINT32(ot, &MF_MT_AUDIO_SAMPLES_PER_SECOND, 48000);
	IMFMediaType_SetUINT32(ot, &MF_MT_AUDIO_NUM_CHANNELS, 2);
	IMFMediaType_SetUINT32(ot, &MF_MT_AUDIO_AVG_BYTES_PER_SECOND, 12000); // 96 kbps
	IMFMediaType_SetUINT32(ot, &MF_MT_AAC_PAYLOAD_TYPE, 0);               // 裸 AAC，和 Mac 版一致
	HRESULT hr = IMFTransform_SetInputType(t, 0, it, 0);
	if (SUCCEEDED(hr)) hr = IMFTransform_SetOutputType(t, 0, ot, 0);
	if (FAILED(hr)) { // 有的系统要求先设输出
		hr = IMFTransform_SetOutputType(t, 0, ot, 0);
		if (SUCCEEDED(hr)) hr = IMFTransform_SetInputType(t, 0, it, 0);
	}
	IMFMediaType_Release(it); IMFMediaType_Release(ot);
	if (FAILED(hr)) { IMFTransform_Release(t); return NULL; }
	IMFTransform_ProcessMessage(t, MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
	IMFTransform_ProcessMessage(t, MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
	return t;
}

static void aacFeed(IMFTransform *t, const int16_t *pcm, LONGLONG *ts) {
	DWORD n = 1024 * 4;
	IMFSample *s = NULL; IMFMediaBuffer *b = NULL; BYTE *p = NULL;
	MFCreateSample(&s); MFCreateMemoryBuffer(n, &b);
	IMFMediaBuffer_Lock(b, &p, NULL, NULL); memcpy(p, pcm, n); IMFMediaBuffer_Unlock(b);
	IMFMediaBuffer_SetCurrentLength(b, n); IMFSample_AddBuffer(s, b);
	IMFSample_SetSampleTime(s, *ts); IMFSample_SetSampleDuration(s, 1024LL * 10000000 / 48000);
	*ts += 1024LL * 10000000 / 48000;
	HRESULT hr = IMFTransform_ProcessInput(t, 0, s, 0);
	IMFMediaBuffer_Release(b); IMFSample_Release(s);
	if (FAILED(hr)) return;
	for (;;) {
		IMFSample *os = NULL; IMFMediaBuffer *ob = NULL;
		MFCreateSample(&os); MFCreateMemoryBuffer(4096, &ob); IMFSample_AddBuffer(os, ob);
		MFT_OUTPUT_DATA_BUFFER od = {0}; od.pSample = os; DWORD st = 0;
		hr = IMFTransform_ProcessOutput(t, 0, 1, &od, &st);
		if (od.pEvents) IMFCollection_Release(od.pEvents);
		if (SUCCEEDED(hr)) {
			BYTE *q = NULL; DWORD m = 0;
			if (SUCCEEDED(IMFMediaBuffer_Lock(ob, &q, NULL, &m)) && m > 0) {
				uint8_t *d = (uint8_t *)malloc(m); memcpy(d, q, m);
				IMFMediaBuffer_Unlock(ob);
				apush(d, (int)m);
			}
		}
		IMFMediaBuffer_Release(ob); IMFSample_Release(os);
		if (FAILED(hr)) break;
	}
}

static DWORD WINAPI audioThread(LPVOID arg) {
	CoInitializeEx(NULL, COINIT_MULTITHREADED);
	MFStartup(MF_VERSION, MFSTARTUP_LITE);
	IMMDeviceEnumerator *en = NULL; IMMDevice *dev = NULL; IAudioClient *ac = NULL; IAudioCaptureClient *cc = NULL;
	WAVEFORMATEX *wf = NULL; IMFTransform *aac = NULL;
	int16_t *acc = (int16_t *)malloc(48000 * 4); int accN = 0; LONGLONG ts = 0;
	double rpos = 0; // 重采样到 48 kHz 的相位
	if (FAILED(CoCreateInstance(&kCLSID_MMDeviceEnumerator, NULL, CLSCTX_ALL, &kIID_IMMDeviceEnumerator, (void **)&en))) goto done;
	if (FAILED(IMMDeviceEnumerator_GetDefaultAudioEndpoint(en, eRender, eConsole, &dev))) goto done;
	if (FAILED(IMMDevice_Activate(dev, &kIID_IAudioClient, CLSCTX_ALL, NULL, (void **)&ac))) goto done;
	if (FAILED(IAudioClient_GetMixFormat(ac, &wf))) goto done;
	if (FAILED(IAudioClient_Initialize(ac, AUDCLNT_SHAREMODE_SHARED, AUDCLNT_STREAMFLAGS_LOOPBACK, 2000000, 0, wf, NULL))) goto done;
	if (FAILED(IAudioClient_GetService(ac, &kIID_IAudioCaptureClient, (void **)&cc))) goto done;
	if (!(aac = aacInit())) goto done;
	int isFloat = wf->wFormatTag == WAVE_FORMAT_IEEE_FLOAT ||
	              (wf->wFormatTag == WAVE_FORMAT_EXTENSIBLE && IsEqualGUID(&((WAVEFORMATEXTENSIBLE *)wf)->SubFormat, &KSDATAFORMAT_SUBTYPE_IEEE_FLOAT));
	int ch = wf->nChannels, bits = wf->wBitsPerSample;
	double step = (double)wf->nSamplesPerSec / 48000.0;
	IAudioClient_Start(ac);
	while (gARun) {
		Sleep(10);
		UINT32 pk = 0;
		while (SUCCEEDED(IAudioCaptureClient_GetNextPacketSize(cc, &pk)) && pk > 0) {
			BYTE *data = NULL; UINT32 frames = 0; DWORD flags = 0;
			if (FAILED(IAudioCaptureClient_GetBuffer(cc, &data, &frames, &flags, NULL, NULL))) break;
			float peak = 0;
			// 逐帧取左右声道（多声道只取前两个），线性重采样到 48 kHz
			for (; rpos < frames; rpos += step) {
				UINT32 i = (UINT32)rpos;
				float l = 0, r = 0;
				if (!(flags & AUDCLNT_BUFFERFLAGS_SILENT)) {
					if (isFloat && bits == 32) { const float *f = (const float *)data + (size_t)i * ch; l = f[0]; r = ch > 1 ? f[1] : l; }
					else if (bits == 16) { const int16_t *s = (const int16_t *)data + (size_t)i * ch; l = s[0] / 32768.f; r = ch > 1 ? s[1] / 32768.f : l; }
				}
				if (l > 1) l = 1; if (l < -1) l = -1; if (r > 1) r = 1; if (r < -1) r = -1;
				if (l > peak) peak = l; if (-l > peak) peak = -l;
				acc[accN * 2] = (int16_t)(l * 32767); acc[accN * 2 + 1] = (int16_t)(r * 32767);
				if (++accN == 1024) { aacFeed(aac, acc, &ts); accN = 0; }
			}
			rpos -= frames;
			IAudioCaptureClient_ReleaseBuffer(cc, frames);
			AcquireSRWLockExclusive(&gMu); if (peak > gAPeak) gAPeak = peak; ReleaseSRWLockExclusive(&gMu);
		}
	}
	IAudioClient_Stop(ac);
done:
	if (aac) IMFTransform_Release(aac);
	if (cc) IAudioCaptureClient_Release(cc);
	if (wf) CoTaskMemFree(wf);
	if (ac) IAudioClient_Release(ac);
	if (dev) IMMDevice_Release(dev);
	if (en) IMMDeviceEnumerator_Release(en);
	free(acc);
	MFShutdown(); CoUninitialize();
	return 0;
}

// ---------------- 启停 ----------------

static void rdStop(void) {
	InterlockedExchange(&gRun, 0); InterlockedExchange(&gARun, 0);
	WakeAllConditionVariable(&gBoxCv);
	if (gThread) { WaitForSingleObject(gThread, 3000); CloseHandle(gThread); gThread = NULL; }
	if (gEThread) { WaitForSingleObject(gEThread, 3000); CloseHandle(gEThread); gEThread = NULL; }
	AcquireSRWLockExclusive(&gBoxMu); free(gBox); gBox = NULL; ReleaseSRWLockExclusive(&gBoxMu);
	if (gAThread) { WaitForSingleObject(gAThread, 3000); CloseHandle(gAThread); gAThread = NULL; }
	AcquireSRWLockExclusive(&gMu);
	while (gVLen) { free(gVQ[gVHead].data); gVHead = (gVHead + 1) % VQ; gVLen--; }
	while (gALen) { free(gAQ[gAHead].data); gAHead = (gAHead + 1) % AQ; gALen--; }
	ReleaseSRWLockExclusive(&gMu);
}

static int startAll(int display, int maxW, int fps, int kbps, int encode, int audio) {
	rdStop();
	gDisplay = display; gMaxW = maxW; gFps = fps > 0 ? fps : 30;
	InterlockedExchange(&gKbps, kbps); InterlockedExchange(&gNewKbps, 0);
	InterlockedExchange(&gEncode, encode); InterlockedExchange(&gPaused, 0);
	InterlockedExchange(&gStopped, 0); InterlockedExchange(&gWantKey, 1); InterlockedExchange(&gRun, 1);
	gThread = CreateThread(NULL, 0, capThread, NULL, 0, NULL);
	if (gThread) SetThreadPriority(gThread, THREAD_PRIORITY_ABOVE_NORMAL);
	if (encode) { gEThread = CreateThread(NULL, 0, encThread, NULL, 0, NULL); if (gEThread) SetThreadPriority(gEThread, THREAD_PRIORITY_ABOVE_NORMAL); }
	if (audio) { InterlockedExchange(&gARun, 1); gAThread = CreateThread(NULL, 0, audioThread, NULL, 0, NULL); }
	return gThread ? 0 : 2;
}

static int rdStart(int display, int maxW, int fps) { return startAll(display, maxW, fps, 0, 0, 0); }
static int rdvStart(int display, int maxW, int fps, int kbps, int audio) { return startAll(display, maxW, fps, kbps, 1, audio); }
static int rdStopped(void) { return gStopped != 0; }
static void rdvKeyframe(void) { InterlockedExchange(&gWantKey, 1); }
static void rdvSetBitrate(int k) { InterlockedExchange(&gNewKbps, k); }
static void rdvPause(int p) { InterlockedExchange(&gPaused, p); if (!p) InterlockedExchange(&gWantKey, 1); }
static uint64_t rdSeq(void) { AcquireSRWLockShared(&gMu); uint64_t s = gSeq; ReleaseSRWLockShared(&gMu); return s; }

static uint64_t rdCopy(uint8_t *dst, size_t cap, int *w, int *h) {
	AcquireSRWLockShared(&gMu);
	*w = gRW; *h = gRH; uint64_t s = gSeq;
	if (dst && gRGBA && (size_t)gRW * gRH * 4 <= cap) memcpy(dst, gRGBA, (size_t)gRW * gRH * 4);
	ReleaseSRWLockShared(&gMu);
	return s;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"time"
	"unsafe"
)

// ---------------- JPEG 兼容模式（浏览器旧版控制端）----------------

type streamCapturer struct {
	display, maxW, fps int
	started            bool
	lastSeq            uint64
}

func newCapturer() capturer { return &streamCapturer{} }

func (c *streamCapturer) configure(display, maxW, fps int) error {
	c.display, c.maxW, c.fps = display, maxW, fps
	if C.rdStart(C.int(display), C.int(maxW), C.int(fps)) != 0 {
		return errors.New("启动截屏失败")
	}
	c.started, c.lastSeq = true, 0
	return nil
}

func (c *streamCapturer) grab() (*image.RGBA, bool, error) {
	if !c.started || C.rdStopped() != 0 {
		if err := c.configure(c.display, c.maxW, c.fps); err != nil {
			return nil, false, err
		}
	}
	if uint64(C.rdSeq()) == c.lastSeq {
		return nil, false, nil
	}
	var w, h C.int
	C.rdCopy(nil, 0, &w, &h)
	if w == 0 || h == 0 {
		return nil, false, nil
	}
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	seq := C.rdCopy((*C.uint8_t)(unsafe.Pointer(&img.Pix[0])), C.size_t(len(img.Pix)), &w, &h)
	if int(w)*int(h)*4 != len(img.Pix) {
		return nil, false, nil
	}
	c.lastSeq = uint64(seq)
	return img, true, nil
}

func (c *streamCapturer) close() {
	if c.started {
		C.rdStop()
		c.started = false
	}
}

// ---------------- H.264（Media Foundation）----------------

func videoAvailable() bool { return true }

// 测速：各分辨率下转换和编码每帧多少毫秒
func winBench() {
	for _, r := range [][4]int{{1920, 1080, 1920, 1080}, {3024, 2268, 1920, 1440}, {2560, 1440, 1920, 1080}} {
		var conv, enc C.double
		C.rdBench(C.int(r[0]), C.int(r[1]), C.int(r[2]), C.int(r[3]), 60, &conv, &enc)
		fmt.Printf("%dx%d → %dx%d：转换 %.1f ms，编码 %.1f ms\n", r[0], r[1], r[2], r[3], float64(conv), float64(enc))
	}
}

func videoStart(display, maxW, fps, kbps int, audio bool) error {
	if rc := C.rdvStart(C.int(display), C.int(maxW), C.int(fps), C.int(kbps), map[bool]C.int{true: 1, false: 0}[audio]); rc != 0 {
		return fmt.Errorf("启动截屏编码失败 (%d)", int(rc))
	}
	return nil
}

func videoNext(timeout time.Duration) (data []byte, key bool, w, h int, ok bool) {
	var p C.rdPkt
	if C.rdvNext(&p, C.int(timeout/time.Millisecond)) == 0 {
		return nil, false, 0, 0, false
	}
	data = C.GoBytes(unsafe.Pointer(p.data), p.len)
	C.rdvFree(unsafe.Pointer(p.data))
	return data, p.key != 0, int(p.w), int(p.h), true
}

func videoCounters() (capN, encN, latSum, latN uint64) {
	var c, e, ls, ln C.uint64_t
	C.rdCounters(&c, &e, &ls, &ln)
	return uint64(c), uint64(e), uint64(ls), uint64(ln)
}

// 下一个 AAC 包（裸 AAC-LC，48kHz 立体声，每包 1024 采样）
func audioNext(timeout time.Duration) ([]byte, bool) {
	var p C.rdAPkt
	if C.rdaNext(&p, C.int(timeout/time.Millisecond)) == 0 {
		return nil, false
	}
	b := C.GoBytes(unsafe.Pointer(p.data), p.len)
	C.rdvFree(unsafe.Pointer(p.data))
	return b, true
}

func audioStats() (uint64, float64) {
	var n C.uint64_t
	var pk C.float
	C.rdAudioStats(&n, &pk)
	return uint64(n), float64(pk)
}

func videoStopped() bool    { return C.rdStopped() != 0 }
func videoKeyframe()        { C.rdvKeyframe() }
func videoSetBitrate(k int) { C.rdvSetBitrate(C.int(k)) }
func videoPause(p bool)     { C.rdvPause(map[bool]C.int{true: 1, false: 0}[p]) }
func videoStop()            { C.rdStop() }
