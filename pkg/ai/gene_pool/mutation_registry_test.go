// K3b：基因池变异算子候选名单与 DSL 注册表的一致性护栏。
//
// 病根（K3 切片 2 审查发现 K3b）：算子名合法集合的单一事实源是
// pkg/expression 的注册表，但 AI 侧多处**写死算子名**：
//   - gene_pool/mutation.go 的 wrapCandidates / unwrapCandidates（本文件守）
//   - ai/yaml/generator.go 的表达式模板（由 generator_gate_test.go 守）
//   - ai/agents 提示词里的示例表达式（示例性质，危害较小，登记未覆盖）
//
// 危害：注册表改名 / 删名后，这些地方会静默产出「过不了 DSL 闸门」的表达式
// ——闸门是 fail-closed 的，于是 AI 白跑一轮、报错还指向不相关的位置。
//
// 修法（本文件）：把两处局部 slice 提成包级变量（行为一字不变），并断言
// 「名单 ⊆ AvailableOperators()」。注册表改名即红。
package gene_pool

import (
	"testing"

	"github.com/ruoxizhnya/quant-trading/pkg/expression"
)

// TestMutationCandidatesAreRegisteredOperators 是 K3b 的名单对齐护栏：
// 变异候选算子名必须都在注册表里，否则变异会产出过不了闸门的表达式。
func TestMutationCandidatesAreRegisteredOperators(t *testing.T) {
	registered := make(map[string]bool, 32)
	for _, op := range expression.AvailableOperators() {
		registered[op] = true
	}

	check := func(label string, names []string) {
		if len(names) == 0 {
			t.Errorf("%s 名单为空（测试夹具过期或被误删）", label)
			return
		}
		for _, name := range names {
			if !registered[name] {
				t.Errorf("%s 的候选算子 %q 不在 DSL 注册表——K3b 漂移：它会产出过不了闸门的表达式。"+
					"修法：改成注册表现有的算子名（可用清单见 expression.AvailableOperators()）",
					label, name)
			}
		}
	}
	check("wrapCandidates", wrapCandidates)
	check("unwrapCandidates", unwrapCandidates)
}

// TestMutationCandidatesProduceGatedExpressions 是行为腿：用候选算子包裹一条
// 合法表达式后，结果仍必须能过闸门（光名单在注册表里还不够——包裹后的语法
// 也要成立）。
func TestMutationCandidatesProduceGatedExpressions(t *testing.T) {
	base := "cs_rank(ts_pct_change(close, 20)) > 0.8"
	for _, fn := range wrapCandidates {
		wrapped := fn + "(" + base + ")"
		expr, err := expression.NewParser().Parse(wrapped)
		if err != nil {
			t.Errorf("候选算子 %q 包裹后无法解析 %q: %v", fn, wrapped, err)
			continue
		}
		if err := expr.Validate(); err != nil {
			t.Errorf("候选算子 %q 包裹后过不了闸门 %q: %v", fn, wrapped, err)
		}
	}
}
