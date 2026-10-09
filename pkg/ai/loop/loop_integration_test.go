package loop

import (
	"context"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/intent"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/pipeline"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/search"
	yamlgen "github.com/ruoxizhnya/quant-trading-agent/pkg/ai/yaml"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubBacktest 顶替真实回测引擎：真回测要查行情、跑撮合，一轮几十次下来太慢，
// 而这里要验证的是「控制器 × pipeline × 实验日志」三者串不串得起来。
type stubBacktest struct{ n int }

func (s *stubBacktest) RunBacktest(ctx context.Context, name string, pool []string, startDate, endDate string) (*domain.BacktestResult, error) {
	s.n++
	return &domain.BacktestResult{
		SharpeRatio: float64(s.n) * 0.25,
		TotalReturn: 0.02 * float64(s.n),
		TotalTrades: s.n * 3,
	}, nil
}

// TestRun_RealPipelineAndRealDB 是 P1-2 的验收：真 pipeline + 真库跑一轮
// 多次尝试，然后从库里把整条路径回放出来。
//
// 这也是 P1-1c 缺的那一半 —— 单次尝试能回放早就有了，这里要证明**多步**
// 也讲得通：seq 连续、状态齐全、父代指得对。
//
// DB 没起就 skip（与 pkg/storage 的约定一致）：连不上库是环境问题。
func TestRun_RealPipelineAndRealDB(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewPostgresStore(ctx,
		"postgres://postgres:postgres@localhost:5432/quant_trading?sslmode=disable")
	if err != nil {
		t.Skipf("skipping: cannot connect to DB: %v", err)
	}
	defer store.Close()

	runID := "itest_loop_run"
	defer store.DB().Exec(ctx, "DELETE FROM experiments WHERE run_id=$1", runID)

	p := pipeline.NewPipelineWithDeps(
		intent.NewParser(), yamlgen.NewGenerator(), &ai.MockClient{},
		pipeline.WithExperimentSink(store),
	)
	// 参数名必须是 lookback_days —— 与意图参数同名，覆盖才生效。
	// 写成 lookback 会静默失效（错配不报错，只是表达式不变），
	// 这个坑正是下面那条「表达式必须不同」的断言要兜住的。
	space := &search.SearchSpace{Params: []search.ParamDef{
		{Name: "lookback_days", Type: "int", Min: 10, Max: 60},
	}}
	ctrl := NewController(p, NewTPEProposer(space, 42), &stubBacktest{})

	const tries = 5
	out, err := ctrl.Run(ctx, Config{
		RunID: runID, Description: "做一个动量策略", MaxTries: tries,
	})
	require.NoError(t, err)
	require.Len(t, out.Tries, tries)
	assert.Equal(t, StopExhausted, out.Stopped)

	// 从库里回放 —— 这是「能回放一条完整探索路径」的实证。
	rows, err := store.ListExperiments(ctx, runID)
	require.NoError(t, err)
	require.Len(t, rows, tries, "跑几次就该有几行，一次都不能少")

	seenParent := 0
	for i, r := range rows {
		assert.Equal(t, i, r.Seq, "seq 必须连续，回放才有意义")
		assert.Equal(t, runID, r.RunID)
		assert.Equal(t, storage.ExperimentStatusCompleted, r.Status, "第 %d 次没跑完", i)
		require.NotNil(t, r.Metrics, "第 %d 次没有指标", i)
		assert.NotEmpty(t, r.Expression, "第 %d 次看不出跑了什么信号", i)
		assert.NotEmpty(t, r.Hypothesis, "第 %d 次没说为什么试这个", i)
		if r.ParentID != nil {
			seenParent++
		}
	}
	assert.Positive(t, seenParent, "TPE 阶段应该产生父子链 —— 否则路径讲不出「为什么转到下一步」")

	// P1-2b 的核心：一轮下来不能全是同一个表达式。全是同一个，说明参数
	// 根本没进执行，这一轮等于把同一个策略跑了 N 遍。
	distinct := map[string]struct{}{}
	for _, r := range rows {
		distinct[r.Expression] = struct{}{}
	}
	assert.Greater(t, len(distinct), 1,
		"一轮探索应出现不同的表达式，实际只有 %d 种（参数没进执行？）", len(distinct))
}

// TestRun_DifferentParamsProduceDifferentConfig 是 P1-2b 的**不带库**回归。
//
// 上面那条依赖真库，DB 缺席时会 skip；而「参数没进执行」这种问题一旦回归，
// 恰恰是最难被发现的（单测照绿、日志照记）。所以这里用真 pipeline 再锁一次。
func TestRun_DifferentParamsProduceDifferentConfig(t *testing.T) {
	p := pipeline.NewPipelineWithDeps(
		intent.NewParser(), yamlgen.NewGenerator(), &ai.MockClient{})
	space := &search.SearchSpace{Params: []search.ParamDef{
		{Name: "lookback_days", Type: "int", Min: 10, Max: 60},
	}}
	ctrl := NewController(p, NewTPEProposer(space, 42), &stubBacktest{})

	out, err := ctrl.Run(context.Background(), Config{
		RunID: "cfg-variety", Description: "做一个动量策略", MaxTries: 6,
	})
	require.NoError(t, err)
	require.Len(t, out.Tries, 6)

	distinct := map[string]struct{}{}
	for _, a := range out.Tries {
		require.NotNil(t, a.Result)
		distinct[a.Result.YAMLConfig] = struct{}{}
	}
	assert.Greater(t, len(distinct), 1,
		"同一句描述、不同参数，必须产出不同配置；现在只有 %d 种", len(distinct))
}
