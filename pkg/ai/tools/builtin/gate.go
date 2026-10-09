package builtin

// GateDecision is the common gate-level metadata embedded in every
// validation gate tool's return value. It lets Hermes uniformly check
// "did this gate pass?" without parsing tool-specific fields.
//
// Design: hermes-agent-integration-system-design.md §6.2
//
// Each L1-L4 gate tool embeds GateDecision in its return struct and
// populates it with:
//   - Level: the gate identifier ("L1", "L2", "L3", "L4")
//   - Passed: whether the gate's threshold was met
//   - Reason: a machine-readable code (GateReason* constants)
//   - Recommendation: LLM-readable natural language advice
//
// The `reason` field uses stable codes so Hermes can pattern-match
// without parsing natural language. The `recommendation` field gives
// Hermes actionable advice in its prompt language (Chinese).
type GateDecision struct {
	Level          string `json:"level"`          // "L1", "L2", "L3", "L4"
	Passed         bool   `json:"passed"`         // true if the gate threshold was met
	Reason         string `json:"reason"`         // machine-readable code (GateReason* constants)
	Recommendation string `json:"recommendation"` // LLM-readable natural language advice
}

// Gate threshold constants (from design doc §6.1).
// These are the canonical thresholds — Hermes relies on the tools to
// enforce them, not on prompt instructions.
//
// Note on L4 semantics: the engine's WalkForwardReport.AvgDegradation is
// the OOS/IS RATIO (higher = better, less overfit). The L4 gate uses the
// complementary "gap" measure: gap = 1 - AvgDegradation. gap = 0 means
// OOS == IS (no overfit); gap = 1 means OOS == 0 (total overfit). The
// gate passes when gap <= GateL4MaxSharpeGap AND oosSharpe >=
// GateL4MinOOSSharpe. The tool converts ratio → gap before calling the
// helpers, so callers can stay in "ratio" space when reading the report.
const (
	// L1: syntax validation. AST node count limit.
	GateL1MaxComplexity = 50

	// L2: quick IC evaluation. IC must exceed this to be "meaningful".
	GateL2MinIC = 0.02

	// L3: standard backtest. Sharpe must exceed this; drawdown must not
	// be worse (more negative) than this.
	GateL3MinSharpe   = 0.50
	GateL3MaxDrawdown = -0.30

	// L4: walk-forward validation. IS/OOS Sharpe gap (1 - OOS/IS ratio)
	// must be below this; OOS Sharpe must exceed this.
	GateL4MaxSharpeGap = 0.30 // gap threshold; equivalent to AvgDegradation >= 0.70
	GateL4MinOOSSharpe = 0.30
)

// Gate reason codes — machine-readable, stable across versions.
// Hermes pattern-matches on these to decide retry/abandon/continue.
const (
	GateReasonPassed            = "passed"
	GateReasonSyntaxError       = "syntax_error"
	GateReasonLowIC             = "low_ic"
	GateReasonLowSharpe         = "low_sharpe"
	GateReasonExcessiveDrawdown = "excessive_drawdown"
	GateReasonSharpeGapExceeded = "sharpe_gap_exceeded"
	GateReasonLowOOSSharpe      = "low_oos_sharpe"
)

// ─── Gate recommendation templates ─────────────────────────────────────
//
// Centralized recommendation strings so the wording is consistent and
// easy to update. Each returns a Chinese-language actionable suggestion
// that Hermes can include in its reasoning.

// gateRecommendationL1 returns the L1 gate recommendation.
func gateRecommendationL1(passed bool) string {
	if passed {
		return "表达式语法正确，可以进入 L2 快速 IC 评估。"
	}
	return "表达式语法错误。请检查 DSL 语法（ts_*/cs_* 函数、字段名、括号匹配），修正后重试。"
}

// gateRecommendationL2 returns the L2 gate recommendation based on IC.
func gateRecommendationL2(ic float64) string {
	if ic >= GateL2MinIC {
		return "因子 IC 达到阈值，可以进入 L3 标准回测。"
	}
	if ic > 0 {
		return "因子 IC 低于阈值，建议尝试变体表达式（调整窗口、组合多个因子）或换方向。"
	}
	return "因子 IC 为零或负，该因子无预测能力，建议放弃并换方向。"
}

// gateRecommendationL3 returns the L3 gate recommendation based on
// Sharpe and max drawdown.
func gateRecommendationL3(passed bool, sharpe, maxDD float64) string {
	if passed {
		return "策略通过 L3 标准回测，可以进入 L4 Walk-Forward 过拟合检测。"
	}
	if sharpe < GateL3MinSharpe && maxDD < GateL3MaxDrawdown {
		return "策略 Sharpe 偏低且回撤过大。建议优化信号逻辑、加入止损规则或调整仓位管理。"
	}
	if sharpe < GateL3MinSharpe {
		return "策略 Sharpe 低于阈值。建议分析 risk_warnings，优化信号或调整参数。"
	}
	return "策略最大回撤超过阈值。建议加入止损规则或降低仓位。"
}

// gateReasonL1 returns the L1 reason code. L1 has a single failure mode
// (syntax error), so the mapping is trivial.
func gateReasonL1(passed bool) string {
	if passed {
		return GateReasonPassed
	}
	return GateReasonSyntaxError
}

// gateReasonL2 returns the L2 reason code. L2 has a single failure mode
// (IC below threshold); zero/negative IC is the same code — the
// recommendation string carries the nuance.
func gateReasonL2(ic float64) string {
	if ic >= GateL2MinIC {
		return GateReasonPassed
	}
	return GateReasonLowIC
}

// gateReasonL3 picks the machine-readable reason code for an L3 failure.
// Priority when both conditions fail: low_sharpe (more actionable — the
// agent should improve expected return first, drawdown often follows).
func gateReasonL3(passed bool, sharpe, maxDD float64) string {
	if passed {
		return GateReasonPassed
	}
	if sharpe < GateL3MinSharpe {
		return GateReasonLowSharpe
	}
	return GateReasonExcessiveDrawdown
}

// gateRecommendationL4 returns the L4 gate recommendation.
//
// gap is the IS/OOS Sharpe gap = 1 - (AvgOOSSharpe / AvgISSharpe).
// Higher gap = more overfit. The gate passes when
// gap <= GateL4MaxSharpeGap AND oosSharpe >= GateL4MinOOSSharpe.
//
// When the gate fails, the recommendation distinguishes the two failure
// modes so the agent knows whether to address overfit (gap large) or
// absolute OOS performance (gap small but Sharpe still too low).
func gateRecommendationL4(passed bool, gap, oosSharpe float64) string {
	if passed {
		return "策略通过 L4 Walk-Forward 验证，过拟合风险低，可以保存到基因池。"
	}
	if gap < GateL4MaxSharpeGap {
		// Gap is acceptable — failure must be due to low absolute OOS Sharpe.
		return "策略 OOS Sharpe 低于阈值，样本外表现不足。建议扩展训练数据或调整策略逻辑。"
	}
	return "策略过拟合风险高。IS/OOS Sharpe 差距超过阈值。建议简化表达式、增加正则化或缩短窗口。"
}

// gateReasonL4 picks the machine-readable reason code for an L4 failure.
// Priority when both conditions fail: sharpe_gap_exceeded (more specific —
// the agent should address overfit first, then re-test OOS Sharpe).
func gateReasonL4(passed bool, gap, oosSharpe float64) string {
	if passed {
		return GateReasonPassed
	}
	if gap >= GateL4MaxSharpeGap {
		return GateReasonSharpeGapExceeded
	}
	return GateReasonLowOOSSharpe
}
