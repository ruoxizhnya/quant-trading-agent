package validation

import (
	"fmt"
	"math"
	"sort"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/statistics"
)

// 冗余校验器：这条新策略，跟已有的那些是不是同一件事。
//
// 它防的是「伪分散」—— 组合里躺着 8 条策略，看起来分散，实际上
// 8 条都是同一个动量思想换了个参数，跌的时候一起跌。相关性是唯一
// 能戳穿这件事的数：分散的是**风险来源**，不是策略数量。
//
// 和前面几维的关系：统计/经济/稳健/偏差各自检查一条策略成不成立，
// 这一维检查**它对现有组合到底有没有新增信息**。一条单独看很好的
// 策略，如果和已有的高度相关，加进去等于把同一份风险下了两次注。

const (
	// HighCorrThreshold 是「高度相关」的经验阈值。
	//
	// 0.7 是组合管理里的常用分界，但它只是约定，不是从数据里校准出来的 ——
	// 真正该问的是「这两条策略的收益是不是由同一个驱动因素决定」。
	HighCorrThreshold = 0.7

	// NearDuplicateThreshold 之上基本就是同一条策略换了参数。
	NearDuplicateThreshold = 0.9

	// MinRedundancySamples 是判断冗余所需的最少收益点数。
	// Pearson 相关性在 n<30 时标准误就宽到 ±0.19 ——
	// 那个区间里「相关 0.5」和「不相关」根本分不开。
	MinRedundancySamples = 30

	// ShrinkagePrior 是样本量收缩系数：可靠性 = n / (n + ShrinkagePrior)。
	//
	// 样本越少，相关估计越不可信，概率就越向中性 0.5 收缩 ——
	// 短样本里看着不相关，不等于真的不相关。
	ShrinkagePrior = 60
)

// NamedReturns 是一条已有策略的收益序列。
type NamedReturns struct {
	Name    string    `json:"name"`
	Returns []float64 `json:"returns"`
}

// RedundancyInput 是冗余校验器的输入。
//
// Candidate 和 Existing 里的序列都是**周期收益**（不是净值）：
// 0.01 = 这一期涨 1%。口径必须一致，否则相关性没有意义。
type RedundancyInput struct {
	Candidate []float64
	Existing  []NamedReturns
}

// RedundancyResult 是冗余校验器的输出。
type RedundancyResult struct {
	// Compared 是真正比过的已有策略条数（算得出相关的才算）。
	Compared int `json:"compared"`
	// Skipped 是因零方差或样本太短而没能比较的策略名。
	//
	// ⚠️ 跳过不能当成「不相关」：一条全程空仓的策略，Pearson 相关
	// 在数学上无定义（分母为 0），而「算不出来」和「算出来是 0」
	// 是两件完全不同的事。把它记在这里，就是要让人看见它没被算。
	Skipped []string `json:"skipped,omitempty"`

	MaxAbsCorr  float64 `json:"max_abs_corr"`            // 与已有策略的最大 |相关|
	MaxCorrName string  `json:"max_corr_name,omitempty"` // 跟谁最相关
	MaxCorrSign int     `json:"max_corr_sign"`           // +1 同向 / -1 反向
	MeanAbsCorr float64 `json:"mean_abs_corr"`           // 平均 |相关|
	Redundant   int     `json:"redundant"`               // |ρ| 超过阈值的条数

	// SampleSize 是参与比较的有效样本点数。
	SampleSize int `json:"sample_size"`
	// CorrStdErr 是这个相关估计的标准误（Fisher z 尺度，≈1/√(n-3)）。
	// 它回答「这个相关系数有多大的可能是噪声」。
	CorrStdErr float64 `json:"corr_std_err"`

	// Probability 是「这条策略带来了独立信息」的校准后概率估计。
	//
	// ⚠️ 与 economic.go 一样是启发式映射，未经数据校准。
	// 它保证两件事：样本越少越向 0.5 收缩（不敢断言）、
	// 相关性越高概率越低（方向正确）。
	Probability float64 `json:"probability"`

	Challenges []Challenge `json:"challenges"`
}

// ValidateRedundancy 检查这条策略跟已有的那些是不是同一件事。
//
// 立场同 bias.go：查不出来就说查不出来。没有可比的已有策略时，
// 概率是中性 0.5 并附 note —— 不是"通过"，是"没查"。
func ValidateRedundancy(in RedundancyInput) RedundancyResult {
	res := RedundancyResult{
		MaxCorrSign: 1,
	}

	// 候选本身能不能算：长度不足或零方差都算不出相关。
	if len(in.Candidate) < 2 || isConstant(in.Candidate) {
		res.Challenges = []Challenge{{
			Dimension: DimensionRedundancy,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"冗余校验无法进行：候选收益序列只有 %d 个点或全程无波动 —— "+
					"一条没有波动的序列，相关系数学上无定义。",
				len(in.Candidate)),
		}}
		res.Probability = 0.5
		return res
	}

	var sumAbs float64
	for _, ex := range in.Existing {
		x, y, ok := align(in.Candidate, ex.Returns)
		if !ok {
			res.Skipped = append(res.Skipped, ex.Name)
			continue
		}
		rho := statistics.Pearson(x, y)
		if math.IsNaN(rho) {
			// Pearson 对零方差序列返回 NaN。它不是一个数，
			// 更不是 0 ——  silently 当 0 会伪装成「完全不相关」。
			res.Skipped = append(res.Skipped, ex.Name)
			continue
		}
		// 浮点误差可能让 |ρ| 略微超过 1。
		abs := math.Min(math.Abs(rho), 1)

		res.Compared++
		sumAbs += abs
		if abs > res.MaxAbsCorr {
			res.MaxAbsCorr = abs
			res.MaxCorrName = ex.Name
			if rho < 0 {
				res.MaxCorrSign = -1
			} else {
				res.MaxCorrSign = 1
			}
		}
		if abs > HighCorrThreshold {
			res.Redundant++
		}
		res.SampleSize = len(x)
	}

	if res.Compared == 0 {
		msg := fmt.Sprintf(
			"冗余未评估：没有可比的已有策略（传入 %d 条，能算 0 条）。"+
				"给出的 0.5 是中性值，不是「不冗余」。",
			len(in.Existing))
		// 「一条都算不出来」有两种：压根没传，和传了但算不出。
		// 后者必须说清楚原因 —— 否则看起来和前者一样，
		// 而「算不出相关」会被误读成「不相关」。
		if len(res.Skipped) > 0 {
			msg += fmt.Sprintf("其中 %d 条（%v）是零方差或样本太短，"+
				"相关系数学上无定义 —— 跳过不等于不相关。",
				len(res.Skipped), res.Skipped)
		}
		res.Challenges = append(res.Challenges, Challenge{
			Dimension: DimensionRedundancy,
			Severity:  SeverityNote,
			Message:   msg,
		})
		res.Probability = 0.5
		return res
	}

	res.MeanAbsCorr = sumAbs / float64(res.Compared)
	if res.SampleSize > 3 {
		res.CorrStdErr = 1 / math.Sqrt(float64(res.SampleSize-3))
	}
	res.Probability = redundancyProbability(res)
	res.Challenges = redundancyChallenges(in, res)
	return res
}

// redundancyProbability 把「最大相关」映射成「带来独立信息」的概率。
//
// 样本越少，估计越粗，就越向 0.5 收缩：20 个点上算出的「不相关」
// 撑不起「真的不相关」这个结论。
func redundancyProbability(r RedundancyResult) float64 {
	reliability := float64(r.SampleSize) / (float64(r.SampleSize) + ShrinkagePrior)
	raw := 1 - r.MaxAbsCorr
	return clampProb(0.5 + (raw-0.5)*reliability)
}

func redundancyChallenges(in RedundancyInput, r RedundancyResult) []Challenge {
	var cs []Challenge

	switch {
	case r.MaxAbsCorr >= NearDuplicateThreshold:
		cs = append(cs, Challenge{
			Dimension: DimensionRedundancy,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"与「%s」的相关 |ρ|=%.2f —— 基本是同一条策略换了参数。"+
					"加进组合不会带来分散，只会把同一份风险下两次注。",
				r.MaxCorrName, r.MaxAbsCorr),
		})
	case r.MaxAbsCorr > HighCorrThreshold:
		cs = append(cs, Challenge{
			Dimension: DimensionRedundancy,
			Severity:  SeverityWarning,
			Message: fmt.Sprintf(
				"与「%s」的相关 |ρ|=%.2f 超过 %.2f —— 伪分散：看起来多了一条策略，"+
					"实际上多的是同一份风险敞口。",
				r.MaxCorrName, r.MaxAbsCorr, HighCorrThreshold),
		})
	}

	// 强负相关也是「不独立」，只是方向相反。它有用（可以对冲），
	// 但它不是新的信息源 —— 说清楚是哪一种，别让人误读成"很分散"。
	if r.MaxCorrSign < 0 && r.MaxAbsCorr > HighCorrThreshold {
		cs = append(cs, Challenge{
			Dimension: DimensionRedundancy,
			Severity:  SeverityWarning,
			Message: fmt.Sprintf(
				"注意方向：与「%s」是**负**相关（ρ≈%.2f）。|ρ| 高说明信息并不独立，"+
					"它更像把那条策略反过来跑，而不是一个新增的风险来源。",
				r.MaxCorrName, -r.MaxAbsCorr),
		})
	}

	if r.Redundant > 1 {
		cs = append(cs, Challenge{
			Dimension: DimensionRedundancy,
			Severity:  SeverityWarning,
			Message: fmt.Sprintf(
				"共有 %d 条已有策略与它高度相关（平均 |ρ|=%.2f）—— "+
					"这不是在加策略，是在加杠杆。",
				r.Redundant, r.MeanAbsCorr),
		})
	}

	// 样本不足时，这个相关系数本身就不该被当真。
	if r.SampleSize < MinRedundancySamples {
		cs = append(cs, Challenge{
			Dimension: DimensionRedundancy,
			Severity:  SeverityWarning,
			Message: fmt.Sprintf(
				"只有 %d 个收益点，相关估计的标准误约 ±%.2f —— "+
					"这个区间里「相关 0.5」和「不相关」分不开，别拿它下结论。",
				r.SampleSize, r.CorrStdErr),
		})
	}

	if len(r.Skipped) > 0 {
		cs = append(cs, Challenge{
			Dimension: DimensionRedundancy,
			Severity:  SeverityNote,
			Message: fmt.Sprintf(
				"%d 条已有策略未参与比较（%v）—— 它们全程无波动或样本太短，"+
					"相关系数学上无定义。**跳过不等于不相关**。",
				len(r.Skipped), r.Skipped),
		})
	}

	return cs
}

// align 把两条序列对齐到公共长度。长度不一致就截断到较短的那条 ——
// 否则 Pearson 直接返回 NaN。
//
// 返回 false 表示没法比（太短或零方差）。
func align(a, b []float64) ([]float64, []float64, bool) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n < 2 {
		return nil, nil, false
	}
	x, y := a[:n], b[:n]
	if isConstant(x) || isConstant(y) {
		return nil, nil, false
	}
	return x, y, true
}

// isConstant 判断序列是否全程无波动。零方差序列的相关系数无定义。
func isConstant(s []float64) bool {
	if len(s) == 0 {
		return true
	}
	first := s[0]
	for _, v := range s[1:] {
		if v != first {
			return false
		}
	}
	return true
}

// RedundancyFromBacktests 把回测结果接成冗余校验器的输入。
//
// 这是这一维的**接线口**：校验器本身只认收益序列，而底座有的是回测
// 结果里的净值曲线。没有这层转换，这一维就只能靠调用方手算
// —— 而手算的东西最容易没人算（P2-13 的教训：加能力不等于接上了线）。
//
// existing 用 map 传入，内部按名字排序后处理 —— 顺序必须确定，
// 否则同一份输入两次跑出的「平均相关」会不同（P1-14）。
func RedundancyFromBacktests(candidate *domain.BacktestResult,
	existing map[string]*domain.BacktestResult) RedundancyInput {

	in := RedundancyInput{Candidate: equityToReturns(candidate)}

	names := make([]string, 0, len(existing))
	for name := range existing {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		in.Existing = append(in.Existing, NamedReturns{
			Name:    name,
			Returns: equityToReturns(existing[name]),
		})
	}
	return in
}

// equityToReturns 把净值曲线转成简单收益序列。
func equityToValues(r *domain.BacktestResult) []float64 {
	if r == nil {
		return nil
	}
	values := make([]float64, 0, len(r.PortfolioValues))
	for _, pv := range r.PortfolioValues {
		values = append(values, pv.TotalValue)
	}
	return values
}

func equityToReturns(r *domain.BacktestResult) []float64 {
	values := equityToValues(r)
	if len(values) < 2 {
		return nil
	}
	out := make([]float64, 0, len(values)-1)
	for i := 1; i < len(values); i++ {
		prev := values[i-1]
		if prev == 0 {
			// 净值为 0 的日子算不出收益率，跳过而不是补 0 ——
			// 补一个假的 0 收益会稀释真实波动，把相关往下拉。
			continue
		}
		out = append(out, (values[i]-prev)/prev)
	}
	return out
}
