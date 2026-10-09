package builtin

import (
	"context"
	"math"
	"testing"

	"github.com/ruoxizhnya/quant-trading/pkg/strategy/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 造一段每日表现序列。fn 决定收益率，权益按收益累积。
func healthSeries(n int, ret func(i int) float64) []interface{} {
	equity := 1_000_000.0
	out := make([]interface{}, 0, n)
	for i := 0; i < n; i++ {
		r := ret(i)
		equity *= (1 + r)
		out = append(out, map[string]interface{}{
			"daily_return": r,
			"equity":       equity,
		})
	}
	return out
}

// 稳稳赚钱的序列：不该报漂移，也不该有告警。
func steadySeries() []interface{} {
	return healthSeries(200, func(i int) float64 {
		// 小幅正收益 + 一点确定性抖动（避免零方差）
		if i%7 == 0 {
			return -0.002
		}
		return 0.004
	})
}

// 前稳后崩：后半段均值明显下移 —— 这正是概念漂移该抓到的形状。
func driftingSeries() []interface{} {
	return healthSeries(200, func(i int) float64 {
		if i < 100 {
			return 0.01
		}
		return -0.02
	})
}

func TestStrategyHealthTool_SteadySeriesIsHealthy(t *testing.T) {
	tool := NewStrategyHealthTool()
	require.Equal(t, "monitor.strategy_health", tool.Name())

	got, err := tool.Execute(context.Background(), map[string]interface{}{
		"series":   steadySeries(),
		"strategy": "steady",
	})
	require.NoError(t, err)
	res := got.(*strategyHealthResult)

	assert.Equal(t, "steady", res.Strategy)
	assert.Equal(t, 200, res.Points)
	assert.False(t, res.DriftDetected, "稳定序列不该报漂移")
	assert.Empty(t, res.Alerts, "稳定序列不该有告警")
}

// 这是 P2-6 的价值所在：均值从 +1% 掉到 -2%，必须被抓到。
// 只看滚动 Sharpe 也能发现，但漂移检测抓的是**分布变化**，
// 它能在指标还没破线时就给出信号。
func TestStrategyHealthTool_DetectsConceptDrift(t *testing.T) {
	tool := NewStrategyHealthTool()

	got, err := tool.Execute(context.Background(), map[string]interface{}{
		"series": driftingSeries(),
	})
	require.NoError(t, err)
	res := got.(*strategyHealthResult)

	assert.True(t, res.DriftDetected,
		"均值从 +1% 掉到 -2% 必须被检出 —— 这是概念漂移检测唯一存在的理由")
	assert.NotEmpty(t, res.Drift)
	assert.Contains(t, res.Verdict, "漂移")
}

// 样本不足时不能假装健康。
//
// drift.Detector 需要 window*2 个样本。用 10 个点算出"未见漂移"比不判断
// 更危险 —— 它会让人以为策略没问题。
func TestStrategyHealthTool_ShortSeriesSaysInsufficient(t *testing.T) {
	tool := NewStrategyHealthTool()

	got, err := tool.Execute(context.Background(), map[string]interface{}{
		"series": healthSeries(10, func(int) float64 { return 0.01 }),
	})
	require.NoError(t, err)
	res := got.(*strategyHealthResult)

	assert.Contains(t, res.Verdict, "样本不足",
		"样本不够必须说出来，不能给出看起来正常的结论")
	assert.False(t, res.DriftDetected)
}

func TestStrategyHealthTool_EmptySeriesIsRejected(t *testing.T) {
	tool := NewStrategyHealthTool()

	_, err := tool.Execute(context.Background(), map[string]interface{}{"series": []interface{}{}})
	require.Error(t, err, "空序列不该返回'健康'")

	_, err = tool.Execute(context.Background(), map[string]interface{}{})
	require.Error(t, err, "缺 series 必须报参数错误")
}

// 无状态：同样的输入跑两次必须一样。
// monitor 本身是有状态的，工具层不能让这个状态泄漏到调用之间。
func TestStrategyHealthTool_IsStateless(t *testing.T) {
	tool := NewStrategyHealthTool()
	args := map[string]interface{}{"series": driftingSeries()}

	first, err := tool.Execute(context.Background(), args)
	require.NoError(t, err)
	second, err := tool.Execute(context.Background(), args)
	require.NoError(t, err)

	a := first.(*strategyHealthResult)
	b := second.(*strategyHealthResult)
	assert.Equal(t, a.Points, b.Points)
	assert.Equal(t, a.DriftDetected, b.DriftDetected)
	assert.Equal(t, a.Verdict, b.Verdict)
}

// 指标：滚动回撤要能算出来（权益从高点掉下来）。
func TestStrategyHealthTool_ComputesRollingDrawdown(t *testing.T) {
	tool := NewStrategyHealthTool()

	got, err := tool.Execute(context.Background(), map[string]interface{}{
		"series": healthSeries(200, func(i int) float64 {
			if i > 150 {
				return -0.01 // 后段持续下跌
			}
			return 0.005
		}),
	})
	require.NoError(t, err)
	res := got.(*strategyHealthResult)

	assert.Greater(t, res.RollingMaxDD, 0.0, "后段持续下跌，滚动回撤必须为正")
	assert.False(t, math.IsNaN(res.RollingSharpe), "Sharpe 不该是 NaN（零方差序列要能处理）")
	assert.NotEmpty(t, res.Alerts, "回撤破线应该有告警")
}

// 阈值要报出来 —— 否则调用方看到"告警"也不知道是相对什么标准。
func TestStrategyHealthTool_ReportsThresholds(t *testing.T) {
	tool := NewStrategyHealthTool()

	got, err := tool.Execute(context.Background(), map[string]interface{}{"series": steadySeries()})
	require.NoError(t, err)
	res := got.(*strategyHealthResult)

	assert.Equal(t, monitor.DefaultWindowSize, res.Thresholds.WindowSize)
	assert.NotZero(t, res.Thresholds.MinSharpe)
	assert.NotZero(t, res.Thresholds.MaxDrawdown)
}
