package main

import "image"

// capturer: 截屏来源。Mac 用 ScreenCaptureKit 推流，其他平台逐张截图。
type capturer interface {
	configure(display, maxW, fps int) error
	// grab 返回最新画面；fresh=false 表示和上次相比屏幕没变
	grab() (img *image.RGBA, fresh bool, err error)
	close()
}
