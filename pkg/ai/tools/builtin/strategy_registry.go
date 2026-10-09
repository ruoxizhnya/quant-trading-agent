package builtin

import (
	"context"
	"fmt"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy"
)

// StrategyListTool lists all strategies registered in the global
// strategy.Registry. It is a read-only view — strategy registration
// still goes through the existing POST /api/strategies endpoint or
// the plugin loader.
//
// Tool name: "strategy.list"
// Input: none
// Output: []strategy.StrategyInfo (name + description + parameters per strategy)
type StrategyListTool struct{}

var _ tools.Tool = (*StrategyListTool)(nil)

// NewStrategyListTool constructs a StrategyListTool. No dependencies —
// it reads from the package-level strategy.DefaultRegistry.
func NewStrategyListTool() *StrategyListTool { return &StrategyListTool{} }

func (t *StrategyListTool) Name() string { return "strategy.list" }

func (t *StrategyListTool) Description() string {
	return "List all registered strategies. Returns each strategy's name, description, and parameter schema. Use this to discover what strategies are available before running a backtest."
}

func (t *StrategyListTool) Parameters() []tools.Parameter { return nil }

func (t *StrategyListTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "array",
		Description: "Array of registered strategies with name, description, and parameter schema.",
		Fields: []tools.OutputField{
			{Name: "name", Type: "string", Description: "Strategy name (use as 'strategy_name' in backtest.run)."},
			{Name: "description", Type: "string", Description: "Human-readable strategy description."},
			{Name: "parameters", Type: "array", Description: "Strategy parameter schema."},
		},
	}
}

func (t *StrategyListTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	// GlobalListWithInfo returns []StrategyInfo sorted by name.
	return strategy.GlobalListWithInfo(), nil
}

// ─── StrategyGetTool ──────────────────────────────────────────────────

// StrategyGetTool fetches a single strategy's full metadata by name.
//
// Tool name: "strategy.get"
// Input: name (required)
// Output: strategy.StrategyInfo (name + description + parameters)
type StrategyGetTool struct{}

var _ tools.Tool = (*StrategyGetTool)(nil)

// NewStrategyGetTool constructs a StrategyGetTool. No dependencies.
func NewStrategyGetTool() *StrategyGetTool { return &StrategyGetTool{} }

func (t *StrategyGetTool) Name() string { return "strategy.get" }

func (t *StrategyGetTool) Description() string {
	return "Get detailed metadata for a single registered strategy by name. Returns the strategy's name, description, and full parameter schema. Useful before calling backtest.run to understand what parameters a strategy accepts."
}

func (t *StrategyGetTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "name",
			Type:        "string",
			Description: "Strategy name (e.g. 'momentum', 'dual_momentum', 'expression_template'). Use strategy.list to discover names.",
			Required:    true,
		},
	}
}

func (t *StrategyGetTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Strategy metadata: name, description, and parameter schema.",
		Fields: []tools.OutputField{
			{Name: "name", Type: "string", Description: "Strategy name."},
			{Name: "description", Type: "string", Description: "Human-readable description."},
			{Name: "parameters", Type: "array", Description: "Parameter schema (name, type, default, required)."},
		},
	}
}

func (t *StrategyGetTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}

	s, err := strategy.GlobalGet(name)
	if err != nil {
		// strategy.GlobalGet returns its own error when not found;
		// wrap it so the HTTP handler can surface a consistent
		// "not registered" classification. We do NOT use
		// tools.ErrToolNotRegistered here — that's for the Tools
		// Registry itself. A missing strategy is a downstream error.
		return nil, fmt.Errorf("strategy.get: %w", err)
	}

	// Build a StrategyInfo from the Strategy interface. We use the
	// strategy.AsConfigurable helper to safely extract Parameters —
	// if the strategy doesn't implement Configurable, Parameters is nil.
	info := strategy.StrategyInfo{
		Name:        s.Name(),
		Description: s.Description(),
	}
	if c := strategy.AsConfigurable(s); c != nil {
		info.Parameters = c.Parameters()
	}
	return info, nil
}
