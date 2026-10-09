package builtin

import (
	"context"
	"testing"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 假 store：只记下写过什么，读的时候从内存 map 里取。
type fakeHypothesisStore struct {
	byName map[domain.FactorType]domain.FactorHypothesis
	saved  []domain.FactorHypothesis
}

func newFakeHypothesisStore() *fakeHypothesisStore {
	return &fakeHypothesisStore{byName: map[domain.FactorType]domain.FactorHypothesis{}}
}

func (f *fakeHypothesisStore) GetFactorHypothesis(_ context.Context, name domain.FactorType) (*domain.FactorHypothesis, error) {
	if h, ok := f.byName[name]; ok {
		return &h, nil
	}
	return nil, nil
}

func (f *fakeHypothesisStore) ListFactorHypotheses(_ context.Context) ([]domain.FactorHypothesis, error) {
	out := make([]domain.FactorHypothesis, 0, len(f.byName))
	for _, h := range f.byName {
		out = append(out, h)
	}
	return out, nil
}

func (f *fakeHypothesisStore) SaveFactorHypothesis(_ context.Context, h domain.FactorHypothesis) error {
	f.byName[h.FactorName] = h
	f.saved = append(f.saved, h)
	return nil
}

// P2-3 的核心：采用一个因子之前，先能问出「它凭什么有效」。
func TestFactorHypothesisTool_LooksupBuiltinFactor(t *testing.T) {
	tool := NewFactorHypothesisTool(newFakeHypothesisStore())
	require.Equal(t, "factor.hypothesis", tool.Name())

	got, err := tool.Execute(context.Background(), map[string]interface{}{"factor_name": "momentum"})
	require.NoError(t, err)

	res, ok := got.(*factorHypothesisResult)
	require.True(t, ok, "返回类型应为 *factorHypothesisResult")
	assert.True(t, res.KnownFactor)
	assert.Equal(t, domain.FactorSourceLiterature, res.SourceKind)
	assert.NotEmpty(t, res.Hypothesis, "必须给出机制陈述，空假设等于没记录")
	assert.Contains(t, res.Reference, "Jegadeesh")
}

// 产业链因子（桥 B1）的来源必须标成 supply_chain，不能混在文献里 ——
// 这两类的可信度论证方式不同。
func TestFactorHypothesisTool_SupplyChainFactorHasSupplyChainSource(t *testing.T) {
	tool := NewFactorHypothesisTool(newFakeHypothesisStore())

	got, err := tool.Execute(context.Background(), map[string]interface{}{"factor_name": "gross_margin_trend"})
	require.NoError(t, err)

	res := got.(*factorHypothesisResult)
	assert.Equal(t, domain.FactorSourceSupplyChain, res.SourceKind)
	assert.Contains(t, res.Reference, "ADR-022")
}

// recorded 必须区分「库里记过」和「回退到内置表」—— 两者可信度不同。
func TestFactorHypothesisTool_RecordedFlagDistinguishesDbFromBuiltin(t *testing.T) {
	store := newFakeHypothesisStore()
	tool := NewFactorHypothesisTool(store)

	got, _ := tool.Execute(context.Background(), map[string]interface{}{"factor_name": "value"})
	assert.False(t, got.(*factorHypothesisResult).Recorded, "库里没记过就该是 false")

	require.NoError(t, store.SaveFactorHypothesis(context.Background(), domain.FactorHypothesis{
		FactorName: domain.FactorValue,
		SourceKind: domain.FactorSourceAI,
		Hypothesis: "AI 提出：低估值叠加分析师上修时超额收益更明显。",
	}))

	got, _ = tool.Execute(context.Background(), map[string]interface{}{"factor_name": "value"})
	res := got.(*factorHypothesisResult)
	assert.True(t, res.Recorded)
	assert.Equal(t, domain.FactorSourceAI, res.SourceKind, "库里的记录优先于内置表")
}

// 因子名拼错不能让整个调用炸掉，但必须说清楚"不知道这个因子"。
func TestFactorHypothesisTool_UnknownFactorIsReportedNotFatal(t *testing.T) {
	tool := NewFactorHypothesisTool(newFakeHypothesisStore())

	got, err := tool.Execute(context.Background(), map[string]interface{}{"factor_name": "not_a_factor"})
	require.NoError(t, err, "拼错名字不该报错 —— 但要把 known_factor 置 false")

	res := got.(*factorHypothesisResult)
	assert.False(t, res.KnownFactor)
	assert.NotEmpty(t, res.Hypothesis, "要给出可操作的提示，而不是静默返回空")
}

// 没有 DB 时工具仍可用（回退内置表）—— 不能因为没连库就让整个能力消失。
func TestFactorHypothesisTool_WorksWithoutStore(t *testing.T) {
	tool := NewFactorHypothesisTool(nil)

	got, err := tool.Execute(context.Background(), map[string]interface{}{"factor_name": "quality"})
	require.NoError(t, err)
	assert.NotEmpty(t, got.(*factorHypothesisResult).Hypothesis)
}

func TestFactorHypothesisTool_ListAllIncludesBuiltins(t *testing.T) {
	tool := NewFactorHypothesisTool(newFakeHypothesisStore())

	got, err := tool.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)

	list := got.(*factorHypothesisListResult)
	assert.Equal(t, len(domain.BuiltinFactorHypotheses), list.Count)
	assert.NotEmpty(t, list.Unrecorded, "内置但没落库的要列出来，提醒「默认值不是数据」")
	for _, h := range list.Hypotheses {
		assert.NotEmpty(t, h.Hypothesis, "每个因子都必须有机制陈述")
		assert.Contains(t,
			[]string{domain.FactorSourceLiterature, domain.FactorSourceSupplyChain,
				domain.FactorSourceAI, domain.FactorSourceAdHoc},
			h.SourceKind)
	}
}

// 内置假设表本身的质量：不能为空，不能有重复，不能缺 source_kind。
// 这张表是"因子凭什么有效"的唯一内置依据，空着就等于没做 P2-3。
func TestBuiltinFactorHypotheses_AreComplete(t *testing.T) {
	require.NotEmpty(t, domain.BuiltinFactorHypotheses)

	seen := map[domain.FactorType]bool{}
	for _, h := range domain.BuiltinFactorHypotheses {
		assert.False(t, seen[h.FactorName], "重复的因子条目：%s", h.FactorName)
		seen[h.FactorName] = true

		assert.NotEmpty(t, h.Hypothesis, "%s 缺机制陈述", h.FactorName)
		assert.NotEmpty(t, h.SourceKind, "%s 缺来源种类", h.FactorName)
		_, known := domain.ParseFactorType(string(h.FactorName))
		assert.True(t, known, "%s 不是可识别的因子名", h.FactorName)
	}

	// 每个内置因子都得有条目 —— 少一个就是"这个因子的来源没人说得清"。
	for _, ft := range []domain.FactorType{
		domain.FactorMomentum, domain.FactorValue, domain.FactorQuality,
		domain.FactorSize, domain.FactorVolatility, domain.FactorGrowth,
		domain.FactorGrossMarginTrend, domain.FactorContractLiabilityRatio,
		domain.FactorOCFToNetProfit, domain.FactorROEDuPontLeverage,
		domain.FactorInventoryTurnoverDelta,
	} {
		_, ok := domain.BuiltinHypothesisFor(ft)
		assert.True(t, ok, "内置因子 %s 缺少假设来源", ft)
	}
}
