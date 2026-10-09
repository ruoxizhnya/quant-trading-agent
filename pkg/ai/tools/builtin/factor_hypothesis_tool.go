package builtin

import (
	"context"
	"fmt"
	"sort"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// ═══════════════════════════════════════════════════════════════════════
//  FactorHypothesisTool (P2-3)
// ═══════════════════════════════════════════════════════════════════════
//
// 回答一个问题：**这个因子凭什么有效？**
//
// 因子的数值在 factor_cache 里，但数值本身不区分「有经济学机制」和
// 「在数据上试出来的相关性」。后者是过拟合的主要来源 —— 换个时间段就散，
// 而看 IC 是看不出来的。这个工具把因子的假设来源暴露给 AI 实验员，
// 让它在采用一个因子之前先问一句「它的机制是什么」。
//
// 不传 factor_name 时列出全部已记录的假设，用于「我们现在有哪些因子、
// 分别凭什么」这类全貌查询。
//
// Tool name: "factor.hypothesis"
// Input: factor_name (optional)
// Output: *factorHypothesisResult 或 *factorHypothesisListResult

// FactorHypothesisStore 是假设来源的读取/写入面（满足者为 *storage.PostgresStore）。
//
// 抽成窄接口是为了能用一个假的 store 单测，不必起数据库。
type FactorHypothesisStore interface {
	GetFactorHypothesis(ctx context.Context, name domain.FactorType) (*domain.FactorHypothesis, error)
	ListFactorHypotheses(ctx context.Context) ([]domain.FactorHypothesis, error)
	SaveFactorHypothesis(ctx context.Context, h domain.FactorHypothesis) error
}

// factorHypothesisResult 是单个因子的查询结果。
//
// Recorded 区分「库里记过」和「回退到内置表」—— 这两者对可信度不一样：
// 记过的可以被引用，回退的只是代码里的默认值，改动它要改代码。
type factorHypothesisResult struct {
	FactorName  string `json:"factor_name"`
	SourceKind  string `json:"source_kind"`
	Hypothesis  string `json:"hypothesis"`
	Reference   string `json:"reference,omitempty"`
	Recorded    bool   `json:"recorded"`     // true = 库里有记录；false = 回退到内置表
	IsBuiltin   bool   `json:"is_builtin"`   // 是否内置因子
	KnownFactor bool   `json:"known_factor"` // 因子名是否可识别
}

type factorHypothesisListResult struct {
	Count      int                      `json:"count"`
	Hypotheses []factorHypothesisResult `json:"hypotheses"`
	Unrecorded []string                 `json:"unrecorded_builtin,omitempty"`
	Note       string                   `json:"note,omitempty"`
}

// FactorHypothesisTool 查询因子的假设来源。
type FactorHypothesisTool struct {
	// store 可能为 nil（DB 没配）。那种情况下只用内置表 —— 工具仍然
	// 可用，只是查不到 AI 挖掘链路写进去的假设。
	//
	// 为什么允许 nil 而不是在接线处 panic：内置表不依赖数据库，让整个
	// 工具因为没连库就不可用，代价大于收益。
	store FactorHypothesisStore
}

var _ tools.Tool = (*FactorHypothesisTool)(nil)

func NewFactorHypothesisTool(store FactorHypothesisStore) *FactorHypothesisTool {
	return &FactorHypothesisTool{store: store}
}

func (t *FactorHypothesisTool) Name() string { return "factor.hypothesis" }

func (t *FactorHypothesisTool) Description() string {
	return "Look up WHY a factor is expected to work — its economic mechanism and source " +
		"(academic literature / supply-chain logic / AI hypothesis / ad-hoc). " +
		"Call this BEFORE adopting a factor: a factor with no stated mechanism may be " +
		"data-mined noise that decays out of sample. " +
		"Omit factor_name to list all recorded hypotheses plus builtin ones never recorded."
}

func (t *FactorHypothesisTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "factor_name",
			Type:        "string",
			Description: "Factor to look up (e.g. 'momentum', 'value', 'gross_margin_trend'). Omit to list all.",
			Required:    false,
		},
	}
}

func (t *FactorHypothesisTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Factor hypothesis: the mechanism, where it came from, and whether it was actually recorded.",
		Fields: []tools.OutputField{
			{Name: "factor_name", Type: "string", Description: "Factor identifier."},
			{Name: "source_kind", Type: "string", Description: "One of: literature, supply_chain, ai_hypothesis, ad_hoc."},
			{Name: "hypothesis", Type: "string", Description: "One-sentence mechanism: why this factor should earn excess return."},
			{Name: "reference", Type: "string", Description: "Citation or design document (may be empty)."},
			{Name: "recorded", Type: "bool", Description: "True if stored in DB; false if falling back to builtin defaults."},
			{Name: "known_factor", Type: "bool", Description: "False if the factor name is not recognized at all."},
		},
	}
}

// Execute 查询单个因子的假设；省略 factor_name 时列出全部。
//
// 找不到时**不报错**，而是返回 known_factor=false —— 因子名拼错和
// "这个因子还没记录"是两回事，但都不该让整个工具调用失败；调用方看到
// known_factor=false 就该意识到自己搞错了名字。
func (t *FactorHypothesisTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	name, err := optionalString(args, "factor_name")
	if err != nil {
		return nil, err
	}

	if name == "" {
		return t.listAll(ctx)
	}

	ft, known := domain.ParseFactorType(name)
	if !known {
		return &factorHypothesisResult{
			FactorName:  name,
			KnownFactor: false,
			Hypothesis:  "未知因子名 —— 无法查到假设来源。请确认拼写（可用 factor.hypothesis 不带参数列出全部）。",
		}, nil
	}

	var h *domain.FactorHypothesis
	if t.store != nil {
		h, err = t.store.GetFactorHypothesis(ctx, ft)
		if err != nil {
			return nil, fmt.Errorf("factor.hypothesis: lookup failed: %w", err)
		}
	}
	if h != nil {
		return &factorHypothesisResult{
			FactorName:  string(h.FactorName),
			SourceKind:  h.SourceKind,
			Hypothesis:  h.Hypothesis,
			Reference:   h.Reference,
			Recorded:    true,
			IsBuiltin:   isBuiltinFactor(ft),
			KnownFactor: true,
		}, nil
	}

	// 库里没记过，回退到内置表并如实标注。
	if b, ok := domain.BuiltinHypothesisFor(ft); ok {
		return &factorHypothesisResult{
			FactorName:  string(b.FactorName),
			SourceKind:  b.SourceKind,
			Hypothesis:  b.Hypothesis,
			Reference:   b.Reference,
			Recorded:    false,
			IsBuiltin:   true,
			KnownFactor: true,
		}, nil
	}

	return &factorHypothesisResult{
		FactorName:  string(ft),
		SourceKind:  domain.FactorSourceAdHoc,
		KnownFactor: true,
		Hypothesis:  "这个因子的假设来源还没记录。没有机制陈述的因子，别太当真。",
	}, nil
}

func (t *FactorHypothesisTool) listAll(ctx context.Context) (interface{}, error) {
	var stored []domain.FactorHypothesis
	if t.store != nil {
		var err error
		stored, err = t.store.ListFactorHypotheses(ctx)
		if err != nil {
			return nil, fmt.Errorf("factor.hypothesis: list failed: %w", err)
		}
	}

	byName := make(map[domain.FactorType]domain.FactorHypothesis, len(stored))
	for _, h := range stored {
		byName[h.FactorName] = h
	}

	out := make([]factorHypothesisResult, 0, len(domain.BuiltinFactorHypotheses)+len(stored))
	seen := make(map[domain.FactorType]bool, len(domain.BuiltinFactorHypotheses))

	// 先走内置表，保证顺序稳定（内置因子的顺序是有意义的）。
	for _, b := range domain.BuiltinFactorHypotheses {
		seen[b.FactorName] = true
		if h, ok := byName[b.FactorName]; ok {
			out = append(out, factorHypothesisResult{
				FactorName: string(h.FactorName), SourceKind: h.SourceKind,
				Hypothesis: h.Hypothesis, Reference: h.Reference,
				Recorded: true, IsBuiltin: true, KnownFactor: true,
			})
			continue
		}
		out = append(out, factorHypothesisResult{
			FactorName: string(b.FactorName), SourceKind: b.SourceKind,
			Hypothesis: b.Hypothesis, Reference: b.Reference,
			Recorded: false, IsBuiltin: true, KnownFactor: true,
		})
	}

	// 再补库里有、内置表里没有的（AI 挖掘链路写进去的）。
	for _, h := range stored {
		if seen[h.FactorName] {
			continue
		}
		out = append(out, factorHypothesisResult{
			FactorName: string(h.FactorName), SourceKind: h.SourceKind,
			Hypothesis: h.Hypothesis, Reference: h.Reference,
			Recorded: true, IsBuiltin: false, KnownFactor: true,
		})
	}

	// 内置但库里没记的：列出来，提醒"这些还没落库"。
	var unrecorded []string
	for _, b := range domain.BuiltinFactorHypotheses {
		if _, ok := byName[b.FactorName]; !ok {
			unrecorded = append(unrecorded, string(b.FactorName))
		}
	}
	sort.Strings(unrecorded)

	res := &factorHypothesisListResult{Count: len(out), Hypotheses: out}
	if len(unrecorded) > 0 {
		res.Unrecorded = unrecorded
		res.Note = "以下内置因子的假设还没落库，当前返回的是代码里的默认值 —— 改动它们要改代码，不是改数据。"
	}
	return res, nil
}

func isBuiltinFactor(ft domain.FactorType) bool {
	_, ok := domain.BuiltinHypothesisFor(ft)
	return ok
}
