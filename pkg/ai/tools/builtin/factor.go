package builtin

import (
	"context"
	"fmt"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/client"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
)

// FactorComputeTool wraps client.FactorClient.ComputeFactor into a Tool.
//
// This activates pkg/ai/client/factor_client.go which was previously
// dead code (no agent used it). With the Tools Registry, an external
// agent can now compute factor values over a symbol list and date
// range via POST /api/tools/factor.compute.
//
// Tool name: "factor.compute"
// Input: formula, symbols, start_date, end_date (all required)
// Output: map[string][]float64 (symbol → time series of factor values)
type FactorComputeTool struct {
	c *client.FactorClient
}

var (
	_ tools.Tool = (*FactorComputeTool)(nil)
)

// NewFactorComputeTool constructs a FactorComputeTool backed by c.
// Panics if c is nil — wiring bug, fail loud at startup.
func NewFactorComputeTool(c *client.FactorClient) *FactorComputeTool {
	if c == nil {
		panic("builtin: NewFactorComputeTool called with nil FactorClient")
	}
	return &FactorComputeTool{c: c}
}

func (t *FactorComputeTool) Name() string { return "factor.compute" }

func (t *FactorComputeTool) Description() string {
	return "Compute a factor expression over a list of symbols and a date range. Returns a map from symbol to its factor value time series. Useful for screening, factor research, and building custom universes."
}

func (t *FactorComputeTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "formula",
			Type:        "string",
			Description: "Factor expression, e.g. 'ts_rank(close, 20)' or '(close - ts_mean(close, 20)) / ts_std(close, 20)'. See expression DSL docs for available functions.",
			Required:    true,
		},
		{
			Name:        "symbols",
			Type:        "[]string",
			Description: "List of stock symbols to compute the factor for, e.g. [\"000001.SZ\", \"600000.SH\"].",
			Required:    true,
		},
		{
			Name:        "start_date",
			Type:        "string",
			Description: "Start date in YYYY-MM-DD format. Inclusive.",
			Required:    true,
		},
		{
			Name:        "end_date",
			Type:        "string",
			Description: "End date in YYYY-MM-DD format. Inclusive.",
			Required:    true,
		},
	}
}

func (t *FactorComputeTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Map from symbol (e.g. '000001.SZ') to an array of factor values over the date range. Array length matches the trading days in [start_date, end_date].",
	}
}

func (t *FactorComputeTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	formula, err := requireString(args, "formula")
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

	req := client.ComputeFactorRequest{
		Formula:   formula,
		Symbols:   symbols,
		StartDate: startDate,
		EndDate:   endDate,
	}
	values, err := t.c.ComputeFactor(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("factor.compute: %w", err)
	}
	return values, nil
}

// ─── FactorEvaluateTool ───────────────────────────────────────────────

// FactorEvaluateTool wraps client.FactorClient.EvaluateFactor into a Tool.
//
// EvaluateFactor computes a factor and then evaluates its predictive
// quality via IC (Information Coefficient) and IR (Information Ratio).
// This is the standard "is this factor any good?" check.
//
// Tool name: "factor.evaluate"
// Input: formula, symbols, start_date, end_date (all required)
// Output: *client.FactorMetrics { IC, IR }
type FactorEvaluateTool struct {
	c *client.FactorClient
}

var _ tools.Tool = (*FactorEvaluateTool)(nil)

// NewFactorEvaluateTool constructs a FactorEvaluateTool backed by c.
// Panics if c is nil.
func NewFactorEvaluateTool(c *client.FactorClient) *FactorEvaluateTool {
	if c == nil {
		panic("builtin: NewFactorEvaluateTool called with nil FactorClient")
	}
	return &FactorEvaluateTool{c: c}
}

func (t *FactorEvaluateTool) Name() string { return "factor.evaluate" }

func (t *FactorEvaluateTool) Description() string {
	return "Evaluate a factor expression's predictive quality. Computes the factor over the symbol list and date range, then returns IC (Information Coefficient) and IR (Information Ratio). IC > 0.03 is generally considered useful; IC > 0.05 is strong."
}

func (t *FactorEvaluateTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "formula",
			Type:        "string",
			Description: "Factor expression to evaluate, e.g. 'ts_rank(close, 20)'.",
			Required:    true,
		},
		{
			Name:        "symbols",
			Type:        "[]string",
			Description: "List of stock symbols to evaluate the factor over.",
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

func (t *FactorEvaluateTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Factor quality metrics.",
		Fields: []tools.OutputField{
			{Name: "ic", Type: "float", Description: "Information Coefficient: rank correlation between factor value and forward return. |IC| > 0.03 is useful."},
			{Name: "ir", Type: "float", Description: "Information Ratio: IC mean / IC std. IR > 0.5 is strong."},
		},
	}
}

func (t *FactorEvaluateTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	formula, err := requireString(args, "formula")
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

	metrics, err := t.c.EvaluateFactor(ctx, formula, symbols, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("factor.evaluate: %w", err)
	}
	return metrics, nil
}
