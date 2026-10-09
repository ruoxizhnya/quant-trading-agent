package main

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets gin to test mode exactly once for the whole package.
//
// 与 cmd/analysis/main_test.go 同源（AUD-12）：gin 的 mode 是两个包级
// 全局，并行测试各自 SetMode 会数据竞争 —— TestMain 在任何测试 goroutine
// 之前设一次。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}
