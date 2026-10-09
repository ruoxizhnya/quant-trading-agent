package builtin

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
)

// ─── Narrow interfaces for testability ─────────────────────────────────
//
// These match the subset of *gene_pool.FactorPool / *gene_pool.StrategyPool
// methods that the Tools need. Defining them locally (rather than using
// the concrete types) lets tests inject simple mocks without a real
// PostgreSQL pool. The concrete types satisfy these interfaces
// implicitly — no adapter is needed in setup.go.
//
// We don't promote these to pkg/backtest/contracts because the return types
// (gene_pool.FactorGene, gene_pool.StrategyGene) would force contracts
// to import gene_pool, breaking its leaf-package invariant.

// FactorPoolClient is the read+write contract for the factor gene pool.
type FactorPoolClient interface {
	List(ctx context.Context, category, status string, minIC float64, limit int) ([]*gene_pool.FactorGene, error)
	Save(ctx context.Context, gene *gene_pool.FactorGene) error
}

// StrategyPoolClient is the read+write contract for the strategy gene pool.
type StrategyPoolClient interface {
	List(ctx context.Context, strategyType, status string, minFitness float64, limit int) ([]*gene_pool.StrategyGene, error)
	Save(ctx context.Context, gene *gene_pool.StrategyGene) error
	// Get retrieves a single strategy gene by ID. Returns the wrapped
	// pgx.ErrNoRows (or equivalent) when the ID is not in the pool —
	// callers should treat any error as "not found" for their domain.
	// Required by GetStrategyLineageTool (Hermes Phase 2.2).
	Get(ctx context.Context, id string) (*gene_pool.StrategyGene, error)
}

// Compile-time assertions that the concrete pool types satisfy the interfaces.
var (
	_ FactorPoolClient   = (*gene_pool.FactorPool)(nil)
	_ StrategyPoolClient = (*gene_pool.StrategyPool)(nil)
)

// ═══════════════════════════════════════════════════════════════════════
//  ListFactorsTool
// ═══════════════════════════════════════════════════════════════════════

// ListFactorsTool queries the factor gene pool with optional filters.
// Returns factors sorted by fitness (descending) — the most promising first.
//
// Tool name: "list_factors"
// Input: category (optional), min_ic (optional, default 0), limit (optional, default 20)
// Output: []*gene_pool.FactorGene (JSON-serializable array)
type ListFactorsTool struct {
	pool FactorPoolClient
}

var _ tools.Tool = (*ListFactorsTool)(nil)

func NewListFactorsTool(pool FactorPoolClient) *ListFactorsTool {
	if pool == nil {
		panic("builtin: NewListFactorsTool called with nil FactorPoolClient")
	}
	return &ListFactorsTool{pool: pool}
}

func (t *ListFactorsTool) Name() string { return "list_factors" }

func (t *ListFactorsTool) Description() string {
	return "Query the factor gene pool. Returns factors sorted by fitness (descending). Filter by category (e.g. 'momentum', 'value') and minimum IC. Use this before creating a new factor to avoid duplicates."
}

func (t *ListFactorsTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "category",
			Type:        "string",
			Description: "Filter by factor category (e.g. 'momentum', 'value', 'quality'). Empty = all categories.",
			Required:    false,
		},
		{
			Name:        "min_ic",
			Type:        "float",
			Description: "Minimum IC (Information Coefficient) threshold. 0.0 = no filter.",
			Required:    false,
			Default:     0.0,
		},
		{
			Name:        "limit",
			Type:        "int",
			Description: "Maximum number of factors to return.",
			Required:    false,
			Default:     20,
		},
	}
}

func (t *ListFactorsTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "array",
		Description: "List of factor genes, sorted by fitness descending. Each element has: id, name, category, formula, ic, ir, turnover, sharpe, fitness, generation, status.",
	}
}

func (t *ListFactorsTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	category, err := optionalString(args, "category")
	if err != nil {
		return nil, err
	}
	minIC, err := optionalFloat(args, "min_ic")
	if err != nil {
		return nil, err
	}
	limit, err := optionalInt(args, "limit", 20)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}

	// status="" means no status filter.
	factors, err := t.pool.List(ctx, category, "", minIC, limit)
	if err != nil {
		return nil, fmt.Errorf("list_factors: %w", err)
	}
	if factors == nil {
		return []*gene_pool.FactorGene{}, nil
	}
	return factors, nil
}

// ═══════════════════════════════════════════════════════════════════════
//  ListStrategiesTool
// ═══════════════════════════════════════════════════════════════════════

// ListStrategiesTool queries the strategy gene pool with optional filters.
// Returns strategies sorted by fitness (descending).
//
// Design deviation (hermes-agent-integration-system-design.md §3.2):
// the design doc specifies `min_sharpe` as the filter parameter, but
// StrategyPool.List filters on `minFitness` (a composite score). We use
// `min_fitness` as the parameter name to accurately reflect the filter.
//
// Tool name: "list_strategies"
// Input: strategy_type (optional), min_fitness (optional, default 0), limit (optional, default 20)
// Output: []*gene_pool.StrategyGene (JSON-serializable array)
type ListStrategiesTool struct {
	pool StrategyPoolClient
}

var _ tools.Tool = (*ListStrategiesTool)(nil)

func NewListStrategiesTool(pool StrategyPoolClient) *ListStrategiesTool {
	if pool == nil {
		panic("builtin: NewListStrategiesTool called with nil StrategyPoolClient")
	}
	return &ListStrategiesTool{pool: pool}
}

func (t *ListStrategiesTool) Name() string { return "list_strategies" }

func (t *ListStrategiesTool) Description() string {
	return "Query the strategy gene pool. Returns strategies sorted by fitness (descending). Filter by strategy_type (e.g. 'expression', 'momentum') and minimum fitness. Use this to find existing strategies before creating a new one."
}

func (t *ListStrategiesTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "strategy_type",
			Type:        "string",
			Description: "Filter by strategy type (e.g. 'expression', 'momentum'). Empty = all types.",
			Required:    false,
		},
		{
			Name:        "min_fitness",
			Type:        "float",
			Description: "Minimum fitness score threshold. 0.0 = no filter.",
			Required:    false,
			Default:     0.0,
		},
		{
			Name:        "limit",
			Type:        "int",
			Description: "Maximum number of strategies to return.",
			Required:    false,
			Default:     20,
		},
	}
}

func (t *ListStrategiesTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "array",
		Description: "List of strategy genes, sorted by fitness descending. Each element has: id, name, description, strategy_type, code (YAML config), params, factor_ids, total_return, sharpe, max_drawdown, win_rate, fitness, generation, status.",
	}
}

func (t *ListStrategiesTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	strategyType, err := optionalString(args, "strategy_type")
	if err != nil {
		return nil, err
	}
	minFitness, err := optionalFloat(args, "min_fitness")
	if err != nil {
		return nil, err
	}
	limit, err := optionalInt(args, "limit", 20)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}

	strategies, err := t.pool.List(ctx, strategyType, "", minFitness, limit)
	if err != nil {
		return nil, fmt.Errorf("list_strategies: %w", err)
	}
	if strategies == nil {
		return []*gene_pool.StrategyGene{}, nil
	}
	return strategies, nil
}

// ═══════════════════════════════════════════════════════════════════════
//  SaveFactorTool
// ═══════════════════════════════════════════════════════════════════════

// SaveFactorTool persists a discovered factor to the gene pool. The tool
// generates a unique ID (fg_<uuid>) and fills in metadata fields the agent
// doesn't provide (generation=0, status="draft", timestamps). The agent
// provides the core research data: name, formula, metrics, rationale.
//
// Tool name: "save_factor"
// Input: name, category, formula, description, ic, turnover (required);
//
//	rationale, sharpe (optional)
//
// Output: { factor_id: string }
type SaveFactorTool struct {
	pool FactorPoolClient
}

var _ tools.Tool = (*SaveFactorTool)(nil)

func NewSaveFactorTool(pool FactorPoolClient) *SaveFactorTool {
	if pool == nil {
		panic("builtin: NewSaveFactorTool called with nil FactorPoolClient")
	}
	return &SaveFactorTool{pool: pool}
}

func (t *SaveFactorTool) Name() string { return "save_factor" }

func (t *SaveFactorTool) Description() string {
	return "Persist a discovered factor to the gene pool. Call this after validate_factor + compute_factor_ic + backtest pass. Returns the new factor_id. The tool generates the ID and fills metadata (generation=0, status='draft')."
}

func (t *SaveFactorTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{Name: "name", Type: "string", Description: "Human-readable factor name, e.g. 'price_momentum_20d'.", Required: true},
		{Name: "category", Type: "string", Description: "Factor category: 'momentum', 'value', 'quality', 'volatility', 'custom'.", Required: true},
		{Name: "formula", Type: "string", Description: "Factor DSL expression, e.g. 'ts_rank(close, 20)'.", Required: true},
		{Name: "description", Type: "string", Description: "What the factor measures and why it might work.", Required: true},
		{Name: "rationale", Type: "string", Description: "Economic rationale (optional but recommended).", Required: false},
		{Name: "ic", Type: "float", Description: "Information Coefficient from compute_factor_ic.", Required: true},
		{Name: "turnover", Type: "float", Description: "Factor turnover (0..1). Lower is better for trading costs.", Required: true},
		{Name: "sharpe", Type: "float", Description: "Factor long-short Sharpe ratio (optional).", Required: false},
	}
}

func (t *SaveFactorTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "The saved factor's identifier.",
		Fields: []tools.OutputField{
			{Name: "factor_id", Type: "string", Description: "Unique ID of the saved factor (format: fg_<uuid>)."},
		},
	}
}

func (t *SaveFactorTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	category, err := requireString(args, "category")
	if err != nil {
		return nil, err
	}
	formula, err := requireString(args, "formula")
	if err != nil {
		return nil, err
	}
	description, err := requireString(args, "description")
	if err != nil {
		return nil, err
	}
	rationale, err := optionalString(args, "rationale")
	if err != nil {
		return nil, err
	}
	ic, err := requireFloat(args, "ic")
	if err != nil {
		return nil, err
	}
	turnover, err := requireFloat(args, "turnover")
	if err != nil {
		return nil, err
	}
	sharpe, err := optionalFloat(args, "sharpe")
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	gene := &gene_pool.FactorGene{
		ID:          "fg_" + uuid.New().String(),
		Name:        name,
		Category:    category,
		Formula:     formula,
		Description: description,
		Rationale:   rationale,
		IC:          ic,
		Turnover:    turnover,
		Sharpe:      sharpe,
		// IR, Fitness: not provided by agent at save time; will be
		// updated later by UpdateMetrics when more analysis is done.
		// For now they default to 0.
		IR:         0,
		Fitness:    0,
		Generation: 0,
		ParentIDs:  []string{},
		Status:     "draft",
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := t.pool.Save(ctx, gene); err != nil {
		return nil, fmt.Errorf("save_factor: %w", err)
	}

	return map[string]interface{}{
		"factor_id": gene.ID,
	}, nil
}

// ═══════════════════════════════════════════════════════════════════════
//  SaveStrategyTool
// ═══════════════════════════════════════════════════════════════════════

// SaveStrategyTool persists a validated strategy to the gene pool. The
// agent provides the YAML config and performance metrics; the tool
// generates the ID and fills metadata.
//
// Tool name: "save_strategy"
// Input: name, strategy_yaml, sharpe, max_drawdown, total_return (required);
//
//	factor_ids (optional)
//
// Output: { strategy_id: string }
type SaveStrategyTool struct {
	pool StrategyPoolClient
}

var _ tools.Tool = (*SaveStrategyTool)(nil)

func NewSaveStrategyTool(pool StrategyPoolClient) *SaveStrategyTool {
	if pool == nil {
		panic("builtin: NewSaveStrategyTool called with nil StrategyPoolClient")
	}
	return &SaveStrategyTool{pool: pool}
}

func (t *SaveStrategyTool) Name() string { return "save_strategy" }

func (t *SaveStrategyTool) Description() string {
	return "Persist a validated strategy to the gene pool. Call this after walk_forward_validate passes. The strategy_yaml is stored as-is for reproducibility. Returns the new strategy_id."
}

func (t *SaveStrategyTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{Name: "name", Type: "string", Description: "Human-readable strategy name, e.g. 'dual_momentum_csi300'.", Required: true},
		{Name: "strategy_yaml", Type: "string", Description: "Strategy YAML config (ExpressionStrategy format). Stored verbatim.", Required: true},
		{Name: "factor_ids", Type: "[]string", Description: "IDs of factors used by this strategy (from save_factor).", Required: false},
		{Name: "sharpe", Type: "float", Description: "Strategy Sharpe ratio from backtest.run.", Required: true},
		{Name: "max_drawdown", Type: "float", Description: "Maximum drawdown from backtest.run (negative, e.g. -0.12).", Required: true},
		{Name: "total_return", Type: "float", Description: "Total return from backtest.run (e.g. 0.15 = +15%).", Required: true},
	}
}

func (t *SaveStrategyTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "The saved strategy's identifier.",
		Fields: []tools.OutputField{
			{Name: "strategy_id", Type: "string", Description: "Unique ID of the saved strategy (format: sg_<uuid>)."},
		},
	}
}

func (t *SaveStrategyTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	strategyYAML, err := requireString(args, "strategy_yaml")
	if err != nil {
		return nil, err
	}
	factorIDs, err := optionalStringSlice(args, "factor_ids")
	if err != nil {
		return nil, err
	}
	sharpe, err := requireFloat(args, "sharpe")
	if err != nil {
		return nil, err
	}
	maxDrawdown, err := requireFloat(args, "max_drawdown")
	if err != nil {
		return nil, err
	}
	totalReturn, err := requireFloat(args, "total_return")
	if err != nil {
		return nil, err
	}

	if factorIDs == nil {
		factorIDs = []string{}
	}

	now := time.Now().UTC()
	gene := &gene_pool.StrategyGene{
		ID:           "sg_" + uuid.New().String(),
		Name:         name,
		Description:  "", // Agent doesn't provide; can be added later
		StrategyType: "expression",
		Code:         strategyYAML,
		Params:       map[string]interface{}{},
		FactorIDs:    factorIDs,
		ParentIDs:    []string{},
		TotalReturn:  totalReturn,
		Sharpe:       sharpe,
		MaxDrawdown:  maxDrawdown,
		WinRate:      0, // Not provided at save time
		Fitness:      0, // Computed later by the evolution engine
		Generation:   0,
		Status:       "validated",
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := t.pool.Save(ctx, gene); err != nil {
		return nil, fmt.Errorf("save_strategy: %w", err)
	}

	return map[string]interface{}{
		"strategy_id": gene.ID,
	}, nil
}
