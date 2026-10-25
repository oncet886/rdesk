//go:build windows

package main

import (
	"fmt"
	"os"
	"time"
)

// RDESK_BENCH=1 rdesk.exe：测转换和编码速度后等 2 分钟退出（方便截图看结果）
func benchAndExit() {
	winBench()
	fmt.Println("测速完成")
	time.Sleep(2 * time.Minute)
	os.Exit(0)
}
