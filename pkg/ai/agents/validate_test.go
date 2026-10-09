package agents

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading/pkg/expression"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDataProvider is a minimal DataProvider for testing.
type mockDataProvider struct {
	data map[string]map[string][]float64
}

func (m *mockDataProvider) GetField(symbol, field string, lookback int) ([]float64, error) {
	if s, ok := m.data[symbol]; ok {
		if f, ok := s[field]; ok {
			n := len(f)
			if lookback > 0 && lookback < n {
				return f[n-lookback:], nil
			}
			return f, nil
		}
	}
	return nil, nil
}

func (m *mockDataProvider) GetSymbols() []string {
	syms := make([]string, 0, len(m.data))
	for k := range m.data {
		syms = append(syms, k)
	}
	return syms
}

// Fields 声明该测试替身能供应的字段（OBS-08 切片 1 接口要求）。
func (m *mockDataProvider) Fields() []string {
	return []string{"close", "high", "low", "open", "volume"}
}

func newMockProvider() *mockDataProvider {
	return &mockDataProvider{
		data: map[string]map[string][]float64{
			"AAPL": {
				"close":  {150.0, 151.0, 152.0, 153.0, 154.0},
				"open":   {149.0, 150.5, 151.5, 152.5, 153.5},
				"high":   {151.0, 152.0, 153.0, 154.0, 155.0},
				"low":    {148.0, 149.5, 150.5, 151.5, 152.5},
				"volume": {1e6, 1.1e6, 1.2e6, 1.3e6, 1.4e6},
			},
			"GOOGL": {
				"close":  {2800.0, 2810.0, 2820.0, 2830.0, 2840.0},
				"open":   {2790.0, 2805.0, 2815.0, 2825.0, 2835.0},
				"high":   {2810.0, 2820.0, 2830.0, 2840.0, 2850.0},
				"low":    {2780.0, 2795.0, 2805.0, 2815.0, 2825.0},
				"volume": {5e5, 5.5e5, 6e5, 6.5e5, 7e5},
			},
		},
	}
}

// ---- L1 Validation Tests ----

func TestValidateAgent_NewValidateAgent(t *testing.T) {
	agent := NewValidateAgent(nil)
	if agent == nil {
		t.Fatal("NewValidateAgent returned nil")
	}
	if agent.parser == nil {
		t.Error("parser should not be nil")
	}
	if agent.icCalc == nil {
		t.Error("icCalc should not be nil")
	}
}

func TestValidateAgent_NewValidateAgentWithBacktest(t *testing.T) {
	agent := NewValidateAgentWithBacktest(nil, nil)
	if agent == nil {
		t.Fatal("NewValidateAgentWithBacktest returned nil")
	}
	if agent.btClient != nil {
		t.Error("btClient should be nil when not provided")
	}
}

func TestValidate_L1Syntax_ValidFormula(t *testing.T) {
	agent := NewValidateAgent(nil)
	result, err := agent.Validate(context.Background(), "ts_mean(close, 5)", L1Syntax)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got: %v", result.Errors)
	}
	if result.Level != L1Syntax {
		t.Errorf("expected level L1Syntax, got %v", result.Level)
	}
	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}
}

func TestValidate_L1Syntax_InvalidFormula(t *testing.T) {
	agent := NewValidateAgent(nil)
	result, err := agent.Validate(context.Background(), "ts_mean(", L1Syntax)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if len(result.Errors) == 0 {
		t.Error("expected parse errors for invalid formula")
	}
}

func TestValidate_L1Syntax_ZeroInputs(t *testing.T) {
	agent := NewValidateAgent(nil)
	// A formula with no market data inputs (pure literals)
	result, err := agent.Validate(context.Background(), "5 + 3", L1Syntax)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning about zero data inputs")
	}
}

func TestValidate_L1Syntax_HighComplexity(t *testing.T) {
	agent := NewValidateAgent(nil)
	// A deeply nested formula with many nodes (> 10)
	formula := "ts_mean(close, 5) + ts_std(close, 10) * (open - close) / volume"
	result, err := agent.Validate(context.Background(), formula, L1Syntax)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning about high complexity")
	}
}

// ---- L2 Validation Tests ----

func TestValidate_L2QuickEval_Success(t *testing.T) {
	provider := newMockProvider()
	agent := NewValidateAgent(provider)
	result, err := agent.Validate(context.Background(), "ts_mean(close, 3)", L2QuickEval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got: %v", result.Errors)
	}
	if result.Score < 2.0 {
		t.Errorf("expected score >= 2.0, got %f", result.Score)
	}
}

func TestValidate_L2QuickEval_NoProvider(t *testing.T) {
	agent := NewValidateAgent(nil) // No data provider
	result, err := agent.Validate(context.Background(), "ts_mean(close, 3)", L2QuickEval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning about no data provider")
	}
}

func TestValidate_L2_ZeroVariance(t *testing.T) {
	// A constant formula produces zero variance
	provider := newMockProvider()
	agent := NewValidateAgent(provider)
	result, err := agent.Validate(context.Background(), "100.0", L2QuickEval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Error("expected zero variance error for constant formula")
	}
}

func TestValidate_L2_InvalidFormula(t *testing.T) {
	provider := newMockProvider()
	agent := NewValidateAgent(provider)
	result, err := agent.Validate(context.Background(), "ts_mean(", L2QuickEval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Error("expected parse error for invalid formula at L2")
	}
}

// ---- ValidateGene Tests ----

func TestValidateGene_Success(t *testing.T) {
	provider := newMockProvider()
	agent := NewValidateAgent(provider)
	gene := &gene_pool.FactorGene{
		ID:      "gene-001",
		Formula: "ts_mean(close, 3)",
		Status:  "pending",
	}
	result, err := agent.ValidateGene(context.Background(), gene, L1Syntax)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if gene.Status != "validated" {
		t.Errorf("expected gene status 'validated', got '%s'", gene.Status)
	}
	if gene.IC != result.IC {
		t.Errorf("expected gene.IC == result.IC, got %f != %f", gene.IC, result.IC)
	}
}

func TestValidateGene_Rejected(t *testing.T) {
	agent := NewValidateAgent(nil) // No provider
	gene := &gene_pool.FactorGene{
		ID:      "gene-002",
		Formula: "ts_mean(", // Invalid formula
		Status:  "pending",
	}
	result, err := agent.ValidateGene(context.Background(), gene, L1Syntax)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if gene.Status != "rejected" {
		t.Errorf("expected gene status 'rejected', got '%s'", gene.Status)
	}
}

// ---- ComputeFitness / BatchValidate Tests ----

func TestValidate_ComputeFitness_NoClient(t *testing.T) {
	agent := NewValidateAgent(nil)
	gene := &gene_pool.FactorGene{
		ID:      "gene-003",
		Formula: "ts_mean(close, 5)",
	}
	result, err := agent.ValidateGene(context.Background(), gene, L3StandardBacktest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning about no backtest client for L3")
	}
}

func TestValidate_BatchValidate_NoClient(t *testing.T) {
	agent := NewValidateAgent(nil)
	genes := []*gene_pool.FactorGene{
		{ID: "gene-1", Formula: "ts_mean(close, 5)"},
		{ID: "gene-2", Formula: "ts_std(volume, 10)"},
	}
	result, err := agent.BatchValidate(context.Background(), genes, L1Syntax)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != len(genes) {
		t.Errorf("expected %d results, got %d", len(genes), len(result))
	}
	for _, g := range genes {
		if g.Status != "validated" && g.Status != "rejected" {
			t.Errorf("gene %s has unexpected status: %s", g.ID, g.Status)
		}
	}
}

// ---- L3/L4 Validation Tests ----

func TestValidate_L3NoClient(t *testing.T) {
	agent := NewValidateAgent(nil)
	result, err := agent.Validate(context.Background(), "ts_mean(close, 5)", L3StandardBacktest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning about no backtest client at L3")
	}
}

func TestValidate_L4NoClient(t *testing.T) {
	agent := NewValidateAgent(nil)
	result, err := agent.Validate(context.Background(), "ts_mean(close, 5)", L4WalkForward)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning about no backtest client at L4")
	}
}

// ---- EstimateComplexity Tests ----

func TestEstimateComplexity(t *testing.T) {
	tests := []struct {
		formula string
		wantGt  int
	}{
		{"close", 0},
		{"5 + 3", 0},
		{"ts_mean(close, 5)", 1},
		{"ts_mean(close, 5) + ts_std(close, 10)", 2},
		{"ts_mean(close, 5) * (open - close)", 3},
	}

	for _, tc := range tests {
		t.Run(tc.formula, func(t *testing.T) {
			expr, err := expressionParser.Parse(tc.formula)
			if err != nil {
				t.Skipf("skipping parseable formula (complexity not easily predictable): %v", err)
			}
			complexity := estimateComplexity(expr.AST)
			if complexity <= tc.wantGt {
				t.Errorf("complexity of %q: got %d, want > %d", tc.formula, complexity, tc.wantGt)
			}
		})
	}
}

// expressionParser is a package-level parser for use in tests.
var expressionParser = expression.NewParser()

// ---- S7-P0-3 (ODR-043-3): ValidateAgent L3 default stock pool ----

// hasAShareSuffix returns true if the symbol looks like an A-share
// stock code (e.g. "000001.SZ", "600519.SH"). Used by the S7-P0-3
// tests to confirm the default pool is no longer US tickers.
func hasAShareSuffix(sym string) bool {
	return strings.HasSuffix(sym, ".SZ") || strings.HasSuffix(sym, ".SH")
}

// TestValidateAgent_DefaultStockPoolIsAShare verifies that the default
// L3 backtest stock pool contains A-share stock codes (suffixed .SZ or
// .SH), not US tickers. Before S7-P0-3 the pool was hardcoded to
// ["AAPL","GOOGL","MSFT"] which cannot resolve in an A-share backtest
// engine and produced misleading L3 validation failures.
func TestValidateAgent_DefaultStockPoolIsAShare(t *testing.T) {
	agent := NewValidateAgent(nil)
	require.NotEmpty(t, agent.defaultStockPool, "default stock pool must not be empty")

	for _, sym := range agent.defaultStockPool {
		assert.True(t, hasAShareSuffix(sym),
			"default stock pool symbol %q must be an A-share code (.SZ/.SH), not a US ticker", sym)
	}
}

// TestValidateAgent_DefaultStockPoolHasNoUSTickers is a direct check
// that the three US tickers removed by S7-P0-3 are absent from the
// default pool.
func TestValidateAgent_DefaultStockPoolHasNoUSTickers(t *testing.T) {
	agent := NewValidateAgent(nil)
	for _, sym := range agent.defaultStockPool {
		assert.NotContains(t, []string{"AAPL", "GOOGL", "MSFT"}, sym,
			"default stock pool must not contain US tickers after S7-P0-3")
	}
}

// TestValidateAgent_SetStockPool_Override verifies that the stock pool
// can be overridden (e.g. for testing or when a caller wants a
// different validation universe). Consistent with the existing
// SetBacktestRunner / SetL4Config override pattern.
func TestValidateAgent_SetStockPool_Override(t *testing.T) {
	agent := NewValidateAgent(nil)
	custom := []string{"000333.SZ", "601318.SH"}
	agent.SetStockPool(custom)

	require.Equal(t, custom, agent.defaultStockPool,
		"SetStockPool must override the default pool")
}

// TestValidateAgent_SourceHasNoUSStockDefaults is a regression guard
// scanning validate.go source: the US tickers must not appear as
// string literals in the source (they were removed by S7-P0-3). If
// this test fails, someone re-introduced the US-stock default.
func TestValidateAgent_SourceHasNoUSStockDefaults(t *testing.T) {
	source, err := os.ReadFile("validate.go")
	require.NoError(t, err, "must be able to read validate.go from the test working dir")

	src := string(source)
	for _, ticker := range []string{`"AAPL"`, `"GOOGL"`, `"MSFT"`} {
		assert.NotContains(t, src, ticker,
			"validate.go must not contain US ticker %s as a string literal; use a.defaultStockPool instead", ticker)
	}
}
