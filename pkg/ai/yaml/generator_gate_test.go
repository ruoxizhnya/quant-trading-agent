// K3b：YAML/意图生成器的产出必须能过 DSL 闸门。
//
// 病根（K3 切片 2 审查发现 K3b）：`defaultSignalExpression` 用**字符串模板**
// 拼表达式（如 `cs_rank(ts_pct_change(close, %d)) > 0.8`），算子名写死在模板
// 里。注册表改名 / 删名后，这些模板会静默产出「过不了 DSL 闸门」的表达式
// ——闸门 fail-closed，AI 白跑一轮且报错指向不相关位置。
//
// ─── 修法裁决：把护栏扩到「产出侧」，而不是改模板结构 ──────────────
// 台账原建议是「生成器改查 AvailableOperators()」。但这里的算子名是**完整
// 表达式模板**的一部分（含嵌套结构与参数关系），不可能从一张扁平算子清单
// 派生——硬改等于为了护栏重写业务。真正的危害是「产出过不了闸门」，所以
// 护栏就钉在这一层：所有模板产出必须 Parse + Validate 通过。这既覆盖算子
// 名漂移，也顺带覆盖参数个数、字段、标量位（K3a）等一切闸门约束。
package yaml

import (
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/intent"
	"github.com/ruoxizhnya/quant-trading/pkg/expression"
)

// TestDefaultSignalExpressionPassesGate 遍历全部策略类型 × 若干回看窗口，
// 断言生成器产出的表达式都能过闸门。
func TestDefaultSignalExpressionPassesGate(t *testing.T) {
	types := []intent.StrategyType{
		intent.StrategyTypeMomentum,
		intent.StrategyTypeMeanReversion,
		intent.StrategyTypeTrendFollowing,
		intent.StrategyTypeBreakout,
		intent.StrategyTypeMultiFactor,
		intent.StrategyTypeValue,
		intent.StrategyTypeQuality,
	}
	lookbacks := []int{5, 10, 20, 60}

	for _, st := range types {
		for _, lb := range lookbacks {
			i := &intent.Intent{
				StrategyType: st,
				Parameters:   []intent.Parameter{{Name: "lookback_days", Value: lb}},
			}
			formula, ok := defaultSignalExpression(i)
			if !ok {
				t.Errorf("策略类型 %q 未产出表达式（预期除 custom 外都应给出）", st)
				continue
			}
			expr, err := expression.NewParser().Parse(formula)
			if err != nil {
				t.Errorf("策略类型 %q (lb=%d) 产出无法解析的表达式 %q: %v", st, lb, formula, err)
				continue
			}
			if err := expr.Validate(); err != nil {
				t.Errorf("策略类型 %q (lb=%d) 产出的表达式过不了闸门 %q: %v", st, lb, formula, err)
			}
		}
	}
}

// TestDefaultSignalExpressionCustomGivesNone custom 类型应诚实返回 ok=false
// （给不出表达式就不给，绝不糊一个默认值）——反证腿，防止有人「为了全绿」
// 给 custom 塞一个默认值。
func TestDefaultSignalExpressionCustomGivesNone(t *testing.T) {
	formula, ok := defaultSignalExpression(&intent.Intent{StrategyType: intent.StrategyTypeCustom})
	if ok {
		t.Errorf("custom 类型不应产出表达式，got %q", formula)
	}
	if formula != "" {
		t.Errorf("custom 类型应返回空字符串，got %q", formula)
	}
}

// TestDefaultSignalExpressionUsesRegisteredOperatorsOnly 是算子名层面的双重
// 保险：抽出产出表达式里所有函数调用名，断言都在注册表中。
// （TestDefaultSignalExpressionPassesGate 已通过闸门覆盖此点，但本测试在
// 注册表改名时给出的报错更直接——点名是哪个算子不在注册表。）
func TestDefaultSignalExpressionUsesRegisteredOperatorsOnly(t *testing.T) {
	registered := make(map[string]bool, 32)
	for _, op := range expression.AvailableOperators() {
		registered[op] = true
	}

	types := []intent.StrategyType{
		intent.StrategyTypeMomentum, intent.StrategyTypeMeanReversion,
		intent.StrategyTypeTrendFollowing, intent.StrategyTypeBreakout,
		intent.StrategyTypeMultiFactor, intent.StrategyTypeValue,
		intent.StrategyTypeQuality,
	}
	for _, st := range types {
		formula, ok := defaultSignalExpression(&intent.Intent{StrategyType: st})
		if !ok {
			continue
		}
		for _, name := range extractCalledNames(formula) {
			if !registered[name] {
				t.Errorf("策略类型 %q 的模板用了未注册的算子 %q（表达式 %q）", st, name, formula)
			}
		}
	}
}

// extractCalledNames 从表达式里抽出所有 `name(` 形式的调用名。
// 只做词法抽取（够用即可）：不解析语法，因为本测试只关心「名字是否在注册表」。
func extractCalledNames(formula string) []string {
	var out []string
	seen := make(map[string]bool)
	for i := 0; i < len(formula); i++ {
		if formula[i] != '(' {
			continue
		}
		// 回退取标识符（字母 / 下划线 / 数字）。
		j := i
		for j > 0 && (formula[j-1] == '_' ||
			(formula[j-1] >= 'a' && formula[j-1] <= 'z') ||
			(formula[j-1] >= '0' && formula[j-1] <= '9')) {
			j--
		}
		if j < i {
			name := formula[j:i]
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}
