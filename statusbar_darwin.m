// 菜单栏图标（Objective-C 部分）。单独放 .m：cgo 的 //export 会把 Go 文件里的 C 代码编译两次，类会重复定义。
#import <AppKit/AppKit.h>
#import <ServiceManagement/ServiceManagement.h>
#include "statusbar_darwin.h"

extern void goMenuAction(int tag);
extern void goMenuWillOpen(void);

@interface RDStatus : NSObject <NSMenuDelegate>
@property (strong) NSStatusItem *item;
@property (strong) NSMenu *menu;
@end

@implementation RDStatus
- (void)act:(NSMenuItem *)m { goMenuAction((int)m.tag); }
- (void)menuNeedsUpdate:(NSMenu *)menu { goMenuWillOpen(); }
@end

static RDStatus *gStatus;

// 菜单栏图标（模板图，自动适配深色/浅色菜单栏）：显示器 + 光标；有手机连着时屏幕填实
static NSImage *rdIcon(BOOL connected) {
	NSImage *img = [NSImage imageWithSize:NSMakeSize(18, 18) flipped:NO drawingHandler:^BOOL(NSRect r) {
		[[NSColor blackColor] set];
		NSBezierPath *scr = [NSBezierPath bezierPathWithRoundedRect:NSMakeRect(1.75, 5.25, 14.5, 10) xRadius:2 yRadius:2];
		scr.lineWidth = 1.5;
		if (connected) [scr fill]; else [scr stroke];
		NSBezierPath *st = [NSBezierPath bezierPath];
		[st moveToPoint:NSMakePoint(9, 5)]; [st lineToPoint:NSMakePoint(9, 2.75)];
		[st moveToPoint:NSMakePoint(5.75, 2.25)]; [st lineToPoint:NSMakePoint(12.25, 2.25)];
		st.lineWidth = 1.5; st.lineCapStyle = NSLineCapStyleRound;
		[st stroke];
		NSBezierPath *ar = [NSBezierPath bezierPath];
		[ar moveToPoint:NSMakePoint(7.6, 13.4)]; [ar lineToPoint:NSMakePoint(7.6, 7.6)];
		[ar lineToPoint:NSMakePoint(9.1, 9.0)]; [ar lineToPoint:NSMakePoint(10.3, 6.9)];
		[ar lineToPoint:NSMakePoint(11.3, 7.4)]; [ar lineToPoint:NSMakePoint(10.2, 9.5)];
		[ar lineToPoint:NSMakePoint(12.2, 9.7)]; [ar closePath];
		if (connected) {
			[[NSGraphicsContext currentContext] setCompositingOperation:NSCompositingOperationDestinationOut];
			[ar fill];
			[[NSGraphicsContext currentContext] setCompositingOperation:NSCompositingOperationSourceOver];
		} else {
			[ar fill];
		}
		return YES;
	}];
	img.template = YES;
	return img;
}

// 菜单项：tag -1 = 分隔线；12 是 3 的「按住 ⌥」替身（显示明文密码）
void rdStatusRun(void) {
	@autoreleasepool {
		[NSApplication sharedApplication];
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		gStatus = [RDStatus new];
		// 有刘海的 Mac 菜单栏图标多时，新图标排在最左边会被刘海挡住：第一次把它放到靠右（时钟附近），
		// 之后用户按住 ⌘ 拖到哪里，系统会记住位置
		NSString *posKey = @"NSStatusItem Preferred Position RDeskHost";
		if (![[NSUserDefaults standardUserDefaults] objectForKey:posKey])
			[[NSUserDefaults standardUserDefaults] setDouble:260 forKey:posKey];
		gStatus.item = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
		gStatus.item.autosaveName = @"RDeskHost";
		gStatus.item.button.image = rdIcon(NO);
		gStatus.item.button.toolTip = @"RDesk Host";
		NSMenu *m = [NSMenu new];
		m.autoenablesItems = NO;
		m.delegate = gStatus;
		int tags[] = {0, 16, -1, 1, 2, 3, 12, 4, 13, -1, 14, 15, -1, 5, 6, -1, 7, 8, 9, 17, 18, -1, 10};
		for (int i = 0; i < (int)(sizeof(tags) / sizeof(tags[0])); i++) {
			if (tags[i] < 0) { [m addItem:[NSMenuItem separatorItem]]; continue; }
			NSMenuItem *it = [[NSMenuItem alloc] initWithTitle:@"…" action:@selector(act:) keyEquivalent:@""];
			it.target = gStatus;
			it.tag = tags[i];
			if (tags[i] == 12) { it.alternate = YES; it.keyEquivalentModifierMask = NSEventModifierFlagOption; }
			[m addItem:it];
		}
		gStatus.menu = m;
		gStatus.item.menu = m;
		if (getenv("RDESK_CDEBUG")) fprintf(stderr, "[menubar] item=%p button=%p main=%d\n", gStatus.item, gStatus.item.button, [NSThread isMainThread]);
	}
	[NSApp run];
}

// 只能在主线程调用（菜单回调里）
void rdStatusSet(int tag, const char *title, int enabled, int checked, int hidden) {
	NSMenuItem *it = [gStatus.menu itemWithTag:tag];
	if (!it) return;
	it.title = [NSString stringWithUTF8String:title];
	it.enabled = enabled != 0;
	it.state = checked ? NSControlStateValueOn : NSControlStateValueOff;
	it.hidden = hidden != 0;
}

void rdStatusConnected(int on) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (gStatus) gStatus.item.button.image = rdIcon(on != 0);
	});
}

void rdPasteboardCopy(const char *s) {
	NSPasteboard *pb = [NSPasteboard generalPasteboard];
	[pb clearContents];
	[pb setString:[NSString stringWithUTF8String:s] forType:NSPasteboardTypeString];
}

void rdOpenURL(const char *u) {
	[[NSWorkspace sharedWorkspace] openURL:[NSURL URLWithString:[NSString stringWithUTF8String:u]]];
}

int rdConfirm(const char *title, const char *msg, const char *ok) {
	[NSApp activateIgnoringOtherApps:YES];
	NSAlert *a = [NSAlert new];
	a.messageText = [NSString stringWithUTF8String:title];
	a.informativeText = [NSString stringWithUTF8String:msg];
	[a addButtonWithTitle:[NSString stringWithUTF8String:ok]];
	[a addButtonWithTitle:@"取消"];
	return [a runModal] == NSAlertFirstButtonReturn;
}

int rdIsBundle(void) { return [[[NSBundle mainBundle] bundlePath] hasSuffix:@".app"]; }

// 登录时自动启动（macOS 13+，只有 .app 才行）
int rdLoginItem(void) {
	if (@available(macOS 13.0, *)) return SMAppService.mainAppService.status == SMAppServiceStatusEnabled;
	return 0;
}

void rdSetLoginItem(int on) {
	if (@available(macOS 13.0, *)) {
		NSError *e = nil;
		if (on) [SMAppService.mainAppService registerAndReturnError:&e];
		else [SMAppService.mainAppService unregisterAndReturnError:&e];
	}
}
