package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// ─── Name / Description / Parameters / OutputSchema ─────────────────────

func TestSummarizeBacktestTool_Name(t *testing.T) {
	tt := NewSummarizeBacktestTool()
	assert.Equal(t, "summarize_backtest", tt.Name())
}

func TestSummarizeBacktestTool_Description(t *testing.T) {
	tt := NewSummarizeBacktestTool()
	assert.NotEmpty(t, tt.Description())
	assert.Contains(t, tt.Description(), "Compress")
}

func TestSummarizeBacktestTool_Parameters(t *testing.T) {
	tt := NewSummarizeBacktestTool()
	params := tt.Parameters()
	require.Len(t, params, 1)
	assert.Equal(t, "result_json", params[0].Name)
	assert.Equal(t, "string", params[0].Type)
	assert.True(t, params[0].Required)
}

func TestSummarizeBacktestTool_OutputSchema(t *testing.T) {
	tt := NewSummarizeBacktestTool()
	schema := tt.OutputSchema()
	assert.Equal(t, "object", schema.Type)
	// Verify key fields exist.
	expected := map[string]bool{
		"total_return":           true,
		"sharpe_ratio":           true,
		"max_drawdown":           true,
		"win_rate":               true,
		"total_trades":           true,
		"risk_level":             true,
		"portfolio_values_count": true,
		"trades_count":           true,
		// L3 GateDecision fields.
		"level":          true,
		"passed":         true,
		"reason":         true,
		"recommendation": true,
	}
	for _, f := range schema.Fields {
		delete(expected, f.Name)
	}
	assert.Empty(t, expected, "missing fields: %v", expected)
}

// ─── Execute happy path ─────────────────────────────────────────────────

func TestSummarizeBacktestTool_Execute_HappyPath(t *testing.T) {
	result := &domain.BacktestResult{
		StartDate:       time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		EndDate:         time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		TotalReturn:     0.23,
		AnnualReturn:    0.11,
		SharpeRatio:     1.2,
		SortinoRatio:    1.5,
		MaxDrawdown:     -0.15,
		MaxDrawdownDate: time.Date(2023, 8, 15, 0, 0, 0, 0, time.UTC),
		WinRate:         0.56,
		TotalTrades:     142,
		AvgHoldingDays:  5.2,
		CalmarRatio:     0.73,
		PortfolioValues: make([]domain.PortfolioValue, 502),
		Trades:          make([]domain.Trade, 142),
	}
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)

	tt := NewSummarizeBacktestTool()
	args := map[string]interface{}{
		"result_json": string(resultJSON),
	}

	out, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	require.NotNil(t, out)

	summary, ok := out.(*backtestSummary)
	require.True(t, ok, "result should be *backtestSummary, got %T", out)

	assert.InDelta(t, 0.23, summary.TotalReturn, 1e-9)
	assert.InDelta(t, 1.2, summary.SharpeRatio, 1e-9)
	assert.InDelta(t, -0.15, summary.MaxDrawdown, 1e-9)
	assert.Equal(t, "2023-08-15", summary.MaxDrawdownDate)
	assert.Equal(t, "2022-01-01", summary.StartDate)
	assert.Equal(t, "2024-01-01", summary.EndDate)
	assert.Equal(t, 142, summary.TotalTrades)
	assert.Equal(t, 502, summary.PortfolioValuesCount)
	assert.Equal(t, 142, summary.TradesCount)
	// Sharpe=1.2 (>=1.0), max_dd=-0.15 (between -0.10 and -0.20) → medium.
	assert.Equal(t, "medium", summary.RiskLevel)

	// L3 GateDecision: Sharpe=1.2 >= 0.50 AND MaxDD=-0.15 > -0.30 → passed.
	assert.Equal(t, "L3", summary.Level, "level should be L3")
	assert.True(t, summary.Passed, "Sharpe=1.2>=0.50 AND MaxDD=-0.15>-0.30 should pass L3")
	assert.Equal(t, GateReasonPassed, summary.Reason, "reason should be 'passed' for healthy backtest")
	assert.NotEmpty(t, summary.Recommendation, "recommendation should be non-empty")
}

// TestSummarizeBacktestTool_Execute_Compression verifies the summary is
// dramatically smaller than the input — this is the tool's core value.
func TestSummarizeBacktestTool_Execute_Compression(t *testing.T) {
	// Build a result with 500 portfolio values (realistic for a 2-year daily backtest).
	pv := make([]domain.PortfolioValue, 500)
	for i := range pv {
		pv[i] = domain.PortfolioValue{
			Date:       time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i),
			TotalValue: 100000.0 * (1.0 + 0.001*float64(i)),
		}
	}
	trades := make([]domain.Trade, 100)
	for i := range trades {
		trades[i] = domain.Trade{Symbol: "000001.SZ", Price: 10.0 + float64(i)*0.1}
	}

	result := &domain.BacktestResult{
		TotalReturn:     0.15,
		SharpeRatio:     1.5,
		MaxDrawdown:     -0.08,
		PortfolioValues: pv,
		Trades:          trades,
	}
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)

	inputSize := len(resultJSON)
	tt := NewSummarizeBacktestTool()
	args := map[string]interface{}{"result_json": string(resultJSON)}

	out, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)

	summaryJSON, err := json.Marshal(out)
	require.NoError(t, err)
	outputSize := len(summaryJSON)

	// Summary should be ~10x smaller than the input.
	assert.Less(t, outputSize, inputSize/5, "summary (%d bytes) should be < 1/5 of input (%d bytes)", outputSize, inputSize)
	assert.Equal(t, 500, out.(*backtestSummary).PortfolioValuesCount)
	assert.Equal(t, 100, out.(*backtestSummary).TradesCount)
}

// ─── Execute missing/invalid args ───────────────────────────────────────

func TestSummarizeBacktestTool_Execute_MissingResultJSON(t *testing.T) {
	tt := NewSummarizeBacktestTool()
	_, err := tt.Execute(context.Background(), map[string]interface{}{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "result_json")
}

func TestSummarizeBacktestTool_Execute_EmptyResultJSON(t *testing.T) {
	tt := NewSummarizeBacktestTool()
	args := map[string]interface{}{"result_json": ""}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

func TestSummarizeBacktestTool_Execute_InvalidJSON(t *testing.T) {
	tt := NewSummarizeBacktestTool()
	args := map[string]interface{}{"result_json": "{not valid json"}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "result_json")
}

func TestSummarizeBacktestTool_Execute_WrongType(t *testing.T) {
	tt := NewSummarizeBacktestTool()
	args := map[string]interface{}{"result_json": 42}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

func TestSummarizeBacktestTool_Execute_EmptyResult(t *testing.T) {
	// Zero-value BacktestResult should still produce a valid summary.
	resultJSON := `{}`
	tt := NewSummarizeBacktestTool()
	args := map[string]interface{}{"result_json": resultJSON}

	out, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	summary := out.(*backtestSummary)
	assert.Equal(t, 0.0, summary.TotalReturn)
	assert.Equal(t, 0, summary.TotalTrades)
	// Sharpe=0 (<0.5), max_dd=0 (>−0.20) → "high" (sharpe<0.5 triggers high).
	assert.Equal(t, "high", summary.RiskLevel)

	// L3 GateDecision: Sharpe=0 < 0.50 → fail with low_sharpe.
	// MaxDrawdown=0 > -0.30 so drawdown is OK; failure is purely Sharpe.
	assert.Equal(t, "L3", summary.Level)
	assert.False(t, summary.Passed, "Sharpe=0 < 0.50 should fail L3")
	assert.Equal(t, GateReasonLowSharpe, summary.Reason, "reason should be 'low_sharpe' since Sharpe fails")
	assert.NotEmpty(t, summary.Recommendation)
}

// ─── summarizeBacktestResult unit tests ─────────────────────────────────

func TestSummarizeBacktestResult_NilInput(t *testing.T) {
	assert.Nil(t, summarizeBacktestResult(nil))
}

func TestSummarizeBacktestResult_PreservesMetrics(t *testing.T) {
	result := &domain.BacktestResult{
		TotalReturn: 0.30,
		SharpeRatio: 2.0,
		MaxDrawdown: -0.05,
		WinRate:     0.65,
		TotalTrades: 50,
		CalmarRatio: 4.0,
	}
	summary := summarizeBacktestResult(result)
	require.NotNil(t, summary)
	assert.InDelta(t, 0.30, summary.TotalReturn, 1e-9)
	assert.InDelta(t, 2.0, summary.SharpeRatio, 1e-9)
	assert.InDelta(t, -0.05, summary.MaxDrawdown, 1e-9)
	assert.InDelta(t, 0.65, summary.WinRate, 1e-9)
	assert.Equal(t, 50, summary.TotalTrades)
	assert.InDelta(t, 4.0, summary.CalmarRatio, 1e-9)
}

// ─── L3 GateDecision table-driven tests ────────────────────────────────

// TestSummarizeBacktestResult_L3GateDecision verifies the L3 gate pass/fail
// logic across the (Sharpe, MaxDrawdown) space:
//   - pass: Sharpe ≥ 0.50 AND MaxDrawdown > -0.30
//   - low_sharpe: Sharpe < 0.50 (priority when both fail)
//   - excessive_drawdown: Sharpe ≥ 0.50 AND MaxDrawdown ≤ -0.30
func TestSummarizeBacktestResult_L3GateDecision(t *testing.T) {
	cases := []struct {
		name       string
		sharpe     float64
		maxDD      float64
		wantPassed bool
		wantReason string
	}{
		{
			name:       "pass: high Sharpe + shallow DD",
			sharpe:     1.5,
			maxDD:      -0.10,
			wantPassed: true,
			wantReason: GateReasonPassed,
		},
		{
			name:       "pass: boundary Sharpe=0.50",
			sharpe:     0.50,
			maxDD:      -0.10,
			wantPassed: true,
			wantReason: GateReasonPassed,
		},
		{
			name:       "pass: boundary MaxDD=-0.299",
			sharpe:     1.0,
			maxDD:      -0.299,
			wantPassed: true,
			wantReason: GateReasonPassed,
		},
		{
			name:       "fail: Sharpe below threshold",
			sharpe:     0.30,
			maxDD:      -0.10,
			wantPassed: false,
			wantReason: GateReasonLowSharpe,
		},
		{
			name:       "fail: drawdown too deep",
			sharpe:     1.0,
			maxDD:      -0.40,
			wantPassed: false,
			wantReason: GateReasonExcessiveDrawdown,
		},
		{
			name:       "fail: boundary MaxDD exactly -0.30 → drawdown fail",
			sharpe:     1.0,
			maxDD:      -0.30,
			wantPassed: false,
			wantReason: GateReasonExcessiveDrawdown,
		},
		{
			name:       "fail: both fail → prefers low_sharpe (more actionable)",
			sharpe:     0.20,
			maxDD:      -0.50,
			wantPassed: false,
			wantReason: GateReasonLowSharpe,
		},
		{
			name:       "fail: zero Sharpe + zero DD (empty result)",
			sharpe:     0.0,
			maxDD:      0.0,
			wantPassed: false,
			wantReason: GateReasonLowSharpe,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := &domain.BacktestResult{
				SharpeRatio: tc.sharpe,
				MaxDrawdown: tc.maxDD,
			}
			summary := summarizeBacktestResult(result)
			require.NotNil(t, summary)
			assert.Equal(t, "L3", summary.Level)
			assert.Equal(t, tc.wantPassed, summary.Passed,
				"passed: sharpe=%.3f maxDD=%.3f", tc.sharpe, tc.maxDD)
			assert.Equal(t, tc.wantReason, summary.Reason,
				"reason: sharpe=%.3f maxDD=%.3f", tc.sharpe, tc.maxDD)
			assert.NotEmpty(t, summary.Recommendation, "recommendation should always be non-empty")
		})
	}
}

// ─── deriveRiskLevel table-driven tests ────────────────────────────────

func TestDeriveRiskLevel(t *testing.T) {
	cases := []struct {
		name        string
		maxDrawdown float64
		sharpe      float64
		want        string
	}{
		{"low risk: shallow DD + high Sharpe", -0.05, 1.5, "low"},
		{"low risk: no DD + high Sharpe", 0.0, 2.0, "low"},
		{"medium: moderate DD + high Sharpe", -0.12, 1.5, "medium"},
		{"medium: shallow DD + moderate Sharpe", -0.05, 0.7, "medium"},
		{"high: deep DD", -0.25, 1.5, "high"},
		{"high: very low Sharpe", -0.05, 0.3, "high"},
		{"high: deep DD + low Sharpe", -0.30, 0.2, "high"},
		{"boundary: DD exactly -0.10 → medium", -0.10, 1.5, "medium"},
		{"boundary: DD -0.099 + Sharpe 1.0 → low", -0.099, 1.0, "low"},
		{"boundary: Sharpe exactly 0.5 → medium", -0.05, 0.5, "medium"},
		{"boundary: Sharpe 0.499 → high", -0.05, 0.499, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveRiskLevel(tc.maxDrawdown, tc.sharpe)
			assert.Equal(t, tc.want, got, "maxDD=%.3f sharpe=%.3f", tc.maxDrawdown, tc.sharpe)
		})
	}
}

// ─── formatDate tests ────────────────────────────────────────────────────

func TestFormatDate_ZeroTime(t *testing.T) {
	assert.Equal(t, "", formatDate(time.Time{}))
}

func TestFormatDate_ValidTime(t *testing.T) {
	got := formatDate(time.Date(2023, 8, 15, 14, 30, 0, 0, time.UTC))
	assert.Equal(t, "2023-08-15", got)
}

// ─── Registry integration ──────────────────────────────────────────────

func TestSummarizeBacktestTool_RegisterInRegistry(t *testing.T) {
	tt := NewSummarizeBacktestTool()
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(tt))
	got, err := reg.Get("summarize_backtest")
	require.NoError(t, err)
	assert.Equal(t, "summarize_backtest", got.Name())
}
