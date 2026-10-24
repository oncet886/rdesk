//go:build !darwin && !windows

package main

import "errors"

// Linux 等平台：暂时只支持看画面，不支持键鼠控制
func inputInit() error {
	return errors.New("当前系统暂不支持键鼠控制，只能查看画面")
}
func inputAvailable() bool                    { return false }
func mouseMove(x, y float64)                  {}
func mouseButton(b int, d bool, x, y float64) {}
func mouseWheel(dx, dy int)                   {}
func keyEvent(code string, down bool)         {}
func typeText(s string)                       {}
func releaseAll()                             {}
func cursorPos() (float64, float64, bool)     { return 0, 0, false }

func cursorShape() ([]byte, float64, float64, float64, float64, bool) { return nil, 0, 0, 0, 0, false }
func cursorShapeReset()                                               {}

func permStatus() (bool, bool) { return true, false }
