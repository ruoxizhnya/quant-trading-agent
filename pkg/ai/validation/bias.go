package validation

import (
	"fmt"
	"math"
)

// 偏差校验器：结论有没有被数据的「采集方式」污染。
//
// 和前几维的区别：统计问「运气成分多大」、经济问「扣费还剩多少」、
// 稳健问「换个参数还成立吗」，而这一维问的是**更前面的一件事** ——
// 这个回测碰过的数据，是当时真能看到的那些吗？
//
// 前三类错误让结论不那么确定，偏差类错误让结论**根本不成立**：
// 一个用了未来数据的策略，Sharpe 再高也只是把答案抄了一遍。
//
// 三个子维度（TASKS P2-9d）：
//  1. 前视（look-ahead）—— 用了当时还不知道的信息
//  2. 幸存者（survivorship）—— 池子里只有活到今天的票
//  3. 复权口径（adjustment）—— 价格口径混用 / 没复权
//
// 与 P0-1（财务读取改按 ann_date）、P2-4、P2-8 的已知债是同一族：
// 那几条是具体的数据债，这里是**检查那些债是否真的结清了**的那一维。
//
// 接线状态（2026-09-17 实测，改之前先看一眼，别重复调查）：
//   - 复权口径：行情落在 `ohlcv_daily_qfq`，即**前复权** —— 默认应传
//     AdjustPre，它会拿到 0.85 并附带「前复权自带前视成分」的提醒。
//   - 池子来源：由数据同步时的 list_status 决定（tushare stock_basic）。
//     同步传 "L"（当前上市）→ PoolSourceCurrent，池子里没有退市票，
//     幸存者偏差是**真的存在**，不是理论风险。见 pkg/data/tushare.go:187。
//   - PIT：equitydeep 链路已强制 ann_date（缺了直接报错），
//     见 pkg/data/equitydeep/snapshot.go:126。但那只是这一条链路，
//     其余取数路径有没有做，得逐个查过才算 PITVerified。

// 概率的上下限。不给 0，也不给 1 ——
// 那是确定性判断，不是概率估计。完全前视的策略也不是「必然错」，
// 而是「没有任何理由相信它对」。
const (
	// BiasProbFloor 是任一子维度能给的最低概率。
	BiasProbFloor = 0.02
	// BiasProbCeil 是任一子维度能给的最高概率。
	// 已做过 PIT 校验也不给满分：校验本身可能有漏。
	BiasProbCeil = 0.98
)

// 股票池来源。它决定了池子里有没有退市票，也就决定了幸存者偏差的量级。
const (
	// PoolSourcePointInTime 表示按历史时点的上市名单取池子 —— 正确做法。
	PoolSourcePointInTime = "point_in_time"
	// PoolSourceCurrent 表示按**今天**的上市名单回测历史 —— 经典错误，
	// 会把期间退市/暴跌的票整个抹掉，收益被系统性高估。
	PoolSourceCurrent = "current"
)

// 价格复权口径。
const (
	AdjustPre   = "pre"   // 前复权
	AdjustPost  = "post"  // 后复权
	AdjustNone  = "none"  // 未复权
	AdjustMixed = "mixed" // 混用
)

// BiasInput 是偏差校验器的输入。
//
// 这一维的指标**几乎都得由调用方提供** —— 回测结果本身不含「这个价格
// 当时能不能看到」这类信息。所以每个子维度都可以是「未评估」，
// 未评估 ≠ 没问题（见 Unknown 的处理）。
type BiasInput struct {
	// --- 前视 ---

	// PITVerified 表示这次回测做过 point-in-time 校验：
	// 每个时点只使用该时点已公布的数据（财务按 ann_date，见 P0-1）。
	PITVerified bool

	// DataLagDays 是数据「可得时点」相对「数据所属期间」的平均滞后天数。
	//
	// 财务数据典型值：A 股一季报滞后约 30 天、年报约 90-120 天。
	// ≤ 0 是用未来数据的强信号 —— 报告期还没结束就"知道"了业绩。
	// 为 0 且未提供其它证据时按「未评估」处理，不冤枉也不放过。
	DataLagDays float64

	// DataLagKnown 表示 DataLagDays 是否真的测过。
	// 没测就是没测 —— 零值和「滞后 0 天」必须区分开。
	DataLagKnown bool

	// FutureDataSignals 是检出使用了未来数据的信号条数。
	FutureDataSignals int
	// TotalSignals 是信号总条数，用来把上面那个数变成比例。
	TotalSignals int

	// --- 幸存者 ---

	// PoolSource 取 PoolSourcePointInTime / PoolSourceCurrent，空串表示未知。
	PoolSource string
	// PoolSize 是回测股票池的规模。
	PoolSize int
	// DelistedInPool 是回测期间退市、且**仍在池子里**被交易的股票数。
	// 它是幸存者偏差最直接的证据：池子里一只退市票都没有，
	// 十有八九是按今天的名单取的。
	DelistedInPool int

	// --- 复权 ---

	// PriceAdjustment 取 AdjustPre / AdjustPost / AdjustNone / AdjustMixed，
	// 空串表示未知。
	PriceAdjustment string
	// CorporateActions 是回测期间的除权除息/送股事件数。
	// 它本身不是错误，但它决定了「没复权」有多致命。
	CorporateActions int
}

// BiasResult 是偏差校验器的输出。
type BiasResult struct {
	// 三个子维度各自的概率。0 表示**未评估**（不是"没问题"）。
	Lookahead    float64 `json:"lookahead"`
	Survivorship float64 `json:"survivorship"`
	Adjustment   float64 `json:"adjustment"`

	// Assessed 是真正评估过的子维度个数（0~3）。
	// 一个都没评估时，下面的 Probability 只是中性值，不是结论。
	Assessed int `json:"assessed"`

	// Probability 是偏差维度上的综合概率，取已评估子维度的**几何平均**。
	//
	// 为什么几何：三个子维度是合取关系 —— 前视成立就足以否掉结论，
	// 幸存者再干净也救不回来。算术平均会让「两维干净」掩盖「一维致命」。
	Probability float64 `json:"probability"`

	Challenges []Challenge `json:"challenges"`
}

// ValidateBias 检查结论有没有被数据的采集方式污染。
//
// 立场：查不出来就说查不出来。没提供指标的子维度标为「未评估」并给
// note，既不进分子也不假装通过 —— 「没查」不是「没问题」。
func ValidateBias(in BiasInput) BiasResult {
	res := BiasResult{
		Lookahead:    biasLookahead(in),
		Survivorship: biasSurvivorship(in),
		Adjustment:   biasAdjustment(in),
	}
	for _, p := range []float64{res.Lookahead, res.Survivorship, res.Adjustment} {
		if p > 0 {
			res.Assessed++
		}
	}
	res.Probability = biasCombined(res)
	res.Challenges = biasChallenges(in, res)
	return res
}

// biasLookahead 评估前视偏差。返回 0 表示未评估。
func biasLookahead(in BiasInput) float64 {
	// 检出了明确使用未来数据的信号 —— 这是硬证据，比例直接决定可信度。
	if in.FutureDataSignals > 0 {
		ratio := 1.0
		if in.TotalSignals > 0 {
			ratio = float64(in.FutureDataSignals) / float64(in.TotalSignals)
		}
		if ratio > 1 {
			ratio = 1
		}
		return clampProb(1 - ratio)
	}

	// 滞后天数为负：数据"可得"早于它所属的期间 —— 只能是未来数据。
	if in.DataLagKnown && in.DataLagDays < 0 {
		return BiasProbFloor
	}

	if in.PITVerified {
		// 做过 PIT 校验也没检出未来数据。仍不给满分：校验可能有漏。
		if in.DataLagKnown && in.DataLagDays == 0 {
			// 声称做过 PIT，但所有数据都是零滞后 —— 这两件事不太能同时成立，
			// 更可能是"以为自己做了"。给中等分并交由质疑说明。
			return 0.5
		}
		return 0.9
	}

	if in.DataLagKnown {
		// 没做 PIT 校验，但滞后天数是正的 —— 至少数据不是来自未来。
		// 滞后越接近真实披露节奏（30~120 天）越可信。
		return clampProb(0.4 + 0.4*math.Min(in.DataLagDays/30, 1))
	}

	return 0 // 未评估
}

// biasSurvivorship 评估幸存者偏差。返回 0 表示未评估。
func biasSurvivorship(in BiasInput) float64 {
	switch in.PoolSource {
	case PoolSourceCurrent:
		// 按今天的名单回测历史：期间退市、暴跌、被 ST 的票全都不在池里。
		// 收益几乎必然被高估，而且高估多少无法从结果里看出来。
		return BiasProbFloor
	case PoolSourcePointInTime:
		return 0.9
	}

	// 来源未知时，看池子里有没有退市票 —— 这是能拿到的最直接的证据。
	if in.PoolSize <= 0 {
		return 0 // 未评估
	}
	if in.DelistedInPool > 0 {
		return 0.8 // 池子里有退市票，好迹象
	}
	// 池子不小却一只退市票都没有：大概率是按今天的名单取的。
	return 0.4
}

// biasAdjustment 评估复权口径。返回 0 表示未评估。
func biasAdjustment(in BiasInput) float64 {
	switch in.PriceAdjustment {
	case AdjustNone:
		return BiasProbFloor
	case AdjustMixed:
		// 混用口径比不复用更隐蔽：数字看起来都是"价格"，
		// 但彼此不可比，收益序列里混进了假的跳空。
		return 0.1
	case AdjustPre:
		return 0.85
	case AdjustPost:
		return 0.9
	}
	return 0 // 未评估
}

// biasCombined 把已评估的子维度合成一个概率。
//
// 一个都没评估 → 0.5（中性）+ 质疑。给高分会让人以为"偏差维度通过了"，
// 给低分又是在没证据的情况下否定 —— 两种都是谎。
func biasCombined(r BiasResult) float64 {
	if r.Assessed == 0 {
		return 0.5
	}
	prod := 1.0
	for _, p := range []float64{r.Lookahead, r.Survivorship, r.Adjustment} {
		if p > 0 {
			prod *= p
		}
	}
	return clampProb(math.Pow(prod, 1.0/float64(r.Assessed)))
}

func biasChallenges(in BiasInput, r BiasResult) []Challenge {
	var cs []Challenge

	// --- 前视 ---
	switch {
	case in.FutureDataSignals > 0:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"%d/%d 条信号使用了当时不可得的数据 —— 这不是「回测误差」，是把答案抄进了特征里。"+
					"前视偏差不会随样本增加而消失，它只会让 Sharpe 更好看。",
				in.FutureDataSignals, in.TotalSignals),
		})
	case in.DataLagKnown && in.DataLagDays < 0:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"数据可得时点比它所属的期间还早 %.0f 天 —— 只能是用到了未来数据。"+
					"财务读取应按 COALESCE(ann_date, trade_date)（P0-1 的修法）。",
				-in.DataLagDays),
		})
	case in.PITVerified && in.DataLagKnown && in.DataLagDays == 0:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityWarning,
			Message: "声称做过 PIT 校验，但所有数据都是零滞后 —— 财报不可能在报告期当天就披露。" +
				"更可能是「以为自己做了 PIT」，请核对 ann_date 是否真的进了过滤条件。",
		})
	case !in.PITVerified && !in.DataLagKnown:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityNote,
			Message: "前视偏差未评估：既没有 PIT 校验记录，也没有数据滞后天数的测量。" +
				"这一维没查，不等于没问题 —— 前视是最容易让回测「看起来很美」的一类错误。",
		})
	case !in.PITVerified:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityWarning,
			Message: fmt.Sprintf(
				"未做 PIT 校验（数据滞后 %.0f 天）—— 只知道数据不是来自未来，"+
					"不知道每个时点是否只用当时已公布的数据。",
				in.DataLagDays),
		})
	}

	// --- 幸存者 ---
	switch {
	case in.PoolSource == PoolSourceCurrent:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"股票池按**今天**的上市名单取（%d 只）—— 期间退市的票根本不在池子里。"+
					"这是幸存者偏差最典型的形态，收益被系统性高估，且高估多少无法从结果看出。",
				in.PoolSize),
		})
	case in.PoolSource == "" && in.PoolSize > 0 && in.DelistedInPool == 0:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityWarning,
			Message: fmt.Sprintf(
				"%d 只票的池子里一只退市股都没有，而池子来源未知 —— "+
					"多半是照今天的名单取的。请确认池子是按历史时点构建的。",
				in.PoolSize),
		})
	case in.PoolSource == "" && in.PoolSize <= 0:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityNote,
			Message:   "幸存者偏差未评估：既不知道池子来源，也没有池子规模与退市股数量。",
		})
	case in.DelistedInPool > 0:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityNote,
			Message: fmt.Sprintf(
				"池子里含 %d 只期间退市的股票 —— 这是好迹象，说明池子不是按今天名单取的。",
				in.DelistedInPool),
		})
	}

	// --- 复权 ---
	switch in.PriceAdjustment {
	case AdjustNone:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityBlocking,
			Message: fmt.Sprintf(
				"价格未复权，而期间有 %d 次除权除息/送股 —— 除权日的跳空会被当成真实收益或亏损。"+
					"这种错误的特点是：错的幅度恰好等于分红，看起来却像策略在某天暴赚或暴亏。",
				in.CorporateActions),
		})
	case AdjustMixed:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityBlocking,
			Message: "复权口径混用：序列里的价格不属于同一把尺子，收益数字不可比 —— " +
				"这一维必须先统一口径，其它几维的结论才谈得上成立。",
		})
	case AdjustPre:
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityNote,
			Message: "用的是前复权价格。回测里最常见，但要知道它**自带前视成分**：" +
				"前复权序列依赖未来的分红送股，历史价格会随之后发生的事件被改写。" +
				"严格追求无前视时应该用后复权。",
		})
	case "":
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityNote,
			Message: fmt.Sprintf(
				"复权口径未知（期间 %d 次除权除息事件）—— 口径错了，收益序列里的跳空就不是收益。",
				in.CorporateActions),
		})
	}

	if r.Assessed == 0 {
		cs = append(cs, Challenge{
			Dimension: DimensionBias,
			Severity:  SeverityNote,
			Message: "偏差维度三个子维度一个都没评估，给出的 0.5 是**中性值不是结论** —— " +
				"没查不等于没偏差。",
		})
	}

	return cs
}

// clampProb 把概率收进 [BiasProbFloor, BiasProbCeil]。
func clampProb(p float64) float64 {
	if math.IsNaN(p) {
		return BiasProbFloor
	}
	if p < BiasProbFloor {
		return BiasProbFloor
	}
	if p > BiasProbCeil {
		return BiasProbCeil
	}
	return p
}
