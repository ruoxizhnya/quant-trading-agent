package validation

// P2-9c 的端到端取证：真引擎跑一组参数邻域 → 稳健校验器。
//
// 单测里的邻域是手写的数字，证不了「引擎真跑出来的邻域能喂进来」。
// 这里跑多次不同 lookback 的回测，把非中心的几次当作邻域点。

import (
	"strings"
	"testing"
)

func TestRobustnessIntegration_RealEngineNeighborhood(t *testing.T) {
	// 3 年数据，好让分段切得出多个时间段；1 年只能切 1 段，
	// 分段一致性就无从判断了。
	base := offlineSpec{nSyms: 20, nDays: 756, topN: 5, lookback: 20, seed: 42, freq: "daily"}

	center := runOfflineBacktest(t, base)

	var nb []NeighborPoint
	for _, lb := range []int{10, 30, 40} {
		spec := base
		spec.lookback = lb
		r := runOfflineBacktest(t, spec)
		nb = append(nb, NeighborPoint{
			Params: map[string]interface{}{"lookback_days": lb},
			Sharpe: r.SharpeRatio,
			Return: r.TotalReturn,
		})
	}

	got := ValidateRobustness(RobustnessInput{Result: center, Neighbors: nb})

	t.Logf("中心 Sharpe=%.3f（lookback=20）", got.CenterSharpe)
	for i, n := range nb {
		t.Logf("邻域[%d] lookback=%v sharpe=%.3f",
			i, n.Params["lookback_days"], n.Sharpe)
	}
	t.Logf("邻域中位数=%.3f 最差=%.3f 衰减比=%.3f 高原面积=%.2f",
		got.NeighborMedian, got.NeighborWorst, got.Decay, got.PlateauRatio)
	for _, s := range got.Segments {
		t.Logf("分段 %s 收益=%.4f", s.Label, s.Return)
	}
	t.Logf("正段占比=%.2f 最大段贡献=%.2f 概率=%.3f",
		got.PositiveSegRatio, got.TopSegmentShare, got.Probability)
	for _, c := range got.Challenges {
		t.Logf("  [%s/%s] %s", c.Dimension, c.Severity, c.Message)
	}

	// 性质 1：三年数据必须切出三段 —— 切不出来说明分段逻辑没读对净值曲线。
	if len(got.Segments) != 3 {
		t.Fatalf("756 个交易日跨三个自然年，应切出 3 段，实际 %d", len(got.Segments))
	}

	// 性质 2：既然给了邻域，就不该报「缺邻域数据」。
	for _, c := range got.Challenges {
		if c.Severity == SeverityNote && c.Dimension == DimensionRobustness {
			if strings.Contains(c.Message, "没有参数邻域数据") {
				t.Fatal("传了邻域却报缺邻域数据")
			}
		}
	}

	// 性质 3：高原面积是比例，必须落在 [0,1]。
	if got.PlateauRatio < 0 || got.PlateauRatio > 1 {
		t.Fatalf("高原面积 %.3f 越界", got.PlateauRatio)
	}

	// 性质 4：邻域最差值必须不大于中位数（否则统计量算反了）。
	if got.NeighborWorst > got.NeighborMedian {
		t.Fatalf("邻域最差 %.3f 不该大于中位数 %.3f", got.NeighborWorst, got.NeighborMedian)
	}
}
