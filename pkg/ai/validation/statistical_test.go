package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P2-9 第一片：统计校验器（多重检验校正）。
//
// PRODUCT 把它排在验证器六维之首，理由写在 §验证器：
// 「试 5 次撞出来的和试 500 次撞出来的，同样结果，可信度差一个量级」。
// 这条正好吃 P1-1 建起来的实验日志 —— 没有「试了多少次」这个数字，
// 这一维根本无从计算。

func TestStatistical_SameSharpeMoreTrialsIsLessCredible(t *testing.T) {
	base := StatisticalInput{Sharpe: 0.8, NumPeriods: 252, NumTrials: 5}
	many := StatisticalInput{Sharpe: 0.8, NumPeriods: 252, NumTrials: 500}

	gotFew := ValidateStatistical(base)
	gotMany := ValidateStatistical(many)

	assert.Greater(t, gotFew.Probability, gotMany.Probability,
		"同样的 Sharpe，试 500 次撞出来的必须比试 5 次的更不可信")
	assert.Greater(t, gotMany.AdjustedPValue, gotFew.AdjustedPValue,
		"试得越多，校正后的 p 值越大（越不显著）")
}

func TestStatistical_SingleTrialKeepsRawPValue(t *testing.T) {
	got := ValidateStatistical(StatisticalInput{Sharpe: 1.0, NumPeriods: 252, NumTrials: 1})

	// 只试一次时，校正不该改变任何东西 —— 没有选择偏差可言。
	assert.InDelta(t, got.RawPValue, got.AdjustedPValue, 1e-12)
}

func TestStatistical_NormalCDFMonotonic(t *testing.T) {
	// 正态 CDF 是其它计算的地基，先锁住它的形状。
	assert.InDelta(t, 0.5, normalCDF(0), 1e-9)
	assert.InDelta(t, 0.8413, normalCDF(1), 1e-3)
	assert.InDelta(t, 0.9772, normalCDF(2), 1e-3)
	assert.Greater(t, normalCDF(1), normalCDF(-1))
}

func TestStatistical_SignificantWhenStrongAndFewTrials(t *testing.T) {
	got := ValidateStatistical(StatisticalInput{Sharpe: 2.5, NumPeriods: 756, NumTrials: 3})

	assert.True(t, got.Significant, "三年数据上 Sharpe 2.5、只试了 3 次，该算显著")
	assert.Greater(t, got.Probability, 0.9)
	assert.Empty(t, blockingChallenges(got.Challenges))
}

func TestStatistical_ChallengesWhenMarginalAndManyTrials(t *testing.T) {
	// 一年数据上 Sharpe 0.5 本来就不显著（单次 p≈0.62），却试了 200 次 ——
	// 典型的选择偏差场景：结论完全可能只是从噪声里挑出来的那个。
	got := ValidateStatistical(StatisticalInput{Sharpe: 0.5, NumPeriods: 252, NumTrials: 200})

	assert.False(t, got.Significant, "试 200 次后的边际结果不该算显著")
	require.NotEmpty(t, got.Challenges, "必须给出质疑，而不是默默判个不通过")

	found := false
	for _, c := range got.Challenges {
		if c.Severity == SeverityBlocking {
			found = true
			assert.Contains(t, c.Message, "200", "质疑里要点明试了多少次")
		}
	}
	assert.True(t, found, "这种情况要有 blocking 级质疑")
}

func TestStatistical_FailedTrialsCountToo(t *testing.T) {
	// 失败也是尝试 —— 只有把失败算进去，「试了多少次」才是真的。
	withFailures := StatisticalInput{Sharpe: 0.8, NumPeriods: 252, NumTrials: 100, FailedTrials: 80}
	without := StatisticalInput{Sharpe: 0.8, NumPeriods: 252, NumTrials: 20}

	assert.Less(t, ValidateStatistical(withFailures).Probability,
		ValidateStatistical(without).Probability)
}

func TestStatistical_RejectsBadInput(t *testing.T) {
	// 输入不合法时不能给出看起来正常的数字 —— 那比报错危险得多。
	got := ValidateStatistical(StatisticalInput{Sharpe: 1.0, NumPeriods: 0, NumTrials: 10})
	assert.False(t, got.Significant)
	require.NotEmpty(t, got.Challenges)
	assert.Equal(t, SeverityBlocking, got.Challenges[0].Severity)

	got2 := ValidateStatistical(StatisticalInput{Sharpe: 1.0, NumPeriods: 100, NumTrials: 0})
	assert.False(t, got2.Significant)
	require.NotEmpty(t, got2.Challenges)
}

func blockingChallenges(cs []Challenge) []Challenge {
	var out []Challenge
	for _, c := range cs {
		if c.Severity == SeverityBlocking {
			out = append(out, c)
		}
	}
	return out
}
