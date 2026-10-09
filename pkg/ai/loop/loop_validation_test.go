package loop

import (
	"context"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/pipeline"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/validation"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// 验证器链（P2-9）接进循环的取证。
//
// 五片校验器写完之后全仓零调用，整包是孤儿 —— 这个测试守的就是「接上了」：
// 每次尝试跑完，Attempt 上必须真的带着质疑清单和概率。

// richTryRunner 返回**完整**的回测结果：带净值曲线和成交。
// 只给 Sharpe 的话，稳健维（要净值曲线）和经济维（要成交）都评估不了，
// 那样的测试证不出接线成功。
type richTryRunner struct {
	sharpe  []float64
	failAt  map[int]bool
	periods int
	nextID  int64
}

func (f *richTryRunner) Execute(ctx context.Context, description string, runner pipeline.BacktestRunner) (*pipeline.Result, error) {
	ec := pipeline.ExperimentContextFrom(ctx)

	sh := 0.0
	if ec.Seq < len(f.sharpe) {
		sh = f.sharpe[ec.Seq]
	}
	f.nextID++

	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	values := make([]domain.PortfolioValue, f.periods)
	eq := 1_000_000.0
	for i := range values {
		eq *= 1.0002
		values[i] = domain.PortfolioValue{Date: start.AddDate(0, 0, i), TotalValue: eq}
	}
	trades := []domain.Trade{{
		Symbol: "600000.SH", Direction: domain.DirectionLong,
		Quantity: 1000, Price: 50, Timestamp: start,
	}}

	res := &pipeline.Result{
		ID:           "job",
		Status:       pipeline.StageComplete,
		ExperimentID: f.nextID,
		BacktestResult: &domain.BacktestResult{
			SharpeRatio:     sh,
			TotalReturn:     eq/1_000_000.0 - 1,
			TotalTrades:     1,
			PortfolioValues: values,
			Trades:          trades,
		},
	}
	if f.failAt[ec.Seq] {
		return res, context.DeadlineExceeded
	}
	return res, nil
}

// stepProposer 每次把窗口往前推一点，模拟一轮真正的参数探索。
type stepProposer struct{}

func (stepProposer) Suggest(observed []Observation) Suggestion {
	window := 15 + len(observed)*5
	return Suggestion{
		Params:     map[string]any{"lookback_days": window},
		Hypothesis: "试试更长的回看窗口",
	}
}

func TestLoop_EachAttemptCarriesVerdict(t *testing.T) {
	runner := &richTryRunner{
		sharpe:  []float64{1.0, 1.2, 0.9, 1.4},
		periods: 300,
	}

	out, err := NewController(runner, stepProposer{}, nil).Run(context.Background(), Config{
		MaxTries: 4,
		Bias: validation.BiasInput{
			PITVerified:     true,
			DataLagKnown:    true,
			DataLagDays:     45,
			PoolSource:      validation.PoolSourcePointInTime,
			PriceAdjustment: validation.AdjustPre,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(out.Tries) != 4 {
		t.Fatalf("应跑满 4 次，实际 %d", len(out.Tries))
	}
	for i, a := range out.Tries {
		if a.Verdict == nil {
			t.Fatalf("第 %d 次尝试没有 Verdict —— 验证器没接上", i)
		}
		if len(a.Verdict.Challenges) == 0 {
			t.Fatalf("第 %d 次的质疑清单是空的，说明某一维根本没跑", i)
		}
	}

	// 偏差维的输入来自 Config，必须被评估到。
	if _, ok := out.Tries[0].Verdict.Dimensions[validation.DimensionBias]; !ok {
		t.Fatalf("传了 Bias 输入却没评估偏差维：dimensions=%v unassessed=%v",
			out.Tries[0].Verdict.Dimensions, out.Tries[0].Verdict.Unassessed)
	}
}

// 邻域来自「本轮已经试过的那些」—— 一轮探索天然就是参数邻域。
// 第 1 次还没有邻域，后面的必须累积起来。
func TestLoop_VerdictNeighborhoodGrowsWithAttempts(t *testing.T) {
	runner := &richTryRunner{
		sharpe:  []float64{1.0, 1.2, 0.9, 1.4},
		periods: 300,
	}

	out, err := NewController(runner, stepProposer{}, nil).Run(context.Background(), Config{MaxTries: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	first := out.Tries[0].Verdict
	if first.Robustness == nil {
		t.Fatal("稳健维应已计算")
	}
	if first.Robustness.NeighborMedian != 0 {
		t.Fatalf("第 1 次尝试还没有邻域，NeighborMedian 应为 0，实际 %.3f",
			first.Robustness.NeighborMedian)
	}

	last := out.Tries[3].Verdict
	if last.Robustness.NeighborMedian == 0 {
		t.Fatal("第 4 次尝试应已累积 3 个邻域点，NeighborMedian 不该是 0")
	}
	t.Logf("第 4 次：邻域中位数 Sharpe=%.3f 稳健概率=%.3f",
		last.Robustness.NeighborMedian, last.Robustness.Probability)
}

// 失败的尝试没有回测结果可比，Verdict 应为 nil ——
// 没有可被证伪的东西，就不要给一个看起来正常的裁决。
func TestLoop_FailedAttemptHasNoVerdict(t *testing.T) {
	runner := &richTryRunner{
		sharpe:  []float64{1.0, 1.2},
		periods: 300,
		failAt:  map[int]bool{1: true},
	}

	out, err := NewController(runner, stepProposer{}, nil).Run(context.Background(), Config{MaxTries: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(out.Tries) != 2 {
		t.Fatalf("失败不该中断整轮，应跑满 2 次，实际 %d", len(out.Tries))
	}
	if out.Tries[0].Verdict == nil {
		t.Fatal("第 1 次成功了，应有 Verdict")
	}
	if out.Tries[1].Verdict != nil {
		t.Fatal("第 2 次失败了，不该有 Verdict")
	}
}

// 失败次数必须进统计维 —— 只报成功次数等于把最有力的那部分选择偏差藏起来。
// 这里让前两次都失败：失败率过半会触发统计维的 warning，是个可观测的信号。
func TestLoop_FailuresReachStatisticalDimension(t *testing.T) {
	runner := &richTryRunner{
		sharpe:  []float64{1.0, 1.2, 1.1},
		periods: 300,
		failAt:  map[int]bool{0: true, 1: true},
	}

	out, err := NewController(runner, stepProposer{}, nil).Run(context.Background(), Config{MaxTries: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	last := out.Tries[2]
	if last.Verdict == nil || last.Verdict.Statistical == nil {
		t.Fatal("第 3 次成功了，统计维应已计算")
	}

	var found bool
	for _, c := range last.Verdict.Statistical.Challenges {
		if c.Severity == validation.SeverityWarning {
			found = true
			t.Logf("失败率质疑：%s", c.Message)
		}
	}
	if !found {
		t.Fatalf("2/3 次失败，统计维却没提失败率：%d 条质疑",
			len(last.Verdict.Statistical.Challenges))
	}
}

// 试得越多，统计维的门槛越紧 —— 这是 P2-9a 的核心，
// 接进循环后必须真的随 seq 变化。
func TestLoop_StatisticalThresholdTightensWithTrials(t *testing.T) {
	runner := &richTryRunner{
		sharpe:  []float64{1.0, 1.0, 1.0},
		periods: 300,
	}

	out, err := NewController(runner, stepProposer{}, nil).Run(context.Background(), Config{MaxTries: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	first := out.Tries[0].Verdict.Statistical
	last := out.Tries[2].Verdict.Statistical
	if first == nil || last == nil {
		t.Fatal("统计维应已计算")
	}
	if last.AdjustedPValue <= first.AdjustedPValue {
		t.Fatalf("同样的 Sharpe，试到第三次时校正后 p 应变大：第 1 次 %.4f → 第 3 次 %.4f",
			first.AdjustedPValue, last.AdjustedPValue)
	}
	t.Logf("同样 Sharpe=1.0：试 1 次 p=%.4f（概率 %.3f），试 3 次 p=%.4f（概率 %.3f）",
		first.AdjustedPValue, first.Probability, last.AdjustedPValue, last.Probability)
}
