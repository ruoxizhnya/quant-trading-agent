package builtin

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// ─── mock BacktestRunner ──────────────────────────────────────────────

// mockRunner is a test double for contracts.BacktestRunner. It records
// the last call's arguments and returns either a canned result or an
// error, so tests can assert delegation behavior without spinning up
// a real backtest engine.
type mockRunner struct {
	result *domain.BacktestResult
	err    error

	// Recorded call arguments.
	lastStrategy string
	lastPool     []string
	lastStart    string
	lastEnd      string
	calls        int
}

func (m *mockRunner) RunBacktest(ctx context.Context, strategyName string, stockPool []string, startDate, endDate string) (*domain.BacktestResult, error) {
	m.calls++
	m.lastStrategy = strategyName
	m.lastPool = stockPool
	m.lastStart = startDate
	m.lastEnd = endDate
	if m.err != nil {
		return nil, m.err
	}
	if m.result != nil {
		return m.result, nil
	}
	return &domain.BacktestResult{TotalTrades: 5, TotalReturn: 0.12}, nil
}

// ─── Name / Description / Parameters ──────────────────────────────────

func TestBacktestTool_Name(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	assert.Equal(t, "backtest.run", tt.Name())
}

func TestBacktestTool_Description(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	desc := tt.Description()
	assert.NotEmpty(t, desc)
	assert.Contains(t, desc, "backtest")
}

func TestBacktestTool_Parameters(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	params := tt.Parameters()
	require.Len(t, params, 4)

	// All 4 are required.
	for _, p := range params {
		assert.True(t, p.Required, "parameter %q should be Required", p.Name)
	}

	// Names match the contract.
	names := []string{"strategy_name", "stock_pool", "start_date", "end_date"}
	gotNames := make([]string, len(params))
	for i, p := range params {
		gotNames[i] = p.Name
	}
	assert.Equal(t, names, gotNames)

	// Types are correct.
	assert.Equal(t, "string", params[0].Type)
	assert.Equal(t, "[]string", params[1].Type)
	assert.Equal(t, "string", params[2].Type)
	assert.Equal(t, "string", params[3].Type)
}

func TestBacktestTool_OutputSchema(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	schema := tt.OutputSchema()
	assert.Equal(t, "object", schema.Type)
	assert.NotEmpty(t, schema.Fields)
	// Verify a couple of expected fields are present.
	found := map[string]bool{}
	for _, f := range schema.Fields {
		found[f.Name] = true
	}
	assert.True(t, found["total_return"], "output schema should list total_return")
	assert.True(t, found["sharpe_ratio"], "output schema should list sharpe_ratio")
	assert.True(t, found["portfolio_values"], "output schema should list portfolio_values")
}

// ─── Execute happy path ───────────────────────────────────────────────

func TestBacktestTool_Execute_HappyPath(t *testing.T) {
	canned := &domain.BacktestResult{
		TotalTrades: 7,
		TotalReturn: 0.23,
		SharpeRatio: 1.5,
	}
	runner := &mockRunner{result: canned}
	tt := NewBacktestTool(runner)

	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ", "600000.SH"},
		"start_date":    "2022-01-01",
		"end_date":      "2024-01-01",
	}

	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Result should be the canned BacktestResult.
	btResult, ok := result.(*domain.BacktestResult)
	require.True(t, ok, "result should be *domain.BacktestResult, got %T", result)
	assert.Equal(t, 7, btResult.TotalTrades)
	assert.InDelta(t, 0.23, btResult.TotalReturn, 1e-9)

	// Runner should have been called with the exact args.
	assert.Equal(t, 1, runner.calls)
	assert.Equal(t, "momentum", runner.lastStrategy)
	assert.Equal(t, []string{"000001.SZ", "600000.SH"}, runner.lastPool)
	assert.Equal(t, "2022-01-01", runner.lastStart)
	assert.Equal(t, "2024-01-01", runner.lastEnd)
}

// Execute with stock_pool as []interface{} (the JSON-decoded form).
func TestBacktestTool_Execute_StockPoolAsInterfaceSlice(t *testing.T) {
	runner := &mockRunner{}
	tt := NewBacktestTool(runner)

	// json.Unmarshal decodes a JSON array into []interface{}.
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []interface{}{"000001.SZ", "600000.SH"},
		"start_date":    "2022-01-01",
		"end_date":      "2024-01-01",
	}

	_, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	assert.Equal(t, []string{"000001.SZ", "600000.SH"}, runner.lastPool)
}

// ─── Execute missing/wrong-typed args ─────────────────────────────────

func TestBacktestTool_Execute_MissingStrategyName(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	args := map[string]interface{}{
		"stock_pool": []string{"000001.SZ"},
		"start_date": "2022-01-01",
		"end_date":   "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "strategy_name")
}

func TestBacktestTool_Execute_MissingStockPool(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"start_date":    "2022-01-01",
		"end_date":      "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "stock_pool")
}

func TestBacktestTool_Execute_MissingStartDate(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
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

func TestBacktestTool_Execute_MissingEndDate(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2022-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "end_date")
}

func TestBacktestTool_Execute_EmptyStrategyName(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	args := map[string]interface{}{
		"strategy_name": "",
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2022-01-01",
		"end_date":      "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "strategy_name")
}

func TestBacktestTool_Execute_EmptyStockPool(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{},
		"start_date":    "2022-01-01",
		"end_date":      "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "stock_pool")
}

func TestBacktestTool_Execute_WrongTypeStrategyName(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	args := map[string]interface{}{
		"strategy_name": 42, // wrong type
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2022-01-01",
		"end_date":      "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "strategy_name")
}

func TestBacktestTool_Execute_WrongTypeStockPool(t *testing.T) {
	tt := NewBacktestTool(&mockRunner{})
	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    "000001.SZ", // string, not []string
		"start_date":    "2022-01-01",
		"end_date":      "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "stock_pool")
}

// ─── Execute runner error ─────────────────────────────────────────────

func TestBacktestTool_Execute_RunnerError(t *testing.T) {
	runnerErr := errors.New("strategy not found: momentum")
	runner := &mockRunner{err: runnerErr}
	tt := NewBacktestTool(runner)

	args := map[string]interface{}{
		"strategy_name": "momentum",
		"stock_pool":    []string{"000001.SZ"},
		"start_date":    "2022-01-01",
		"end_date":      "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	// Runner error should propagate (wrapped), NOT be ErrInvalidArgs
	// (args were valid; the failure is downstream).
	assert.True(t, errors.Is(err, runnerErr), "runner error should propagate verbatim")
	assert.False(t, errors.Is(err, tools.ErrInvalidArgs), "runner error must not be conflated with ErrInvalidArgs")
}

// ─── Construction ─────────────────────────────────────────────────────

func TestNewBacktestTool_NilRunnerPanics(t *testing.T) {
	// NewBacktestTool panics on nil runner — this is a programming
	// bug (wiring error in main.go), not a runtime condition.
	assert.Panics(t, func() {
		NewBacktestTool(nil)
	})
}

// ─── Tool interface compliance ────────────────────────────────────────

func TestBacktestTool_SatisfiesToolInterface(t *testing.T) {
	// Compile-time assertion via the var _ = ... pattern in backtest.go
	// already covers this. This test is a runtime sanity check that
	// the Tool can be registered and retrieved.
	tt := NewBacktestTool(&mockRunner{})
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(tt))

	got, err := reg.Get("backtest.run")
	require.NoError(t, err)
	assert.Equal(t, "backtest.run", got.Name())

	info := reg.List()
	require.Len(t, info, 1)
	assert.Equal(t, "backtest.run", info[0].Name)
	assert.Len(t, info[0].Parameters, 4)
}
