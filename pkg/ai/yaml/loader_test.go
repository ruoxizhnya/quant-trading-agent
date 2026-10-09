package yaml

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy/expression"
)

// uniqueName builds a strategy name unique per test invocation (including
// -count=2 re-runs) so GlobalRegister never collides across tests or
// re-runs. The global strategy registry is package-level state with no
// Unregister, so fixed names flake under -count=2.
func uniqueName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test_%s_%d", t.Name(), time.Now().UnixNano())
}

// TestParseConfig_ValidFullYAML verifies that a complete YAML document
// with all sections (including the new expression section) parses
// correctly and every field is populated as expected.
func TestParseConfig_ValidFullYAML(t *testing.T) {
	yamlStr := `strategy:
  name: my_expr_strat
  type: expression
  description: test expression strategy
expression:
  signal:
    expression: "cs_rank(close) > 0.8"
    action: buy
    direction: long
    min_strength: 0.5
    lookback: 90
  sizing:
    method: equal
    fixed_weight: 0.05
    max_per_stock: 0.15
    max_total: 0.9
  risk:
    max_position_pct: 0.15
    max_open_positions: 25
    min_cash_buffer: 0.10
backtest:
  start_date: 2022-01-01
  end_date: 2024-01-01
  initial_capital: 1000000
data:
  universe: csi300
  timeframe: 1d
`
	config, err := ParseConfig(yamlStr)
	require.NoError(t, err)
	require.NotNil(t, config)

	// Strategy section
	assert.Equal(t, "my_expr_strat", config.Strategy.Name)
	assert.Equal(t, "expression", config.Strategy.Type)
	assert.Equal(t, "test expression strategy", config.Strategy.Description)

	// Expression.Signal
	assert.Equal(t, "cs_rank(close) > 0.8", config.Expression.Signal.Expression)
	assert.Equal(t, "buy", config.Expression.Signal.Action)
	assert.Equal(t, "long", config.Expression.Signal.Direction)
	assert.Equal(t, 0.5, config.Expression.Signal.MinStrength)
	assert.Equal(t, 90, config.Expression.Signal.Lookback)

	// Expression.Sizing
	assert.Equal(t, "equal", config.Expression.Sizing.Method)
	assert.Equal(t, 0.05, config.Expression.Sizing.FixedWeight)
	assert.Equal(t, 0.15, config.Expression.Sizing.MaxPerStock)
	assert.Equal(t, 0.9, config.Expression.Sizing.MaxTotal)

	// Expression.Risk
	assert.Equal(t, 0.15, config.Expression.Risk.MaxPositionPct)
	assert.Equal(t, 25, config.Expression.Risk.MaxOpenPositions)
	assert.Equal(t, 0.10, config.Expression.Risk.MinCashBuffer)

	// Backtest section
	assert.Equal(t, "2022-01-01", config.Backtest.StartDate)
	assert.Equal(t, "csi300", config.Data.Universe)
}

// TestParseConfig_EmptyString verifies that an empty input is rejected
// with a clear error mentioning "empty".
func TestParseConfig_EmptyString(t *testing.T) {
	_, err := ParseConfig("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

// TestParseConfig_MalformedYAML verifies that a syntactically invalid
// YAML document is rejected with a parse error.
func TestParseConfig_MalformedYAML(t *testing.T) {
	// Unclosed quote + bad indentation should fail yaml.v3 parsing.
	yamlStr := `strategy:
  name: "unterminated
  type: expression
`
	_, err := ParseConfig(yamlStr)
	require.Error(t, err)
}

// TestParseConfig_MissingStrategySection verifies that a YAML document
// without the required top-level strategy: section is rejected.
func TestParseConfig_MissingStrategySection(t *testing.T) {
	yamlStr := `backtest:
  start_date: 2022-01-01
data:
  universe: csi300
`
	_, err := ParseConfig(yamlStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "strategy:")
}

// TestParseConfig_NoExpressionSection verifies that a YAML document
// with strategy/backtest/data but WITHOUT an expression section parses
// successfully — the Expression field is simply zero-valued. This is
// the backward-compatible case: existing YAML without expression data
// still parses, and LoadStrategy (Phase 2) decides whether to error.
func TestParseConfig_NoExpressionSection(t *testing.T) {
	yamlStr := `strategy:
  name: simple_strategy
  type: momentum
  description: no expression here
backtest:
  start_date: 2022-01-01
data:
  universe: csi300
`
	config, err := ParseConfig(yamlStr)
	require.NoError(t, err)
	require.NotNil(t, config)

	assert.Equal(t, "simple_strategy", config.Strategy.Name)
	// Expression section is absent → zero value.
	assert.Equal(t, "", config.Expression.Signal.Expression)
	assert.Equal(t, "", config.Expression.Sizing.Method)
	assert.Equal(t, 0, config.Expression.Risk.MaxOpenPositions)
}

// ─── LoadStrategy tests ───────────────────────────────────────────────

// fullExpressionYAML returns a complete YAML string with the expression
// section populated. Used as a fixture for the LoadStrategy tests.
func fullExpressionYAML() string {
	return `strategy:
  name: test_expr_strat
  type: expression
  description: test expression strategy
expression:
  signal:
    expression: "cs_rank(close) > 0.8"
    action: buy
    direction: long
    min_strength: 0.5
    lookback: 90
  sizing:
    method: equal
    fixed_weight: 0.05
    max_per_stock: 0.15
    max_total: 0.9
  risk:
    max_position_pct: 0.15
    max_open_positions: 25
    min_cash_buffer: 0.10
backtest:
  start_date: 2022-01-01
  end_date: 2024-01-01
  initial_capital: 1000000
data:
  universe: csi300
  timeframe: 1d
`
}

// TestLoadStrategy_HappyPath verifies that a full YAML with expression
// section builds an ExpressionStrategy whose Name and Parameters match
// the YAML input.
func TestLoadStrategy_HappyPath(t *testing.T) {
	s, err := LoadStrategy(fullExpressionYAML())
	require.NoError(t, err)
	require.NotNil(t, s)

	assert.Equal(t, "test_expr_strat", s.Name())

	// Verify the strategy is an *ExpressionStrategy and its Parameters
	// reflect the YAML values. We use Parameters() (part of the
	// strategy.Configurable interface) because it's the public API.
	exprStrat, ok := s.(*expression.ExpressionStrategy)
	require.True(t, ok, "expected *expression.ExpressionStrategy")

	params := exprStrat.Parameters()
	require.NotEmpty(t, params)

	// Build a map for easy lookup.
	paramMap := make(map[string]interface{}, len(params))
	for _, p := range params {
		paramMap[p.Name] = p.Default
	}

	// Verify signal params carried over from YAML.
	assert.Equal(t, "cs_rank(close) > 0.8", paramMap["signal_expr"])
	assert.Equal(t, "buy", paramMap["action"])
	assert.Equal(t, "long", paramMap["direction"])
	assert.Equal(t, 0.5, paramMap["min_strength"])
	assert.Equal(t, 90, paramMap["lookback"])

	// Verify sizing params.
	assert.Equal(t, "equal", paramMap["sizing_method"])
	assert.Equal(t, 0.15, paramMap["max_per_stock"])
	assert.Equal(t, 0.9, paramMap["max_total"])

	// Verify risk params.
	assert.Equal(t, 0.15, paramMap["max_position_pct"])
	assert.Equal(t, 25, paramMap["max_open_positions"])
	assert.Equal(t, 0.10, paramMap["min_cash_buffer"])
}

// TestLoadStrategy_DefaultsApplied verifies that a YAML with
// strategy.type=expression but NO expression section builds a strategy
// using the package defaults (cs_rank(close) > 0.8, equal sizing, etc.).
func TestLoadStrategy_DefaultsApplied(t *testing.T) {
	yamlStr := `strategy:
  name: default_expr_strat
  type: expression
  description: uses package defaults
backtest:
  start_date: 2022-01-01
data:
  universe: csi300
`
	s, err := LoadStrategy(yamlStr)
	require.NoError(t, err)
	require.NotNil(t, s)

	assert.Equal(t, "default_expr_strat", s.Name())

	exprStrat, ok := s.(*expression.ExpressionStrategy)
	require.True(t, ok)

	// The default signal expression should be cs_rank(close) > 0.8.
	params := exprStrat.Parameters()
	for _, p := range params {
		if p.Name == "signal_expr" {
			assert.Equal(t, "cs_rank(close) > 0.8", p.Default)
			return
		}
	}
	t.Fatal("signal_expr parameter not found")
}

// TestLoadStrategy_InvalidDirection verifies that an unrecognized
// direction string is rejected with a clear error.
func TestLoadStrategy_InvalidDirection(t *testing.T) {
	yamlStr := `strategy:
  name: bad_dir_strat
  type: expression
expression:
  signal:
    expression: "close > 50"
    direction: sideways
`
	_, err := LoadStrategy(yamlStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "direction")
	assert.Contains(t, err.Error(), "sideways")
}

// TestLoadStrategy_InvalidSizingMethod verifies that an unrecognized
// sizing method is rejected by NewPositionSizer.
func TestLoadStrategy_InvalidSizingMethod(t *testing.T) {
	yamlStr := `strategy:
  name: bad_sizing_strat
  type: expression
expression:
  signal:
    expression: "close > 50"
  sizing:
    method: martingale
`
	_, err := LoadStrategy(yamlStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "martingale")
}

// TestLoadStrategy_InvalidExpression verifies that a syntactically
// invalid DSL expression is rejected by NewSignalGenerator (the parser
// surfaces a parse error).
func TestLoadStrategy_InvalidExpression(t *testing.T) {
	yamlStr := `strategy:
  name: bad_expr_strat
  type: expression
expression:
  signal:
    expression: "cs_rank(close >"
`
	_, err := LoadStrategy(yamlStr)
	require.Error(t, err)
	// The error chain should mention parse or expression.
	assert.Contains(t, err.Error(), "expression")
}

// TestLoadStrategy_EmptyName verifies that a YAML with an empty
// strategy.name is rejected (ParseConfig enforces this).
func TestLoadStrategy_EmptyName(t *testing.T) {
	yamlStr := `strategy:
  name: ""
  type: expression
expression:
  signal:
    expression: "close > 50"
`
	_, err := LoadStrategy(yamlStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

// TestLoadStrategy_RejectsExpressionTemplateName verifies that the
// reserved name "expression_template" is rejected to avoid collision
// with the self-registered default strategy.
func TestLoadStrategy_RejectsExpressionTemplateName(t *testing.T) {
	yamlStr := `strategy:
  name: expression_template
  type: expression
expression:
  signal:
    expression: "close > 50"
`
	_, err := LoadStrategy(yamlStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expression_template")
	assert.Contains(t, err.Error(), "reserved")
}

// TestLoadStrategy_NonExpressionTypeWithoutSection verifies that a YAML
// with strategy.type != "expression" and no expression section returns
// a clear error directing the caller to add the section or change type.
func TestLoadStrategy_NonExpressionTypeWithoutSection(t *testing.T) {
	yamlStr := `strategy:
  name: momentum_strat
  type: momentum
  description: a momentum strategy
backtest:
  start_date: 2022-01-01
`
	_, err := LoadStrategy(yamlStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expression")
}

// TestLoadAndRegister_HappyPath verifies the convenience wrapper
// registers the strategy so GlobalGet can find it. We use a unique name
// (per invocation) to avoid collisions across tests and -count=2 re-runs
// — the global registry has no Unregister.
func TestLoadAndRegister_HappyPath(t *testing.T) {
	name := uniqueName(t)
	yamlStr := fmt.Sprintf(`strategy:
  name: %s
  type: expression
expression:
  signal:
    expression: "close > 50"
`, name)
	s, err := LoadAndRegister(yamlStr)
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Equal(t, name, s.Name())

	// Verify it's retrievable via the global registry.
	retrieved, err := strategy.GlobalGet(name)
	require.NoError(t, err)
	assert.Equal(t, s.Name(), retrieved.Name())
}

// TestLoadAndRegister_DuplicateName verifies that registering a strategy
// with an already-registered name returns an error from GlobalRegister.
func TestLoadAndRegister_DuplicateName(t *testing.T) {
	name := uniqueName(t)
	yamlStr := fmt.Sprintf(`strategy:
  name: %s
  type: expression
expression:
  signal:
    expression: "close > 50"
`, name)
	// First registration succeeds.
	_, err := LoadAndRegister(yamlStr)
	require.NoError(t, err)

	// Second registration of the same name should fail.
	_, err = LoadAndRegister(yamlStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), name)
}

// verifyDirectionConstants ensures our test assumptions about
// domain.Direction string values are correct. If domain.Direction
// constants change, this test fails loudly rather than letting the
// LoadStrategy direction validation silently break.
func TestDirectionConstants(t *testing.T) {
	assert.Equal(t, "long", string(domain.DirectionLong))
	assert.Equal(t, "short", string(domain.DirectionShort))
	assert.Equal(t, "close", string(domain.DirectionClose))
	assert.Equal(t, "hold", string(domain.DirectionHold))
}

// ─── S7-P3-2 Phase 4: ExpressionParamsFromConfig tests ──────────────

// TestExpressionParamsFromConfig verifies that a Config with a fully
// populated Expression section yields a params map containing every
// ExpressionStrategy.Configure parameter with the correct value.
func TestExpressionParamsFromConfig(t *testing.T) {
	config := &Config{
		Expression: ExpressionYAML{
			Signal: SignalYAML{
				Expression:  "cs_rank(close) > 0.8",
				Action:      "buy",
				Direction:   "long",
				MinStrength: 0.5,
				Lookback:    60,
			},
			Sizing: SizingYAML{
				Method:      "equal",
				FixedWeight: 0.05,
				MaxPerStock: 0.10,
				MaxTotal:    1.0,
			},
			Risk: RiskYAML{
				MaxPositionPct:   0.10,
				MaxDrawdown:      0.20,
				MaxOpenPositions: 20,
				MinCashBuffer:    0.05,
			},
		},
	}

	params := ExpressionParamsFromConfig(config)

	assert.Equal(t, "cs_rank(close) > 0.8", params["signal_expr"])
	assert.Equal(t, "buy", params["action"])
	assert.Equal(t, "long", params["direction"])
	assert.Equal(t, 0.5, params["min_strength"])
	assert.Equal(t, 60, params["lookback"])
	assert.Equal(t, "equal", params["sizing_method"])
	assert.Equal(t, 0.05, params["fixed_weight"])
	assert.Equal(t, 0.10, params["max_per_stock"])
	assert.Equal(t, 1.0, params["max_total"])
	assert.Equal(t, 0.10, params["max_position_pct"])
	assert.Equal(t, 20, params["max_open_positions"])
	assert.Equal(t, 0.05, params["min_cash_buffer"])

	// max_drawdown is NOT a Configure param (it's only in RiskConfig for
	// future use), so it must not appear in the params map.
	_, present := params["max_drawdown"]
	assert.False(t, present, "max_drawdown is not a Configure parameter")
}

// TestExpressionParamsFromConfig_PartialFields verifies that zero-valued
// fields are omitted from the params map so Configure preserves the
// existing strategy's values for those fields (partial-update semantics).
func TestExpressionParamsFromConfig_PartialFields(t *testing.T) {
	config := &Config{
		Expression: ExpressionYAML{
			Signal: SignalYAML{
				Expression: "close > 50",
				// Action, Direction, MinStrength, Lookback all zero → omitted
			},
			// Sizing and Risk fully zero → omitted
		},
	}

	params := ExpressionParamsFromConfig(config)

	// signal_expr is always present (even if empty, for the "keep current"
	// Configure semantics on the type: expression path).
	assert.Equal(t, "close > 50", params["signal_expr"])

	// Zero-valued optional fields must be absent.
	for _, key := range []string{
		"action", "direction", "min_strength", "lookback",
		"sizing_method", "fixed_weight", "max_per_stock", "max_total",
		"max_position_pct", "max_open_positions", "min_cash_buffer",
	} {
		_, present := params[key]
		assert.False(t, present, "zero-valued field %q should be omitted", key)
	}
}

// TestExpressionParamsFromConfig_EmptySection verifies the type:expression
// path (no explicit expression: block) yields a map with only
// signal_expr="" — Configure treats this as "keep current value".
func TestExpressionParamsFromConfig_EmptySection(t *testing.T) {
	config := &Config{} // zero-valued Expression

	params := ExpressionParamsFromConfig(config)

	require.Len(t, params, 1)
	assert.Equal(t, "", params["signal_expr"])
}

// TestExpressionParamsFromConfig_NilConfig verifies a nil config returns
// nil rather than panicking — defensive guard for caller bugs.
func TestExpressionParamsFromConfig_NilConfig(t *testing.T) {
	assert.Nil(t, ExpressionParamsFromConfig(nil))
}
