package validation

import (
	"math"
	"testing"
)

func hasRedundancyChallenge(r RedundancyResult, severity string) bool {
	for _, c := range r.Challenges {
		if c.Dimension == DimensionRedundancy && c.Severity == severity {
			return true
		}
	}
	return false
}

// 确定性伪随机，避免测试自己引入非确定性（P1-14 的教训）。
func series(n int, seed int64) []float64 {
	x := seed
	out := make([]float64, n)
	for i := range out {
		x = (x*1103515245 + 12345) % 2147483648
		out[i] = float64(x)/2147483648.0*0.04 - 0.02
	}
	return out
}

// 这一维存在的理由：分散的是风险来源，不是策略数量。
// 8 条策略如果都是同一个动量思想换参数，跌的时候一起跌。

func TestRedundancy_IdenticalSeriesIsNearDuplicate(t *testing.T) {
	s := series(300, 1)
	got := ValidateRedundancy(RedundancyInput{
		Candidate: s,
		Existing:  []NamedReturns{{Name: "same", Returns: s}},
	})

	if got.MaxAbsCorr < 0.999 {
		t.Fatalf("和完全相同的序列比，|ρ| 应接近 1，实际 %.4f", got.MaxAbsCorr)
	}
	if !hasRedundancyChallenge(got, SeverityBlocking) {
		t.Fatalf("|ρ|≈1 必须是 blocking：%+v", got.Challenges)
	}
	if got.Probability > 0.1 {
		t.Fatalf("完全冗余不该给有信心的概率，实际 %.3f", got.Probability)
	}
}

func TestRedundancy_IndependentSeriesScoresHigh(t *testing.T) {
	got := ValidateRedundancy(RedundancyInput{
		Candidate: series(300, 1),
		Existing:  []NamedReturns{{Name: "other", Returns: series(300, 7777)}},
	})

	if got.MaxAbsCorr > 0.2 {
		t.Fatalf("两条独立序列不该高相关，实际 %.3f", got.MaxAbsCorr)
	}
	if got.Probability < 0.8 {
		t.Fatalf("明显不相关且样本充足（n=%d）应给高分，实际 %.3f", got.SampleSize, got.Probability)
	}
}

// 强负相关是「反向复制」：|ρ| 高说明信息不独立，
// 但方向相反 —— 说清楚是哪一种，别让人误读成"很分散"。
func TestRedundancy_NegativeCorrelationIsFlagged(t *testing.T) {
	s := series(300, 42)
	inv := make([]float64, len(s))
	for i := range s {
		inv[i] = -s[i]
	}

	got := ValidateRedundancy(RedundancyInput{
		Candidate: s,
		Existing:  []NamedReturns{{Name: "inverse", Returns: inv}},
	})

	if got.MaxCorrSign != -1 {
		t.Fatalf("应识别出是负相关，实际 sign=%d", got.MaxCorrSign)
	}
	if got.MaxAbsCorr < 0.999 {
		t.Fatalf("取反后的 |ρ| 应接近 1，实际 %.4f", got.MaxAbsCorr)
	}
	if !hasRedundancyChallenge(got, SeverityBlocking) {
		t.Fatalf("反向复制也该是 blocking：%+v", got.Challenges)
	}
	// 必须有一条专门讲方向的质疑，否则容易被读成"负相关=很分散"。
	found := false
	for _, c := range got.Challenges {
		if c.Dimension == DimensionRedundancy && c.Severity == SeverityWarning &&
			contains(c.Message, "负") {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺少「方向：负相关」的提醒，实际质疑：%+v", got.Challenges)
	}
}

// 零方差序列（全程空仓）的相关系数无定义。
// 绝不能当 0 —— 那会伪装成「完全不相关」。
func TestRedundancy_ZeroVarianceIsSkippedNotZero(t *testing.T) {
	flat := make([]float64, 300)
	got := ValidateRedundancy(RedundancyInput{
		Candidate: series(300, 5),
		Existing:  []NamedReturns{{Name: "always-flat", Returns: flat}},
	})

	if got.Compared != 0 {
		t.Fatalf("零方差序列不该参与比较，实际 compared=%d", got.Compared)
	}
	if len(got.Skipped) != 1 || got.Skipped[0] != "always-flat" {
		t.Fatalf("应记为跳过，实际 %v", got.Skipped)
	}
	if got.MaxAbsCorr != 0 {
		t.Fatalf("没有可比对象时 MaxAbsCorr 应为 0（未评估），实际 %.3f", got.MaxAbsCorr)
	}
	if got.Probability != 0.5 {
		t.Fatalf("无可比对象应给中性 0.5，实际 %.3f", got.Probability)
	}
	found := false
	for _, c := range got.Challenges {
		if c.Dimension == DimensionRedundancy && c.Severity == SeverityNote &&
			contains(c.Message, "跳过不等于不相关") {
			found = true
		}
	}
	if !found {
		t.Fatalf("必须明说「跳过不等于不相关」，实际质疑：%+v", got.Challenges)
	}
}

func TestRedundancy_NoExistingIsNeutralNotPassing(t *testing.T) {
	got := ValidateRedundancy(RedundancyInput{Candidate: series(300, 9)})

	if got.Probability != 0.5 {
		t.Fatalf("没有已有策略可比时应是中性 0.5，实际 %.3f", got.Probability)
	}
	if !hasRedundancyChallenge(got, SeverityNote) {
		t.Fatal("未评估必须明说，不能静默")
	}
}

// 样本不足时，相关系数本身就不该被当真 ——
// 概率要向 0.5 收缩，而不是照着 1-|ρ| 给出自信判断。
func TestRedundancy_ShortSampleShrinksTowardNeutral(t *testing.T) {
	short := ValidateRedundancy(RedundancyInput{
		Candidate: series(20, 3)[:20],
		Existing:  []NamedReturns{{Name: "other", Returns: series(20, 8888)[:20]}},
	})
	long := ValidateRedundancy(RedundancyInput{
		Candidate: series(500, 3),
		Existing:  []NamedReturns{{Name: "other", Returns: series(500, 8888)}},
	})

	if !hasRedundancyChallenge(short, SeverityWarning) {
		t.Fatalf("样本 %d 不足应给 warning：%+v", short.SampleSize, short.Challenges)
	}
	if math.Abs(short.Probability-0.5) >= math.Abs(long.Probability-0.5) {
		t.Fatalf("短样本的概率应更靠近中性 0.5：short=%.3f(n=%d) long=%.3f(n=%d)",
			short.Probability, short.SampleSize, long.Probability, long.SampleSize)
	}
	if short.CorrStdErr < 0.15 {
		t.Fatalf("n=20 的相关标准误应约 0.24，实际 %.3f", short.CorrStdErr)
	}
}

// 长度不一致时截断到较短那条，而不是直接放弃。
func TestRedundancy_UnequalLengthAligns(t *testing.T) {
	got := ValidateRedundancy(RedundancyInput{
		Candidate: series(300, 11),
		Existing:  []NamedReturns{{Name: "shorter", Returns: series(120, 11)}},
	})

	if got.Compared != 1 {
		t.Fatalf("长度不同也应能比（截断到较短），实际 compared=%d", got.Compared)
	}
	if got.SampleSize != 120 {
		t.Fatalf("应对齐到较短那条的长度 120，实际 %d", got.SampleSize)
	}
}

// 多条已有策略都高度相关 = 不是在加策略，是在加杠杆。
func TestRedundancy_ManyRedundantPeers(t *testing.T) {
	s := series(300, 21)
	mk := func(seed int64) []float64 {
		base := series(300, seed)
		out := make([]float64, len(s))
		for i := range s {
			out[i] = 0.9*s[i] + 0.1*base[i]
		}
		return out
	}
	got := ValidateRedundancy(RedundancyInput{
		Candidate: s,
		Existing: []NamedReturns{
			{Name: "a", Returns: mk(101)},
			{Name: "b", Returns: mk(202)},
			{Name: "c", Returns: mk(303)},
		},
	})

	if got.Redundant < 3 {
		t.Fatalf("三条都应被判高度相关，实际 %d", got.Redundant)
	}
	if got.Probability > 0.2 {
		t.Fatalf("一堆高度相关的同类，概率应很低，实际 %.3f", got.Probability)
	}
}

func TestRedundancy_ProbabilityStaysInsideBounds(t *testing.T) {
	cases := []RedundancyInput{
		{Candidate: series(300, 1), Existing: []NamedReturns{{Name: "same", Returns: series(300, 1)}}},
		{Candidate: series(300, 1)},
		{Candidate: nil},
		{Candidate: make([]float64, 5)},
		{Candidate: series(300, 1), Existing: []NamedReturns{{Name: "flat", Returns: make([]float64, 300)}}},
	}
	for i, in := range cases {
		got := ValidateRedundancy(in)
		if got.Probability < 0 || got.Probability > 1 {
			t.Fatalf("case %d：概率 %.3f 越界", i, got.Probability)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
