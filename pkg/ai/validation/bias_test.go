package validation

import "testing"

func hasBiasChallenge(r BiasResult, severity string) bool {
	for _, c := range r.Challenges {
		if c.Dimension == DimensionBias && c.Severity == severity {
			return true
		}
	}
	return false
}

// 这一维存在的理由：前三类错误（统计、经济、稳健）让结论不那么确定，
// 偏差类错误让结论**根本不成立** —— 一个用了未来数据的策略，
// Sharpe 再高也只是把答案抄了一遍。

func TestBias_NothingAssessedIsNeutralNotPassing(t *testing.T) {
	got := ValidateBias(BiasInput{})

	if got.Assessed != 0 {
		t.Fatalf("一个指标都没给，不该算评估过任何子维度：Assessed=%d", got.Assessed)
	}
	// 0.5 是「不知道」，不是「没问题」。给高分会被当成这一维通过了。
	if got.Probability != 0.5 {
		t.Fatalf("未评估时的概率应是中性 0.5，实际 %.3f", got.Probability)
	}
	if !hasBiasChallenge(got, SeverityNote) {
		t.Fatal("未评估必须明说「没查不等于没偏差」，不能静默")
	}
}

func TestBias_FutureDataIsBlocking(t *testing.T) {
	got := ValidateBias(BiasInput{
		FutureDataSignals: 20,
		TotalSignals:      100,
	})

	if !hasBiasChallenge(got, SeverityBlocking) {
		t.Fatalf("检出 20%% 的信号用未来数据却只有这些质疑：%+v", got.Challenges)
	}
	// 20% 的信号被污染 → 前视这一维只剩 0.8。
	if got.Lookahead > 0.85 || got.Lookahead < 0.75 {
		t.Fatalf("前视概率应随污染比例下降，期望 ≈0.80，实际 %.3f", got.Lookahead)
	}
}

func TestBias_AllSignalsFutureDataIsHopeless(t *testing.T) {
	got := ValidateBias(BiasInput{
		FutureDataSignals: 50,
		TotalSignals:      50,
	})

	// 全部信号都用未来数据：不给 0，但必须贴地板。
	if got.Lookahead > 0.05 {
		t.Fatalf("全部信号前视，概率应贴地板（%.2f），实际 %.3f", BiasProbFloor, got.Lookahead)
	}
	if got.Lookahead <= 0 {
		t.Fatal("概率不给 0 —— 那是确定性判断，不是概率估计")
	}
}

func TestBias_NegativeLagMeansFutureData(t *testing.T) {
	got := ValidateBias(BiasInput{
		DataLagKnown: true,
		DataLagDays:  -30,
	})

	if !hasBiasChallenge(got, SeverityBlocking) {
		t.Fatalf("数据可比所属期间还早 30 天，必须是 blocking：%+v", got.Challenges)
	}
	if got.Lookahead > 0.05 {
		t.Fatalf("负滞后 = 未来数据，概率应贴地板，实际 %.3f", got.Lookahead)
	}
}

// 自相矛盾的情形：声称做过 PIT，但所有数据零滞后。
// 财报不可能在报告期当天披露 —— 这两件事不能同时为真。
func TestBias_PITClaimedButZeroLagIsSuspect(t *testing.T) {
	got := ValidateBias(BiasInput{
		PITVerified:  true,
		DataLagKnown: true,
		DataLagDays:  0,
	})

	if !hasBiasChallenge(got, SeverityWarning) {
		t.Fatalf("声称 PIT 却零滞后，应给出 warning：%+v", got.Challenges)
	}
	if got.Lookahead > 0.6 {
		t.Fatalf("不该因为自称做过 PIT 就给高分，实际 %.3f", got.Lookahead)
	}
}

func TestBias_PITVerifiedWithRealisticLagScoresHigh(t *testing.T) {
	got := ValidateBias(BiasInput{
		PITVerified:  true,
		DataLagKnown: true,
		DataLagDays:  45,
	})

	if got.Lookahead < 0.8 {
		t.Fatalf("做过 PIT 校验且滞后 45 天（接近真实披露节奏）应给高分，实际 %.3f", got.Lookahead)
	}
	// 也不给满分 —— 校验本身可能有漏。
	if got.Lookahead > 0.95 {
		t.Fatalf("不给满分：PIT 校验也可能有漏，实际 %.3f", got.Lookahead)
	}
}

func TestBias_CurrentPoolIsSurvivorshipBias(t *testing.T) {
	got := ValidateBias(BiasInput{
		PoolSource: PoolSourceCurrent,
		PoolSize:   300,
	})

	if !hasBiasChallenge(got, SeverityBlocking) {
		t.Fatalf("按今天名单回测历史必须是 blocking：%+v", got.Challenges)
	}
	if got.Survivorship > 0.05 {
		t.Fatalf("幸存者偏差应贴地板，实际 %.3f", got.Survivorship)
	}
}

// 池子来源未知时，用「池子里有没有退市票」反推。
func TestBias_NoDelistedInLargePoolIsSuspicious(t *testing.T) {
	got := ValidateBias(BiasInput{PoolSize: 300, DelistedInPool: 0})

	if !hasBiasChallenge(got, SeverityWarning) {
		t.Fatalf("300 只票一只退市股都没有，应给 warning：%+v", got.Challenges)
	}
	if got.Survivorship > 0.5 {
		t.Fatalf("可疑情形下不该给及格分，实际 %.3f", got.Survivorship)
	}
}

func TestBias_DelistedInPoolIsGoodSign(t *testing.T) {
	got := ValidateBias(BiasInput{PoolSize: 300, DelistedInPool: 7})

	if got.Survivorship < 0.7 {
		t.Fatalf("池子里有 7 只退市股是好迹象，应给高分，实际 %.3f", got.Survivorship)
	}
	if !hasBiasChallenge(got, SeverityNote) {
		t.Logf("（可选）应说明这是好迹象，实际质疑：%+v", got.Challenges)
	}
}

func TestBias_UnadjustedPricesAreBlocking(t *testing.T) {
	got := ValidateBias(BiasInput{
		PriceAdjustment:  AdjustNone,
		CorporateActions: 12,
	})

	if !hasBiasChallenge(got, SeverityBlocking) {
		t.Fatalf("未复权 + 12 次除权除息必须是 blocking：%+v", got.Challenges)
	}
	if got.Adjustment > 0.05 {
		t.Fatalf("未复权应贴地板，实际 %.3f", got.Adjustment)
	}
}

// 前复权不是"标准答案"：它的历史价格依赖未来的分红送股，
// 自带前视成分。这条 note 存在的意义是让人知道自己换来了什么。
func TestBias_PreAdjustedCarriesLookaheadComponent(t *testing.T) {
	got := ValidateBias(BiasInput{PriceAdjustment: AdjustPre})

	if got.Adjustment < 0.8 {
		t.Fatalf("前复权是回测常见做法，不应给低分，实际 %.3f", got.Adjustment)
	}
	if !hasBiasChallenge(got, SeverityNote) {
		t.Fatalf("必须提醒前复权自带前视成分，实际质疑：%+v", got.Challenges)
	}
}

// 合取性：一维致命，另两维再干净也不能把综合概率拉起来。
// 这正是用几何平均而不是算术平均的原因。
func TestBias_OneFatalDimensionDragsTheWholeThingDown(t *testing.T) {
	got := ValidateBias(BiasInput{
		PITVerified:      true,
		DataLagKnown:     true,
		DataLagDays:      60,
		PoolSource:       PoolSourcePointInTime,
		PriceAdjustment:  AdjustNone, // 致命这一维
		CorporateActions: 5,
	})

	if got.Assessed != 3 {
		t.Fatalf("三个子维度都应已评估，实际 %d", got.Assessed)
	}
	// 前视 0.9、幸存者 0.9、复权 0.02：算术平均 = 0.61（看起来还行），
	// 几何平均 = 0.25（正确的合取判断）。
	if got.Probability > 0.35 {
		t.Fatalf("一维致命时综合概率不该被另两维拉起来，实际 %.3f", got.Probability)
	}
	if got.Probability < 0.02 {
		t.Fatalf("也不该跌破地板，实际 %.3f", got.Probability)
	}
}

func TestBias_ProbabilityStaysInsideBounds(t *testing.T) {
	cases := []BiasInput{
		{},
		{FutureDataSignals: 1, TotalSignals: 1},
		{DataLagKnown: true, DataLagDays: -100},
		{PoolSource: PoolSourceCurrent, PoolSize: 10},
		{PriceAdjustment: AdjustMixed},
		{PITVerified: true, DataLagKnown: true, DataLagDays: 90,
			PoolSource: PoolSourcePointInTime, PriceAdjustment: AdjustPost},
	}
	for i, in := range cases {
		got := ValidateBias(in)
		if got.Probability < BiasProbFloor || got.Probability > BiasProbCeil {
			t.Fatalf("case %d：综合概率 %.3f 越界 [%g, %g]",
				i, got.Probability, BiasProbFloor, BiasProbCeil)
		}
	}
}
