package validation

import (
	"math"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// 造一份回测结果：净值曲线 + 若干笔成交。
// sharpe 只影响统计维的读数；trades 决定经济维能不能算。
func resultFixture(periods int, sharpe float64, nTrades int) *domain.BacktestResult {
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	values := make([]domain.PortfolioValue, periods)
	eq := 1_000_000.0
	for i := range values {
		eq *= 1 + 0.0004*sharpe
		values[i] = domain.PortfolioValue{
			Date:       start.AddDate(0, 0, i),
			TotalValue: eq,
			Cash:       eq * 0.2,
			Positions:  eq * 0.8,
		}
	}

	trades := make([]domain.Trade, 0, nTrades)
	for i := 0; i < nTrades; i++ {
		trades = append(trades, domain.Trade{
			Symbol:    "600000.SH",
			Direction: domain.DirectionLong,
			Quantity:  1000,
			Price:     50,
			Timestamp: start.AddDate(0, 0, i%periods),
		})
	}

	return &domain.BacktestResult{
		SharpeRatio:     sharpe,
		TotalReturn:     eq/1_000_000.0 - 1,
		TotalTrades:     nTrades,
		PortfolioValues: values,
		Trades:          trades,
		// OBS-01：这份 fixture 模拟的是「引擎跑出来的正常回测」—— 引擎总会
		// 把票池统计挂上。这里显式给一个非空票池，否则新加的「票池非空」维
		// 会把正常结果误判成无效运行（详见 aggregate.go 的 DimensionUniverse）。
		UniverseMaxSize: intPtr(40),
	}
}

// 聚合层存在的理由：五片写完之后全仓零调用，整包是孤儿。
// 这一层就是那个「有人用」的把手。

func TestAggregate_NoResultIsHonestlyEmpty(t *testing.T) {
	v := ValidateProposal(Proposal{NumTrials: 10})

	if v.Probability != 0.5 {
		t.Fatalf("没有回测结果时应是中性 0.5，实际 %.3f", v.Probability)
	}
	if len(v.Dimensions) != 0 {
		t.Fatalf("不该有任何维度被评估，实际 %v", v.Dimensions)
	}
	if len(v.Unassessed) == 0 {
		t.Fatal("必须列出未评估的维度，否则看起来像「都查过了」")
	}
	if v.Blocking == 0 {
		t.Fatal("没有回测结果是 blocking，不是 note")
	}
}

func TestAggregate_ProbabilityIsTheWeakestLink(t *testing.T) {
	v := ValidateProposal(Proposal{
		Name:      "p",
		Result:    resultFixture(300, 1.5, 40),
		NumTrials: 3,
		Bias: BiasInput{
			PITVerified:     true,
			DataLagKnown:    true,
			DataLagDays:     45,
			PoolSource:      PoolSourcePointInTime,
			PriceAdjustment: AdjustPost,
		},
	})

	if len(v.Dimensions) == 0 {
		t.Fatalf("给足输入后应有维度被评估，实际未评估：%v", v.Unassessed)
	}

	// 综合概率必须等于某一维的概率（取的是 min，不是平均）。
	found := false
	for d, p := range v.Dimensions {
		if math.Abs(p-v.Probability) < 1e-12 {
			found = true
			if d != v.Weakest {
				t.Fatalf("最弱维度应记为 %s，实际 %s", d, v.Weakest)
			}
		}
		if v.Probability > p+1e-12 {
			t.Fatalf("综合概率 %.3f 不该高于任一维度（%s=%.3f）—— 必须取 min",
				v.Probability, d, p)
		}
	}
	if !found {
		t.Fatalf("综合概率 %.3f 与任何一维都对不上：%v", v.Probability, v.Dimensions)
	}
}

// 一维致命时，min 会把它压到底；几何平均不会 —— 这正是用 min 的原因。
func TestAggregate_OneFatalDimensionCannotBeAveragedAway(t *testing.T) {
	// 换手极大（每天 40 笔成交 × 300 天）→ 经济维必然崩。
	v := ValidateProposal(Proposal{
		Result:    resultFixture(300, 1.5, 12000),
		NumTrials: 2,
		Bias: BiasInput{
			PITVerified:     true,
			DataLagKnown:    true,
			DataLagDays:     60,
			PoolSource:      PoolSourcePointInTime,
			PriceAdjustment: AdjustPost,
		},
	})

	eco, ok := v.Dimensions[DimensionEconomic]
	if !ok {
		t.Fatalf("经济维应已评估，实际维度：%v", v.Dimensions)
	}
	if v.Probability > eco+1e-12 {
		t.Fatalf("综合概率应等于崩掉的那一维：probability=%.3f economic=%.3f",
			v.Probability, eco)
	}
	if v.Weakest != DimensionEconomic {
		t.Fatalf("最弱的应是经济维，实际 %s", v.Weakest)
	}
	if v.Blocking == 0 {
		t.Fatalf("经济维崩了必须有 blocking 质疑：%+v", v.Challenges)
	}
}

// 偏差维的 0 是「未评估」不是 0 分 —— 塞进 Dimensions 会被取 min 拉到 0。
func TestAggregate_UnassessedBiasIsExcludedNotZero(t *testing.T) {
	v := ValidateProposal(Proposal{
		Result:    resultFixture(300, 1.0, 40),
		NumTrials: 3,
		// Bias 全空 = 三个子维度都没查
	})

	if _, ok := v.Dimensions[DimensionBias]; ok {
		t.Fatal("偏差维未评估时不该出现在 Dimensions 里 —— 那会被当成 0 分")
	}
	found := false
	for _, d := range v.Unassessed {
		if d == DimensionBias {
			found = true
		}
	}
	if !found {
		t.Fatalf("偏差维应记入 Unassessed，实际 %v", v.Unassessed)
	}
	// 其它维度照常参与，概率不该被一个未评估的维度拖到 0。
	if v.Probability <= 0 {
		t.Fatalf("概率不该是 0，实际 %.3f（未评估 ≠ 0 分）", v.Probability)
	}
}

func TestAggregate_RedundancyIncludedWhenPeersGiven(t *testing.T) {
	cand := resultFixture(300, 1.0, 40)
	peers := map[string]*domain.BacktestResult{
		"twin": cand, // 完全相同的曲线 → ρ=1
	}

	v := ValidateProposal(Proposal{
		Result:    cand,
		NumTrials: 3,
		Existing:  peers,
	})

	if _, ok := v.Dimensions[DimensionRedundancy]; !ok {
		t.Fatalf("给了已有策略就该评估冗余维，实际 %v（未评估：%v）",
			v.Dimensions, v.Unassessed)
	}
	if v.Dimensions[DimensionRedundancy] > 0.1 {
		t.Fatalf("与完全相同的策略比，冗余概率应很低，实际 %.3f",
			v.Dimensions[DimensionRedundancy])
	}
}

// 确定性：同一份提案跑两次，weakest 和概率必须一样。
// Dimensions 是 map，遍历顺序随机 —— 靠 finalizeVerdict 里的固定 order 保证。
func TestAggregate_IsDeterministic(t *testing.T) {
	p := Proposal{
		Result:    resultFixture(300, 1.2, 40),
		NumTrials: 5,
		Bias: BiasInput{
			PITVerified:     true,
			DataLagKnown:    true,
			DataLagDays:     45,
			PoolSource:      PoolSourcePointInTime,
			PriceAdjustment: AdjustPre,
		},
	}
	a := ValidateProposal(p)
	b := ValidateProposal(p)

	if a.Weakest != b.Weakest || a.Probability != b.Probability {
		t.Fatalf("两次结果不一致：%s/%.6f vs %s/%.6f",
			a.Weakest, a.Probability, b.Weakest, b.Probability)
	}
	if len(a.Challenges) != len(b.Challenges) {
		t.Fatalf("质疑条数不一致：%d vs %d", len(a.Challenges), len(b.Challenges))
	}
}

func TestAggregate_AllUnassessedIsNeutral(t *testing.T) {
	v := ValidateProposal(Proposal{})

	if v.Probability != 0.5 {
		t.Fatalf("全部未评估时概率应是中性 0.5，实际 %.3f", v.Probability)
	}
	if v.Blocking == 0 {
		t.Fatal("连回测结果都没有，必须是 blocking")
	}
}

// intPtr 返回 int 的指针（OBS-01 审查修复：UniverseMaxSize 改为 *int，
// nil = 未评估、0 = 明确为空 —— 未知不等于空）。
func intPtr(v int) *int { return &v }
