package validation

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// 因果维：讲得出为什么吗（六维里唯一需要语言模型的一维）。
//
// 难点不是「让 LLM 讲讲为什么」—— 语言模型能为**任何**结果编一个听起来
// 合理的故事，事后讲故事是它最擅长的事。所以这一维的形态不是叙述，而是：
//
//	LLM 给出机制 + **可证伪的预测** → 用确定性检验去验那些预测。
//
// 因此有一条硬规矩：**给 LLM 看的请求里不含回测结果**
//（见 CausalRequest.Result 的注释）。知道答案之后再做的"预测"不是预测，
// 是对观测到的事实的复述 —— 那样这一维会永远通过，也就等于不存在。
// 讲得出但预测不中的，等于没讲。

// Narrator 把一次提案讲成一个可被证伪的理论。
//
// 用接口而不是 *ai.Client：这一维的验证逻辑必须能在没有 LLM 的情况下测
// （注入一个"故意讲错"的假叙述者），而且 Narrator 为 nil 时这一维应当是
// **未评估**，不是"通过"。
type Narrator interface {
	Narrate(ctx context.Context, req CausalRequest) (*CausalTheory, error)
}

// CausalRequest 交给叙述者的素材。
type CausalRequest struct {
	Name         string         // 策略名 / 意图描述
	Hypothesis   string         // 为什么试这个（来自 Proposer）
	Params       map[string]any // 试的参数
	IntentType   string         // momentum / mean_reversion / ...
	UniverseSize int            // 池子规模
	Periods      int            // 回测交易日数

	// Result 只给**确定性检验**用，绝不进 LLM 的请求体。
	//
	// 这不是洁癖：一旦把 Sharpe / 胜率 / 回撤递给模型，它就会照着这些数字
	// 反推出一套「机制」，预测必然应验 —— 那一维就成了一个只会点头的摆设。
	// 想让预测有信息量，就必须让它在看到结果之前下注。
	Result *domain.BacktestResult
}

// CausalTheory 是叙述者给出的理论：一个机制 + 若干可证伪的预测。
type CausalTheory struct {
	Mechanism string `json:"mechanism"`
	// Predictions 是「若这个机制成立，应当观察到什么」。
	// 每条都要带数值边界 —— 没有边界的陈述无法被打脸，也就没有信息量。
	Predictions []Prediction `json:"predictions"`
}

// 可验证的预测种类。LLM 只能从这里面挑 ——
// 让它自由发挥"应观察到 X"的自由文本，等于让它自己当裁判。
const (
	// PredictionWinRate：胜率（0~1）。
	PredictionWinRate = "win_rate"
	// PredictionTradeCount：成交笔数。
	PredictionTradeCount = "trade_count"
	// PredictionHoldingDays：平均持有天数。
	PredictionHoldingDays = "holding_days"
	// PredictionMaxDrawdown：最大回撤幅度（0~1，取绝对值）。
	PredictionMaxDrawdown = "max_drawdown"
	// PredictionReturnConcentration：收益集中度 = 收益最高的 TopK 个交易日
	// 贡献了总收益的多大比例（0~1）。机制若为真，收益应来自很多天，
	// 而不是三天的运气。
	PredictionReturnConcentration = "return_concentration"
)

// Prediction 是一条预测。
type Prediction struct {
	Kind string `json:"kind"`
	// Statement 是人话版本的「应当观察到什么」，给人看。
	Statement string `json:"statement"`
	// Min / Max 是数值边界，至少一个非空 —— 空 = 不可证伪。
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// TopK 只对 return_concentration 有意义，为 0 时取默认 5。
	TopK int `json:"top_k,omitempty"`
}

// DefaultConcentrationTopK 是收益集中度默认看的天数。
const DefaultConcentrationTopK = 5

// CausalResult 是因果维的输出。
type CausalResult struct {
	Mechanism string `json:"mechanism,omitempty"`

	Predictions []PredictionResult `json:"predictions"`
	// Testable 是**可证伪**的预测条数；Passed 是其中应验的条数。
	// 只有这两者的比值才说明问题 —— 讲了十条，九条算不出来，等于只讲了一条。
	Testable int `json:"testable"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`

	Probability float64     `json:"probability"`
	Challenges  []Challenge `json:"challenges"`
}

// PredictionResult 是一条预测的验证结果。
type PredictionResult struct {
	Kind      string  `json:"kind"`
	Statement string  `json:"statement"`
	Observed  float64 `json:"observed"`
	// Verifiable=false 表示这条**没能**被用来检验：要么这个量在本次回测里
	// 算不出来，要么它压根没给边界。它既不算通过也不算失败。
	Verifiable bool   `json:"verifiable"`
	Passed     bool   `json:"passed"`
	Note       string `json:"note,omitempty"`
}

// ValidateCausal 跑因果维：让叙述者先下注，再确定性验证。
//
// n 为 nil 或叙述失败时返回 (nil, err)：这一维是**未评估**，不是通过 ——
// 「没讲」和「讲了但没验」都不是「讲得通」。
func ValidateCausal(ctx context.Context, req CausalRequest, n Narrator) (*CausalResult, error) {
	if n == nil {
		return nil, nil
	}
	// 剜掉结果再递给模型 —— 靠"约定不看"是不管用的，只要字段在那儿，
	// 迟早有人图省事把它拼进 prompt。让它物理上拿不到，这条规矩才立得住。
	blind := req
	blind.Result = nil
	theory, err := n.Narrate(ctx, blind)
	if err != nil {
		return nil, err
	}
	if theory == nil {
		return nil, fmt.Errorf("叙述者返回了空理论")
	}
	return verifyTheory(theory, req.Result), nil
}

// verifyTheory 用回测结果逐条验证预测。叙述者看不到 Result，验证者看得到 ——
// 这一刀切在中间，就是这一维的全部意义。
func verifyTheory(t *CausalTheory, r *domain.BacktestResult) *CausalResult {
	out := &CausalResult{Mechanism: t.Mechanism}

	for _, p := range t.Predictions {
		out.Predictions = append(out.Predictions, verifyPrediction(p, r))
	}
	for _, pr := range out.Predictions {
		switch {
		case !pr.Verifiable:
			// 不计入分子分母：算不出来不等于通过。
		case pr.Passed:
			out.Passed++
			out.Testable++
		default:
			out.Failed++
			out.Testable++
		}
	}

	out.Probability = causalProbability(out)
	out.Challenges = causalChallenges(out)
	return out
}

func verifyPrediction(p Prediction, r *domain.BacktestResult) PredictionResult {
	pr := PredictionResult{Kind: p.Kind, Statement: p.Statement}

	if p.Min == nil && p.Max == nil {
		pr.Note = "这条预测没有数值边界 —— 无论观察到什么都能自圆其说，不构成证据。"
		return pr
	}
	obs, ok := observePrediction(p, r)
	if !ok {
		pr.Note = fmt.Sprintf("这个量（%s）在本次回测里算不出来，无法检验这条预测。", p.Kind)
		return pr
	}
	pr.Observed = obs
	pr.Verifiable = true

	if p.Min != nil && obs < *p.Min {
		return pr
	}
	if p.Max != nil && obs > *p.Max {
		return pr
	}
	pr.Passed = true
	return pr
}

// observePrediction 把预测种类映射到实测值。
//
// 返回 false 表示该量本次算不出来 —— 例如没成交就没有胜率、总收益为负时
// 谈不上「收益集中度」。算不出来必须和「算出来是 0」区分开（与冗余维
// 零方差序列的纪律一致）。
func observePrediction(p Prediction, r *domain.BacktestResult) (float64, bool) {
	if r == nil {
		return 0, false
	}
	switch p.Kind {
	case PredictionWinRate:
		if r.TotalTrades <= 0 {
			return 0, false
		}
		return r.WinRate, true
	case PredictionTradeCount:
		if r.TotalTrades <= 0 {
			return 0, false
		}
		return float64(r.TotalTrades), true
	case PredictionHoldingDays:
		if r.AvgHoldingDays <= 0 {
			return 0, false
		}
		return r.AvgHoldingDays, true
	case PredictionMaxDrawdown:
		// 引擎里回撤是负数（Calmar 用 math.Abs 取过），这里统一成正幅度。
		dd := math.Abs(r.MaxDrawdown)
		if dd <= 0 {
			return 0, false
		}
		return dd, true
	case PredictionReturnConcentration:
		k := p.TopK
		if k <= 0 {
			k = DefaultConcentrationTopK
		}
		return topKReturnShare(r.PortfolioValues, k)
	}
	// 未知种类：不算通过，也不冤枉它失败 —— 只是这一条验不了。
	return 0, false
}

// causalProbability 由「下了多少注、中了多少」推出概率。
//
// 用 Beta(1,1) 先验的后验均值 (k+1)/(n+2)：一条都没验过时不给 0 也不给 1，
// 验过的条数越多才越敢下结论 —— 押三条全中（0.8）比押一条中（0.67）更可信。
//
// 有机制却一条都没法验证时给低分（0.25）：那不是"讲得通"，是讲了段散文。
func causalProbability(c *CausalResult) float64 {
	if c.Testable == 0 {
		return 0.25
	}
	p := float64(c.Passed+1) / float64(c.Testable+2)
	return clampProb(p)
}

func causalChallenges(c *CausalResult) []Challenge {
	var cs []Challenge

	if c.Testable == 0 {
		cs = append(cs, Challenge{
			Dimension: DimensionCausal,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"只讲了机制（%d 条预测，无一条可证伪）—— 没有下任何会被打脸的赌注。"+
					"为任何结果编一个合理的故事都很容易，这在因果维上等于没讲。",
				len(c.Predictions)),
		})
	}

	for _, pr := range c.Predictions {
		switch {
		case !pr.Verifiable:
			cs = append(cs, Challenge{
				Dimension: DimensionCausal,
				Severity:  SeverityNote,
				Message:   fmt.Sprintf("预测无法检验（%s）：%s", pr.Kind, pr.Note),
			})
		case pr.Passed:
			cs = append(cs, Challenge{
				Dimension: DimensionCausal,
				Severity:  SeverityNote,
				Message: fmt.Sprintf("预测应验（%s，实测 %.3f）：%s",
					pr.Kind, pr.Observed, pr.Statement),
			})
		default:
			// 预测被证伪，是这个维度最硬的一条证据：
			// 机制说应当看到 X，实际看到的不是 X —— 故事和数据对不上。
			cs = append(cs, Challenge{
				Dimension: DimensionCausal,
				Severity:  SeverityBlocking,
				Message: fmt.Sprintf("预测被证伪（%s，实测 %.3f）：%s —— "+
					"机制和数据对不上；讲得出但预测不中，等于没讲。",
					pr.Kind, pr.Observed, pr.Statement),
			})
		}
	}

	if c.Testable > 0 {
		cs = append(cs, Challenge{
			Dimension: DimensionCausal,
			Severity:  SeverityNote,
			Message: fmt.Sprintf("%d 条可证伪预测，%d 条应验、%d 条被证伪。",
				c.Testable, c.Passed, c.Failed),
		})
	}
	return cs
}

// topKReturnShare 算收益最高的 K 天贡献了总收益的多大比例。
//
// 总收益 ≤ 0 时返回 false：亏损的"集中度"没有意义 ——
// 那时该问的是为什么会亏，不是亏得集不集中。
func topKReturnShare(pvs []domain.PortfolioValue, k int) (float64, bool) {
	if len(pvs) < 3 || k <= 0 {
		return 0, false
	}
	rets := make([]float64, 0, len(pvs)-1)
	for i := 1; i < len(pvs); i++ {
		if pvs[i-1].TotalValue <= 0 {
			continue
		}
		rets = append(rets, pvs[i].TotalValue/pvs[i-1].TotalValue-1)
	}
	if len(rets) == 0 {
		return 0, false
	}

	total := 0.0
	for _, r := range rets {
		total += r
	}
	if total <= 0 {
		return 0, false
	}

	sort.Slice(rets, func(i, j int) bool { return rets[i] > rets[j] })
	if k > len(rets) {
		k = len(rets)
	}
	top := 0.0
	for _, r := range rets[:k] {
		if r > 0 {
			top += r
		}
	}
	return top / total, true
}
