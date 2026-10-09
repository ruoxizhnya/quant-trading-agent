// Package validation 是 P2-9 的验证器链。
//
// PRODUCT §验证器：六个维度里五个是纯计算，只有「因果」一项真的需要 LLM。
// 验证器**被调用，不自主循环** —— 输入提案 + 实验日志，跑一次，输出
// 概率估计 + 质疑清单，结束。
//
// 立场（PRODUCT §边界：决策权在人）：输出的是**概率估计 + 质疑清单**，
// 不是通过/不通过。判生死的不是验证器。
package validation

import (
	"fmt"
	"math"
)

// 质疑的严重程度。
const (
	// SeverityBlocking 表示这条质疑足以否掉这个结论。
	SeverityBlocking = "blocking"
	// SeverityWarning 需要人注意，但不单独否掉结论。
	SeverityWarning = "warning"
	// SeverityNote 只是提醒，供复盘时参考。
	SeverityNote = "note"
)

// 验证器的六个维度。前五个确定性，最后一个需要语义理解。
const (
	DimensionStatistical = "statistical"
	DimensionEconomic    = "economic"
	DimensionRobustness  = "robustness"
	DimensionBias        = "bias"
	DimensionRedundancy  = "redundancy"
	DimensionCausal      = "causal"
)

// DimensionUniverse 是 OBS-01 新增的「票池非空」维。
//
// 它不属于原来那六个「评估策略好坏」的维度，而是**运行有效性**的前置检查：
// 一个空票池跑出来的任何数字都无从评价（0 成交、垃圾指标），所以票池为空
// 时这一维进 Dimensions 且概率 0；票池非空时**不进 map**（= 不参与综合概率，
// 避免把正常回测打成低分）。语义与「未评估维度不出现在 map」一致。
const DimensionUniverse = "universe"

// DefaultAlpha 是名义显著性水平。
const DefaultAlpha = 0.05

// TradingDaysPerYear 用于把交易日数折成年数 —— Sharpe 是年化的，
// 标准误只能按年数收缩。
const TradingDaysPerYear = 252

// Challenge 是一条质疑。
type Challenge struct {
	Dimension string `json:"dimension"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
}

// StatisticalInput 是统计校验器的输入。
//
// NumTrials 必须**含失败** —— 只统计成功的次数，等于把最有力的那部分
// 选择偏差藏起来。
type StatisticalInput struct {
	Sharpe       float64 // 训练期年化 Sharpe
	NumPeriods   int     // 观测期数（交易日）
	NumTrials    int     // 这一轮试了多少次（含失败）
	FailedTrials int     // 其中失败多少次
}

// StatisticalResult 是统计校验器的输出。
type StatisticalResult struct {
	RawPValue      float64 `json:"raw_p_value"`      // 单次检验的 p 值
	AdjustedPValue float64 `json:"adjusted_p_value"` // 多重检验校正后
	Significant    bool    `json:"significant"`
	// Probability 是校准后的概率估计（1 - 校正后 p 值）。
	// 系统说 60% 就该真中 60%（PRODUCT §目标是可校准），所以这个数字
	// 必须能拿去配仓位，不能是拍出来的。
	Probability float64     `json:"probability"`
	Challenges  []Challenge `json:"challenges"`
}

// ValidateStatistical 做多重检验校正。
//
// 核心事实：试了 N 次才撞出来的结果，显著性要按 N 收紧。用同一把尺子量
// 第二次，置信度不会增加；但从 500 次里挑最好的那个，它的「显著性」大头
// 来自选择偏差，不是来自策略本身。
func ValidateStatistical(in StatisticalInput) StatisticalResult {
	if in.NumPeriods <= 0 || in.NumTrials <= 0 {
		return StatisticalResult{
			Challenges: []Challenge{{
				Dimension: DimensionStatistical,
				Severity:  SeverityBlocking,
				Message: fmt.Sprintf(
					"统计校验无法进行：观测期数=%d、尝试次数=%d 都必须为正 —— 缺任何一个就给不出诚实的概率",
					in.NumPeriods, in.NumTrials),
			}},
		}
	}

	// H0：真实 Sharpe = 0。年化 Sharpe 的标准误约为 1/sqrt(年数)，
	// 于是 z = Sharpe × sqrt(年数)。年数由交易日数折算而来。
	years := float64(in.NumPeriods) / TradingDaysPerYear
	z := math.Abs(in.Sharpe) * math.Sqrt(years)

	rawP := 2 * (1 - normalCDF(z))
	if rawP < 0 {
		rawP = 0
	}

	// 族系误差：试 N 次，至少有一次达到这个水平的概率。
	// 这是 Bonferroni 的精确版（Bonferroni 用 alpha/N 作近似，这里直接算）。
	adjP := 1 - math.Pow(1-rawP, float64(in.NumTrials))
	if adjP > 1 {
		adjP = 1
	}

	res := StatisticalResult{
		RawPValue:      rawP,
		AdjustedPValue: adjP,
		Significant:    adjP < DefaultAlpha,
		Probability:    1 - adjP,
	}
	res.Challenges = statisticalChallenges(in, res)
	return res
}

func statisticalChallenges(in StatisticalInput, r StatisticalResult) []Challenge {
	var cs []Challenge

	if !r.Significant {
		cs = append(cs, Challenge{
			Dimension: DimensionStatistical,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"试了 %d 次之后，Sharpe=%.2f 已不显著：校正后 p=%.3f ≥ %.2f（单次 p=%.3f）。"+
					"试的次数越多，这个结果越可能只是被挑出来的那个。",
				in.NumTrials, in.Sharpe, r.AdjustedPValue, DefaultAlpha, r.RawPValue),
		})
	} else if r.RawPValue >= DefaultAlpha/2 {
		cs = append(cs, Challenge{
			Dimension: DimensionStatistical,
			Severity:  SeverityWarning,
			Message: fmt.Sprintf(
				"单次检验只是勉强显著（p=%.3f），试了 %d 次后校正为 p=%.3f —— 换一批数据大概率不重现。",
				r.RawPValue, in.NumTrials, r.AdjustedPValue),
		})
	}

	// 失败率本身就是信号：一大半尝试都跑不出来，说明这个方向的数据或
	// 参数空间有问题，而不是「试了很多次终于找到宝」。
	if in.FailedTrials > 0 && float64(in.FailedTrials)/float64(in.NumTrials) > 0.5 {
		cs = append(cs, Challenge{
			Dimension: DimensionStatistical,
			Severity:  SeverityWarning,
			Message: fmt.Sprintf(
				"%d/%d 次尝试是失败的 —— 成功样本只占少数，在这上面挑最优，选择偏差比看上去更大。",
				in.FailedTrials, in.NumTrials),
		})
	}

	return cs
}

// normalCDF 是标准正态分布的累积分布函数。
// 用 math.Erf 算：Φ(x) = ½(1 + erf(x/√2))。
func normalCDF(x float64) float64 {
	return 0.5 * (1 + math.Erf(x/math.Sqrt2))
}
