package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// ─── SummarizeBacktestTool ─────────────────────────────────────────────
//
// Compresses a full *domain.BacktestResult (which may contain 500+
// portfolio_values and 100+ trades) into a ~200-byte summary suitable
// for an LLM's context window. Hermes calls backtest.run → gets the
// full JSON → passes it here → gets a compact view for reasoning.
//
// The tool is a pure data transformation: no storage dependency, no LLM
// call. Risk warnings and natural-language suggestions are Hermes's job
// — the tool provides the structured data, Hermes interprets it.
//
// L3 GateDecision is embedded in the summary. Pass requires:
//   - SharpeRatio >= GateL3MinSharpe (0.50)
//   - MaxDrawdown  >  GateL3MaxDrawdown (-0.30, i.e. not deeper than 30%)
//
// Both conditions must hold. If either fails, the gate fails and the
// reason code identifies the dominant cause.
//
// Tool name: "summarize_backtest"
// Input: result_json (string, required) — JSON-serialized BacktestResult
// Output: *backtestSummary (embeds GateDecision + compressed metrics)
type SummarizeBacktestTool struct{}

var _ tools.Tool = (*SummarizeBacktestTool)(nil)

func NewSummarizeBacktestTool() *SummarizeBacktestTool {
	return &SummarizeBacktestTool{}
}

func (t *SummarizeBacktestTool) Name() string { return "summarize_backtest" }

func (t *SummarizeBacktestTool) Description() string {
	return "Compress a full BacktestResult JSON (from backtest.run) into a compact summary for LLM reasoning. Strips portfolio_values and trades arrays, keeps scalar metrics + trade counts + derived risk level. Call this after backtest.run before analyzing the result."
}

func (t *SummarizeBacktestTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "result_json",
			Type:        "string",
			Description: "JSON-serialized BacktestResult from backtest.run. Pass the entire response body of backtest.run as this string.",
			Required:    true,
		},
	}
}

func (t *SummarizeBacktestTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Compressed backtest summary + L3 GateDecision. ~200 bytes vs ~50KB for the full result. Hermes reads `passed` to decide whether to advance to L4 Walk-Forward.",
		Fields: []tools.OutputField{
			{Name: "total_return", Type: "float", Description: "Cumulative return (e.g. 0.15 = +15%)."},
			{Name: "annual_return", Type: "float", Description: "Annualized return."},
			{Name: "sharpe_ratio", Type: "float", Description: "Risk-adjusted return (annualized)."},
			{Name: "sortino_ratio", Type: "float", Description: "Downside-adjusted Sharpe."},
			{Name: "max_drawdown", Type: "float", Description: "Peak-to-trough drawdown (negative)."},
			{Name: "max_drawdown_date", Type: "string", Description: "Date of max drawdown (YYYY-MM-DD)."},
			{Name: "win_rate", Type: "float", Description: "Fraction of winning trades (0..1)."},
			{Name: "total_trades", Type: "int", Description: "Number of closed trades."},
			{Name: "avg_holding_days", Type: "float", Description: "Average holding period in days."},
			{Name: "calmar_ratio", Type: "float", Description: "Annual return / |max drawdown|."},
			{Name: "start_date", Type: "string", Description: "Backtest start date (YYYY-MM-DD)."},
			{Name: "end_date", Type: "string", Description: "Backtest end date (YYYY-MM-DD)."},
			{Name: "risk_level", Type: "string", Description: "Derived risk assessment: 'low', 'medium', or 'high'."},
			{Name: "portfolio_values_count", Type: "int", Description: "Number of data points in the equity curve (drill-down hint)."},
			{Name: "trades_count", Type: "int", Description: "Number of trade records available in the full result."},
			{Name: "level", Type: "string", Description: "Gate identifier: always \"L3\"."},
			{Name: "passed", Type: "bool", Description: "true if Sharpe >= 0.50 AND MaxDrawdown > -0.30."},
			{Name: "reason", Type: "string", Description: "Machine-readable reason code: \"passed\", \"low_sharpe\", or \"excessive_drawdown\"."},
			{Name: "recommendation", Type: "string", Description: "LLM-facing actionable hint (Chinese)."},
		},
	}
}

func (t *SummarizeBacktestTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	resultJSON, err := requireString(args, "result_json")
	if err != nil {
		return nil, err
	}

	var result domain.BacktestResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, fmt.Errorf("%w: result_json is not valid BacktestResult JSON: %v", tools.ErrInvalidArgs, err)
	}

	return summarizeBacktestResult(&result), nil
}

// ─── summary type ──────────────────────────────────────────────────────

// backtestSummary is the LLM-friendly compressed view of a
// domain.BacktestResult. Strips the PortfolioValues and Trades arrays
// (which can be 500+ and 100+ elements respectively) and keeps only
// the decision-relevant scalars + a derived risk_level + the L3
// GateDecision (embedded).
type backtestSummary struct {
	TotalReturn     float64 `json:"total_return"`
	AnnualReturn    float64 `json:"annual_return"`
	SharpeRatio     float64 `json:"sharpe_ratio"`
	SortinoRatio    float64 `json:"sortino_ratio"`
	MaxDrawdown     float64 `json:"max_drawdown"`
	MaxDrawdownDate string  `json:"max_drawdown_date"`
	WinRate         float64 `json:"win_rate"`
	TotalTrades     int     `json:"total_trades"`
	AvgHoldingDays  float64 `json:"avg_holding_days"`
	CalmarRatio     float64 `json:"calmar_ratio"`

	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`

	// RiskLevel is derived from max_drawdown + sharpe_ratio:
	//   "high":   max_drawdown <= -0.20 OR sharpe < 0.5
	//   "medium": max_drawdown <= -0.10 OR sharpe < 1.0
	//   "low":    max_drawdown > -0.10 AND sharpe >= 1.0
	RiskLevel string `json:"risk_level"`

	PortfolioValuesCount int `json:"portfolio_values_count"`
	TradesCount          int `json:"trades_count"`

	// GateDecision is the L3 gate result. Pass requires:
	//   SharpeRatio >= GateL3MinSharpe AND MaxDrawdown > GateL3MaxDrawdown
	GateDecision
}

// summarizeBacktestResult compresses a full BacktestResult into a summary
// and computes the L3 GateDecision. Returns nil for nil input (defensive).
func summarizeBacktestResult(r *domain.BacktestResult) *backtestSummary {
	if r == nil {
		return nil
	}

	// L3 gate: both conditions must hold.
	sharpeOK := r.SharpeRatio >= GateL3MinSharpe
	drawdownOK := r.MaxDrawdown > GateL3MaxDrawdown // e.g. -0.20 > -0.30
	passed := sharpeOK && drawdownOK

	summary := &backtestSummary{
		TotalReturn:          r.TotalReturn,
		AnnualReturn:         r.AnnualReturn,
		SharpeRatio:          r.SharpeRatio,
		SortinoRatio:         r.SortinoRatio,
		MaxDrawdown:          r.MaxDrawdown,
		MaxDrawdownDate:      formatDate(r.MaxDrawdownDate),
		WinRate:              r.WinRate,
		TotalTrades:          r.TotalTrades,
		AvgHoldingDays:       r.AvgHoldingDays,
		CalmarRatio:          r.CalmarRatio,
		StartDate:            formatDate(r.StartDate),
		EndDate:              formatDate(r.EndDate),
		RiskLevel:            deriveRiskLevel(r.MaxDrawdown, r.SharpeRatio),
		PortfolioValuesCount: len(r.PortfolioValues),
		TradesCount:          len(r.Trades),
		GateDecision: GateDecision{
			Level:          "L3",
			Passed:         passed,
			Reason:         gateReasonL3(passed, r.SharpeRatio, r.MaxDrawdown),
			Recommendation: gateRecommendationL3(passed, r.SharpeRatio, r.MaxDrawdown),
		},
	}

	return summary
}

// formatDate converts time.Time to "YYYY-MM-DD" string. Returns "" for
// zero time. Used to compress time.Time fields (which serialize as long
// RFC3339 strings) into the shorter date-only format.
func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

// deriveRiskLevel classifies a backtest's risk based on drawdown depth
// and Sharpe ratio. The thresholds are standard quant heuristics:
//   - 20% drawdown is the "unacceptable for most strategies" line
//   - Sharpe < 0.5 means the strategy barely beats risk-free
//   - Sharpe >= 1.0 is the "investible" threshold
func deriveRiskLevel(maxDrawdown, sharpe float64) string {
	if maxDrawdown <= -0.20 || sharpe < 0.5 {
		return "high"
	}
	if maxDrawdown <= -0.10 || sharpe < 1.0 {
		return "medium"
	}
	return "low"
}
