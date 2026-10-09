package builtin

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy"
)

// uniqueStrategyName builds a strategy name unique per test invocation
// (including -count=2 re-runs) so GlobalRegister never collides.
// The global strategy registry has no Unregister, so fixed names
// flake under -count=2. Mirrors the pattern in pkg/ai/yaml/loader_test.go.
func uniqueStrategyName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("tool_test_%s_%d", t.Name(), time.Now().UnixNano())
}

// stubStrategy is a minimal Strategy implementation for registry tests.
// It embeds *strategy.BaseStrategy to get Configure/Cleanup/Parameters
// defaults, and adds no-op GenerateSignals/Weight.
type stubStrategy struct {
	*strategy.BaseStrategy
	name string
}

func newStubStrategy(name string) *stubStrategy {
	s := &stubStrategy{name: name}
	s.BaseStrategy = strategy.NewBaseStrategy(name, "stub strategy for tool tests")
	return s
}

func (s *stubStrategy) GenerateSignals(ctx context.Context, bars map[string][]domain.OHLCV, portfolio *domain.Portfolio) ([]strategy.Signal, error) {
	return nil, nil
}

func (s *stubStrategy) Weight(signal strategy.Signal, portfolioValue float64) float64 {
	return 0
}

// ─── StrategyListTool tests ───────────────────────────────────────────

func TestStrategyListTool_Name(t *testing.T) {
	tt := NewStrategyListTool()
	assert.Equal(t, "strategy.list", tt.Name())
}

func TestStrategyListTool_Description(t *testing.T) {
	tt := NewStrategyListTool()
	assert.NotEmpty(t, tt.Description())
	assert.Contains(t, tt.Description(), "strateg")
}

func TestStrategyListTool_Parameters(t *testing.T) {
	tt := NewStrategyListTool()
	// strategy.list takes no parameters.
	assert.Nil(t, tt.Parameters())
}

func TestStrategyListTool_OutputSchema(t *testing.T) {
	tt := NewStrategyListTool()
	schema := tt.OutputSchema()
	assert.Equal(t, "array", schema.Type)
	assert.NotEmpty(t, schema.Fields)
}

func TestStrategyListTool_Execute_ReturnsSlice(t *testing.T) {
	// Register a unique strategy so the list is non-empty.
	name := uniqueStrategyName(t)
	require.NoError(t, strategy.GlobalRegister(newStubStrategy(name)))

	tt := NewStrategyListTool()
	result, err := tt.Execute(context.Background(), nil)
	require.NoError(t, err)

	list, ok := result.([]strategy.StrategyInfo)
	require.True(t, ok, "result should be []strategy.StrategyInfo, got %T", result)
	assert.NotEmpty(t, list, "list should contain at least the strategy we just registered")

	// Verify our strategy is in the list.
	found := false
	for _, info := range list {
		if info.Name == name {
			found = true
			break
		}
	}
	assert.True(t, found, "registered strategy %q should appear in list", name)
}

// ─── StrategyGetTool tests ────────────────────────────────────────────

func TestStrategyGetTool_Name(t *testing.T) {
	tt := NewStrategyGetTool()
	assert.Equal(t, "strategy.get", tt.Name())
}

func TestStrategyGetTool_Description(t *testing.T) {
	tt := NewStrategyGetTool()
	assert.NotEmpty(t, tt.Description())
}

func TestStrategyGetTool_Parameters(t *testing.T) {
	tt := NewStrategyGetTool()
	params := tt.Parameters()
	require.Len(t, params, 1)
	assert.Equal(t, "name", params[0].Name)
	assert.True(t, params[0].Required)
}

func TestStrategyGetTool_OutputSchema(t *testing.T) {
	tt := NewStrategyGetTool()
	schema := tt.OutputSchema()
	assert.Equal(t, "object", schema.Type)
	assert.NotEmpty(t, schema.Fields)
}

func TestStrategyGetTool_Execute_HappyPath(t *testing.T) {
	name := uniqueStrategyName(t)
	require.NoError(t, strategy.GlobalRegister(newStubStrategy(name)))

	tt := NewStrategyGetTool()
	args := map[string]interface{}{"name": name}
	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)

	info, ok := result.(strategy.StrategyInfo)
	require.True(t, ok, "result should be strategy.StrategyInfo, got %T", result)
	assert.Equal(t, name, info.Name)
	assert.Equal(t, "stub strategy for tool tests", info.Description)
}

func TestStrategyGetTool_Execute_NotFound(t *testing.T) {
	tt := NewStrategyGetTool()
	args := map[string]interface{}{"name": "nonexistent_strategy_xyz_123"}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	// Should NOT be ErrInvalidArgs — args were valid, strategy just doesn't exist.
	assert.False(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "strategy.get")
}

func TestStrategyGetTool_Execute_MissingName(t *testing.T) {
	tt := NewStrategyGetTool()
	_, err := tt.Execute(context.Background(), map[string]interface{}{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "name")
}

func TestStrategyGetTool_Execute_EmptyName(t *testing.T) {
	tt := NewStrategyGetTool()
	args := map[string]interface{}{"name": ""}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

func TestStrategyGetTool_Execute_WrongTypeName(t *testing.T) {
	tt := NewStrategyGetTool()
	args := map[string]interface{}{"name": 42}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

// ─── Registry integration ─────────────────────────────────────────────

func TestStrategyTools_RegisterInRegistry(t *testing.T) {
	listTool := NewStrategyListTool()
	getTool := NewStrategyGetTool()

	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(listTool))
	require.NoError(t, reg.Register(getTool))

	list := reg.List()
	require.Len(t, list, 2)
	// Sorted: strategy.get < strategy.list
	assert.Equal(t, "strategy.get", list[0].Name)
	assert.Equal(t, "strategy.list", list[1].Name)
}
