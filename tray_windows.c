//go:build windows

// 任务栏托盘图标（C 部分）。单独放 .c：cgo 的 //export 会把 Go 文件里的 C 代码编译两次。
#define UNICODE
#define _UNICODE
#include <windows.h>
#include <shellapi.h>
#include <stdlib.h>
#include "tray_windows.h"

extern void goMenuAction(int tag);
extern void goMenuWillOpen(void);

#define WM_TRAY (WM_APP + 1)
#define TAG_BASE 100
#define MAXTAG 32

typedef struct { WCHAR title[300]; int enabled, checked, hidden, used; } Item;
static Item items[MAXTAG];
static HWND gWnd;
static NOTIFYICONDATAW nid;
static UINT gTaskbarCreated;
static WCHAR gTip[128] = L"RDesk Host";
static int gAddOK = -1, gAddErr = 0, gWndErr = 0;
void rdTrayDiag(int *ok, int *err, int *wnd) { *ok = gAddOK; *err = gAddErr; *wnd = gWndErr; }

// 菜单顺序（-1 = 分隔线），和 Mac 菜单栏一致
static const int order[] = {0, 16, -1, 1, 2, 12, 4, 13, -1, 14, 15, -1, 5, -1, 7, 8, 9, 17, 18, -1, 10};

static WCHAR *wide(const char *s) {
	int n = MultiByteToWideChar(CP_UTF8, 0, s, -1, NULL, 0);
	WCHAR *w = (WCHAR *)calloc(n + 1, sizeof(WCHAR));
	MultiByteToWideChar(CP_UTF8, 0, s, -1, w, n);
	return w;
}

void rdTraySet(int tag, const char *title, int enabled, int checked, int hidden) {
	if (tag < 0 || tag >= MAXTAG) return;
	WCHAR *w = wide(title);
	lstrcpynW(items[tag].title, w, 300);
	free(w);
	items[tag].enabled = enabled; items[tag].checked = checked; items[tag].hidden = hidden; items[tag].used = 1;
}

static void addIcon(void) {
	gAddOK = Shell_NotifyIconW(NIM_ADD, &nid);
	if (!gAddOK) gAddErr = (int)GetLastError();
	nid.uVersion = NOTIFYICON_VERSION_4;
	Shell_NotifyIconW(NIM_SETVERSION, &nid);
}

void rdTraySetTip(const char *tip) {
	WCHAR *w = wide(tip);
	lstrcpynW(gTip, w, 128);
	free(w);
	if (!gWnd) return;
	lstrcpynW(nid.szTip, gTip, 128);
	nid.uFlags = NIF_TIP | NIF_SHOWTIP;
	Shell_NotifyIconW(NIM_MODIFY, &nid);
}

void rdTrayRemove(void) {
	if (gWnd) Shell_NotifyIconW(NIM_DELETE, &nid);
}

static void showMenu(void) {
	goMenuWillOpen();
	HMENU m = CreatePopupMenu();
	int lastSep = 1;
	for (int i = 0; i < (int)(sizeof(order) / sizeof(order[0])); i++) {
		int t = order[i];
		if (t < 0) { if (!lastSep) AppendMenuW(m, MF_SEPARATOR, 0, NULL); lastSep = 1; continue; }
		Item *it = &items[t];
		if (!it->used || it->hidden || !it->title[0]) continue;
		UINT f = MF_STRING | (it->enabled ? 0 : MF_GRAYED) | (it->checked ? MF_CHECKED : 0);
		AppendMenuW(m, f, TAG_BASE + t, it->title);
		lastSep = 0;
	}
	POINT p;
	GetCursorPos(&p);
	SetForegroundWindow(gWnd); // 不先这样做，点菜单外面时菜单不会消失
	int cmd = TrackPopupMenu(m, TPM_RETURNCMD | TPM_RIGHTBUTTON | TPM_BOTTOMALIGN, p.x, p.y, 0, gWnd, NULL);
	PostMessageW(gWnd, WM_NULL, 0, 0);
	DestroyMenu(m);
	if (cmd >= TAG_BASE) goMenuAction(cmd - TAG_BASE);
}

static LRESULT CALLBACK wndProc(HWND h, UINT msg, WPARAM wp, LPARAM lp) {
	if (msg == WM_TRAY) {
		switch (LOWORD(lp)) {
		case WM_CONTEXTMENU:
		case NIN_SELECT:
		case NIN_KEYSELECT:
			showMenu();
			break;
		case WM_LBUTTONDBLCLK:
			goMenuAction(7); // 双击图标：打开控制面板
			break;
		}
		return 0;
	}
	if (msg == gTaskbarCreated && gTaskbarCreated) { addIcon(); return 0; } // 资源管理器重启后图标会消失：重新加上
	return DefWindowProcW(h, msg, wp, lp);
}

void rdTrayRun(void) {
	HINSTANCE inst = GetModuleHandleW(NULL);
	WNDCLASSW wc = {0};
	wc.lpfnWndProc = wndProc;
	wc.hInstance = inst;
	wc.lpszClassName = L"RDeskHostTray";
	RegisterClassW(&wc);
	gWnd = CreateWindowExW(0, wc.lpszClassName, L"RDesk Host", WS_OVERLAPPED, 0, 0, 0, 0, NULL, NULL, inst, NULL);
	if (!gWnd) gWndErr = (int)GetLastError();
	gTaskbarCreated = RegisterWindowMessageW(L"TaskbarCreated");

	WCHAR exe[MAX_PATH];
	GetModuleFileNameW(NULL, exe, MAX_PATH);
	HICON small = NULL;
	ExtractIconExW(exe, 0, NULL, &small, 1);
	nid.cbSize = sizeof(nid);
	nid.hWnd = gWnd;
	nid.uID = 1;
	nid.uFlags = NIF_ICON | NIF_MESSAGE | NIF_TIP | NIF_SHOWTIP;
	nid.uCallbackMessage = WM_TRAY;
	nid.hIcon = small ? small : LoadIconW(NULL, IDI_APPLICATION);
	lstrcpynW(nid.szTip, gTip, 128);
	addIcon();

	MSG m;
	while (GetMessageW(&m, NULL, 0, 0) > 0) {
		TranslateMessage(&m);
		DispatchMessageW(&m);
	}
}

void rdCopyText(const char *s) {
	WCHAR *w = wide(s);
	SIZE_T n = (lstrlenW(w) + 1) * sizeof(WCHAR);
	if (OpenClipboard(gWnd)) {
		EmptyClipboard();
		HGLOBAL g = GlobalAlloc(GMEM_MOVEABLE, n);
		if (g) {
			memcpy(GlobalLock(g), w, n);
			GlobalUnlock(g);
			if (!SetClipboardData(CF_UNICODETEXT, g)) GlobalFree(g);
		}
		CloseClipboard();
	}
	free(w);
}

void rdOpenURL(const char *u) {
	WCHAR *w = wide(u);
	ShellExecuteW(NULL, L"open", w, NULL, NULL, SW_SHOWNORMAL);
	free(w);
}

int rdConfirm(const char *title, const char *msg, int okCancel) {
	WCHAR *t = wide(title), *m = wide(msg);
	UINT f = MB_TOPMOST | MB_SETFOREGROUND | (okCancel ? (MB_OKCANCEL | MB_ICONQUESTION) : (MB_OK | MB_ICONINFORMATION));
	int r = MessageBoxW(NULL, m, t, f);
	free(t); free(m);
	return r == IDOK;
}
