package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/intent"
	yamlgen "github.com/ruoxizhnya/quant-trading-agent/pkg/ai/yaml"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OBS-01：无效运行（空票池 / 0 成交 / 垃圾指标）不得再报
// "Backtest completed successfully"，而要走失败路径且原因可见。

func newValidityPipeline() *Pipeline {
	return NewPipelineWithDeps(intent.NewParser(), yamlgen.NewGenerator(), &ai.MockClient{})
}

func TestExecute_InvalidRunGoesFailurePath(t *testing.T) {
	p := newValidityPipeline()
	runner := &mockBacktestRunner{result: &domain.BacktestResult{
		TotalTrades:     0,
		UniverseMaxSize: intPtr(0),
		InvalidReasons:  []string{"empty_universe", "zero_trades", "garbage_metric"},
		SortinoRatio:    1.7976931348623157e308,
	}}

	res, err := p.Execute(context.Background(), "做一个动量策略", runner)
	require.Error(t, err, "无效运行必须返回 error（调用方能判出「这次不算成功」）")
	require.NotNil(t, res)

	assert.Equal(t, StageFailed, res.Status, "无效运行的 pipeline 状态必须是失败，而不是 complete")
	require.NotNil(t, res.BacktestResult, "失败不等于信息丢失：无效原因仍在结果里")
	assert.Contains(t, res.BacktestResult.InvalidReasons, "empty_universe")
	assert.Contains(t, res.BacktestError, "invalid run")
	assert.Contains(t, res.BacktestError, "empty_universe")

	for _, line := range res.Logs {
		assert.NotContains(t, strings.ToLower(line), "completed successfully",
			"无效运行不得出现成功字样：%q", line)
	}
}

func TestExecute_ValidRunKeepsSuccessPath(t *testing.T) {
	p := newValidityPipeline()
	runner := &mockBacktestRunner{result: &domain.BacktestResult{
		TotalTrades:     12,
		UniverseMaxSize: intPtr(40),
		SharpeRatio:     1.1,
		TotalReturn:     0.18,
		// 无 InvalidReasons = 有效运行
	}}

	res, err := p.Execute(context.Background(), "做一个动量策略", runner)
	require.NoError(t, err, "正常回测路径不受 OBS-01 影响")
	require.NotNil(t, res)
	assert.Equal(t, StageComplete, res.Status)

	joined := strings.Join(res.Logs, "\n")
	assert.Contains(t, joined, "Backtest completed successfully",
		"有效运行照旧报成功 —— 护栏不能误伤正常路径")
}

// intPtr 返回 int 的指针（OBS-01 审查修复：UniverseMaxSize 改为 *int，
// nil = 未评估、0 = 明确为空 —— 未知不等于空）。
func intPtr(v int) *int { return &v }
