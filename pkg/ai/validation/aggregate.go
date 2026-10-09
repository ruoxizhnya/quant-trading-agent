package validation

import (
	"fmt"
	"math"
	"strings"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// 验证器链的聚合入口：一次提案喂进去，六维的质疑清单吐出来。
//
// 前五片各自独立（统计 / 经济 / 稳健 / 偏差 / 冗余），这一层把它们拼成
// 人能看的一页。**它不替人下结论** —— 输出的是概率估计 + 质疑清单，
// 判生死的仍然是人（PRODUCT §边界：决策权在人）。
//
// 为什么必须有这一层：五片写完之后全仓零调用，整包是孤儿。能力造出来
// 不等于有人用（P2-13 的教训），聚合入口就是那个「有人用」的把手。

// Proposal 是一次提案：一条策略 + 它的回测结果 + 上下文。
//
// 只有 Result 是必需的，其余都是「有就更准」 —— 但缺了它们，
// 对应那一维会退化成未评估，而不是假装没事。
type Proposal struct {
	Name string `json:"name,omitempty"`

	// Result 是这次尝试的回测结果。五维里有三维直接吃它。
	Result *domain.BacktestResult `json:"-"`

	// NumTrials / FailedTrials 是这一轮探索总共试了多少次（**含失败**）。
	// 统计维靠它收紧门槛：只报成功次数，等于把最有力的那部分选择偏差藏起来。
	NumTrials    int `json:"num_trials"`
	FailedTrials int `json:"failed_trials"`

	// Neighbors 是同一轮探索里试过的邻近参数 —— 稳健维靠它分辨高原还是尖峰。
	Neighbors []NeighborPoint `json:"-"`

	// Cost 是成本模型，零值 = 用 A 股默认费率（见 economic.go）。
	Cost CostModel `json:"-"`

	// Bias 是偏差维的输入（前视 / 幸存者 / 复权口径）。
	Bias BiasInput `json:"-"`

	// Existing 是组合里已有的策略，冗余维拿它比相关性。
	Existing map[string]*domain.BacktestResult `json:"-"`

	// Causal 是因果维的结果，由调用方**预先跑好**再喂进来。
	//
	// 为什么不在这里调 LLM：聚合器必须是纯的、快跑的 —— 探索里每次尝试
	// 都要过一遍验证器，而因果维是唯一要花一次模型调用的一维。让它留在
	// 调用方手里，才能决定「只给最终候选做一次」还是「每次都做」。
	// 为 nil = 未评估（不是通过），会被记进 Unassessed。
	Causal *CausalResult `json:"-"`
}

// Verdict 是聚合后的结论。
type Verdict struct {
	// Dimensions 是各维的概率。缺的那一维不出现在 map 里 ——
	// 不在 = 没评估，而不是 0 分。
	Dimensions map[string]float64 `json:"dimensions"`
	// Unassessed 是没评估的维度名，让「没查」在输出里看得见。
	Unassessed []string `json:"unassessed,omitempty"`

	// Probability 是综合概率，取**已评估维度的最小值**。
	//
	// 为什么是 min 而不是平均：这几维是合取关系 —— 一条策略必须在每一维
	// 都站得住，才谈得上下注。任何一个塌了，其它维再漂亮也救不回来。
	// 算术平均会让「四维优秀」掩盖「一维致命」，几何平均稀释得太狠
	//（0.02 配四个 0.9，几何平均还有 0.42，看起来居然还行）。
	// min 的解释性也最强：结论受限于最弱的那一环，一眼看得出该修哪。
	Probability float64 `json:"probability"`
	// Weakest 是最弱的那一维 —— 想提高结论可信度，就该从它下手。
	Weakest string `json:"weakest,omitempty"`

	// GeometricMean 是各维的几何平均，作为参考一起给出。
	// 它和 min 差得越多，说明各维越不均衡（有一维在拖后腿）。
	GeometricMean float64 `json:"geometric_mean"`

	// Blocking 是 blocking 级质疑的条数。>0 时这个提案基本可以否掉了。
	Blocking   int         `json:"blocking"`
	Challenges []Challenge `json:"challenges"`

	// 各维的完整结果，未计算的为 nil。
	Statistical *StatisticalResult `json:"statistical,omitempty"`
	Economic    *EconomicResult    `json:"economic,omitempty"`
	Robustness  *RobustnessResult  `json:"robustness,omitempty"`
	BiasResult  *BiasResult        `json:"bias,omitempty"`
	Redundancy  *RedundancyResult  `json:"redundancy,omitempty"`
	Causal      *CausalResult      `json:"causal,omitempty"`
}

// ValidateProposal 跑一遍验证器链，输出质疑清单 + 概率估计。
//
// 各维能算就算，算不了就记进 Unassessed —— 缺哪一维都说得清清楚楚，
// 不拿剩下的几维凑一个「看起来完整」的结论。
func ValidateProposal(p Proposal) Verdict {
	v := Verdict{Dimensions: map[string]float64{}}

	if p.Result == nil {
		v.Unassessed = []string{
			DimensionStatistical, DimensionEconomic,
			DimensionRobustness, DimensionRedundancy,
		}
		v.Challenges = []Challenge{{
			Dimension: DimensionBias,
			Severity:  SeverityBlocking,
			Message: "没有回测结果，五维里四维无法评估。给不出结论就是给不出 —— " +
				"不会拿剩下的一维凑一个看起来完整的数字。",
		}}
		v.Blocking = 1
		v.Probability = 0.5
		return v
	}

	periods := len(p.Result.PortfolioValues)

	// --- 票池非空（OBS-01）---
	//
	// 数据源：Proposal.Result 里的引擎票池统计（UniverseMaxSize）—— 引擎算一次、
	// 这里只读，不重算（单一实现）。空票池是「运行有效性」的前置失败：整轮回测
	// 一个可交易标的都没有，后面每一维都在给垃圾数字打分。所以空票池让这一维
	// 进 Dimensions 且概率 0（会被综合概率的 min 拉到 0，等于一票否决）。
	//
	// 票池非空时**不进 map** —— 沿用「未评估维度不出现在 map」的既有语义，
	// 避免把正常回测无端打成低分。
	// nil（未评估）**不进 map** —— 与「未评估维度不出现在 map」同一语义；
	// 把未评估当空票池同样会无端压低（或一票否决）别的生产路径的结果。
	if p.Result.UniverseMaxSize != nil && *p.Result.UniverseMaxSize <= 0 {
		v.Dimensions[DimensionUniverse] = 0
		v.Challenges = append(v.Challenges, Challenge{
			Dimension: DimensionUniverse,
			Severity:  SeverityBlocking,
			Message: "回测期间票池始终为空（没有任何一个交易日有可交易标的）—— " +
				"这是一次无效运行：0 成交与垃圾指标不构成任何可评价的证据，" +
				"结论受限于此，先确认票池/数据源再谈其它维度。",
		})
	}

	// --- 统计 ---
	if p.NumTrials > 0 && periods > 0 {
		sr := ValidateStatistical(StatisticalInput{
			Sharpe:       p.Result.SharpeRatio,
			NumPeriods:   periods,
			NumTrials:    p.NumTrials,
			FailedTrials: p.FailedTrials,
		})
		v.Statistical = &sr
		v.Dimensions[DimensionStatistical] = sr.Probability
		v.Challenges = append(v.Challenges, sr.Challenges...)
	} else {
		v.Unassessed = append(v.Unassessed, DimensionStatistical)
		v.Challenges = append(v.Challenges, Challenge{
			Dimension: DimensionStatistical,
			Severity:  SeverityNote,
			Message: fmt.Sprintf(
				"统计维未评估：需要尝试次数（当前 %d）和观测期数（当前 %d）。"+
					"不知道试了多少次，就无法判断这个 Sharpe 是不是被挑出来的。",
				p.NumTrials, periods),
		})
	}

	// --- 经济 ---
	if er, ok := ValidateEconomicFromBacktest(p.Result, p.Cost); ok {
		v.Economic = &er
		v.Dimensions[DimensionEconomic] = er.Probability
		v.Challenges = append(v.Challenges, er.Challenges...)
	} else {
		v.Unassessed = append(v.Unassessed, DimensionEconomic)
		v.Challenges = append(v.Challenges, Challenge{
			Dimension: DimensionEconomic,
			Severity:  SeverityNote,
			Message: "经济维未评估：回测没有成交，算不出换手率，也就算不出成本。" +
				"（零成交不是零成本 —— 它是什么都没证明。）",
		})
	}

	// --- 稳健 ---
	rr := ValidateRobustness(RobustnessInput{
		Result:    p.Result,
		Neighbors: p.Neighbors,
	})
	v.Robustness = &rr
	v.Dimensions[DimensionRobustness] = rr.Probability
	v.Challenges = append(v.Challenges, rr.Challenges...)

	// --- 偏差 ---
	br := ValidateBias(p.Bias)
	v.BiasResult = &br
	// 偏差维里 0 表示「未评估」，不能塞进 Dimensions ——
	// 那样会被当成 0 分，而它是"不知道"。
	if br.Assessed > 0 {
		v.Dimensions[DimensionBias] = br.Probability
	} else {
		v.Unassessed = append(v.Unassessed, DimensionBias)
	}
	v.Challenges = append(v.Challenges, br.Challenges...)

	// --- 冗余 ---
	if len(p.Existing) > 0 {
		in := RedundancyFromBacktests(p.Result, p.Existing)
		dr := ValidateRedundancy(in)
		v.Redundancy = &dr
		if dr.Compared > 0 {
			v.Dimensions[DimensionRedundancy] = dr.Probability
		} else {
			v.Unassessed = append(v.Unassessed, DimensionRedundancy)
		}
		v.Challenges = append(v.Challenges, dr.Challenges...)
	} else {
		v.Unassessed = append(v.Unassessed, DimensionRedundancy)
		v.Challenges = append(v.Challenges, Challenge{
			Dimension: DimensionRedundancy,
			Severity:  SeverityNote,
			Message:   "冗余维未评估：没有传入已有策略，无从判断这是不是伪分散。",
		})
	}

	// --- 因果 ---
	// 六维里唯一需要语言模型的一维，由调用方决定什么时候跑。
	if p.Causal != nil {
		v.Causal = p.Causal
		v.Dimensions[DimensionCausal] = p.Causal.Probability
		v.Challenges = append(v.Challenges, p.Causal.Challenges...)
	} else {
		v.Unassessed = append(v.Unassessed, DimensionCausal)
		v.Challenges = append(v.Challenges, Challenge{
			Dimension: DimensionCausal,
			Severity:  SeverityNote,
			Message: "因果维未评估：这一维要先让模型讲出机制并下可证伪的预测，" +
				"再用确定性检验去验。没讲就是没讲 —— 不拿剩下的五维凑数。",
		})
	}

	finalizeVerdict(&v)
	return v
}

// AttachCausal 把因果维补进已有裁决，并重算综合概率。
//
// 因果维的节奏天生和另外五维不一样：另外五维每次尝试都跑，而它是**只对
// 最终候选做一次**（一次模型调用不是免费的）。所以它是事后补进来的 ——
// 补完之后必须重算综合概率，否则最弱维还停留在补之前的那一维上。
func AttachCausal(v *Verdict, c *CausalResult) {
	if v == nil || c == nil {
		return
	}
	if _, ok := v.Dimensions[DimensionCausal]; !ok {
		// 之前记过「未评估」，现在补上了 —— 从 Unassessed 里摘掉。
		v.Unassessed = removeString(v.Unassessed, DimensionCausal)
		// 同时摘掉那条「未评估」的 note，免得页面上自相矛盾。
		v.Challenges = filterChallenges(v.Challenges, func(ch Challenge) bool {
			return ch.Dimension == DimensionCausal && ch.Severity == SeverityNote &&
				len(ch.Message) > 0 && containsUnassessedCausal(ch.Message)
		})
	}
	v.Causal = c
	v.Dimensions[DimensionCausal] = c.Probability
	v.Challenges = append(v.Challenges, c.Challenges...)
	finalizeVerdict(v)
}

// containsUnassessedCausal 认出「因果维未评估」那条 note。
//
// 用前缀而不是把文案抽成常量再比相等：那条 note 是给人读的，它属于输出
// 而不是身份，为它建一个常量反而会诱导调用方去依赖文案本身。
func containsUnassessedCausal(msg string) bool {
	return strings.HasPrefix(msg, "因果维未评估")
}

func removeString(xs []string, s string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func filterChallenges(cs []Challenge, drop func(Challenge) bool) []Challenge {
	out := cs[:0]
	for _, c := range cs {
		if !drop(c) {
			out = append(out, c)
		}
	}
	return out
}

// finalizeVerdict 算综合概率与统计信息。
func finalizeVerdict(v *Verdict) {
	if len(v.Dimensions) == 0 {
		v.Probability = 0.5
		v.Challenges = append(v.Challenges, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityNote,
			Message:   "六维一个都没评估，给出的 0.5 是中性值，不是结论。",
		})
		return
	}

	// 顺序固定 —— 同一个提案两次跑出来的 weakest 必须一样。
	order := []string{
		DimensionStatistical, DimensionEconomic, DimensionRobustness,
		DimensionBias, DimensionRedundancy, DimensionCausal, DimensionUniverse,
	}
	// 缺的维度（未评估）也要排进来，否则因果维这种"后补"的一维
	// 会因为不在 order 里而被漏掉。
	for d := range v.Dimensions {
		if !containsString(order, d) {
			order = append(order, d)
		}
	}

	minProb, weakest, prod := 2.0, "", 1.0
	n := 0
	for _, d := range order {
		p, ok := v.Dimensions[d]
		if !ok {
			continue
		}
		n++
		prod *= p
		if p < minProb {
			minProb, weakest = p, d
		}
	}
	v.Probability = minProb
	v.Weakest = weakest
	v.GeometricMean = math.Pow(prod, 1.0/float64(n))

	// 重算而不是累加：AttachCausal 会二次调用，累加会把质疑数算成两倍。
	v.Blocking = 0
	for _, c := range v.Challenges {
		if c.Severity == SeverityBlocking {
			v.Blocking++
		}
	}
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
