package validation

import (
	"math"
	"testing"
)

func hasEconomicChallenge(r EconomicResult, severity string) bool {
	for _, c := range r.Challenges {
		if c.Dimension == DimensionEconomic && c.Severity == severity {
			return true
		}
	}
	return false
}

// 这一维存在的理由：回测里看到的毛收益，是不交手续费、不交印花税、
// 不承受冲击成本的收益。实盘每一分钱的换手都要付钱。
// 一个年化 8% 的毛策略，如果一年换手 30 倍，扣费后是亏的。

func TestEconomic_CostsEraseMarginalStrategy(t *testing.T) {
	// 年化 8% 摊到 3 年是 24% 毛收益；换手 30 倍/年是周频轮动的常见量级。
	got := ValidateEconomic(EconomicInput{
		GrossReturn: 0.24,
		Turnover:    30,
		Periods:     756,
	})

	if got.NetReturn >= 0 {
		t.Fatalf("扣费后不该还赚钱：净收益 = %.4f（毛 %.4f − 成本 %.4f）",
			got.NetReturn, got.GrossReturn, got.TotalCost)
	}
	if !hasEconomicChallenge(got, SeverityBlocking) {
		t.Fatalf("净收益为负必须有 blocking 质疑，实际质疑：%+v", got.Challenges)
	}
}

func TestEconomic_LowTurnoverSurvives(t *testing.T) {
	got := ValidateEconomic(EconomicInput{
		GrossReturn: 0.24,
		Turnover:    2,
		Periods:     756,
	})

	if got.NetReturn <= 0 {
		t.Fatalf("低换手不该被成本吃掉：净收益 = %.4f（成本 %.4f）", got.NetReturn, got.TotalCost)
	}
	if got.CostShare > 0.2 {
		t.Fatalf("2 倍年换手的成本占比 %.3f 不该超过 20%%", got.CostShare)
	}
}

func TestEconomic_BreakEvenTurnover(t *testing.T) {
	got := ValidateEconomic(EconomicInput{
		GrossReturn: 0.24,
		Turnover:    10,
		Periods:     756,
	})

	// 单位换手 3 年的总成本率 = 3 × (0.00126 + 0.00176) = 0.00906
	// 盈亏平衡换手 = 0.24 / 0.00906 ≈ 26.5 倍/年
	if math.Abs(got.BreakEvenTurnover-26.5) > 0.5 {
		t.Fatalf("盈亏平衡换手算错了：期望 ≈26.5，实际 %.3f", got.BreakEvenTurnover)
	}
	if got.Turnover > got.BreakEvenTurnover {
		t.Fatalf("换手 %.1f 不该超过盈亏平衡 %.1f", got.Turnover, got.BreakEvenTurnover)
	}
}

func TestEconomic_StampDutyIsSellSideOnly(t *testing.T) {
	got := ValidateEconomic(EconomicInput{
		GrossReturn: 0.10,
		Turnover:    5,
		Periods:     252,
	})

	if got.CostBreakdown.BuyRate >= got.CostBreakdown.SellRate {
		t.Fatalf("A 股印花税只在卖出端收：买入 %.5f 必须严格低于卖出 %.5f",
			got.CostBreakdown.BuyRate, got.CostBreakdown.SellRate)
	}
	if got.CostBreakdown.StampDuty <= 0 {
		t.Fatal("印花税应当单独计入成本，实际为 0 —— 混在别处就说不清钱花在哪")
	}
}

func TestEconomic_MinCommissionHurtsSmallAccounts(t *testing.T) {
	big := ValidateEconomic(EconomicInput{
		GrossReturn: 0.10, Turnover: 5, Periods: 252, AvgTradeValue: 1_000_000,
	})
	small := ValidateEconomic(EconomicInput{
		GrossReturn: 0.10, Turnover: 5, Periods: 252, AvgTradeValue: 5_000,
	})

	if small.TotalCost <= big.TotalCost {
		t.Fatalf("最低 5 元佣金对小账户是纯摊薄：小账户 %.6f 应高于大账户 %.6f",
			small.TotalCost, big.TotalCost)
	}
}

func TestEconomic_ZeroTurnoverHasNoCost(t *testing.T) {
	got := ValidateEconomic(EconomicInput{
		GrossReturn: 0.24,
		Turnover:    0,
		Periods:     756,
	})

	if got.TotalCost != 0 {
		t.Fatalf("不换手就没有交易成本，实际 %.6f", got.TotalCost)
	}
	if math.Abs(got.NetReturn-0.24) > 1e-9 {
		t.Fatalf("零换手时净收益应等于毛收益，实际 %.6f", got.NetReturn)
	}
}

func TestEconomic_RejectsBadInput(t *testing.T) {
	cases := map[string]EconomicInput{
		"负观测期": {GrossReturn: 0.10, Turnover: 5, Periods: -1},
		"零观测期": {GrossReturn: 0.10, Turnover: 5, Periods: 0},
		"负换手率": {GrossReturn: 0.10, Turnover: -3, Periods: 252},
	}

	for name, in := range cases {
		got := ValidateEconomic(in)
		if !hasEconomicChallenge(got, SeverityBlocking) {
			t.Fatalf("%s 必须被 blocking 拒绝，实际质疑：%+v", name, got.Challenges)
		}
	}
}

func TestEconomic_CostEatingMostOfProfitIsChallenged(t *testing.T) {
	// 20 倍年换手 × 1 年：成本 = 20 × 0.00302 = 0.0604，占毛收益 60%。
	got := ValidateEconomic(EconomicInput{
		GrossReturn: 0.10,
		Turnover:    20,
		Periods:     252,
	})

	if got.CostShare < 0.5 {
		t.Fatalf("成本占比 %.3f 应当过半才会触发质疑", got.CostShare)
	}
	if len(got.Challenges) == 0 {
		t.Fatal("成本吃掉过半利润必须有质疑")
	}
}

func TestEconomic_ExplicitCostModelOverridesDefault(t *testing.T) {
	// 显式给一个很贵的佣金率（千分之 8），用来验证默认值确实被覆盖。
	explicit := ValidateEconomic(EconomicInput{
		GrossReturn: 0.10,
		Turnover:    5,
		Periods:     252,
		Cost:        CostModel{CommissionRate: 0.008},
	})
	byDefault := ValidateEconomic(EconomicInput{
		GrossReturn: 0.10,
		Turnover:    5,
		Periods:     252,
	})

	if explicit.TotalCost <= byDefault.TotalCost {
		t.Fatalf("显式成本模型应当生效：显式 %.6f vs 默认 %.6f",
			explicit.TotalCost, byDefault.TotalCost)
	}
}

func TestEconomic_UnspecifiedCostModelFallsBackToAShareDefault(t *testing.T) {
	// 没传成本模型，不该得到一个全零的假结果，也不该直接 blocking ——
	// 应当用 A 股默认费率算，同时明确提醒这是估算。
	got := ValidateEconomic(EconomicInput{GrossReturn: 0.10, Turnover: 50, Periods: 252})
	explicit := ValidateEconomic(EconomicInput{
		GrossReturn: 0.10, Turnover: 50, Periods: 252, Cost: DefaultAShareCostModel(),
	})

	if math.Abs(got.TotalCost-explicit.TotalCost) > 1e-12 {
		t.Fatalf("未指定成本模型应当等价于显式默认：%.6f vs %.6f", got.TotalCost, explicit.TotalCost)
	}
	if got.TotalCost <= 0 {
		t.Fatal("50 倍年换手不可能零成本")
	}
	if !hasEconomicChallenge(got, SeverityNote) {
		t.Fatalf("用了估算费率就必须提醒校准，实际质疑：%+v", got.Challenges)
	}
}

func TestEconomic_ProbabilityIsNotNaiveNetPositive(t *testing.T) {
	// 净收益刚过线，和净收益远超成本，不该给同一个概率。
	marginal := ValidateEconomic(EconomicInput{GrossReturn: 0.10, Turnover: 30, Periods: 252})
	comfortable := ValidateEconomic(EconomicInput{GrossReturn: 0.40, Turnover: 2, Periods: 252})

	if comfortable.Probability <= marginal.Probability {
		t.Fatalf("净收益远超成本的应当更可信：宽松 %.3f vs 勉强 %.3f",
			comfortable.Probability, marginal.Probability)
	}
	if marginal.Probability > 0.6 {
		t.Fatalf("勉强覆盖成本的策略不该给出 %.3f 这么高的概率", marginal.Probability)
	}
}
