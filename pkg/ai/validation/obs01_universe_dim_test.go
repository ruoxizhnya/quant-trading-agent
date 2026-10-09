package validation

import "testing"

// OBS-01：验证器新增的「票池非空」维。
//
// 正证据：空票池 ⇒ 该维进 Dimensions 且概率 0（一票否决）。
// 反证腿：票池非空 ⇒ 该维不进 map（不把正常回测打成低分）。

func TestAggregate_EmptyUniverseIsScoredZero(t *testing.T) {
	res := resultFixture(300, 1.0, 40)
	res.UniverseMaxSize = intPtr(0) // 空票池（明确为空）

	v := ValidateProposal(Proposal{
		Result:    res,
		NumTrials: 3,
		Bias: BiasInput{
			PITVerified:     true,
			DataLagKnown:    true,
			DataLagDays:     45,
			PoolSource:      PoolSourcePointInTime,
			PriceAdjustment: AdjustPost,
		},
	})

	p, ok := v.Dimensions[DimensionUniverse]
	if !ok {
		t.Fatalf("空票池必须让「票池」维进 Dimensions，实际 %v（未评估：%v）",
			v.Dimensions, v.Unassessed)
	}
	if p != 0 {
		t.Fatalf("空票池的该维概率必须是 0，实际 %.3f", p)
	}
	if v.Probability != 0 {
		t.Fatalf("空票池会被综合概率的 min 拉到 0，实际 %.3f", v.Probability)
	}
	if v.Weakest != DimensionUniverse {
		t.Fatalf("空票池时最弱维应是 %s，实际 %s", DimensionUniverse, v.Weakest)
	}

	sawBlocking := false
	for _, c := range v.Challenges {
		if c.Dimension == DimensionUniverse && c.Severity == SeverityBlocking {
			sawBlocking = true
		}
	}
	if !sawBlocking {
		t.Fatalf("空票池必须有 blocking 质疑，实际 %+v", v.Challenges)
	}
}

func TestAggregate_NonEmptyUniverseIsNotInDimensions(t *testing.T) {
	res := resultFixture(300, 1.0, 40) // UniverseMaxSize = 40（非空）

	v := ValidateProposal(Proposal{Result: res, NumTrials: 3})

	if _, ok := v.Dimensions[DimensionUniverse]; ok {
		t.Fatalf("票池非空时该维不该进 Dimensions（会无端压低正常回测），实际 %v",
			v.Dimensions)
	}
	if v.Probability <= 0 {
		t.Fatalf("正常回测的概率不该被这一维拉到 0，实际 %.3f", v.Probability)
	}
}

// TestAggregate_UnassessedUniverseIsNotInDimensions 是审查修复的护栏：
// nil（未评估）**不进 map** —— 与「未评估维度不出现在 map」同一语义；
// 把未评估当空票池会无端压低（或一票否决）别的生产路径的结果。
func TestAggregate_UnassessedUniverseIsNotInDimensions(t *testing.T) {
	res := resultFixture(300, 1.0, 40)
	res.UniverseMaxSize = nil // 未评估（别的生产路径没填）≠ 空票池

	v := ValidateProposal(Proposal{Result: res, NumTrials: 3})

	if _, ok := v.Dimensions[DimensionUniverse]; ok {
		t.Fatalf("未评估时该维不该进 Dimensions（nil 不等于 0），实际 %v", v.Dimensions)
	}
	if v.Probability <= 0 {
		t.Fatalf("正常回测的概率不该被这一维拉到 0，实际 %.3f", v.Probability)
	}
}
