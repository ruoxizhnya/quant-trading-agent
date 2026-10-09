package builtin

import (
	"context"
	"fmt"
	"math"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// ─── WalkForwardRunner (narrow interface) ─────────────────────────────
//
// WalkForwardRunner is the narrow interface WalkForwardValidateTool
// depends on. The concrete *pkg/backtest/walkforward.WalkForwardEngine
// satisfies it via an adapter in cmd/analysis/setup.go (composition root).
//
// We define the interface here (rather than in pkg/backtest/contracts) because
// it takes domain.WalkForwardParams which would force contracts to
// import more types. Keeping it local to builtin is the pragmatic
// choice — the only consumer is this Tool, and the only implementor
// is the adapter in setup.go.
//
// If a second consumer appears later (e.g. an AI agent), the interface
// can be promoted to pkg/backtest/contracts.
type WalkForwardRunner interface {
	// RunWalkForward runs walk-forward validation for a named strategy
	// over the given stock pool and date range, using train_days/test_days
	// window sizes. Returns the full report; the Tool compresses it into
	// a summary for the agent.
	RunWalkForward(
		ctx context.Context,
		strategyName string,
		stockPool []string,
		startDate, endDate string,
		params domain.WalkForwardParams,
	) (*domain.WalkForwardReport, error)
}

// ─── WalkForwardValidateTool ───────────────────────────────────────────
//
// L4 validation gate: runs walk-forward validation to detect overfitting.
// Splits the date range into train/test windows, runs backtest on each,
// and measures OOS (out-of-sample) performance degradation vs IS
// (in-sample). Returns a structured summary focused on what the agent
// needs to decide: is this strategy robust, or does it overfit?
//
// L4 GateDecision (embedded in the summary):
//   - level    = "L4"
//   - passed   = (gap <= GateL4MaxSharpeGap) AND (oosSharpe >= GateL4MinOOSSharpe)
//   - reason   = "passed" | "sharpe_gap_exceeded" | "low_oos_sharpe"
//   - recommendation = LLM-facing actionable hint (Chinese)
//
// Where gap = 1 - AvgDegradation (AvgDegradation is the OOS/IS Sharpe
// RATIO from the engine). gap = 0 means OOS == IS (no overfit); gap = 1
// means OOS = 0 (total overfit). Note: the L4 gate thresholds differ
// from the engine's internal OverallPass (which uses AvgTestSharpe > 0.5
// AND AvgDegradation < 0.7). The L4 `passed` field may therefore differ
// from `overall_pass` — that's intentional; the gate is the canonical
// decision point for "should Hermes save this strategy to the gene pool?".
//
// Tool name: "walk_forward_validate"
// Input: strategy_name, stock_pool, start_date, end_date (required);
//
//	train_days (default 250), test_days (default 60)
//
// Output: *walkForwardSummary (LLM-friendly compressed report + GateDecision)
//
// Design deviation (hermes-agent-integration-system-design.md §3.2):
// the design doc specifies a `strategy_yaml` parameter so Hermes can
// validate an ad-hoc YAML strategy without registering it. That path
// requires a "register temp strategy from YAML" capability which is
// Phase 2. For Phase 1 we accept a `strategy_name` (already-registered)
// + explicit `stock_pool`, matching the existing WalkForwardEngine API.
type WalkForwardValidateTool struct {
	runner WalkForwardRunner
}

var _ tools.Tool = (*WalkForwardValidateTool)(nil)

// NewWalkForwardValidateTool constructs a WalkForwardValidateTool backed
// by runner. Panics if runner is nil — wiring bug, fail loud at startup.
func NewWalkForwardValidateTool(runner WalkForwardRunner) *WalkForwardValidateTool {
	if runner == nil {
		panic("builtin: NewWalkForwardValidateTool called with nil WalkForwardRunner")
	}
	return &WalkForwardValidateTool{runner: runner}
}

func (t *WalkForwardValidateTool) Name() string { return "walk_forward_validate" }

func (t *WalkForwardValidateTool) Description() string {
	return "Run walk-forward validation to detect overfitting (L4 gate). Splits the date range into train/test windows, runs backtest on each, and measures out-of-sample performance degradation. Returns avg OOS Sharpe, degradation ratio, overfit score, and per-window breakdown. Slow (~minutes) — only call after L1-L3 gates pass."
}

func (t *WalkForwardValidateTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "strategy_name",
			Type:        "string",
			Description: "Name of a previously-registered strategy to validate.",
			Required:    true,
		},
		{
			Name:        "stock_pool",
			Type:        "[]string",
			Description: "Stock symbols to validate over, e.g. [\"000001.SZ\", \"600000.SH\"].",
			Required:    true,
		},
		{
			Name:        "start_date",
			Type:        "string",
			Description: "Validation window start date in YYYY-MM-DD format. Should span at least train_days+test_days of trading days.",
			Required:    true,
		},
		{
			Name:        "end_date",
			Type:        "string",
			Description: "Validation window end date in YYYY-MM-DD format.",
			Required:    true,
		},
		{
			Name:        "train_days",
			Type:        "int",
			Description: "Training window size in trading days (~250 = 1 year).",
			Required:    false,
			Default:     250,
		},
		{
			Name:        "test_days",
			Type:        "int",
			Description: "Test window size in trading days (~60 = 3 months). Also used as step size for rolling windows.",
			Required:    false,
			Default:     60,
		},
	}
}

func (t *WalkForwardValidateTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Walk-forward validation summary + L4 GateDecision. Focus on avg_test_sharpe (OOS performance), avg_degradation (OOS/IS ratio, <0.5 = high overfit), overall_pass (engine's pass), and the L4 gate fields (level/passed/reason/recommendation).",
		Fields: []tools.OutputField{
			{Name: "strategy_id", Type: "string", Description: "Strategy name validated."},
			{Name: "num_windows", Type: "int", Description: "Number of walk-forward windows tested."},
			{Name: "avg_test_sharpe", Type: "float", Description: "Average out-of-sample Sharpe ratio. >0.5 is acceptable; >1.0 is strong."},
			{Name: "avg_degradation", Type: "float", Description: "OOS Sharpe / IS Sharpe. >0.7 = low overfit; 0.5-0.7 = medium; <0.5 = high overfit."},
			{Name: "overfit_score", Type: "float", Description: "0-1, higher = more overfit. Derived from degradation + variance."},
			{Name: "overall_pass", Type: "bool", Description: "Engine's internal pass flag (AvgTestSharpe > 0.5 AND AvgDegradation < 0.7)."},
			{Name: "pass_rate", Type: "float", Description: "Fraction of windows that passed (0..1)."},
			{Name: "windows", Type: "array", Description: "Per-window breakdown: train_sharpe, test_sharpe, test_return, test_max_drawdown."},
			{Name: "level", Type: "string", Description: "Gate identifier: always \"L4\"."},
			{Name: "passed", Type: "bool", Description: "true if the L4 gate passed (gap <= 0.30 AND avg_test_sharpe >= 0.30). Stricter than overall_pass — see tool doc."},
			{Name: "reason", Type: "string", Description: "Machine-readable reason code: \"passed\", \"sharpe_gap_exceeded\", or \"low_oos_sharpe\"."},
			{Name: "recommendation", Type: "string", Description: "LLM-facing actionable hint (Chinese)."},
		},
	}
}

func (t *WalkForwardValidateTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	strategyName, err := requireString(args, "strategy_name")
	if err != nil {
		return nil, err
	}
	stockPool, err := requireStringSlice(args, "stock_pool")
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
	trainDays, err := optionalInt(args, "train_days", 250)
	if err != nil {
		return nil, err
	}
	testDays, err := optionalInt(args, "test_days", 60)
	if err != nil {
		return nil, err
	}

	if trainDays <= 0 {
		return nil, fmt.Errorf("%w: train_days must be positive, got %d", tools.ErrInvalidArgs, trainDays)
	}
	if testDays <= 0 {
		return nil, fmt.Errorf("%w: test_days must be positive, got %d", tools.ErrInvalidArgs, testDays)
	}

	params := domain.WalkForwardParams{
		TrainDays: trainDays,
		TestDays:  testDays,
		StepDays:  testDays, // non-overlapping rolling windows
	}

	report, err := t.runner.RunWalkForward(ctx, strategyName, stockPool, startDate, endDate, params)
	if err != nil {
		return nil, fmt.Errorf("walk_forward_validate: %w", err)
	}

	return summarizeWalkForwardReport(report), nil
}

// ─── summary types ─────────────────────────────────────────────────────

// walkForwardSummary is the LLM-friendly compressed view of a
// domain.WalkForwardReport. The full report embeds *domain.BacktestResult
// per window (with portfolio_values arrays, trade lists, etc.) which is
// far more than an agent needs to decide "overfit or not". This summary
// keeps only the decision-relevant scalars + a thin per-window breakdown
// + the L4 GateDecision (embedded).
type walkForwardSummary struct {
	StrategyID     string          `json:"strategy_id"`
	NumWindows     int             `json:"num_windows"`
	AvgTestSharpe  float64         `json:"avg_test_sharpe"`
	AvgTestReturn  float64         `json:"avg_test_return"`
	AvgDegradation float64         `json:"avg_degradation"`
	OverfitScore   float64         `json:"overfit_score"`
	OverallPass    bool            `json:"overall_pass"`
	PassRate       float64         `json:"pass_rate"`
	Windows        []windowSummary `json:"windows"`

	// GateDecision is the L4 gate result. Pass requires:
	//   gap <= GateL4MaxSharpeGap AND AvgTestSharpe >= GateL4MinOOSSharpe
	// where gap = 1 - AvgDegradation. May differ from OverallPass (which
	// uses the engine's internal thresholds).
	GateDecision
}

type windowSummary struct {
	WindowIndex int     `json:"window_index"`
	TrainStart  string  `json:"train_start"`
	TrainEnd    string  `json:"train_end"`
	TestStart   string  `json:"test_start"`
	TestEnd     string  `json:"test_end"`
	TrainSharpe float64 `json:"train_sharpe"`
	TestSharpe  float64 `json:"test_sharpe"`
	TestReturn  float64 `json:"test_return"`
	TestMaxDD   float64 `json:"test_max_drawdown"`
	OOSvsTrain  float64 `json:"oos_vs_train"`
}

// summarizeWalkForwardReport compresses a full WalkForwardReport into
// the summary struct and computes the L4 GateDecision. Returns nil for
// nil input (defensive).
func summarizeWalkForwardReport(r *domain.WalkForwardReport) *walkForwardSummary {
	if r == nil {
		return nil
	}

	windows := make([]windowSummary, 0, len(r.Windows))
	for _, w := range r.Windows {
		if w == nil {
			continue
		}
		windows = append(windows, windowSummary{
			WindowIndex: w.WindowIndex,
			TrainStart:  w.TrainStart,
			TrainEnd:    w.TrainEnd,
			TestStart:   w.TestStart,
			TestEnd:     w.TestEnd,
			TrainSharpe: w.TrainSharpe,
			TestSharpe:  w.TestSharpe,
			TestReturn:  w.TestReturn,
			TestMaxDD:   w.TestMaxDrawdown,
			OOSvsTrain:  w.OOSvsTrain,
		})
	}

	// L4 gate: convert AvgDegradation (OOS/IS ratio) to gap (1 - ratio).
	// gap = 0 → no overfit; gap = 1 → total overfit. NaN-safe: a NaN
	// AvgDegradation (e.g. empty report) is treated as gap = 1.0 (fail).
	var gap float64
	if math.IsNaN(r.AvgDegradation) {
		gap = 1.0
	} else {
		gap = 1.0 - r.AvgDegradation
	}
	oosSharpe := r.AvgTestSharpe
	passed := gap <= GateL4MaxSharpeGap && oosSharpe >= GateL4MinOOSSharpe

	return &walkForwardSummary{
		StrategyID:     r.StrategyID,
		NumWindows:     len(r.Windows),
		AvgTestSharpe:  r.AvgTestSharpe,
		AvgTestReturn:  r.AvgTestReturn,
		AvgDegradation: r.AvgDegradation,
		OverfitScore:   r.OverfitScore,
		OverallPass:    r.OverallPass,
		PassRate:       r.PassRate,
		Windows:        windows,
		GateDecision: GateDecision{
			Level:          "L4",
			Passed:         passed,
			Reason:         gateReasonL4(passed, gap, oosSharpe),
			Recommendation: gateRecommendationL4(passed, gap, oosSharpe),
		},
	}
}
