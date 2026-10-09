package main

import (
	"context"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/pipeline"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubBacktestRunner is a test double implementing pipeline.BacktestRunner.
// It records that RunBacktest was invoked, proving the handler wires the
// runner through to the pipeline instead of passing nil.
type stubBacktestRunner struct {
	called       bool
	strategyName string
}

func (s *stubBacktestRunner) RunBacktest(ctx context.Context, strategyName string, stockPool []string, startDate, endDate string) (*domain.BacktestResult, error) {
	s.called = true
	s.strategyName = strategyName
	return &domain.BacktestResult{}, nil
}

// TestNewPipelineHandler_DefaultHasNoRunner verifies backward compatibility:
// when no runner option is supplied, handler.runner stays nil and the pipeline
// will skip the backtest stage (the legacy behaviour).
func TestNewPipelineHandler_DefaultHasNoRunner(t *testing.T) {
	handler := NewPipelineHandler()
	assert.Nil(t, handler.runner, "default handler should have nil runner for backward compat")
}

// TestNewPipelineHandler_WithRunner verifies the S7-P0-1 fix: the handler
// must accept a BacktestRunner dependency via functional option so that
// main.go can inject copilotRunner instead of leaving it nil.
//
// Before the fix, NewPipelineHandler accepted no arguments and RunPipeline
// hardcoded nil as the runner, causing Stage 5 (backtest) to be silently
// skipped on every request — the pipeline was never end-to-end runnable.
func TestNewPipelineHandler_WithRunner(t *testing.T) {
	runner := &stubBacktestRunner{}
	handler := NewPipelineHandler(WithBacktestRunner(runner))

	require.NotNil(t, handler.runner, "handler must hold the injected runner")
	assert.Same(t, runner, handler.runner, "handler.runner must be the exact instance injected")
}

// countingSink 记录实验日志被写入了几次。
type countingSink struct {
	inserts   int
	completed int
}

func (c *countingSink) InsertExperiment(ctx context.Context, e *storage.Experiment) (int64, error) {
	c.inserts++
	return int64(c.inserts), nil
}

func (c *countingSink) UpdateExperiment(ctx context.Context, id int64, u storage.ExperimentUpdate) error {
	return nil
}

func (c *countingSink) CompleteExperiment(ctx context.Context, id int64, m *storage.ExperimentMetrics, errMsg string) error {
	c.completed++
	return nil
}

// TestNewPipelineHandler_WiresExperimentSink 锁的是 P1-1b 的最后一环：
//
// pipeline 有能力记日志还不够 —— **服务启动时必须真的把 sink 接上**。
// 少了这一步，线上跑一百次实验，库里依然一行都没有，而过拟合检测完全
// 建立在「试了多少次」这个数字上。
func TestNewPipelineHandler_WiresExperimentSink(t *testing.T) {
	sink := &countingSink{}
	h := NewPipelineHandler(WithExperimentSink(sink))

	_, err := h.pipeline.Execute(context.Background(), "做一个动量策略", nil)
	require.NoError(t, err)

	assert.Equal(t, 1, sink.inserts, "注入 sink 后，跑一次必须落一行日志")
	assert.Equal(t, 1, sink.completed, "跑完要收尾，不能永远 running")
}

// TestNewPipelineHandler_NoSinkStillRuns：不注入 sink 时链路照跑 ——
// 日志是观测设施，不是硬依赖。
func TestNewPipelineHandler_NoSinkStillRuns(t *testing.T) {
	h := NewPipelineHandler()

	_, err := h.pipeline.Execute(context.Background(), "做一个动量策略", nil)
	require.NoError(t, err)
}

// TestNewPipelineHandler_WithRunner_NilStaysNil verifies that explicitly
// passing a nil runner via the option is equivalent to not passing it.
func TestNewPipelineHandler_WithRunner_NilStaysNil(t *testing.T) {
	handler := NewPipelineHandler(WithBacktestRunner(nil))
	assert.Nil(t, handler.runner)
}

// TestPipelineHandler_ImplementsRunnerContract is a compile-time check that
// stubBacktestRunner satisfies the pipeline.BacktestRunner interface.
func TestPipelineHandler_ImplementsRunnerContract(t *testing.T) {
	var _ pipeline.BacktestRunner = (*stubBacktestRunner)(nil)
}
