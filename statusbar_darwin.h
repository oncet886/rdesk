// 菜单栏图标：Objective-C 实现在 statusbar_darwin.m
void rdStatusRun(void);
void rdStatusSet(int tag, const char *title, int enabled, int checked, int hidden);
void rdStatusConnected(int on);
void rdPasteboardCopy(const char *s);
void rdOpenURL(const char *u);
int rdConfirm(const char *title, const char *msg, const char *ok);
int rdIsBundle(void);
int rdLoginItem(void);
void rdSetLoginItem(int on);
