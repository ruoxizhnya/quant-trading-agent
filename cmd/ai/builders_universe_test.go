package main

// DATA-01 的 agent 侧单测：`universe: csi300` 必须走 IndexCode，不再进 StockPool。
//
// 背景（逐字核对）：探索链路把 YAML 的 universe 整串交给回测适配器，而
// `csi300` 是**指数**不是股票代码。以前它被塞进 StockPool ⇒ core 侧按股票代码
// 去上市日历里找 ⇒ 找不到 ⇒ 票池归零 ⇒ 回测 fail-loud invalid，
// 表现成「数据没灌」，实际是口径错位。

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildBacktestRequest_UniverseIndexGoesToIndexCode(t *testing.T) {
	req := buildBacktestRequest("momentum", []string{"csi300"}, "2025-01-01", "2026-10-01")

	assert.Equal(t, "csi300", req.IndexCode, "universe:csi300 必须走 IndexCode 展开分支")
	assert.Empty(t, req.StockPool, "\"csi300\" 不得再进 StockPool（会被当成一只股票）")
	assert.Equal(t, "momentum", req.Strategy)
}

func TestBuildBacktestRequest_StockPoolUnchanged(t *testing.T) {
	pool := []string{"600519.SH", "000001.SZ"}
	req := buildBacktestRequest("momentum", pool, "2025-01-01", "2026-10-01")

	assert.Empty(t, req.IndexCode, "个股池不得被误判成指数")
	assert.Equal(t, pool, req.StockPool, "个股池原样透传")
}

func TestSplitUniverseIndex(t *testing.T) {
	cases := []struct {
		name      string
		in        []string
		wantCode  string
		wantIndex bool
	}{
		{"YAML 默认", []string{"csi300"}, "csi300", true},
		{"大小写", []string{"CSI300"}, "CSI300", true},
		{"带空格", []string{" csi500 "}, "csi500", true},
		{"中文写法", []string{"沪深300"}, "沪深300", true},
		{"已是指数代码", []string{"000300.SH"}, "000300.SH", true},
		{"未登记但像指数", []string{"csi1000"}, "csi1000", true},
		{"单只个股", []string{"600519.SH"}, "", false},
		{"深市个股（000001.SZ 是股票不是指数）", []string{"000001.SZ"}, "", false},
		{"个股池", []string{"600519.SH", "000001.SZ"}, "", false},
		{"空", nil, "", false},
		{"all（parseUniverse 的空语义）", []string{}, "", false},
		{"混合列表不猜", []string{"csi300", "600519.SH"}, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, rest, ok := splitUniverseIndex(tc.in)
			assert.Equal(t, tc.wantIndex, ok)
			assert.Equal(t, tc.wantCode, code)
			if tc.wantIndex {
				assert.Empty(t, rest, "指数形态下不应再有个股残留")
			} else {
				assert.Equal(t, tc.in, rest, "非指数形态必须原样返回，交给原有路径")
			}
		})
	}
}
