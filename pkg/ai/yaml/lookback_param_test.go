package yaml

import (
	"fmt"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/intent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P1-2b：默认表达式的回看窗口必须跟着意图参数走。
//
// 此前窗口写死 20，后果是控制器采样出来的参数**进不了执行** —— 一轮搜索
// 跑下来是同一个策略重复 N 遍，实验日志看着热闹，其实什么都没试。

// TestDefaultSignalExpression_DifferentLookbackDifferentExpr 是这一片的核心：
// 换参数必须换表达式。这条不成立，搜索就等于没搜。
func TestDefaultSignalExpression_DifferentLookbackDifferentExpr(t *testing.T) {
	mk := func(lb int) string {
		i := &intent.Intent{
			StrategyType: intent.StrategyTypeMomentum,
			Parameters:   []intent.Parameter{{Name: "lookback_days", Value: lb}},
		}
		e, ok := defaultSignalExpression(i)
		require.True(t, ok)
		return e
	}

	assert.NotEqual(t, mk(20), mk(45), "换一组参数，表达式必须真的变")
}

// TestDefaultSignalExpression_UsesLookbackParam：各种意图类型都要用上参数，
// 不能只有动量生效 —— 只改一处会留下「某些策略搜了没用」的坑。
func TestDefaultSignalExpression_UsesLookbackParam(t *testing.T) {
	cases := []struct {
		name string
		typ  intent.StrategyType
		lb   int
	}{
		{"momentum", intent.StrategyTypeMomentum, 35},
		{"mean_reversion", intent.StrategyTypeMeanReversion, 10},
		{"trend_following", intent.StrategyTypeTrendFollowing, 25},
		{"breakout", intent.StrategyTypeBreakout, 40},
		{"multi_factor", intent.StrategyTypeMultiFactor, 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := &intent.Intent{
				StrategyType: tc.typ,
				Parameters:   []intent.Parameter{{Name: "lookback_days", Value: tc.lb}},
			}
			expr, ok := defaultSignalExpression(i)
			require.True(t, ok)
			assert.Contains(t, expr, fmt.Sprintf("%d", tc.lb),
				"表达式里应出现回看窗口 %d，实际: %s", tc.lb, expr)
		})
	}
}

// TestDefaultSignalExpression_DefaultsWhenNoParam：意图没给参数时要退回默认
// 窗口，而不是生成空表达式或报错 —— 大量既有调用不带参数。
func TestDefaultSignalExpression_DefaultsWhenNoParam(t *testing.T) {
	i := &intent.Intent{StrategyType: intent.StrategyTypeMomentum}

	expr, ok := defaultSignalExpression(i)
	require.True(t, ok)
	assert.Contains(t, expr, "20", "没给参数时退回默认窗口 20")
}

// TestDefaultSignalExpression_RejectsBadLookback：非法窗口不能原样进表达式 ——
// 搜索空间的边界值偶尔会采到奇葩数，不能让它污染出 `ts_mean(close, -5)`。
func TestDefaultSignalExpression_RejectsBadLookback(t *testing.T) {
	for _, bad := range []int{-5, 0} {
		i := &intent.Intent{
			StrategyType: intent.StrategyTypeMomentum,
			Parameters:   []intent.Parameter{{Name: "lookback_days", Value: bad}},
		}
		expr, ok := defaultSignalExpression(i)
		require.True(t, ok)
		assert.NotContains(t, expr, fmt.Sprintf("(%d", bad),
			"非法窗口 %d 不该进表达式，实际: %s", bad, expr)
	}
}
