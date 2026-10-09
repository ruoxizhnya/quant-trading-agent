package validation

import (
	"math"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// synthResult 造一个可控的回测结果。
// days 个交易日、市值恒为 equity、trades 笔、每笔 value 元。
func synthResult(days, trades int, equity, value float64) *domain.BacktestResult {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &domain.BacktestResult{
		StartDate: start,
		EndDate:   start.AddDate(0, 0, days),
	}
	for i := 0; i < days; i++ {
		r.PortfolioValues = append(r.PortfolioValues, domain.PortfolioValue{
			Date:       start.AddDate(0, 0, i),
			TotalValue: equity,
		})
	}
	for i := 0; i < trades; i++ {
		r.Trades = append(r.Trades, domain.Trade{
			Quantity:  1,
			Price:     value,
			Timestamp: start.AddDate(0, 0, i%(days+1)),
		})
	}
	return r
}

func TestTurnoverFromBacktest_Basic(t *testing.T) {
	// 一年（252 个交易日）、市值 100 万、全年买卖合计 100 万、100 笔。
	// 单边 50 万 → 年化单边换手 0.5。
	got, ok := TurnoverFromBacktest(synthResult(252, 100, 1_000_000, 10_000))
	if !ok {
		t.Fatal("输入完整，应当算得出换手率")
	}
	if math.Abs(got.AnnualTurnover-0.5) > 1e-6 {
		t.Fatalf("年化单边换手应为 0.5，实际 %.6f", got.AnnualTurnover)
	}
}

func TestTurnoverFromBacktest_AvgTradeValue(t *testing.T) {
	// 全年成交 100 万 / 100 笔 = 每笔 1 万。
	got, ok := TurnoverFromBacktest(synthResult(252, 100, 1_000_000, 10_000))
	if !ok {
		t.Fatal("应当算得出")
	}
	if math.Abs(got.AvgTradeValue-10_000) > 1e-6 {
		t.Fatalf("平均单笔应为 10000，实际 %.2f —— 最低佣金摊薄全靠这个数", got.AvgTradeValue)
	}
}

func TestTurnoverFromBacktest_PeriodsComeFromEquityCurve(t *testing.T) {
	got, ok := TurnoverFromBacktest(synthResult(500, 100, 1_000_000, 10_000))
	if !ok {
		t.Fatal("应当算得出")
	}
	if got.Periods != 500 {
		t.Fatalf("交易日数应当来自市值曲线长度 500，实际 %d", got.Periods)
	}
	// 500 个交易日 ≈ 1.984 年，换手被摊薄到 0.5/1.984 ≈ 0.252
	if math.Abs(got.AnnualTurnover-0.252) > 0.01 {
		t.Fatalf("换手率应当按年化摊薄，期望 ≈0.252，实际 %.4f", got.AnnualTurnover)
	}
}

func TestTurnoverFromBacktest_NilOrEmptyIsNotZero(t *testing.T) {
	if _, ok := TurnoverFromBacktest(nil); ok {
		t.Fatal("nil 结果必须报算不出，不能返回 0 —— 那会伪装成「零换手无成本」")
	}
	if _, ok := TurnoverFromBacktest(&domain.BacktestResult{}); ok {
		t.Fatal("没有成交记录时必须报算不出")
	}
}

func TestTurnoverFromBacktest_NeedsEquity(t *testing.T) {
	// 有成交但市值序列全空且无日期区间 —— 只能放弃。
	r := &domain.BacktestResult{}
	r.Trades = append(r.Trades, domain.Trade{Quantity: 1, Price: 10_000})
	if _, ok := TurnoverFromBacktest(r); ok {
		t.Fatal("既无市值又无日期，必须报算不出而不是拿 0 当分母")
	}
}

func TestTurnoverFromBacktest_FallsBackToDateRange(t *testing.T) {
	// 没有市值曲线，但日期区间完整 —— 用日历天数折算年数。
	r := &domain.BacktestResult{
		StartDate: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	r.Trades = append(r.Trades, domain.Trade{Quantity: 1, Price: 500_000})
	got, ok := TurnoverFromBacktest(r) // avgEquity 仍然拿不到
	if ok {
		t.Fatalf("缺市值时不应给出换手率，实际 %.4f", got.AnnualTurnover)
	}
}

// 这一条是 P2-9b 真正要接上的线：回测结果 → 换手率 → 经济校验。
// 单独测校验器不算数，要能从一次真实回测直接走到「扣费后还赚不赚」。

func TestEconomic_FromRealBacktestShape(t *testing.T) {
	// 高频轮动：一年 252 个交易日、市值 100 万、全年成交 5000 万
	//（5000 笔 × 1 万），单边 2500 万 → 年换手 25 倍。
	r := synthResult(252, 5000, 1_000_000, 10_000)
	r.TotalReturn = 0.12 // 看起来还不错的毛收益

	to, ok := TurnoverFromBacktest(r)
	if !ok {
		t.Fatal("应当算得出换手率")
	}
	if math.Abs(to.AnnualTurnover-25) > 0.01 {
		t.Fatalf("年换手应为 25，实际 %.2f", to.AnnualTurnover)
	}

	got := ValidateEconomic(EconomicInput{
		GrossReturn:   r.TotalReturn,
		Turnover:      to.AnnualTurnover,
		Periods:       to.Periods,
		AvgTradeValue: to.AvgTradeValue,
	})

	// 成本 = 25 × 0.00302 = 7.55% —— 吃掉毛收益 12% 的一大半，但还是赚。
	// 换到 35 倍就该亏了。这里断言的**不是**「一定亏」，而是
	// 「成本必须被真正算进来，且占比大到会被质疑」。
	if got.TotalCost < 0.05 {
		t.Fatalf("25 倍换手的成本不该只有 %.4f%%", got.TotalCost*100)
	}
	if got.NetReturn >= r.TotalReturn {
		t.Fatal("净收益必须严格小于毛收益")
	}
	if got.CostShare < 0.5 {
		t.Fatalf("成本占比 %.3f 应当过半并触发质疑", got.CostShare)
	}
	if !hasEconomicChallenge(got, SeverityBlocking) && !hasEconomicChallenge(got, SeverityWarning) {
		t.Fatalf("这么高的换手必须被质疑，实际：%+v", got.Challenges)
	}
}
