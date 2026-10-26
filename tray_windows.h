// 任务栏托盘图标（Windows）
void rdTrayRun(void);
void rdTraySet(int tag, const char *title, int enabled, int checked, int hidden);
void rdTraySetTip(const char *tip);
void rdTrayRemove(void);
void rdCopyText(const char *s);
void rdOpenURL(const char *u);
int rdConfirm(const char *title, const char *msg, int okCancel);
void rdTrayDiag(int *ok, int *err, int *wnd);
