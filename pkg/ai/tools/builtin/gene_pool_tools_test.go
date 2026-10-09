package builtin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
)

// ─── mock FactorPoolClient ──────────────────────────────────────────────

type mockFactorPool struct {
	factors []*gene_pool.FactorGene
	err     error

	lastCategory string
	lastStatus   string
	lastMinIC    float64
	lastLimit    int

	savedGene *gene_pool.FactorGene
	saveErr   error
}

func (m *mockFactorPool) List(ctx context.Context, category, status string, minIC float64, limit int) ([]*gene_pool.FactorGene, error) {
	m.lastCategory = category
	m.lastStatus = status
	m.lastMinIC = minIC
	m.lastLimit = limit
	if m.err != nil {
		return nil, m.err
	}
	return m.factors, nil
}

func (m *mockFactorPool) Save(ctx context.Context, gene *gene_pool.FactorGene) error {
	m.savedGene = gene
	return m.saveErr
}

// ─── mock StrategyPoolClient ────────────────────────────────────────────

type mockStrategyPool struct {
	strategies []*gene_pool.StrategyGene
	err        error

	lastType   string
	lastStatus string
	lastMinFit float64
	lastLimit  int

	savedGene *gene_pool.StrategyGene
	saveErr   error

	// byID holds strategies returned by Get. If a requested ID is not
	// present here, Get returns getErr (defaulting to pgx-no-rows-like
	// error). Tests for GetStrategyLineageTool populate this map.
	byID   map[string]*gene_pool.StrategyGene
	getErr error
}

func (m *mockStrategyPool) List(ctx context.Context, strategyType, status string, minFitness float64, limit int) ([]*gene_pool.StrategyGene, error) {
	m.lastType = strategyType
	m.lastStatus = status
	m.lastMinFit = minFitness
	m.lastLimit = limit
	if m.err != nil {
		return nil, m.err
	}
	return m.strategies, nil
}

func (m *mockStrategyPool) Save(ctx context.Context, gene *gene_pool.StrategyGene) error {
	m.savedGene = gene
	return m.saveErr
}

// Get returns the strategy stored under byID[id], or getErr if not found.
// A nil getErr with no entry returns a sentinel "not found" error so
// callers can distinguish "ID absent" from "ID present but pool broken".
func (m *mockStrategyPool) Get(ctx context.Context, id string) (*gene_pool.StrategyGene, error) {
	if m.byID != nil {
		if gene, ok := m.byID[id]; ok {
			return gene, nil
		}
	}
	if m.getErr != nil {
		return nil, m.getErr
	}
	return nil, fmt.Errorf("get strategy gene: not found (id=%q)", id)
}

// ═══════════════════════════════════════════════════════════════════════
//  ListFactorsTool tests
// ═══════════════════════════════════════════════════════════════════════

func TestListFactorsTool_Name(t *testing.T) {
	tt := NewListFactorsTool(&mockFactorPool{})
	assert.Equal(t, "list_factors", tt.Name())
}

func TestListFactorsTool_Description(t *testing.T) {
	tt := NewListFactorsTool(&mockFactorPool{})
	assert.NotEmpty(t, tt.Description())
	assert.Contains(t, tt.Description(), "factor")
}

func TestListFactorsTool_Parameters(t *testing.T) {
	tt := NewListFactorsTool(&mockFactorPool{})
	params := tt.Parameters()
	require.Len(t, params, 3)
	// All optional.
	for _, p := range params {
		assert.False(t, p.Required, "param %q should be optional", p.Name)
	}
	assert.Equal(t, "category", params[0].Name)
	assert.Equal(t, "min_ic", params[1].Name)
	assert.Equal(t, "limit", params[2].Name)
	assert.Equal(t, 20, params[2].Default)
}

func TestListFactorsTool_OutputSchema(t *testing.T) {
	tt := NewListFactorsTool(&mockFactorPool{})
	schema := tt.OutputSchema()
	assert.Equal(t, "array", schema.Type)
	assert.NotEmpty(t, schema.Description)
}

func TestListFactorsTool_Execute_HappyPath(t *testing.T) {
	canned := []*gene_pool.FactorGene{
		{ID: "fg_1", Name: "momentum_20d", Category: "momentum", IC: 0.045, Fitness: 0.8},
		{ID: "fg_2", Name: "value_pe", Category: "value", IC: 0.038, Fitness: 0.7},
	}
	pool := &mockFactorPool{factors: canned}
	tt := NewListFactorsTool(pool)

	args := map[string]interface{}{}
	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)

	factors, ok := result.([]*gene_pool.FactorGene)
	require.True(t, ok, "result should be []*gene_pool.FactorGene, got %T", result)
	assert.Len(t, factors, 2)

	// Verify default args passed to pool.
	assert.Equal(t, "", pool.lastCategory, "default category should be empty")
	assert.Equal(t, 0.0, pool.lastMinIC, "default min_ic should be 0")
	assert.Equal(t, 20, pool.lastLimit, "default limit should be 20")
}

func TestListFactorsTool_Execute_WithFilters(t *testing.T) {
	pool := &mockFactorPool{}
	tt := NewListFactorsTool(pool)

	args := map[string]interface{}{
		"category": "momentum",
		"min_ic":   0.03,
		"limit":    5,
	}
	_, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	assert.Equal(t, "momentum", pool.lastCategory)
	assert.InDelta(t, 0.03, pool.lastMinIC, 1e-9)
	assert.Equal(t, 5, pool.lastLimit)
}

func TestListFactorsTool_Execute_EmptyResult(t *testing.T) {
	pool := &mockFactorPool{factors: nil}
	tt := NewListFactorsTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	factors, ok := result.([]*gene_pool.FactorGene)
	require.True(t, ok)
	// nil from pool → empty (non-nil) slice for JSON.
	assert.Len(t, factors, 0)
}

func TestListFactorsTool_Execute_PoolError(t *testing.T) {
	pool := &mockFactorPool{err: errors.New("db connection lost")}
	tt := NewListFactorsTool(pool)

	_, err := tt.Execute(context.Background(), map[string]interface{}{})
	require.Error(t, err)
	assert.False(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "list_factors")
}

func TestListFactorsTool_Execute_WrongTypeMinIC(t *testing.T) {
	tt := NewListFactorsTool(&mockFactorPool{})
	args := map[string]interface{}{"min_ic": "0.03"}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

func TestNewListFactorsTool_NilPoolPanics(t *testing.T) {
	assert.Panics(t, func() { NewListFactorsTool(nil) })
}

func TestListFactorsTool_RegisterInRegistry(t *testing.T) {
	tt := NewListFactorsTool(&mockFactorPool{})
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(tt))
	got, err := reg.Get("list_factors")
	require.NoError(t, err)
	assert.Equal(t, "list_factors", got.Name())
}

// ═══════════════════════════════════════════════════════════════════════
//  ListStrategiesTool tests
// ═══════════════════════════════════════════════════════════════════════

func TestListStrategiesTool_Name(t *testing.T) {
	tt := NewListStrategiesTool(&mockStrategyPool{})
	assert.Equal(t, "list_strategies", tt.Name())
}

func TestListStrategiesTool_Description(t *testing.T) {
	tt := NewListStrategiesTool(&mockStrategyPool{})
	assert.NotEmpty(t, tt.Description())
	assert.Contains(t, tt.Description(), "strategy")
}

func TestListStrategiesTool_Parameters(t *testing.T) {
	tt := NewListStrategiesTool(&mockStrategyPool{})
	params := tt.Parameters()
	require.Len(t, params, 3)
	for _, p := range params {
		assert.False(t, p.Required)
	}
	assert.Equal(t, "strategy_type", params[0].Name)
	assert.Equal(t, "min_fitness", params[1].Name)
	assert.Equal(t, "limit", params[2].Name)
}

func TestListStrategiesTool_Execute_HappyPath(t *testing.T) {
	canned := []*gene_pool.StrategyGene{
		{ID: "sg_1", Name: "dual_momentum", Sharpe: 1.2, Fitness: 0.85},
	}
	pool := &mockStrategyPool{strategies: canned}
	tt := NewListStrategiesTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	strategies, ok := result.([]*gene_pool.StrategyGene)
	require.True(t, ok)
	assert.Len(t, strategies, 1)
	assert.Equal(t, 20, pool.lastLimit)
}

func TestListStrategiesTool_Execute_WithFilters(t *testing.T) {
	pool := &mockStrategyPool{}
	tt := NewListStrategiesTool(pool)

	args := map[string]interface{}{
		"strategy_type": "expression",
		"min_fitness":   0.5,
		"limit":         10,
	}
	_, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	assert.Equal(t, "expression", pool.lastType)
	assert.InDelta(t, 0.5, pool.lastMinFit, 1e-9)
	assert.Equal(t, 10, pool.lastLimit)
}

func TestListStrategiesTool_Execute_PoolError(t *testing.T) {
	pool := &mockStrategyPool{err: errors.New("db error")}
	tt := NewListStrategiesTool(pool)
	_, err := tt.Execute(context.Background(), map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list_strategies")
}

func TestNewListStrategiesTool_NilPoolPanics(t *testing.T) {
	assert.Panics(t, func() { NewListStrategiesTool(nil) })
}

func TestListStrategiesTool_RegisterInRegistry(t *testing.T) {
	tt := NewListStrategiesTool(&mockStrategyPool{})
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(tt))
	got, err := reg.Get("list_strategies")
	require.NoError(t, err)
	assert.Equal(t, "list_strategies", got.Name())
}

// ═══════════════════════════════════════════════════════════════════════
//  SaveFactorTool tests
// ═══════════════════════════════════════════════════════════════════════

func TestSaveFactorTool_Name(t *testing.T) {
	tt := NewSaveFactorTool(&mockFactorPool{})
	assert.Equal(t, "save_factor", tt.Name())
}

func TestSaveFactorTool_Description(t *testing.T) {
	tt := NewSaveFactorTool(&mockFactorPool{})
	assert.NotEmpty(t, tt.Description())
}

func TestSaveFactorTool_Parameters(t *testing.T) {
	tt := NewSaveFactorTool(&mockFactorPool{})
	params := tt.Parameters()
	require.Len(t, params, 8)

	required := map[string]bool{"name": true, "category": true, "formula": true, "description": true, "ic": true, "turnover": true}
	for _, p := range params {
		if required[p.Name] {
			assert.True(t, p.Required, "param %q should be required", p.Name)
		} else {
			assert.False(t, p.Required, "param %q should be optional", p.Name)
		}
	}
}

func TestSaveFactorTool_OutputSchema(t *testing.T) {
	tt := NewSaveFactorTool(&mockFactorPool{})
	schema := tt.OutputSchema()
	assert.Equal(t, "object", schema.Type)
	require.Len(t, schema.Fields, 1)
	assert.Equal(t, "factor_id", schema.Fields[0].Name)
}

func TestSaveFactorTool_Execute_HappyPath(t *testing.T) {
	pool := &mockFactorPool{}
	tt := NewSaveFactorTool(pool)

	args := map[string]interface{}{
		"name":        "momentum_20d",
		"category":    "momentum",
		"formula":     "ts_rank(close, 20)",
		"description": "20-day price momentum rank.",
		"ic":          0.045,
		"turnover":    0.15,
	}
	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)

	m, ok := result.(map[string]interface{})
	require.True(t, ok)
	factorID, ok := m["factor_id"].(string)
	require.True(t, ok, "factor_id should be string")
	assert.Contains(t, factorID, "fg_", "factor_id should start with 'fg_'")

	// Verify the gene was saved with correct fields.
	require.NotNil(t, pool.savedGene)
	assert.Equal(t, "momentum_20d", pool.savedGene.Name)
	assert.Equal(t, "ts_rank(close, 20)", pool.savedGene.Formula)
	assert.InDelta(t, 0.045, pool.savedGene.IC, 1e-9)
	assert.Equal(t, "draft", pool.savedGene.Status)
	assert.Equal(t, 0, pool.savedGene.Generation)
	assert.Empty(t, pool.savedGene.Rationale, "rationale not provided → empty")
}

func TestSaveFactorTool_Execute_WithOptionalFields(t *testing.T) {
	pool := &mockFactorPool{}
	tt := NewSaveFactorTool(pool)

	args := map[string]interface{}{
		"name":        "value_pe",
		"category":    "value",
		"formula":     "1.0 / pe",
		"description": "Earnings yield.",
		"rationale":   "Low PE stocks tend to outperform.",
		"ic":          0.038,
		"turnover":    0.08,
		"sharpe":      0.92,
	}
	_, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	require.NotNil(t, pool.savedGene)
	assert.Equal(t, "Low PE stocks tend to outperform.", pool.savedGene.Rationale)
	assert.InDelta(t, 0.92, pool.savedGene.Sharpe, 1e-9)
}

func TestSaveFactorTool_Execute_MissingName(t *testing.T) {
	tt := NewSaveFactorTool(&mockFactorPool{})
	args := map[string]interface{}{
		"category": "momentum", "formula": "close", "description": "x", "ic": 0.04, "turnover": 0.1,
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "name")
}

func TestSaveFactorTool_Execute_MissingFormula(t *testing.T) {
	tt := NewSaveFactorTool(&mockFactorPool{})
	args := map[string]interface{}{
		"name": "x", "category": "momentum", "description": "x", "ic": 0.04, "turnover": 0.1,
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "formula")
}

func TestSaveFactorTool_Execute_MissingIC(t *testing.T) {
	tt := NewSaveFactorTool(&mockFactorPool{})
	args := map[string]interface{}{
		"name": "x", "category": "momentum", "formula": "close", "description": "x", "turnover": 0.1,
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "ic")
}

func TestSaveFactorTool_Execute_PoolError(t *testing.T) {
	pool := &mockFactorPool{saveErr: errors.New("duplicate key")}
	tt := NewSaveFactorTool(pool)
	args := map[string]interface{}{
		"name": "x", "category": "momentum", "formula": "close", "description": "x", "ic": 0.04, "turnover": 0.1,
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.False(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "save_factor")
}

func TestNewSaveFactorTool_NilPoolPanics(t *testing.T) {
	assert.Panics(t, func() { NewSaveFactorTool(nil) })
}

func TestSaveFactorTool_RegisterInRegistry(t *testing.T) {
	tt := NewSaveFactorTool(&mockFactorPool{})
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(tt))
	got, err := reg.Get("save_factor")
	require.NoError(t, err)
	assert.Equal(t, "save_factor", got.Name())
}

// ═══════════════════════════════════════════════════════════════════════
//  SaveStrategyTool tests
// ═══════════════════════════════════════════════════════════════════════

func TestSaveStrategyTool_Name(t *testing.T) {
	tt := NewSaveStrategyTool(&mockStrategyPool{})
	assert.Equal(t, "save_strategy", tt.Name())
}

func TestSaveStrategyTool_Description(t *testing.T) {
	tt := NewSaveStrategyTool(&mockStrategyPool{})
	assert.NotEmpty(t, tt.Description())
}

func TestSaveStrategyTool_Parameters(t *testing.T) {
	tt := NewSaveStrategyTool(&mockStrategyPool{})
	params := tt.Parameters()
	require.Len(t, params, 6)

	required := map[string]bool{"name": true, "strategy_yaml": true, "sharpe": true, "max_drawdown": true, "total_return": true}
	for _, p := range params {
		if required[p.Name] {
			assert.True(t, p.Required)
		} else {
			assert.False(t, p.Required)
		}
	}
}

func TestSaveStrategyTool_OutputSchema(t *testing.T) {
	tt := NewSaveStrategyTool(&mockStrategyPool{})
	schema := tt.OutputSchema()
	assert.Equal(t, "object", schema.Type)
	require.Len(t, schema.Fields, 1)
	assert.Equal(t, "strategy_id", schema.Fields[0].Name)
}

func TestSaveStrategyTool_Execute_HappyPath(t *testing.T) {
	pool := &mockStrategyPool{}
	tt := NewSaveStrategyTool(pool)

	args := map[string]interface{}{
		"name":          "dual_momentum_csi300",
		"strategy_yaml": "name: dual_momentum\nuniverse: csi300\n",
		"sharpe":        1.3,
		"max_drawdown":  -0.12,
		"total_return":  0.25,
	}
	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)

	m, ok := result.(map[string]interface{})
	require.True(t, ok)
	strategyID, ok := m["strategy_id"].(string)
	require.True(t, ok)
	assert.Contains(t, strategyID, "sg_")

	require.NotNil(t, pool.savedGene)
	assert.Equal(t, "dual_momentum_csi300", pool.savedGene.Name)
	assert.Equal(t, "name: dual_momentum\nuniverse: csi300\n", pool.savedGene.Code)
	assert.InDelta(t, 1.3, pool.savedGene.Sharpe, 1e-9)
	assert.Equal(t, "validated", pool.savedGene.Status)
	assert.Empty(t, pool.savedGene.FactorIDs, "factor_ids not provided → empty slice")
}

func TestSaveStrategyTool_Execute_WithFactorIDs(t *testing.T) {
	pool := &mockStrategyPool{}
	tt := NewSaveStrategyTool(pool)

	args := map[string]interface{}{
		"name":          "test",
		"strategy_yaml": "yaml",
		"factor_ids":    []string{"fg_1", "fg_2"},
		"sharpe":        1.0,
		"max_drawdown":  -0.1,
		"total_return":  0.15,
	}
	_, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	require.NotNil(t, pool.savedGene)
	assert.Equal(t, []string{"fg_1", "fg_2"}, pool.savedGene.FactorIDs)
}

func TestSaveStrategyTool_Execute_FactorIDsAsInterfaceSlice(t *testing.T) {
	// JSON-decoded form.
	pool := &mockStrategyPool{}
	tt := NewSaveStrategyTool(pool)

	args := map[string]interface{}{
		"name":          "test",
		"strategy_yaml": "yaml",
		"factor_ids":    []interface{}{"fg_1", "fg_2"},
		"sharpe":        1.0,
		"max_drawdown":  -0.1,
		"total_return":  0.15,
	}
	_, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	assert.Equal(t, []string{"fg_1", "fg_2"}, pool.savedGene.FactorIDs)
}

func TestSaveStrategyTool_Execute_MissingName(t *testing.T) {
	tt := NewSaveStrategyTool(&mockStrategyPool{})
	args := map[string]interface{}{
		"strategy_yaml": "yaml", "sharpe": 1.0, "max_drawdown": -0.1, "total_return": 0.15,
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "name")
}

func TestSaveStrategyTool_Execute_MissingYAML(t *testing.T) {
	tt := NewSaveStrategyTool(&mockStrategyPool{})
	args := map[string]interface{}{
		"name": "x", "sharpe": 1.0, "max_drawdown": -0.1, "total_return": 0.15,
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "strategy_yaml")
}

func TestSaveStrategyTool_Execute_MissingSharpe(t *testing.T) {
	tt := NewSaveStrategyTool(&mockStrategyPool{})
	args := map[string]interface{}{
		"name": "x", "strategy_yaml": "yaml", "max_drawdown": -0.1, "total_return": 0.15,
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "sharpe")
}

func TestSaveStrategyTool_Execute_PoolError(t *testing.T) {
	pool := &mockStrategyPool{saveErr: errors.New("db error")}
	tt := NewSaveStrategyTool(pool)
	args := map[string]interface{}{
		"name": "x", "strategy_yaml": "yaml", "sharpe": 1.0, "max_drawdown": -0.1, "total_return": 0.15,
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save_strategy")
}

func TestNewSaveStrategyTool_NilPoolPanics(t *testing.T) {
	assert.Panics(t, func() { NewSaveStrategyTool(nil) })
}

func TestSaveStrategyTool_RegisterInRegistry(t *testing.T) {
	tt := NewSaveStrategyTool(&mockStrategyPool{})
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(tt))
	got, err := reg.Get("save_strategy")
	require.NoError(t, err)
	assert.Equal(t, "save_strategy", got.Name())
}

// ═══════════════════════════════════════════════════════════════════════
//  Cross-tool: all 4 gene pool tools register together
// ═══════════════════════════════════════════════════════════════════════

func TestGenePoolTools_AllRegisterInRegistry(t *testing.T) {
	fp := &mockFactorPool{}
	sp := &mockStrategyPool{}

	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(NewListFactorsTool(fp)))
	require.NoError(t, reg.Register(NewListStrategiesTool(sp)))
	require.NoError(t, reg.Register(NewSaveFactorTool(fp)))
	require.NoError(t, reg.Register(NewSaveStrategyTool(sp)))

	list := reg.List()
	require.Len(t, list, 4)

	expectedNames := []string{"list_factors", "list_strategies", "save_factor", "save_strategy"}
	gotNames := make([]string, len(list))
	for i, info := range list {
		gotNames[i] = info.Name
	}
	assert.Equal(t, expectedNames, gotNames, "registry should list all 4 tools sorted by name")
}
