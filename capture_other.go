//go:build !darwin

package main

import (
	"errors"
	"image"
	"time"

	"github.com/kbinani/screenshot"
)

type shotCapturer struct{ display, maxW int }

func newCapturer() capturer { return &shotCapturer{} }

func (c *shotCapturer) configure(display, maxW, fps int) error {
	c.display, c.maxW = display, maxW
	return nil
}

func (c *shotCapturer) grab() (*image.RGBA, bool, error) {
	img, err := screenshot.CaptureRect(screenshot.GetDisplayBounds(c.display))
	if err != nil {
		return nil, false, err
	}
	return downscale(img, c.maxW), true, nil
}

func (c *shotCapturer) close() {}

// 其他平台暂无硬件 H.264，走 JPEG
func videoAvailable() bool { return false }
func videoStart(display, maxW, fps, kbps int, audio bool) error {
	return errors.New("不支持")
}
func videoNext(timeout time.Duration) ([]byte, bool, int, int, bool) {
	time.Sleep(timeout)
	return nil, false, 0, 0, false
}
func videoStopped() bool                              { return false }
func videoCounters() (uint64, uint64, uint64, uint64) { return 0, 0, 0, 0 }
func videoKeyframe()                                  {}
func videoSetBitrate(k int)                           {}
func videoPause(p bool)                               {}
func videoStop()                                      {}

func audioNext(timeout time.Duration) ([]byte, bool) {
	time.Sleep(timeout)
	return nil, false
}

func audioStats() (uint64, float64) { return 0, 0 }
