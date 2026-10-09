package validation

import (
	"math"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// TurnoverEstimate 是从一次回测结果反推出来的换手率。
type TurnoverEstimate struct {
	AnnualTurnover float64 `json:"annual_turnover"` // 年化单边换手率
	AvgTradeValue  float64 `json:"avg_trade_value"` // 平均单笔成交金额（元）
	Periods        int     `json:"periods"`         // 交易日数
	TradedValue    float64 `json:"traded_value"`    // 区间总成交金额（双边合计）
}

// TurnoverFromBacktest 从回测结果反推年化单边换手率。
//
// 为什么必须有这个函数：经济校验器要的是「年化单边换手率」，而回测引擎
// 只给 TotalTrades 和逐笔成交，不给换手率。让调用方自己拍一个，等于把
// 这一维最关键的 input 变成了猜测 —— 那扣费后的数字也就没有意义了。
//
// 口径：
//   - 区间总成交 = Σ|quantity × price|（买卖双向合计）
//   - 单边成交 = 总成交 / 2
//   - 年化单边换手 = 单边成交 / (年数 × 平均组合市值)
//
// ⚠️ 两个必须知道的近似：
//  1. 「总成交 / 2」假设买卖金额对称。只买不卖（或反之）的策略会低估一半。
//     domain.Trade 的 Direction 是持仓方向（long/short/close），不是买卖方向，
//     所以现在区分不了 —— 这一点在 Challenge 里如实提醒。
//  2. 平均组合市值取市值序列均值，不等于日均持仓市值，但量级一致。
//
// 算不出就返回 ok=false。**绝不返回 0** —— 0 会被当成「零换手、零成本」，
// 那是最危险的假数字。
func TurnoverFromBacktest(r *domain.BacktestResult) (TurnoverEstimate, bool) {
	if r == nil || len(r.Trades) == 0 {
		return TurnoverEstimate{}, false
	}

	var traded, count float64
	for _, t := range r.Trades {
		v := math.Abs(t.Quantity) * t.Price
		if !finite(v) || v <= 0 {
			continue
		}
		traded += v
		count++
	}
	if count == 0 {
		return TurnoverEstimate{}, false
	}

	years, periods := resultYears(r)
	if years <= 0 {
		return TurnoverEstimate{}, false
	}

	avgEquity := meanEquity(r)
	if avgEquity <= 0 {
		// 没有市值就没有分母。拿初始资金兜底也不行 —— BacktestResult
		// 里没有 InitialCapital，只有回测参数里有。
		return TurnoverEstimate{}, false
	}

	return TurnoverEstimate{
		AnnualTurnover: (traded / 2) / (years * avgEquity),
		AvgTradeValue:  traded / count,
		Periods:        periods,
		TradedValue:    traded,
	}, true
}

// resultYears 折算回测区间年数，优先用市值曲线长度（交易日口径），
// 退回日历天数。
func resultYears(r *domain.BacktestResult) (years float64, periods int) {
	if n := len(r.PortfolioValues); n >= 2 {
		periods = n
		return float64(n) / TradingDaysPerYear, periods
	}
	if r.StartDate.IsZero() || r.EndDate.IsZero() || !r.EndDate.After(r.StartDate) {
		return 0, 0
	}
	days := r.EndDate.Sub(r.StartDate).Hours() / 24
	if days <= 0 {
		return 0, 0
	}
	years = days / 365.25
	return years, int(years * TradingDaysPerYear)
}

// finite 判断浮点数是否可用。Go 的 math 没有 IsFinite，只有 IsNaN / IsInf。
func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func meanEquity(r *domain.BacktestResult) float64 {
	var sum float64
	var n int
	for _, pv := range r.PortfolioValues {
		if pv.TotalValue > 0 && finite(pv.TotalValue) {
			sum += pv.TotalValue
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// ValidateEconomicFromBacktest 把一次回测结果直接送进经济校验器。
//
// 这是 P2-9b 的正式入口：调用方不需要自己知道换手率怎么算。
// 换手率算不出来时**不给结果**（ok=false），而不是拿 0 假装零成本。
func ValidateEconomicFromBacktest(r *domain.BacktestResult, cost CostModel) (EconomicResult, bool) {
	to, ok := TurnoverFromBacktest(r)
	if !ok {
		return EconomicResult{}, false
	}

	res := ValidateEconomic(EconomicInput{
		GrossReturn:   r.TotalReturn,
		Turnover:      to.AnnualTurnover,
		Periods:       to.Periods,
		AvgTradeValue: to.AvgTradeValue,
		Cost:          cost,
	})

	// 换手率是从成交额反推的，把近似前提如实写进质疑清单。
	res.Challenges = append(res.Challenges, Challenge{
		Dimension: DimensionEconomic,
		Severity:  SeverityNote,
		Message: "换手率由成交额反推（总成交 ÷ 2 假设买卖对称）；" +
			"domain.Trade 的 Direction 是持仓方向，区分不了买卖，单边换手可能有偏差。",
	})
	return res, true
}
