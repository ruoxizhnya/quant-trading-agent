// Package builtin contains the default set of Tool implementations
// shipped with quant-trading. Each Tool wraps an existing capability
// (backtest runner, factor client, data-service HTTP client, strategy
// registry) into the tools.Tool interface so it can be discovered and
// invoked via the /api/tools HTTP API.
//
// These implementations live in a subpackage (rather than in
// pkg/ai/tools/ itself) to keep pkg/ai/tools/ a pure interfaces+Registry
// package with no reverse dependencies on pkg/ai, pkg/strategy, or
// pkg/domain.
package builtin

import (
	"context"
	"fmt"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/backtest/contracts"
)

// BacktestTool wraps a contracts.BacktestRunner into a tools.Tool.
//
// It is the "coexistence adapter" described in S7-P3-3 D4: the
// existing BacktestRunner interface (used by pkg/ai/agents/ValidateAgent
// and pkg/ai/pipeline.Pipeline) is left untouched, and this Tool
// simply delegates Execute → RunBacktest. This means:
//
//   - Existing agents keep working without modification.
//   - The HTTP API (POST /api/tools/backtest.run) exposes the same
//     capability to external agents.
//   - When pkg/ai/agents/ is eventually removed (S7-P3-7), the
//     BacktestRunner interface can be deleted and BacktestTool can
//     hold a direct HTTP client instead — but that's a future decision.
//
// Tool name: "backtest.run"
// Input: strategy_name, stock_pool, start_date, end_date (all required)
// Output: *domain.BacktestResult (JSON-serializable)
type BacktestTool struct {
	runner contracts.BacktestRunner
}

// Compile-time assertions that BacktestTool satisfies the Tool facets.
var (
	_ tools.ToolCore       = (*BacktestTool)(nil)
	_ tools.SchemaProvider = (*BacktestTool)(nil)
	_ tools.Executable     = (*BacktestTool)(nil)
	_ tools.Tool           = (*BacktestTool)(nil)
)

// NewBacktestTool constructs a BacktestTool that delegates to runner.
// Panics if runner is nil — a Tool with no delegate is a programming
// bug, not a runtime condition, and we want to fail loud at wiring
// time (in main.go) rather than return a confusing error on every
// Execute call.
func NewBacktestTool(runner contracts.BacktestRunner) *BacktestTool {
	if runner == nil {
		panic("builtin: NewBacktestTool called with nil runner")
	}
	return &BacktestTool{runner: runner}
}

// Name returns the tool's HTTP-safe identifier.
func (t *BacktestTool) Name() string { return "backtest.run" }

// Description returns a human-readable summary for the discovery API.
func (t *BacktestTool) Description() string {
	return "Run a backtest of a named strategy over a stock pool and date range. Returns performance metrics (returns, Sharpe, drawdown, win rate), portfolio equity curve, and trade list."
}

// Parameters returns the input schema in stable order (required first).
func (t *BacktestTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "strategy_name",
			Type:        "string",
			Description: "Name of a previously-registered strategy (see strategy.list / strategy.get). Example: 'momentum', 'dual_momentum'.",
			Required:    true,
		},
		{
			Name:        "stock_pool",
			Type:        "[]string",
			Description: "List of stock symbols to trade, e.g. [\"000001.SZ\", \"600000.SH\"]. Use strategy.list to discover universe codes.",
			Required:    true,
		},
		{
			Name:        "start_date",
			Type:        "string",
			Description: "Backtest start date in YYYY-MM-DD format. Inclusive.",
			Required:    true,
		},
		{
			Name:        "end_date",
			Type:        "string",
			Description: "Backtest end date in YYYY-MM-DD format. Inclusive.",
			Required:    true,
		},
	}
}

// OutputSchema describes the returned *domain.BacktestResult.
//
// We list only the top-level scalar fields an agent most commonly
// consumes; PortfolioValues and Trades are arrays and are described
// in the Description rather than enumerated.
func (t *BacktestTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Backtest performance report. Contains scalar metrics, a portfolio_values equity curve array, and a trades array.",
		Fields: []tools.OutputField{
			{Name: "total_return", Type: "float", Description: "Cumulative return over the period (e.g. 0.15 = +15%)."},
			{Name: "annual_return", Type: "float", Description: "Annualized return."},
			{Name: "sharpe_ratio", Type: "float", Description: "Risk-adjusted return (annualized)."},
			{Name: "sortino_ratio", Type: "float", Description: "Downside-adjusted Sharpe."},
			{Name: "max_drawdown", Type: "float", Description: "Peak-to-trough drawdown (negative, e.g. -0.12 = -12%)."},
			{Name: "win_rate", Type: "float", Description: "Fraction of winning trades (0..1)."},
			{Name: "total_trades", Type: "int", Description: "Number of closed trades."},
			{Name: "portfolio_values", Type: "array", Description: "Daily portfolio equity curve."},
			{Name: "trades", Type: "array", Description: "List of executed trades with entry/exit prices."},
		},
	}
}

// Execute validates args, delegates to BacktestRunner.RunBacktest, and
// returns the *domain.BacktestResult. Missing or wrong-typed required
// parameters return ErrInvalidArgs (wrapped with the field name).
func (t *BacktestTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
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

	result, err := t.runner.RunBacktest(ctx, strategyName, stockPool, startDate, endDate)
	if err != nil {
		// Propagate the runner's error verbatim — callers use errors.Is
		// to distinguish "not registered" (strategy not found) from
		// "execution failed" (data missing, etc.). We do NOT wrap with
		// ErrInvalidArgs here because the args were valid; the failure
		// is downstream.
		return nil, fmt.Errorf("backtest.run: %w", err)
	}
	return result, nil
}

// ─── arg-extraction helpers ───────────────────────────────────────────
//
// These are shared across all builtin Tools. They enforce the type
// contract declared by Parameter.Type and return ErrInvalidArgs
// (wrapped with the field name) on failure. Keeping them unexported
// and in this package means each Tool's Execute stays declarative.

// requireString extracts a required string field from args.
func requireString(args map[string]interface{}, field string) (string, error) {
	v, ok := args[field]
	if !ok {
		return "", fmt.Errorf("%w: missing required field %q", tools.ErrInvalidArgs, field)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: field %q must be a string, got %T", tools.ErrInvalidArgs, field, v)
	}
	if s == "" {
		return "", fmt.Errorf("%w: field %q must not be empty", tools.ErrInvalidArgs, field)
	}
	return s, nil
}

// requireStringSlice extracts a required []string field from args.
// Accepts both []string and []interface{} of strings (the latter is
// what json.Unmarshal produces for a JSON array).
func requireStringSlice(args map[string]interface{}, field string) ([]string, error) {
	v, ok := args[field]
	if !ok {
		return nil, fmt.Errorf("%w: missing required field %q", tools.ErrInvalidArgs, field)
	}
	switch s := v.(type) {
	case []string:
		if len(s) == 0 {
			return nil, fmt.Errorf("%w: field %q must not be empty", tools.ErrInvalidArgs, field)
		}
		return s, nil
	case []interface{}:
		out := make([]string, 0, len(s))
		for i, item := range s {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%w: field %q[%d] must be a string, got %T", tools.ErrInvalidArgs, field, i, item)
			}
			out = append(out, str)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%w: field %q must not be empty", tools.ErrInvalidArgs, field)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: field %q must be a []string, got %T", tools.ErrInvalidArgs, field, v)
	}
}

// requireFloat extracts a required float64 field from args.
// Accepts float64 and int (json.Unmarshal may produce int for
// whole numbers).
func requireFloat(args map[string]interface{}, field string) (float64, error) {
	v, ok := args[field]
	if !ok {
		return 0, fmt.Errorf("%w: missing required field %q", tools.ErrInvalidArgs, field)
	}
	switch f := v.(type) {
	case float64:
		return f, nil
	case int:
		return float64(f), nil
	case int64:
		return float64(f), nil
	default:
		return 0, fmt.Errorf("%w: field %q must be a float, got %T", tools.ErrInvalidArgs, field, v)
	}
}

// optionalString extracts an optional string field; returns "" if absent.
func optionalString(args map[string]interface{}, field string) (string, error) {
	v, ok := args[field]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: field %q must be a string, got %T", tools.ErrInvalidArgs, field, v)
	}
	return s, nil
}

// optionalFloat extracts an optional float64 field; returns 0 if absent.
func optionalFloat(args map[string]interface{}, field string) (float64, error) {
	v, ok := args[field]
	if !ok {
		return 0, nil
	}
	switch f := v.(type) {
	case float64:
		return f, nil
	case int:
		return float64(f), nil
	case int64:
		return float64(f), nil
	default:
		return 0, fmt.Errorf("%w: field %q must be a float, got %T", tools.ErrInvalidArgs, field, v)
	}
}

// optionalInt extracts an optional int field; returns defaultVal if absent.
// Accepts int, int64, and float64 (json.Unmarshal produces float64 for all
// numbers — a whole-number float is accepted and truncated to int).
func optionalInt(args map[string]interface{}, field string, defaultVal int) (int, error) {
	v, ok := args[field]
	if !ok {
		return defaultVal, nil
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("%w: field %q must be an int, got %T", tools.ErrInvalidArgs, field, v)
	}
}

// optionalStringSlice extracts an optional []string field; returns nil if
// absent. Accepts both []string and []interface{} of strings (the latter is
// what json.Unmarshal produces for a JSON array). Returns an empty (non-nil)
// slice if the field is present but empty — callers should check len().
func optionalStringSlice(args map[string]interface{}, field string) ([]string, error) {
	v, ok := args[field]
	if !ok {
		return nil, nil
	}
	switch s := v.(type) {
	case []string:
		return s, nil
	case []interface{}:
		out := make([]string, 0, len(s))
		for i, item := range s {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%w: field %q[%d] must be a string, got %T", tools.ErrInvalidArgs, field, i, item)
			}
			out = append(out, str)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: field %q must be a []string, got %T", tools.ErrInvalidArgs, field, v)
	}
}
