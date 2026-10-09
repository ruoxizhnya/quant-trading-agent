package validation

import (
	"fmt"
	"math"
)

// A 股默认交易成本（2026-09 校准）。
//
// 这些数字会变，所以它们集中在这里、带来源注释，而不是散落在公式里。
// 换券商、改费率、印花税调整时，只改这一处。
const (
	// DefaultCommissionRate 是单边佣金率。万 2.5 是当前券商普遍水平。
	DefaultCommissionRate = 0.00025
	// DefaultMinCommission 是单笔最低佣金（元）。它不随成交额缩小，
	// 所以小账户的实际费率会**高于**名义费率 —— 这是必须单独建模的一项。
	DefaultMinCommission = 5.0
	// DefaultStampDutyRate 是印花税率，仅在**卖出**端收取。
	// 2023-08-28 起由 0.1% 下调至 0.05%。
	DefaultStampDutyRate = 0.0005
	// DefaultTransferFeeRate 是过户费率，沪深双边收取，按成交金额的 0.001%。
	DefaultTransferFeeRate = 0.00001
	// DefaultSlippageRate 是单边滑点/冲击成本，取 10bp 作为保守估计。
	//
	// ⚠️ 这是**线性近似**：真实的冲击成本随单笔规模**超线性**增长
	//（挂单簿越吃越薄）。所以资金量上去之后，这一项会被系统性低估。
	// 在给不出订单簿数据时，宁可用保守的常数，也不要假装精确。
	DefaultSlippageRate = 0.001
)

// CostModel 是一组交易成本参数。零值表示「不收费」，那会让这一维失去
// 鉴别力，所以会被明确质疑（见 ValidateEconomic）。
type CostModel struct {
	CommissionRate  float64 `json:"commission_rate"`   // 单边佣金率
	MinCommission   float64 `json:"min_commission"`    // 单笔最低佣金（元）
	StampDutyRate   float64 `json:"stamp_duty_rate"`   // 印花税率（仅卖出端）
	TransferFeeRate float64 `json:"transfer_fee_rate"` // 过户费率（双边）
	SlippageRate    float64 `json:"slippage_rate"`     // 单边滑点
}

// DefaultAShareCostModel 返回 A 股默认成本模型。
//
// 用函数而不是包级变量：调用方改动返回的副本，不会污染后续调用。
func DefaultAShareCostModel() CostModel {
	return CostModel{
		CommissionRate:  DefaultCommissionRate,
		MinCommission:   DefaultMinCommission,
		StampDutyRate:   DefaultStampDutyRate,
		TransferFeeRate: DefaultTransferFeeRate,
		SlippageRate:    DefaultSlippageRate,
	}
}

// isZero 判断成本模型是否全零。全零等于没建模。
func (c CostModel) isZero() bool {
	return c.CommissionRate <= 0 && c.StampDutyRate <= 0 &&
		c.TransferFeeRate <= 0 && c.SlippageRate <= 0
}

// EconomicInput 是经济校验器的输入。
//
// 口径统一说明，避免和成本打架：
//   - GrossReturn 是**区间总收益**（不是年化），0.24 = 24%。
//   - Turnover 是**年化单边换手率**：一年内买入（或卖出）金额 / 组合市值。
//     1.0 = 一年换手一遍；50 ≈ 日频。总成交额按 2× 计（一买一卖）。
type EconomicInput struct {
	GrossReturn float64 // 区间总收益（小数）
	Turnover    float64 // 年化单边换手率
	Periods     int     // 回测期交易日数

	// AvgTradeValue 是平均单笔成交金额（元），用于算最低佣金的摊薄效应。
	// 为 0 表示未知 —— 那就按名义费率算，并附一条 note 说明实际会更高。
	AvgTradeValue float64

	// Cost 是成本模型。零值表示「没指定」—— 此时用 A 股默认费率，
	// 并附一条 note 提醒按实际费率校准。
	//
	// 之所以不让零值代表「不收费」：现实中不存在零成本交易，让人忘传
	// 就拿到一个全零的假结果，比悄悄用默认费率危险得多。
	Cost CostModel
}

// CostBreakdown 把总成本拆开，让人看清钱花在哪一项上。
// 拆不开就没法判断「该降换手」还是「该换券商」。
type CostBreakdown struct {
	BuyRate       float64 `json:"buy_rate"`                  // 买入端成本率（占成交额）
	SellRate      float64 `json:"sell_rate"`                 // 卖出端成本率（含印花税）
	Commission    float64 `json:"commission"`                // 佣金总额
	StampDuty     float64 `json:"stamp_duty"`                // 印花税总额
	TransferFee   float64 `json:"transfer_fee"`              // 过户费总额
	Slippage      float64 `json:"slippage"`                  // 滑点总额
	EffectiveComm float64 `json:"effective_commission_rate"` // 计入最低佣金后的实际费率
}

// EconomicResult 是经济校验器的输出。
type EconomicResult struct {
	GrossReturn float64 `json:"gross_return"`
	Turnover    float64 `json:"turnover"`
	TotalCost   float64 `json:"total_cost"`
	NetReturn   float64 `json:"net_return"` // 毛收益 − 总成本

	// CostShare 是成本占毛收益的比例。>1 意味着成本比赚的还多。
	CostShare float64 `json:"cost_share"`

	// BreakEvenTurnover 是毛收益撑得住的年化换手率上限。
	// 超过它就亏钱 —— 这个数能直接回答「这个策略能跑多高频」。
	// 成本模型为零时无意义，取 0；那种情形本身会被 blocking 质疑。
	BreakEvenTurnover float64 `json:"break_even_turnover"`

	// AnnualCostDrag 是年化成本拖累，即每年被成本吃掉多少。
	AnnualCostDrag float64 `json:"annual_cost_drag"`

	// Probability 是「经济维度上站得住」的校准后概率估计。
	//
	// ⚠️ 老实说：这是**启发式映射**，不是从历史数据里校准出来的。
	// 它现在只能保证单调（净收益余量越大越可信）和边界合理
	//（净收益为负 → 接近 0）。要拿去配仓位之前，必须先用实验日志
	// 校准这条曲线 —— 和 PRODUCT §目标是校准 是同一条要求。
	Probability float64 `json:"probability"`

	CostBreakdown CostBreakdown `json:"cost_breakdown"`
	Challenges    []Challenge   `json:"challenges"`
}

// ValidateEconomic 扣掉交易成本，看策略还成不成立。
//
// 这是六维里唯一能直接否掉「回测很美、实盘亏钱」的一维：毛收益是实验室
// 条件，成本是现实条件。换手越高的策略，被扣得越狠 —— 日频策略光成本
// 一年就要去掉十几个点。
func ValidateEconomic(in EconomicInput) EconomicResult {
	if in.Periods <= 0 || in.Turnover < 0 {
		return EconomicResult{
			GrossReturn: in.GrossReturn,
			Turnover:    in.Turnover,
			CostBreakdown: CostBreakdown{
				EffectiveComm: in.Cost.CommissionRate,
			},
			Challenges: []Challenge{{
				Dimension: DimensionEconomic,
				Severity:  SeverityBlocking,
				Message: fmt.Sprintf(
					"经济校验无法进行：观测期数=%d（须为正）、换手率=%.2f（须非负）。"+
						"给不出诚实的成本估计，就不给数字。",
					in.Periods, in.Turnover),
			}},
		}
	}

	usingDefault := in.Cost.isZero()
	cost := in.Cost
	if usingDefault {
		cost = DefaultAShareCostModel()
	}

	years := float64(in.Periods) / TradingDaysPerYear

	// 最低佣金不随成交额缩小，所以小账户的实际费率高于名义费率。
	// 给不出单笔金额时按名义费率算，并诚实标注「实际只会更高」。
	effComm := cost.CommissionRate
	minCommApplies := cost.MinCommission > 0 && in.AvgTradeValue > 0
	if minCommApplies {
		if floor := cost.MinCommission / in.AvgTradeValue; floor > effComm {
			effComm = floor
		}
	}

	buyRate := effComm + cost.TransferFeeRate + cost.SlippageRate
	sellRate := buyRate + cost.StampDutyRate // 印花税只在卖出端
	roundTripRate := buyRate + sellRate

	// 总成交额 = 2 × 换手率 × 市值，但换手率这里是年化单边，
	// 所以按「买入 turnover、卖出 turnover」各计一次。
	traded := in.Turnover * years

	res := EconomicResult{
		GrossReturn: in.GrossReturn,
		Turnover:    in.Turnover,
		CostBreakdown: CostBreakdown{
			BuyRate:       buyRate,
			SellRate:      sellRate,
			Commission:    traded * effComm * 2,
			StampDuty:     traded * cost.StampDutyRate, // 只卖出端
			TransferFee:   traded * cost.TransferFeeRate * 2,
			Slippage:      traded * cost.SlippageRate * 2,
			EffectiveComm: effComm,
		},
		AnnualCostDrag: in.Turnover * roundTripRate,
	}
	res.TotalCost = res.CostBreakdown.Commission + res.CostBreakdown.StampDuty +
		res.CostBreakdown.TransferFee + res.CostBreakdown.Slippage
	res.NetReturn = in.GrossReturn - res.TotalCost

	if in.GrossReturn != 0 {
		res.CostShare = res.TotalCost / math.Abs(in.GrossReturn)
	}
	if roundTripRate > 0 {
		res.BreakEvenTurnover = in.GrossReturn / (years * roundTripRate)
	}

	res.Probability = economicProbability(res)
	res.Challenges = economicChallenges(in, res, minCommApplies, usingDefault)
	return res
}

// economicProbability 把「净收益相对成本的余量」映射成概率。
//
// m = 净收益 / 总成本：m=0 是刚好打平，m=1 是净收益等于成本
// （也就是成本吃掉一半毛利）。取 logistic，中点放在 m=1。
func economicProbability(r EconomicResult) float64 {
	// 零换手：没有成本可扣，这一维既不支持也不质疑 —— 给中性值，
	// 不给满分。给满分会被当成「经济维度通过了」，那是谎。
	if r.TotalCost <= 0 {
		return 0.5
	}
	margin := r.NetReturn / r.TotalCost
	return 1 / (1 + math.Exp(-(margin - 1)))
}

func economicChallenges(in EconomicInput, r EconomicResult, minCommApplies, usingDefault bool) []Challenge {
	var cs []Challenge

	if usingDefault && in.Turnover > 0 {
		cs = append(cs, Challenge{
			Dimension: DimensionEconomic,
			Severity:  SeverityNote,
			Message: "未指定成本模型，已按 A 股默认费率估算（佣金万 2.5 / 印花税 0.05% 卖出单边 / " +
				"过户费 0.001% 双边 / 滑点 10bp 单边）。你的实际费率若不同，净收益结论可能翻转 —— 请显式传入。",
		})
	}

	if r.TotalCost > 0 && r.NetReturn <= 0 {
		cs = append(cs, Challenge{
			Dimension: DimensionEconomic,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"扣费后不赚钱：毛 %.2f%%，成本 %.2f%%（年换手 %.1f 倍 × %d 个交易日），"+
					"净 %.2f%%。这个策略的收益全在手续费里。",
				in.GrossReturn*100, r.TotalCost*100, in.Turnover, in.Periods, r.NetReturn*100),
		})
	} else if r.CostShare > 0.5 {
		cs = append(cs, Challenge{
			Dimension: DimensionEconomic,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"成本吃掉了 %.0f%% 的毛收益（成本 %.2f%% / 毛 %.2f%%），净只剩 %.2f%% —— "+
					"这么薄的余量，成本估计差一点结论就翻转。",
				r.CostShare*100, r.TotalCost*100, in.GrossReturn*100, r.NetReturn*100),
		})
	} else if r.CostShare > 0.25 {
		cs = append(cs, Challenge{
			Dimension: DimensionEconomic,
			Severity:  SeverityWarning,
			Message: fmt.Sprintf(
				"成本占毛收益 %.0f%%，年化拖累 %.2f%% —— 换手再高一档就危险了。",
				r.CostShare*100, r.AnnualCostDrag*100),
		})
	}

	// 盈亏平衡换手是这一维最有用的输出：它把「该跑多快」变成一个数。
	if r.BreakEvenTurnover > 0 && in.Turnover > r.BreakEvenTurnover {
		cs = append(cs, Challenge{
			Dimension: DimensionEconomic,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"当前换手 %.1f 倍/年已超过盈亏平衡线 %.1f 倍/年 —— 再快就是给券商打工。",
				in.Turnover, r.BreakEvenTurnover),
		})
	}

	if !minCommApplies && in.Cost.MinCommission > 0 && in.Turnover > 0 {
		cs = append(cs, Challenge{
			Dimension: DimensionEconomic,
			Severity:  SeverityNote,
			Message: "未提供平均单笔成交金额，最低佣金（5 元/笔）的摊薄效应未计入 —— " +
				"实盘成本只会比这里更高，小账户尤其明显。",
		})
	}

	if r.TotalCost > 0 && r.CostBreakdown.Slippage/r.TotalCost > 0.3 {
		cs = append(cs, Challenge{
			Dimension: DimensionEconomic,
			Severity:  SeverityNote,
			Message: fmt.Sprintf(
				"滑点占总成本 %.0f%%，而它是按固定 bp 线性估算的 —— "+
					"真实冲击成本随单笔规模超线性增长，资金量上去后这一项会被显著低估。",
				r.CostBreakdown.Slippage/r.TotalCost*100),
		})
	}

	return cs
}
