package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/intent"
	yamlgen "github.com/ruoxizhnya/quant-trading-agent/pkg/ai/yaml"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P0-5：Execute 此前有两条平行的路 —— Stage 2 产出可执行的 YAML，
// Stage 3/4 让 LLM 写一段 Go 代码编译完就删掉。而 Stage 5 的回测拿
// `parsedIntent.StrategyName` 去查策略，这个名字**从未被注册过**，
// 于是必然 strategy not found。
//
// 下面这组测试锁的契约是：回测跑的必须是「已注册进 registry 的 YAML /
// 表达式策略」，而 LLM 生成的 Go 代码只是可审阅的 artifact（编译不过
// 也不该拖累回测）。

// registryBacktestRunner 模拟真实引擎的行为：按名字从全局 registry 取
// 策略，取不到就报错。真实引擎就是这么找策略的（见
// cmd/analysis 的 strategyEngineAdapter → backtest.Engine）。
type registryBacktestRunner struct {
	RequestedName string
	Calls         int
}

func (r *registryBacktestRunner) RunBacktest(ctx context.Context, strategyName string, stockPool []string, startDate, endDate string) (*domain.BacktestResult, error) {
	r.RequestedName = strategyName
	r.Calls++
	if _, err := strategy.GlobalGet(strategyName); err != nil {
		return nil, fmt.Errorf("strategy not found: %s", strategyName)
	}
	return &domain.BacktestResult{TotalTrades: 3, TotalReturn: 0.05}, nil
}

func newTestPipeline(mock *ai.MockClient) *Pipeline {
	return NewPipelineWithDeps(intent.NewParser(), yamlgen.NewGenerator(), mock)
}

// TestExecute_RegistersStrategyBeforeBacktest 是 P0-5 的核心回归：
// 回测之前，策略必须已经在 registry 里。
func TestExecute_RegistersStrategyBeforeBacktest(t *testing.T) {
	mock := &ai.MockClient{Configured: true, ChatResponse: "package broken\nthis is not go code"}
	runner := &registryBacktestRunner{}

	res, err := newTestPipeline(mock).Execute(context.Background(), "做一个动量策略", runner)

	require.NoError(t, err)
	assert.Equal(t, StageComplete, res.Status)
	assert.Equal(t, 1, runner.Calls, "回测应该真的被跑了一次")
	require.NotNil(t, res.BacktestResult, "回测必须出结果，而不是 strategy not found")
	assert.Empty(t, res.BacktestError)
}

// TestExecute_BrokenGeneratedCodeIsArtifactOnly：LLM 写的代码编译不过时，
// 只记录不阻断 —— 真正执行的是 YAML/表达式策略，两者不是同一个东西。
func TestExecute_BrokenGeneratedCodeIsArtifactOnly(t *testing.T) {
	mock := &ai.MockClient{Configured: true, ChatResponse: "package broken\nthis is not go code"}
	runner := &registryBacktestRunner{}

	res, err := newTestPipeline(mock).Execute(context.Background(), "做一个均值回归策略", runner)

	require.NoError(t, err)
	assert.Equal(t, StageComplete, res.Status)
	assert.NotEmpty(t, res.GeneratedCode, "生成的代码要留在结果里供审阅")
	assert.NotEmpty(t, res.BuildError, "编译失败要如实记录")
	require.NotNil(t, res.BacktestResult, "artifact 编译失败不该拖累回测")
}

// TestExecute_WithoutConfiguredLLM_StillBacktests：没配 LLM 时，
// 规则解析 + YAML 生成这条路本来就够跑，不该被代码生成环节卡住。
// （LLM 只影响 artifact 的有无，不影响能不能回测。）
func TestExecute_WithoutConfiguredLLM_StillBacktests(t *testing.T) {
	mock := &ai.MockClient{Configured: false}
	runner := &registryBacktestRunner{}

	res, err := newTestPipeline(mock).Execute(context.Background(), "做一个动量策略", runner)

	require.NoError(t, err)
	assert.Equal(t, StageComplete, res.Status)
	require.NotNil(t, res.BacktestResult)
	assert.Empty(t, res.GeneratedCode, "没有 LLM 就不该有生成的代码")
}

// TestExecuteAsync_RegistersStrategyBeforeBacktest：异步路径与主路径
// 是复制粘贴的两份逻辑，别只修一边。
func TestExecuteAsync_RegistersStrategyBeforeBacktest(t *testing.T) {
	mock := &ai.MockClient{Configured: false}
	runner := &registryBacktestRunner{}

	p := newTestPipeline(mock)
	jobID := p.ExecuteAsync(context.Background(), "做一个突破策略", runner)
	res := p.GetJob(jobID)
	res.WaitDone()

	assert.Equal(t, StageComplete, res.Status)
	require.NotNil(t, res.BacktestResult, "异步路径同样必须注册后再回测")
}
