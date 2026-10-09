package validation

import (
	"math"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

func hasRobustnessChallenge(r RobustnessResult, severity string) bool {
	for _, c := range r.Challenges {
		if c.Dimension == DimensionRobustness && c.Severity == severity {
			return true
		}
	}
	return false
}

// equityCurve 造一条按给定年度收益演进的净值曲线。
// yearly 是每年的收益，从 1.0 起算，每年 252 个交易日。
func equityCurve(yearly []float64) []domain.PortfolioValue {
	start := time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)
	var out []domain.PortfolioValue
	value := 1_000_000.0
	for y, ret := range yearly {
		yearStart := value
		for d := 0; d < 252; d++ {
			frac := float64(d+1) / 252
			out = append(out, domain.PortfolioValue{
				Date:       start.AddDate(y, 0, d),
				TotalValue: yearStart * (1 + ret*frac),
			})
		}
		value = yearStart * (1 + ret)
	}
	return out
}

func resultWithCurve(sharpes ...float64) *domain.BacktestResult {
	return &domain.BacktestResult{
		SharpeRatio:     sharpes[0],
		PortfolioValues: equityCurve(sharpes),
	}
}

func neighbors(sharpes ...float64) []NeighborPoint {
	out := make([]NeighborPoint, 0, len(sharpes))
	for _, s := range sharpes {
		out = append(out, NeighborPoint{Sharpe: s, Return: s * 0.05})
	}
	return out
}

// 邻域一塌，说明中心那个点多半是撞出来的尖峰，不是高原。
func TestRobustness_NeighborhoodCollapses(t *testing.T) {
	got := ValidateRobustness(RobustnessInput{
		Result:    &domain.BacktestResult{SharpeRatio: 2.0, PortfolioValues: equityCurve([]float64{0.10, 0.10, 0.10})},
		Neighbors: neighbors(-0.3, 0.1, -0.5, 0.05, -0.2),
	})

	if got.PlateauRatio > 0.3 {
		t.Fatalf("邻域几乎全塌，高原面积不该有 %.2f", got.PlateauRatio)
	}
	if !hasRobustnessChallenge(got, SeverityBlocking) {
		t.Fatalf("尖峰必须被 blocking 质疑，实际：%+v", got.Challenges)
	}
	if got.Probability > 0.4 {
		t.Fatalf("邻域塌陷时概率不该给到 %.3f", got.Probability)
	}
}

func TestRobustness_PlateauSurvives(t *testing.T) {
	got := ValidateRobustness(RobustnessInput{
		Result:    &domain.BacktestResult{SharpeRatio: 1.5, PortfolioValues: equityCurve([]float64{0.08, 0.08, 0.08})},
		Neighbors: neighbors(1.2, 1.4, 1.3, 1.6, 1.1),
	})

	if got.PlateauRatio < 0.8 {
		t.Fatalf("邻域全都站得住，高原面积应接近 1，实际 %.2f", got.PlateauRatio)
	}
	if hasRobustnessChallenge(got, SeverityBlocking) {
		t.Fatalf("真高原不该被否掉，实际：%+v", got.Challenges)
	}
	if got.Probability < 0.6 {
		t.Fatalf("高原 + 分段一致，概率不该只有 %.3f", got.Probability)
	}
}

// 三年里只有一年赚钱，另两年在亏 —— 总收益为正也不可信。
func TestRobustness_OneSegmentCarriesEverything(t *testing.T) {
	got := ValidateRobustness(RobustnessInput{
		Result:    &domain.BacktestResult{SharpeRatio: 0.9, PortfolioValues: equityCurve([]float64{-0.05, -0.03, 0.45})},
		Neighbors: neighbors(0.8, 0.9, 0.7),
	})

	if got.PositiveSegRatio > 0.5 {
		t.Fatalf("三段里只有一段为正，正段占比不该是 %.2f", got.PositiveSegRatio)
	}
	if got.TopSegmentShare < 0.8 {
		t.Fatalf("最好一段应贡献绝大部分收益，实际 %.2f", got.TopSegmentShare)
	}
	if !hasRobustnessChallenge(got, SeverityWarning) && !hasRobustnessChallenge(got, SeverityBlocking) {
		t.Fatalf("收益集中于单段必须被质疑，实际：%+v", got.Challenges)
	}
}

func TestRobustness_SegmentsReadFromEquityCurve(t *testing.T) {
	got := ValidateRobustness(RobustnessInput{
		Result: &domain.BacktestResult{SharpeRatio: 1.0, PortfolioValues: equityCurve([]float64{0.10, -0.02, 0.30})},
	})

	if len(got.Segments) != 3 {
		t.Fatalf("三年数据应切出 3 段，实际 %d", len(got.Segments))
	}
	if math.Abs(got.Segments[0].Return-0.10) > 0.005 {
		t.Fatalf("第一段应为 0.10，实际 %.4f", got.Segments[0].Return)
	}
	if math.Abs(got.Segments[1].Return+0.02) > 0.005 {
		t.Fatalf("第二段应为 -0.02，实际 %.4f", got.Segments[1].Return)
	}
}

// 没有邻域数据不等于「稳健」，也不等于「不稳健」—— 是没信息。
// 这时只能看分段，而且要如实说明。
func TestRobustness_NoNeighborsIsNotedNotFailed(t *testing.T) {
	got := ValidateRobustness(RobustnessInput{
		Result: &domain.BacktestResult{SharpeRatio: 1.2, PortfolioValues: equityCurve([]float64{0.10, 0.10})},
	})

	if hasRobustnessChallenge(got, SeverityBlocking) {
		t.Fatalf("没有邻域不该直接否掉，实际：%+v", got.Challenges)
	}
	if !hasRobustnessChallenge(got, SeverityNote) {
		t.Fatalf("缺邻域数据必须如实说明，实际：%+v", got.Challenges)
	}
}

func TestRobustness_RejectsBadInput(t *testing.T) {
	if got := ValidateRobustness(RobustnessInput{}); !hasRobustnessChallenge(got, SeverityBlocking) {
		t.Fatal("nil 结果必须被 blocking 拒绝")
	}
	if got := ValidateRobustness(RobustnessInput{
		Result: &domain.BacktestResult{SharpeRatio: 1.0},
	}); !hasRobustnessChallenge(got, SeverityBlocking) {
		t.Fatal("没有净值曲线算不出分段，必须被 blocking 拒绝")
	}
}

// 概率必须随证据单调：高原 + 分段一致 > 尖峰 + 单段独撑。
func TestRobustness_ProbabilityOrdersEvidence(t *testing.T) {
	strong := ValidateRobustness(RobustnessInput{
		Result:    &domain.BacktestResult{SharpeRatio: 1.5, PortfolioValues: equityCurve([]float64{0.10, 0.10, 0.10})},
		Neighbors: neighbors(1.4, 1.5, 1.3),
	})
	weak := ValidateRobustness(RobustnessInput{
		Result:    &domain.BacktestResult{SharpeRatio: 1.5, PortfolioValues: equityCurve([]float64{-0.10, -0.10, 0.60})},
		Neighbors: neighbors(-0.4, 0.1, -0.2),
	})

	if strong.Probability <= weak.Probability {
		t.Fatalf("证据强的概率应更高：%.3f vs %.3f", strong.Probability, weak.Probability)
	}
}

// 一片位于零附近的平原仍然是平原 —— 平坦不等于有效。
// 这条是取证时真踩出来的：真引擎跑出的中心 Sharpe=0.031、邻域紧贴中心，
// 高原面积 1.00，概率直接给到 1.000。
func TestRobustness_FlatButUselessPlateau(t *testing.T) {
	got := ValidateRobustness(RobustnessInput{
		Result:    &domain.BacktestResult{SharpeRatio: 0.03, PortfolioValues: equityCurve([]float64{0.01, 0.01, 0.01})},
		Neighbors: neighbors(0.028, 0.032, 0.029),
	})

	if got.PlateauRatio < 0.9 {
		t.Fatalf("邻域紧贴中心，高原面积确实是满的（%.2f）—— 问题不在这里", got.PlateauRatio)
	}
	if got.Probability > 0.4 {
		t.Fatalf("中心只有 %.2f，再平坦也不该给到 %.3f 的概率", got.CenterSharpe, got.Probability)
	}
	if !hasRobustnessChallenge(got, SeverityWarning) {
		t.Fatalf("「稳定地不赚钱」必须被点出来，实际：%+v", got.Challenges)
	}
}
