package validation

// P2-9e 的端到端取证：真引擎跑出来的两条策略，相关性喂进冗余校验器。
//
// 单测里的序列是造出来的数字，证不了「回测结果能接进这一维」。
// 这里跑两组不同参数的 momentum，看校验器认不认得出它们是同一件事。

import (
	"testing"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

func TestRedundancyIntegration_RealEngineOutputIsComparable(t *testing.T) {
	base := offlineSpec{nSyms: 20, nDays: 400, topN: 5, lookback: 20, seed: 42, freq: "daily"}

	center := runOfflineBacktest(t, base)

	close := base
	close.lookback = 25
	near := runOfflineBacktest(t, close)

	far := base
	far.lookback = 200
	distant := runOfflineBacktest(t, far)

	// 自己和自己比：ρ 必须是 1，否则接线或计算有一处是错的。
	self := RedundancyFromBacktests(center, map[string]*domain.BacktestResult{
		"itself": center,
	})
	selfRes := ValidateRedundancy(self)
	if selfRes.MaxAbsCorr < 0.999 {
		t.Fatalf("同一条策略和自己比，|ρ| 应≈1，实际 %.4f —— 接线或相关计算有问题",
			selfRes.MaxAbsCorr)
	}
	if selfRes.Probability > 0.1 {
		t.Fatalf("完全冗余不该给高概率，实际 %.3f", selfRes.Probability)
	}

	// 相近参数 vs 相差很远的参数。
	nearIn := RedundancyFromBacktests(center, map[string]*domain.BacktestResult{
		"lookback=25": near,
	})
	farIn := RedundancyFromBacktests(center, map[string]*domain.BacktestResult{
		"lookback=200": distant,
	})
	nearRes := ValidateRedundancy(nearIn)
	farRes := ValidateRedundancy(farIn)

	t.Logf("候选 lookback=20 vs 25：|ρ|=%.3f 概率=%.3f 样本=%d",
		nearRes.MaxAbsCorr, nearRes.Probability, nearRes.SampleSize)
	t.Logf("候选 lookback=20 vs 200：|ρ|=%.3f 概率=%.3f 样本=%d",
		farRes.MaxAbsCorr, farRes.Probability, farRes.SampleSize)

	// 接线性质：收益序列必须真的从净值曲线里取出来，长度 = 净值点 − 1。
	if len(nearIn.Candidate) != len(center.PortfolioValues)-1 {
		t.Fatalf("收益点数应为净值点数−1：%d vs %d",
			len(nearIn.Candidate), len(center.PortfolioValues)-1)
	}
	if nearRes.Compared != 1 {
		t.Fatalf("应比过 1 条，实际 %d（跳过了 %v）", nearRes.Compared, nearRes.Skipped)
	}

	// 方向性质：参数越接近，收益越该像 —— 这是这一维能用的前提。
	// 不写死具体数值（那取决于合成数据），只断言相对关系。
	if nearRes.MaxAbsCorr < farRes.MaxAbsCorr {
		t.Fatalf("lookback 25 应比 lookback 200 更像 lookback 20，实际 %.3f < %.3f —— "+
			"若方向反了，这一维的排序就没意义了",
			nearRes.MaxAbsCorr, farRes.MaxAbsCorr)
	}

	// 概率方向：更冗余的那组，概率必须更低。
	if nearRes.Probability >= farRes.Probability {
		t.Fatalf("更冗余的一组概率应更低：near=%.3f far=%.3f",
			nearRes.Probability, farRes.Probability)
	}

	for _, c := range nearRes.Challenges {
		t.Logf("  [%s/%s] %s", c.Dimension, c.Severity, c.Message)
	}
}
