package pipeline

import (
	"context"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/intent"
	yamlgen "github.com/ruoxizhnya/quant-trading-agent/pkg/ai/yaml"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 真库取证：1a 的存储层与 1b 的生产者必须真的能串起来。
//
// 内存 sink 只能证明 pipeline 的行为契约，证明不了「库里真的有行」——
// 而 P1-1 的验收标准恰恰是后者。所以这里跑真 PostgresStore。
//
// DB 没起就 skip（与 pkg/storage 的 testStore 同一套约定）：连不上库是
// 环境问题，不是被测代码的问题，失败信号没有意义。
func TestExperimentLog_RealDB_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewPostgresStore(ctx,
		"postgres://postgres:postgres@localhost:5432/quant_trading?sslmode=disable")
	if err != nil {
		t.Skipf("skipping: cannot connect to DB: %v", err)
	}
	defer store.Close()

	runID := "itest_run_momentum"
	defer store.DB().Exec(ctx, "DELETE FROM experiments WHERE run_id=$1", runID)

	p := NewPipelineWithDeps(
		intent.NewParser(), yamlgen.NewGenerator(), &ai.MockClient{},
		WithExperimentSink(store),
	)
	runner := &mockBacktestRunner{result: &domain.BacktestResult{
		TotalTrades: 9, TotalReturn: 0.31, SharpeRatio: 1.45,
	}}

	execCtx := WithExperimentContext(ctx, ExperimentContext{
		RunID:      runID,
		Seq:        0,
		Hypothesis: "动量大窗口更好",
	})
	res, err := p.Execute(execCtx, "做一个动量策略", runner)
	require.NoError(t, err)
	require.Equal(t, StageComplete, res.Status)

	// 从库里读回来 —— 这是「能回放一条完整探索路径」的最小证明。
	list, err := store.ListExperiments(ctx, runID)
	require.NoError(t, err)
	require.Len(t, list, 1, "跑完一次尝试，库里必须有一行")

	got := list[0]
	assert.Equal(t, storage.ExperimentStatusCompleted, got.Status)
	assert.Equal(t, "动量大窗口更好", got.Hypothesis)
	assert.NotEmpty(t, got.Expression, "回放时要能看出这次跑的是什么信号")
	assert.NotEmpty(t, got.StrategyName)
	require.NotNil(t, got.Metrics)
	assert.InDelta(t, 1.45, got.Metrics.SharpeRatio, 1e-9)
	assert.Equal(t, 9, got.Metrics.TotalTrades)
	assert.NotNil(t, got.FinishedAt)
	assert.NotEmpty(t, got.Params["universe"], "参数向量里要有回测输入")
}
