package pipeline

import (
	"context"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/intent"
	yamlgen "github.com/ruoxizhnya/quant-trading-agent/pkg/ai/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P1-2b：搜索控制器采出的参数必须能进到底座执行。
//
// 没有这条通道，控制器搜它的、底座跑自己的 —— 一轮下来是同一个策略重复
// N 遍，实验日志看着热闹，其实什么都没试。

func newOverridePipeline() *Pipeline {
	return NewPipelineWithDeps(intent.NewParser(), yamlgen.NewGenerator(), &ai.MockClient{})
}

// TestExecute_ParameterOverridesReachYAML：覆盖的参数要真的进到生成的配置里。
func TestExecute_ParameterOverridesReachYAML(t *testing.T) {
	ctx := WithParameterOverrides(context.Background(), map[string]any{"lookback_days": 45})

	res, err := newOverridePipeline().Execute(ctx, "做一个动量策略", &mockBacktestRunner{})
	require.NoError(t, err)

	assert.Contains(t, res.YAMLConfig, "45")
	assert.NotContains(t, res.YAMLConfig, "ts_pct_change(close, 20)",
		"不能仍是默认窗口 —— 那样等于覆盖没生效")
}

// TestExecute_SameDescriptionDifferentParams：同一句描述，换参数要换策略。
// 这条就是「搜索」二字的具体含义。
func TestExecute_SameDescriptionDifferentParams(t *testing.T) {
	p := newOverridePipeline()
	base := context.Background()

	r1, err := p.Execute(
		WithParameterOverrides(base, map[string]any{"lookback_days": 20}),
		"做一个动量策略", &mockBacktestRunner{})
	require.NoError(t, err)

	r2, err := p.Execute(
		WithParameterOverrides(base, map[string]any{"lookback_days": 50}),
		"做一个动量策略", &mockBacktestRunner{})
	require.NoError(t, err)

	assert.NotEqual(t, r1.YAMLConfig, r2.YAMLConfig,
		"换一组参数必须换一份配置，否则搜索毫无意义")
}

// TestExecute_NoOverridesKeepsDefaultBehaviour：不传参时行为不变 ——
// 大量既有调用不带参数，不能因为加了通道就改掉它们。
func TestExecute_NoOverridesKeepsDefaultBehaviour(t *testing.T) {
	res, err := newOverridePipeline().Execute(context.Background(), "做一个动量策略", &mockBacktestRunner{})
	require.NoError(t, err)

	assert.Contains(t, res.YAMLConfig, "ts_pct_change(close, 20)", "不传参时用默认窗口")
}

// TestExecute_OverridesAddNewParameter：覆盖一个意图里原本没有的参数，
// 也要能带上（搜索空间不限于意图解析得出的那几个）。
func TestExecute_OverridesAddNewParameter(t *testing.T) {
	ctx := WithParameterOverrides(context.Background(), map[string]any{"min_strength": 0.9})

	res, err := newOverridePipeline().Execute(ctx, "做一个动量策略", &mockBacktestRunner{})
	require.NoError(t, err)
	require.NotNil(t, res.Intent)

	found := false
	for _, p := range res.Intent.Parameters {
		if p.Name == "min_strength" {
			found = true
		}
	}
	assert.True(t, found, "新参数要出现在意图里，才会被 YAML 生成用上")
}
