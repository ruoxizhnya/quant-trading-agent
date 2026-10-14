package main

// DATA-01 端到端集成测试：`universe: csi300` → IndexCode → 真票池 → 回测跑通。
//
// 这条测试串起两仓的改动：
//
//	agent 侧 buildBacktestRequest：universe 里的 "csi300" 识别为指数 → 填 IndexCode
//	core 侧 index_alias + resolveStockPool：csi300 → 000300.SH → 展开成分股
//
// 断言用 BacktestResult.UniverseMaxSize（OBS-01 引入的显式票池规模字段，
// 指针语义：nil = 未评估、0 = 明确为空）—— 「票池非空」这句话必须落在一个
// 可被质问的数字上，而不是靠"没报错"推断。
//
// 前置（缺任一项即 skip，跳过不是通过）：
//   - CONFIG_PATH 指向 core 的 analysis-service.yaml
//   - DATABASE_PASSWORD 已设
//   - DATA_SERVICE_URL 指向一个活着的 data-service（引擎经 HTTP provider 取数）
//   - index_constituents 已灌数（POST /sync/index-constituents/000300.SH）

import (
	"context"
	"os"
	"testing"

	"github.com/ruoxizhnya/quant-trading/pkg/bootstrap"
	"github.com/stretchr/testify/require"
)

func TestData01_IndexUniverse_EndToEndBacktest(t *testing.T) {
	if os.Getenv("CONFIG_PATH") == "" {
		t.Skip("需要 CONFIG_PATH（core 的 analysis-service.yaml）")
	}
	if os.Getenv("DATABASE_PASSWORD") == "" {
		t.Skip("需要 DATABASE_PASSWORD")
	}

	logger := bootstrap.InitLogger()
	_ = bootstrap.InitMetrics(logger)
	v := bootstrap.LoadConfig(logger, os.Getenv("CONFIG_PATH"))

	engine, _ := bootstrap.BuildBacktestEngine(v, logger)
	store := bootstrap.InitStore(v, logger)
	require.NotNil(t, store, "需要可用的 PostgresStore")
	engine.SetStore(store)

	adapter := NewStrategyEngineAdapter(engine)
	ctx := context.Background()

	// 区间必须落在 index_constituents 的快照覆盖内（Tushare index_weight
	// 实测覆盖 2024-10-31 ~ 2026-09-30）。区间外会展开为空并 fail-loud ——
	// 那是正确行为，另有测试专测（见 core 的 universe_expand_integration_test.go）。
	//
	// 区间刻意取短（一个季度）：引擎逐票经 HTTP provider 拉行情，300 只票 ×
	// 一年半实测要 20 分钟以上（2026-10-14 实测超时），这里要证的是「票池能
	// 展开 + 回测能跑出有效运行」，不是回测性能。
	res, err := adapter.RunBacktest(ctx, "momentum", []string{"csi300"}, "2026-01-01", "2026-03-31")
	require.NoError(t, err, "universe:csi300 展开后不应 fail-loud（IndexCode 链路已通）")
	require.NotNil(t, res)

	// 本测试的断言只压在**票池展开**上（DATA-01 的目标），不压在「回测有效」上：
	// 现场装配没有注册任何策略（引擎日志 available: []），因此必然 0 交易 ⇒
	// invalid_reasons=[zero_trades, garbage_metric]。那是既有装配状况，与本单无关，
	// 拿它当断言只会让测试验错东西。
	require.NotNil(t, res.UniverseMaxSize, "UniverseMaxSize 必须是测量值而不是 nil（未评估）")
	require.Greater(t, *res.UniverseMaxSize, 0, "票池规模必须 > 0 —— universe:csi300 真的展开成了成分股")

	t.Logf("端到端：universe=[csi300] → 票池规模 %d，trades=%d，invalid_reasons=%v（策略未注册属既有装配状况）",
		*res.UniverseMaxSize, res.TotalTrades, res.InvalidReasons)
}
