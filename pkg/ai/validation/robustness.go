package validation

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// NeighborPoint 是参数邻域里的一次尝试 —— 和中心点相近但不完全相同。
//
// 它来自实验日志（P1-1）：同一轮探索里，AI 试过的其它参数组合。
// 没有这些数据就不知道中心那个点是「高原」还是「尖峰」。
type NeighborPoint struct {
	Params map[string]interface{} `json:"params,omitempty"`
	Sharpe float64                `json:"sharpe"`
	Return float64                `json:"return,omitempty"`
}

// SegmentReturn 是净值曲线切出来的一个时间段（按自然年）的收益。
type SegmentReturn struct {
	Label  string  `json:"label"`
	Return float64 `json:"return"`
}

// RobustnessInput 是稳健校验器的输入。
//
// 稳健 = 两件独立的事，缺一件这一维就只有一半信息：
//  1. 邻域不塌 —— 参数稍微动一动，结论还在不在
//  2. 分段一致 —— 收益是不是集中在某一个幸运时段
type RobustnessInput struct {
	Result    *domain.BacktestResult
	Neighbors []NeighborPoint
}

// RobustnessResult 是稳健校验器的输出。
type RobustnessResult struct {
	CenterSharpe float64 `json:"center_sharpe"`

	// NeighborMedian / NeighborWorst 是邻域的收益中位数与最差值。
	NeighborMedian float64 `json:"neighbor_median"`
	NeighborWorst  float64 `json:"neighbor_worst"`

	// Decay 是衰减比 = 邻域中位数 / 中心。1.0 表示邻域和中心一样好，
	// 接近 0 表示一动就塌。
	Decay float64 `json:"decay"`

	// PlateauRatio 是「高原面积」——邻域里仍然站得住的点占比。
	// 站得住 = Sharpe ≥ max(中心的一半, 0)：既要接近中心，又至少要赚钱。
	//
	// 这是 PRODUCT 里「按高原面积 × 因果强度排序」的那个高原面积。
	// 它比 Sharpe 本身重要：一个 2.0 的尖峰，不如一片 1.2 的高原。
	PlateauRatio float64 `json:"plateau_ratio"`

	Segments         []SegmentReturn `json:"segments"`
	PositiveSegRatio float64         `json:"positive_segment_ratio"`

	// TopSegmentShare 是最好的那一段贡献了总收益的多大比例。
	// 接近 1 说明整个策略的收益都押在一个幸运时段上。
	TopSegmentShare float64 `json:"top_segment_share"`

	// Probability 是「稳健维度上站得住」的校准后概率估计。
	// 和 economic.go 一样：这是启发式组合（几何平均），未经数据校准。
	Probability float64     `json:"probability"`
	Challenges  []Challenge `json:"challenges"`
}

// ValidateRobustness 看这个结果是高原还是尖峰、是全时段还是单时段。
//
// 为什么几何平均而不是算术：稳健的两个子维度是**合取**关系 ——
// 邻域塌了，分段再漂亮也不可信；反过来也一样。算术平均会让一半的
// 优秀掩盖另一半的崩塌，几何平均不会。
func ValidateRobustness(in RobustnessInput) RobustnessResult {
	// 先取长度再判断：曾经在错误分支里直接 len(in.Result.PortfolioValues)，
	// 结果 nil 输入时校验器自己 panic —— 报错的代码比被校验的代码先崩，
	// 是最讽刺的一种失败。
	var nPoints int
	if in.Result != nil {
		nPoints = len(in.Result.PortfolioValues)
	}
	if in.Result == nil || nPoints < 2 {
		return RobustnessResult{
			Challenges: []Challenge{{
				Dimension: DimensionRobustness,
				Severity:  SeverityBlocking,
				Message: fmt.Sprintf(
					"稳健校验无法进行：回测结果为空或净值曲线不足 2 点（%d）。"+
						"没有曲线就切不出时间段，也就说不清收益是不是集中在某一个幸运时段。",
					nPoints),
			}},
		}
	}

	res := RobustnessResult{
		CenterSharpe: in.Result.SharpeRatio,
		Segments:     segmentReturns(in.Result.PortfolioValues),
	}
	res.PositiveSegRatio = positiveRatio(res.Segments)
	res.TopSegmentShare = topSegmentShare(res.Segments, totalReturn(in.Result.PortfolioValues))

	if len(in.Neighbors) > 0 {
		res.NeighborMedian, res.NeighborWorst = neighborStats(in.Neighbors)
		res.PlateauRatio = plateauRatio(in.Neighbors, res.CenterSharpe)
		if res.CenterSharpe > 0 {
			res.Decay = res.NeighborMedian / res.CenterSharpe
		}
	}

	res.Probability = robustnessProbability(res, len(in.Neighbors) > 0)
	res.Challenges = robustnessChallenges(in, res)
	return res
}

// MinMeaningfulSharpe 是「值得讨论」的最低年化 Sharpe。
//
// 低于这条线的策略，参数再平坦也没意义 —— 平坦性说的是「结论稳不稳」，
// 有效性说的是「值不值得做」，两者不能互相替代。
const MinMeaningfulSharpe = 0.5

// robustnessProbability 组合两个子维度。
// hasNeighbors 为假时只能用分段（并如实说明缺了一半证据）。
func robustnessProbability(r RobustnessResult, hasNeighbors bool) float64 {
	if !hasNeighbors {
		return r.PositiveSegRatio
	}
	// 高原面积要按中心的高度打折：一片位于零附近的平原仍然是平原，
	// 但没人该在上面建房子。取证时真踩到过 —— 中心 Sharpe=0.03、
	// 邻域紧贴中心，高原面积 1.00，概率直接给到 1.000，荒唐。
	level := 1.0
	if r.CenterSharpe > 0 {
		level = math.Min(1, r.CenterSharpe/MinMeaningfulSharpe)
	} else {
		level = 0
	}
	return math.Sqrt(r.PlateauRatio * level * r.PositiveSegRatio)
}

func robustnessChallenges(in RobustnessInput, r RobustnessResult) []Challenge {
	var cs []Challenge

	if len(in.Neighbors) > 0 {
		if r.PlateauRatio < 0.3 {
			cs = append(cs, Challenge{
				Dimension: DimensionRobustness,
				Severity:  SeverityBlocking,
				Message: fmt.Sprintf(
					"参数邻域几乎全塌：%d 个邻近参数里只有 %.0f%% 站得住（中心 Sharpe=%.2f，邻域中位数 %.2f、最差 %.2f）。"+
						"这是尖峰不是高原 —— 参数稍动就失效的策略，实盘拿不住。",
					len(in.Neighbors), r.PlateauRatio*100, r.CenterSharpe, r.NeighborMedian, r.NeighborWorst),
			})
		} else if r.PlateauRatio < 0.6 {
			cs = append(cs, Challenge{
				Dimension: DimensionRobustness,
				Severity:  SeverityWarning,
				Message: fmt.Sprintf(
					"邻域只有 %.0f%% 站得住，衰减比 %.2f —— 高原偏窄，参数要留余量。",
					r.PlateauRatio*100, r.Decay),
			})
		}
		if r.CenterSharpe < MinMeaningfulSharpe && r.PlateauRatio >= 0.6 {
			cs = append(cs, Challenge{
				Dimension: DimensionRobustness,
				Severity:  SeverityWarning,
				Message: fmt.Sprintf(
					"高原很平，但整片高原都太低：中心 Sharpe=%.2f、邻域中位数 %.2f，都低于 %.1f。"+
						"平坦只说明参数不敏感，不说明策略有效 —— 别把「稳定地不赚钱」当成稳健。",
					r.CenterSharpe, r.NeighborMedian, MinMeaningfulSharpe),
			})
		}
	} else {
		cs = append(cs, Challenge{
			Dimension: DimensionRobustness,
			Severity:  SeverityNote,
			Message: "没有参数邻域数据（实验日志里没有同轮其它尝试），" +
				"无法判断这是高原还是尖峰 —— 本轮稳健性只看了时间分段，证据缺了一半。",
		})
	}

	if len(r.Segments) >= 2 {
		if r.TopSegmentShare > 0.8 && r.PositiveSegRatio <= 0.6 {
			cs = append(cs, Challenge{
				Dimension: DimensionRobustness,
				Severity:  SeverityWarning,
				Message: fmt.Sprintf(
					"收益集中在单一时段：%d 段里只有 %.0f%% 为正，最好的一段贡献了 %.0f%% 的总收益。"+
						"这不是一个持续有效的策略，是踩中了一次行情。",
					len(r.Segments), r.PositiveSegRatio*100, r.TopSegmentShare*100),
			})
		}
		if r.PositiveSegRatio < 0.5 {
			cs = append(cs, Challenge{
				Dimension: DimensionRobustness,
				Severity:  SeverityWarning,
				Message: fmt.Sprintf(
					"%d 个时间段里只有 %.0f%% 赚钱 —— 多数时段在亏，总收益为正全靠少数时段撑着。",
					len(r.Segments), r.PositiveSegRatio*100),
			})
		}
	} else {
		cs = append(cs, Challenge{
			Dimension: DimensionRobustness,
			Severity:  SeverityNote,
			Message: fmt.Sprintf(
				"净值曲线只切出 %d 段，不足以判断收益是否跨时段稳定 —— 回测期再长一些才有意义。",
				len(r.Segments)),
		})
	}

	return cs
}

// segmentReturns 按自然年切分净值曲线，返回每年的收益。
func segmentReturns(pvs []domain.PortfolioValue) []SegmentReturn {
	byYear := make(map[int][]domain.PortfolioValue)
	var years []int
	for _, pv := range pvs {
		if pv.TotalValue <= 0 {
			continue
		}
		y := pv.Date.Year()
		if _, seen := byYear[y]; !seen {
			years = append(years, y)
		}
		byYear[y] = append(byYear[y], pv)
	}
	sort.Ints(years)

	out := make([]SegmentReturn, 0, len(years))
	for _, y := range years {
		bars := byYear[y]
		if len(bars) < 2 {
			continue
		}
		first, last := bars[0].TotalValue, bars[len(bars)-1].TotalValue
		if first <= 0 {
			continue
		}
		out = append(out, SegmentReturn{
			Label:  strconv.Itoa(y),
			Return: last/first - 1,
		})
	}
	return out
}

func positiveRatio(segs []SegmentReturn) float64 {
	if len(segs) == 0 {
		return 0
	}
	var n int
	for _, s := range segs {
		if s.Return > 0 {
			n++
		}
	}
	return float64(n) / float64(len(segs))
}

// topSegmentShare 是最好的一段占总收益的比例。
// 总收益为负时这个比值没有意义，返回 0。
func topSegmentShare(segs []SegmentReturn, total float64) float64 {
	if len(segs) == 0 || total <= 1e-9 {
		return 0
	}
	best := math.Inf(-1)
	for _, s := range segs {
		if s.Return > best {
			best = s.Return
		}
	}
	if best <= 0 {
		return 0
	}
	share := best / total
	if share > 1 {
		return 1
	}
	return share
}

func totalReturn(pvs []domain.PortfolioValue) float64 {
	first, last := 0.0, 0.0
	for _, pv := range pvs {
		if pv.TotalValue <= 0 {
			continue
		}
		if first == 0 {
			first = pv.TotalValue
		}
		last = pv.TotalValue
	}
	if first <= 0 {
		return 0
	}
	return last/first - 1
}

func neighborStats(ns []NeighborPoint) (median, worst float64) {
	vals := make([]float64, 0, len(ns))
	for _, n := range ns {
		vals = append(vals, n.Sharpe)
	}
	sort.Float64s(vals)
	worst = vals[0]
	m := len(vals) / 2
	if len(vals)%2 == 1 {
		median = vals[m]
	} else {
		median = (vals[m-1] + vals[m]) / 2
	}
	return median, worst
}

// plateauRatio 计算高原面积：邻域里「站得住」的点占比。
// 站得住 = 至少赚钱，且不低于中心的一半 —— 两个条件缺一不可，
// 否则一个 2.0 的尖峰会因为邻域全是 0.1 而被算成「大部分站得住」。
func plateauRatio(ns []NeighborPoint, center float64) float64 {
	threshold := math.Max(center*0.5, 0)
	var n int
	for _, p := range ns {
		if p.Sharpe >= threshold {
			n++
		}
	}
	return float64(n) / float64(len(ns))
}
