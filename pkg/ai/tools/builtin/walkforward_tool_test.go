package builtin

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// ─── mock WalkForwardRunner ────────────────────────────────────────────

// mockWFRunner is a test double for the WalkForwardRunner interface.
// It records the last call's arguments and returns either a canned
// report or an error.
type mockWFRunner struct {
	report *domain.WalkForwardReport
	err    error

	// Recorded call arguments.
	lastStrategy string
	lastPool     []string
	lastStart    string
	lastEnd      string
	lastParams   domain.WalkForwardParams
	calls        int
}

func (m *mockWFRunner) RunWalkForward(
	ctx context.Context,
	strategyName string,
	stockPool []string,
	startDate, endDate string,
	params domain.WalkForwardParams,
) (*domain.WalkForwardReport, error) {
	m.calls++
	m.lastStrategy = strategyName
	m.lastPool = stockPool
	m.lastStart = startDate
	m.lastEnd = endDate
	m.lastParams = params
	if m.err != nil {
		return nil, m.err
	}
	if m.report != nil {
		return m.report, nil
	}
	// Default canned report.
	return &domain.WalkForwardReport{
		StrategyID:     strategyName,
		Windows:        []*domain.WalkForwardResult{{WindowIndex: 0, TrainSharpe: 1.2, TestSharpe: 0.9, TestReturn: 0.08, TestMaxDrawdown: -0.05, OOSvsTrain: 0.75}},
		AvgTestSharpe:  0.9,
		AvgTestReturn:  0.08,
		AvgDegradation: 0.75,
		OverfitScore:   0.25,
		OverallPass:    true,
		PassRate:       1.0,
	}, nil
}

// ─── Name / Description / Parameters / OutputSchema ────────────────────

func TestWalkForwardValidateTool_Name(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	assert.Equal(t, "walk_forward_validate", tt.Name())
}

func TestWalkForwardValidateTool_Description(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	desc := tt.Description()
	assert.NotEmpty(t, desc)
	assert.Contains(t, desc, "walk-forward")
	assert.Contains(t, desc, "L4 gate")
}

func TestWalkForwardValidateTool_Parameters(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	params := tt.Parameters()
	require.Len(t, params, 6)

	// First 4 are required, last 2 are optional with defaults.
	required := []string{"strategy_name", "stock_pool", "start_date", "end_date"}
	for i, name := range required {
		assert.Equal(t, name, params[i].Name, "param %d name", i)
		assert.True(t, params[i].Required, "param %q should be required", name)
	}

	assert.Equal(t, "train_days", params[4].Name)
	assert.False(t, params[4].Required)
	assert.Equal(t, 250, params[4].Default)

	assert.Equal(t, "test_days", params[5].Name)
	assert.False(t, params[5].Required)
	assert.Equal(t, 60, params[5].Default)
}

func TestWalkForwardValidateTool_OutputSchema(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	schema := tt.OutputSchema()
	assert.Equal(t, "object", schema.Type)
	require.NotEmpty(t, schema.Fields)

	expected := map[string]bool{
		"strategy_id":     true,
		"num_windows":     true,
		"avg_test_sharpe": true,
		"avg_degradation": true,
		"overfit_score":   true,
		"overall_pass":    true,
		"pass_rate":       true,
		"windows":         true,
		// L4 GateDecision fields.
		"level":          true,
		"passed":         true,
		"reason":         true,
		"recommendation": true,
	}
	for _, f := range schema.Fields {
		delete(expected, f.Name)
	}
	assert.Empty(t, expected, "missing fields in schema: %v", expected)
}

// ─── Execute happy path ─────────────────────────────────────────────────

func TestWalkForwardValidateTool_Execute_HappyPath(t *testing.T) {
	canned := &domain.WalkForwardReport{
		StrategyID:     "momentum",
		Windows:        []*domain.WalkForwardResult{{WindowIndex: 0, TrainSharpe: 1.5, TestSharpe: 1.1, TestReturn: 0.12, TestMaxDrawdown: -0.07, OOSvsTrain: 0.73}},
		AvgTestSharpe:  1.1,
		AvgDegradation: 0.73,
		OverfitScore:   0.2,
		OverallPass:    true,
		PassRate:       1.0,
	}
	runner := &mockWFRunner{report: canned}
	tt := NewWalkForwardValidateTool(runner)

	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ", "600000.SH"},
		"start_date":    "2020-01-01",
		"end_date":      "2024-01-01",
	}

	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	require.NotNil(t, result)

	summary, ok := result.(*walkForwardSummary)
	require.True(t, ok, "result should be *walkForwardSummary, got %T", result)
	assert.Equal(t, "momentum", summary.StrategyID)
	assert.Equal(t, 1, summary.NumWindows)
	assert.InDelta(t, 1.1, summary.AvgTestSharpe, 1e-9)
	assert.InDelta(t, 0.73, summary.AvgDegradation, 1e-9)
	assert.True(t, summary.OverallPass)
	assert.InDelta(t, 1.0, summary.PassRate, 1e-9)
	require.Len(t, summary.Windows, 1)
	assert.InDelta(t, 1.5, summary.Windows[0].TrainSharpe, 1e-9)
	assert.InDelta(t, 1.1, summary.Windows[0].TestSharpe, 1e-9)

	// L4 GateDecision: gap = 1 - 0.73 = 0.27 <= 0.30 AND oosSharpe=1.1 >= 0.30 → pass.
	// Note: OverallPass (engine's check using 0.5/0.7 thresholds) also passes here,
	// so L4 `passed` aligns with `overall_pass` for this healthy case.
	assert.Equal(t, "L4", summary.Level)
	assert.True(t, summary.Passed,
		"gap=0.27<=0.30 AND oosSharpe=1.1>=0.30 should pass L4")
	assert.Equal(t, GateReasonPassed, summary.Reason)
	assert.NotEmpty(t, summary.Recommendation)

	// Verify runner was called with correct args.
	assert.Equal(t, 1, runner.calls)
	assert.Equal(t, "momentum", runner.lastStrategy)
	assert.Equal(t, []string{"000001.SZ", "600000.SH"}, runner.lastPool)
	assert.Equal(t, "2020-01-01", runner.lastStart)
	assert.Equal(t, "2024-01-01", runner.lastEnd)
	// Defaults: train_days=250, test_days=60, step_days=60.
	assert.Equal(t, 250, runner.lastParams.TrainDays)
	assert.Equal(t, 60, runner.lastParams.TestDays)
	assert.Equal(t, 60, runner.lastParams.StepDays)
}

func TestWalkForwardValidateTool_Execute_CustomWindowSizes(t *testing.T) {
	runner := &mockWFRunner{}
	tt := NewWalkForwardValidateTool(runner)

	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2020-01-01",
		"end_date":      "2024-01-01",
		"train_days":    180,
		"test_days":     45,
	}

	_, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	assert.Equal(t, 180, runner.lastParams.TrainDays)
	assert.Equal(t, 45, runner.lastParams.TestDays)
	assert.Equal(t, 45, runner.lastParams.StepDays, "step_days should equal test_days for non-overlapping windows")
}

func TestWalkForwardValidateTool_Execute_TrainDaysAsFloat(t *testing.T) {
	// JSON-decoded numbers come as float64.
	runner := &mockWFRunner{}
	tt := NewWalkForwardValidateTool(runner)

	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2020-01-01",
		"end_date":      "2024-01-01",
		"train_days":    float64(200),
		"test_days":     float64(50),
	}

	_, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	assert.Equal(t, 200, runner.lastParams.TrainDays)
	assert.Equal(t, 50, runner.lastParams.TestDays)
}

// ─── Execute missing/wrong-typed args ───────────────────────────────────

func TestWalkForwardValidateTool_Execute_MissingStrategyName(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	args := map[string]interface{}{
		"stock_pool": []string{"000001.SZ"},
		"start_date": "2020-01-01",
		"end_date":   "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "strategy_name")
}

func TestWalkForwardValidateTool_Execute_MissingStockPool(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"start_date":    "2020-01-01",
		"end_date":      "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "stock_pool")
}

func TestWalkForwardValidateTool_Execute_MissingStartDate(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ"},
		"end_date":      "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "start_date")
}

func TestWalkForwardValidateTool_Execute_MissingEndDate(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2020-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "end_date")
}

func TestWalkForwardValidateTool_Execute_ZeroTrainDays(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2020-01-01",
		"end_date":      "2024-01-01",
		"train_days":    0,
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "train_days")
}

func TestWalkForwardValidateTool_Execute_NegativeTestDays(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2020-01-01",
		"end_date":      "2024-01-01",
		"test_days":     -10,
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "test_days")
}

func TestWalkForwardValidateTool_Execute_WrongTypeTrainDays(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2020-01-01",
		"end_date":      "2024-01-01",
		"train_days":    "250", // string, not int
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "train_days")
}

// ─── Execute runner error ───────────────────────────────────────────────

func TestWalkForwardValidateTool_Execute_RunnerError(t *testing.T) {
	runnerErr := errors.New("insufficient trading days: need 310, got 100")
	runner := &mockWFRunner{err: runnerErr}
	tt := NewWalkForwardValidateTool(runner)

	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2020-01-01",
		"end_date":      "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	// Runner error propagates verbatim — NOT ErrInvalidArgs.
	assert.True(t, errors.Is(err, runnerErr))
	assert.False(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "walk_forward_validate")
}

// ─── summarizeWalkForwardReport unit tests ──────────────────────────────

func TestSummarizeWalkForwardReport_NilInput(t *testing.T) {
	summary := summarizeWalkForwardReport(nil)
	assert.Nil(t, summary)
}

func TestSummarizeWalkForwardReport_EmptyWindows(t *testing.T) {
	report := &domain.WalkForwardReport{
		StrategyID:    "test",
		Windows:       []*domain.WalkForwardResult{},
		AvgTestSharpe: 0.5,
		OverallPass:   false,
	}
	summary := summarizeWalkForwardReport(report)
	require.NotNil(t, summary)
	assert.Equal(t, "test", summary.StrategyID)
	assert.Equal(t, 0, summary.NumWindows)
	assert.Empty(t, summary.Windows)
	assert.InDelta(t, 0.5, summary.AvgTestSharpe, 1e-9)
}

func TestSummarizeWalkForwardReport_NilWindowsSkipped(t *testing.T) {
	report := &domain.WalkForwardReport{
		StrategyID: "test",
		Windows: []*domain.WalkForwardResult{
			{WindowIndex: 0, TrainSharpe: 1.0, TestSharpe: 0.8},
			nil, // should be skipped
			{WindowIndex: 2, TrainSharpe: 1.2, TestSharpe: 0.9},
		},
	}
	summary := summarizeWalkForwardReport(report)
	require.NotNil(t, summary)
	require.Len(t, summary.Windows, 2, "nil windows should be skipped")
	assert.Equal(t, 0, summary.Windows[0].WindowIndex)
	assert.Equal(t, 2, summary.Windows[1].WindowIndex)
}

// ─── L4 GateDecision table-driven tests ───────────────────────────────

// TestSummarizeWalkForwardReport_L4GateDecision verifies the L4 gate logic
// across the (AvgDegradation, AvgTestSharpe) space. AvgDegradation is the
// OOS/IS Sharpe ratio (higher = less overfit); the gate uses the
// complementary "gap" measure: gap = 1 - ratio.
//
//   - pass: gap <= 0.30 AND oosSharpe >= 0.30
//   - sharpe_gap_exceeded: gap > 0.30 (priority when both fail)
//   - low_oos_sharpe: gap <= 0.30 AND oosSharpe < 0.30
//
// Also verifies that L4 `passed` may legitimately differ from the engine's
// `overall_pass` field (which uses different thresholds).
func TestSummarizeWalkForwardReport_L4GateDecision(t *testing.T) {
	cases := []struct {
		name           string
		avgDegradation float64 // OOS/IS ratio
		avgTestSharpe  float64
		engineOverall  bool // engine's OverallPass field (informational)
		wantPassed     bool
		wantReason     string
	}{
		{
			name:           "pass: low overfit + strong OOS",
			avgDegradation: 0.80, // gap = 0.20
			avgTestSharpe:  1.0,
			engineOverall:  true,
			wantPassed:     true,
			wantReason:     GateReasonPassed,
		},
		{
			name:           "pass: just below gap boundary (ratio=0.71 → gap=0.29)",
			avgDegradation: 0.71, // gap = 0.29, safely below 0.30 threshold
			avgTestSharpe:  1.0,
			engineOverall:  true,
			wantPassed:     true,
			wantReason:     GateReasonPassed,
		},
		{
			name:           "pass: boundary oosSharpe=0.30",
			avgDegradation: 0.80,
			avgTestSharpe:  0.30,
			engineOverall:  false, // engine requires > 0.5, so engine fails
			wantPassed:     true,  // but L4 gate only requires >= 0.30
			wantReason:     GateReasonPassed,
		},
		{
			name:           "fail: high overfit (gap > 0.30)",
			avgDegradation: 0.50, // gap = 0.50 > 0.30
			avgTestSharpe:  1.0,
			engineOverall:  false,
			wantPassed:     false,
			wantReason:     GateReasonSharpeGapExceeded,
		},
		{
			name:           "fail: low OOS Sharpe (gap OK)",
			avgDegradation: 0.80, // gap = 0.20 <= 0.30
			avgTestSharpe:  0.20, // < 0.30
			engineOverall:  false,
			wantPassed:     false,
			wantReason:     GateReasonLowOOSSharpe,
		},
		{
			name:           "fail: both fail → prefers sharpe_gap_exceeded",
			avgDegradation: 0.40, // gap = 0.60 > 0.30
			avgTestSharpe:  0.10, // < 0.30
			engineOverall:  false,
			wantPassed:     false,
			wantReason:     GateReasonSharpeGapExceeded,
		},
		{
			name:           "fail: zero everything (empty report)",
			avgDegradation: 0.0, // gap = 1.0 > 0.30
			avgTestSharpe:  0.0, // < 0.30
			engineOverall:  false,
			wantPassed:     false,
			wantReason:     GateReasonSharpeGapExceeded,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := &domain.WalkForwardReport{
				StrategyID:     "test",
				AvgTestSharpe:  tc.avgTestSharpe,
				AvgDegradation: tc.avgDegradation,
				OverallPass:    tc.engineOverall,
			}
			summary := summarizeWalkForwardReport(report)
			require.NotNil(t, summary)
			assert.Equal(t, "L4", summary.Level)
			assert.Equal(t, tc.wantPassed, summary.Passed,
				"passed: degr=%.3f oosSharpe=%.3f", tc.avgDegradation, tc.avgTestSharpe)
			assert.Equal(t, tc.wantReason, summary.Reason,
				"reason: degr=%.3f oosSharpe=%.3f", tc.avgDegradation, tc.avgTestSharpe)
			assert.NotEmpty(t, summary.Recommendation, "recommendation should always be non-empty")
			// Engine's OverallPass should be preserved as-is (informational).
			assert.Equal(t, tc.engineOverall, summary.OverallPass,
				"overall_pass should be preserved from engine without modification")
		})
	}
}

// TestSummarizeWalkForwardReport_L4GateDecision_NaNDegradation covers the
// NaN-safe path: if AvgDegradation is NaN (e.g. all windows had zero
// train Sharpe), the gate fails with gap=1.0 → sharpe_gap_exceeded.
func TestSummarizeWalkForwardReport_L4GateDecision_NaNDegradation(t *testing.T) {
	report := &domain.WalkForwardReport{
		StrategyID:     "test",
		AvgTestSharpe:  1.0,
		AvgDegradation: math.NaN(),
		OverallPass:    false,
	}
	summary := summarizeWalkForwardReport(report)
	require.NotNil(t, summary)
	assert.Equal(t, "L4", summary.Level)
	assert.False(t, summary.Passed, "NaN degradation should fail L4")
	assert.Equal(t, GateReasonSharpeGapExceeded, summary.Reason,
		"NaN degradation → gap=1.0 > 0.30 → sharpe_gap_exceeded")
}

// ─── Construction ───────────────────────────────────────────────────────

func TestNewWalkForwardValidateTool_NilRunnerPanics(t *testing.T) {
	assert.Panics(t, func() {
		NewWalkForwardValidateTool(nil)
	})
}

// ─── Registry integration ──────────────────────────────────────────────

func TestWalkForwardValidateTool_RegisterInRegistry(t *testing.T) {
	tt := NewWalkForwardValidateTool(&mockWFRunner{})
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(tt))

	got, err := reg.Get("walk_forward_validate")
	require.NoError(t, err)
	assert.Equal(t, "walk_forward_validate", got.Name())

	info := reg.List()
	require.Len(t, info, 1)
	assert.Equal(t, "walk_forward_validate", info[0].Name)
	assert.Len(t, info[0].Parameters, 6)
}
