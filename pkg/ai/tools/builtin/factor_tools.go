package builtin

import (
	"context"
	"fmt"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/client"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/expression"
)

// ─── ValidateFactorTool ────────────────────────────────────────────────
//
// L1 validation gate: checks that a factor expression parses successfully
// under the expression DSL grammar **and passes the operator/field gate**
// (`expression.Expression.Validate`). Does NOT compute values — that is
// the job of factor.compute / compute_factor_ic. Cheap (<1ms), so an
// agent should always call this before committing to a longer compute.
//
// OBS-06: parsing alone is not enough. `CROSS(MA(close,5), MA(close,20))`
// parses (both are just function-call syntax) but both operators are
// unknown. The gate is **fail-closed**: any operator name not in the
// registry, any unknown data field, any arity mismatch → valid=false.
//
// Tool name: "validate_factor"
// Input: expression (required)
// Output: { valid, inputs, ast, error, level, passed, reason, recommendation }
//
// The four `level/passed/reason/recommendation` fields constitute the
// GateDecision (see gate.go). They let Hermes uniformly check "did this
// gate pass?" without parsing tool-specific fields. For L1:
//   - level    = "L1"
//   - passed   = valid (parse + gate result)
//   - reason   = "passed" or "syntax_error"
//   - recommendation = LLM-facing actionable hint (Chinese)
//
// Note on error semantics: a parse failure OR a gate failure is NOT
// returned as a Go error from Execute — it's a successful validation
// result with valid=false. The agent reads the `valid` flag and decides
// whether to retry. Tool-level errors (missing args) are still returned
// as ErrInvalidArgs.
type ValidateFactorTool struct{}

var _ tools.Tool = (*ValidateFactorTool)(nil)

// NewValidateFactorTool constructs a ValidateFactorTool. No dependencies
// are injected because expression.Parser carries per-call mutable state
// (tokens, pos) and is not safe for concurrent use across goroutines.
// Instead, Execute creates a fresh Parser per call — cheap and safe.
func NewValidateFactorTool() *ValidateFactorTool {
	return &ValidateFactorTool{}
}

func (t *ValidateFactorTool) Name() string { return "validate_factor" }

func (t *ValidateFactorTool) Description() string {
	return "Validate a factor expression's syntax (L1 gate). Returns validity, required data inputs (e.g. close, volume), and the parsed AST. Does not compute values — call compute_factor_ic for that."
}

func (t *ValidateFactorTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "expression",
			Type:        "string",
			Description: "Factor DSL expression to validate, e.g. 'ts_rank(close, 20)' or '(close - ts_mean(close, 20)) / ts_std(close, 20)'.",
			Required:    true,
		},
	}
}

func (t *ValidateFactorTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Validation result. If valid is false, error explains the syntax problem. The level/passed/reason/recommendation fields are the L1 GateDecision — Hermes reads `passed` to decide whether to advance to L2.",
		Fields: []tools.OutputField{
			{Name: "valid", Type: "bool", Description: "true if the expression parses successfully."},
			{Name: "inputs", Type: "array", Description: "Required data fields (e.g. [\"close\", \"volume\"]). Empty if invalid."},
			{Name: "ast", Type: "string", Description: "String representation of the parsed AST. Empty if invalid."},
			{Name: "error", Type: "string", Description: "Parse error message. Empty if valid."},
			{Name: "level", Type: "string", Description: "Gate identifier: always \"L1\"."},
			{Name: "passed", Type: "bool", Description: "true if the gate passed (mirrors `valid`)."},
			{Name: "reason", Type: "string", Description: "Machine-readable reason code: \"passed\" or \"syntax_error\"."},
			{Name: "recommendation", Type: "string", Description: "LLM-facing actionable hint (Chinese)."},
		},
	}
}

func (t *ValidateFactorTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	expr, err := requireString(args, "expression")
	if err != nil {
		return nil, err
	}

	// Fresh parser per call: expression.Parser holds mutable tokens/pos
	// state on the receiver, so a shared instance would race across
	// concurrent HTTP requests. NewParser is allocation-cheap.
	parser := expression.NewParser()
	parsed, err := parser.Parse(expr)
	if err != nil {
		// Parse failure is a validation result, not a tool error. The
		// agent inspects valid=false and the error string to retry.
		passed := false
		return map[string]interface{}{
			"valid":          false,
			"inputs":         []string{},
			"ast":            "",
			"error":          err.Error(),
			"level":          "L1",
			"passed":         passed,
			"reason":         gateReasonL1(passed),
			"recommendation": l1FailureRecommendation(),
		}, nil
	}

	// OBS-06：Parse 成功 ≠ 合法。必须再跑语法 + 算子闸门
	// （expression.Expression.Validate）。fail-closed —— 未登记算子 /
	// 未知字段 / 参数个数不符，一律不通过。
	if err := parsed.Validate(); err != nil {
		passed := false
		return map[string]interface{}{
			"valid":          false,
			"inputs":         []string{},
			"ast":            "",
			"error":          err.Error(),
			"level":          "L1",
			"passed":         passed,
			"reason":         gateReasonL1(passed),
			"recommendation": l1FailureRecommendation(),
		}, nil
	}

	passed := true
	return map[string]interface{}{
		"valid":          true,
		"inputs":         parsed.Inputs,
		"ast":            parsed.AST.String(),
		"error":          "",
		"level":          "L1",
		"passed":         passed,
		"reason":         gateReasonL1(passed),
		"recommendation": gateRecommendationL1(passed),
	}, nil
}

// l1FailureRecommendation builds the L1 failure recommendation text with
// the DSL's legal field set appended (grouped by data source). This lets
// an agent that tripped the gate self-correct: it sees exactly which
// fields exist and which source each belongs to (OBS-08 切片 1, 3.6).
//
// 只改文本内容 —— JSON 输出字段结构（valid/inputs/ast/error/level/
// passed/reason/recommendation）保持不变。
func l1FailureRecommendation() string {
	return gateRecommendationL1(false) + " " + expression.AvailableFieldsHint()
}

// ─── ComputeFactorICTool ───────────────────────────────────────────────
//
// L2 validation gate: computes a factor expression's Information
// Coefficient (IC) and Information Ratio (IR) over a symbol list and
// date range. IC > 0.03 is generally useful; IC > 0.05 is strong.
//
// Tool name: "compute_factor_ic"
// Input: expression, symbols, start_date, end_date (all required)
// Output: *computeFactorICResult (embeds GateDecision + IC/IR metrics)
//
// GateDecision (L2):
//   - level    = "L2"
//   - passed   = (IC >= GateL2MinIC)
//   - reason   = "passed" or "low_ic"
//   - recommendation = LLM-facing actionable hint (Chinese)
//
// Design note (deviation from hermes-agent-integration-system-design.md
// §3.2): the design doc specifies a `universe` parameter (default
// "csi300") instead of `symbols`. A universe resolver would map a named
// pool ("csi300") to a concrete []string. That resolver is Phase 2
// work (needs storage or data-service backing). For Phase 1 we accept
// an explicit `symbols` list, matching the existing factor.evaluate /
// factor.compute tools. When the resolver lands, a future commit can
// add `universe` as an alternative input — both fields will be accepted
// and exactly one will be required.
type ComputeFactorICTool struct {
	c *client.FactorClient
}

var _ tools.Tool = (*ComputeFactorICTool)(nil)

// NewComputeFactorICTool constructs a ComputeFactorICTool backed by c.
// Panics if c is nil — wiring bug, fail loud at startup.
func NewComputeFactorICTool(c *client.FactorClient) *ComputeFactorICTool {
	if c == nil {
		panic("builtin: NewComputeFactorICTool called with nil FactorClient")
	}
	return &ComputeFactorICTool{c: c}
}

func (t *ComputeFactorICTool) Name() string { return "compute_factor_ic" }

func (t *ComputeFactorICTool) Description() string {
	return "Compute a factor expression's Information Coefficient (IC) and Information Ratio (IR) over a symbol list and date range (L2 gate). IC > 0.03 is useful; IC > 0.05 is strong."
}

func (t *ComputeFactorICTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "expression",
			Type:        "string",
			Description: "Factor DSL expression, e.g. 'ts_rank(close, 20)'.",
			Required:    true,
		},
		{
			Name:        "symbols",
			Type:        "[]string",
			Description: "List of stock symbols to evaluate the factor over, e.g. [\"000001.SZ\", \"600000.SH\"].",
			Required:    true,
		},
		{
			Name:        "start_date",
			Type:        "string",
			Description: "Evaluation window start date in YYYY-MM-DD format.",
			Required:    true,
		},
		{
			Name:        "end_date",
			Type:        "string",
			Description: "Evaluation window end date in YYYY-MM-DD format.",
			Required:    true,
		},
	}
}

func (t *ComputeFactorICTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Factor quality metrics + L2 GateDecision. Hermes reads `passed` to decide whether the factor is worth a full backtest (L3).",
		Fields: []tools.OutputField{
			{Name: "ic", Type: "float", Description: "Information Coefficient: rank correlation between factor value and forward return. |IC| > 0.03 is useful."},
			{Name: "ir", Type: "float", Description: "Information Ratio: IC mean / IC std. IR > 0.5 is strong."},
			{Name: "level", Type: "string", Description: "Gate identifier: always \"L2\"."},
			{Name: "passed", Type: "bool", Description: "true if IC >= 0.02 (the L2 threshold)."},
			{Name: "reason", Type: "string", Description: "Machine-readable reason code: \"passed\" or \"low_ic\"."},
			{Name: "recommendation", Type: "string", Description: "LLM-facing actionable hint (Chinese)."},
		},
	}
}

func (t *ComputeFactorICTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	expr, err := requireString(args, "expression")
	if err != nil {
		return nil, err
	}
	symbols, err := requireStringSlice(args, "symbols")
	if err != nil {
		return nil, err
	}
	startDate, err := requireString(args, "start_date")
	if err != nil {
		return nil, err
	}
	endDate, err := requireString(args, "end_date")
	if err != nil {
		return nil, err
	}

	m, err := t.c.EvaluateFactor(ctx, expr, symbols, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("compute_factor_ic: %w", err)
	}

	passed := m.IC >= GateL2MinIC
	return &computeFactorICResult{
		GateDecision: GateDecision{
			Level:          "L2",
			Passed:         passed,
			Reason:         gateReasonL2(m.IC),
			Recommendation: gateRecommendationL2(m.IC),
		},
		IC: m.IC,
		IR: m.IR,
	}, nil
}

// computeFactorICResult wraps *client.FactorMetrics with the L2
// GateDecision. We define a local struct (rather than embedding
// GateDecision in client.FactorMetrics) to keep the gate decision
// logic inside the builtin package — the upstream client should not
// depend on gate concepts.
//
// JSON shape: { ic, ir, level, passed, reason, recommendation }
type computeFactorICResult struct {
	GateDecision
	IC float64 `json:"ic"`
	IR float64 `json:"ir"`
}
