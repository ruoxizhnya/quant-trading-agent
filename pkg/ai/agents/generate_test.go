package agents

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGenerateAgent(t *testing.T) {
	agent := NewGenerateAgent()
	require.NotNil(t, agent)
	assert.NotNil(t, agent.llm)
}

func TestGenerateAgent_GenerateStrategy_Unconfigured(t *testing.T) {
	agent := NewGenerateAgent()

	ctx := context.Background()
	description := "A simple momentum strategy"

	// When LLM is not configured, should return error
	result, err := agent.GenerateStrategy(ctx, description)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AI client not configured")
	assert.Nil(t, result)
}

func TestGenerateAgent_GenerateFromFactors_Unconfigured(t *testing.T) {
	agent := NewGenerateAgent()

	ctx := context.Background()
	factors := []string{
		"ts_mean(close, 20)",
		"ts_std(close, 60)",
	}
	description := "Combine momentum and volatility factors"

	// When LLM is not configured, should return error
	result, err := agent.GenerateFromFactors(ctx, factors, description)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM request failed")
	assert.Nil(t, result)
}

func TestGenerateAgent_GenerateFromFactors_EmptyFactors(t *testing.T) {
	agent := NewGenerateAgent()

	ctx := context.Background()
	_, err := agent.GenerateFromFactors(ctx, []string{}, "test description")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no factors provided")
}

func TestGenerateAgent_GenerateFromFactors_NilFactors(t *testing.T) {
	agent := NewGenerateAgent()

	ctx := context.Background()
	_, err := agent.GenerateFromFactors(ctx, nil, "test description")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no factors provided")
}

func TestGenerateAgent_generateFallbackStrategy(t *testing.T) {
	agent := NewGenerateAgent()

	description := "test strategy"
	result := agent.generateFallbackStrategy(description)

	assert.NotNil(t, result)
	assert.Equal(t, "SimpleMomentum", result.Name)
	assert.Equal(t, "momentum", result.Type)
	assert.Equal(t, description, result.Description)
	assert.NotEmpty(t, result.Code)
	assert.NotEmpty(t, result.Params)
	assert.NotEmpty(t, result.Factors)
	assert.InDelta(t, 0.5, result.Confidence, 0.001)
	assert.NotEmpty(t, result.ID)
}

func TestGenerateAgent_generateFallbackStrategy_EmptyDescription(t *testing.T) {
	agent := NewGenerateAgent()

	result := agent.generateFallbackStrategy("")
	assert.NotNil(t, result)
	assert.Equal(t, "SimpleMomentum", result.Name)
	assert.Empty(t, result.Description)
}

func TestStrategyTemplate_Structure(t *testing.T) {
	template := &StrategyTemplate{
		ID:          "test_id",
		Name:        "TestStrategy",
		Description: "Test description",
		Type:        "momentum",
		Code:        "buy when close > ma20",
		Params: []Param{
			{Name: "lookback", Type: "int", Default: 20, Description: "Lookback period"},
		},
		Factors:    []string{"close", "ts_mean(close, 20)"},
		Confidence: 0.85,
	}

	assert.Equal(t, "test_id", template.ID)
	assert.Equal(t, "TestStrategy", template.Name)
	assert.Equal(t, "Test description", template.Description)
	assert.Equal(t, "momentum", template.Type)
	assert.Equal(t, "buy when close > ma20", template.Code)
	assert.Len(t, template.Params, 1)
	assert.Equal(t, "lookback", template.Params[0].Name)
	assert.Equal(t, 20, template.Params[0].Default)
	assert.Len(t, template.Factors, 2)
	assert.InDelta(t, 0.85, template.Confidence, 0.001)
}

func TestParam_Structure(t *testing.T) {
	param := Param{
		Name:        "threshold",
		Type:        "float",
		Default:     0.05,
		Description: "Signal threshold",
	}

	assert.Equal(t, "threshold", param.Name)
	assert.Equal(t, "float", param.Type)
	assert.Equal(t, 0.05, param.Default)
	assert.Equal(t, "Signal threshold", param.Description)
}

func TestGenerateStrategyID(t *testing.T) {
	// Reset counter for deterministic testing
	strategyIDMutex.Lock()
	strategyIDCounter = 0
	strategyIDMutex.Unlock()

	id1 := generateStrategyID()
	id2 := generateStrategyID()
	id3 := generateStrategyID()

	assert.Equal(t, "strategy_1", id1)
	assert.Equal(t, "strategy_2", id2)
	assert.Equal(t, "strategy_3", id3)
	assert.NotEqual(t, id1, id2)
	assert.NotEqual(t, id2, id3)
}

func TestGenerateStrategyID_ThreadSafety(t *testing.T) {
	// Test that generateStrategyID is thread-safe
	const numGoroutines = 100
	ids := make(chan string, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			ids <- generateStrategyID()
		}()
	}

	collected := make(map[string]bool)
	for i := 0; i < numGoroutines; i++ {
		id := <-ids
		collected[id] = true
	}

	// All IDs should be unique
	assert.Len(t, collected, numGoroutines)
}

func TestGenerateAgent_generateFallbackStrategy_Params(t *testing.T) {
	agent := NewGenerateAgent()

	result := agent.generateFallbackStrategy("test")

	require.Len(t, result.Params, 2)
	assert.Equal(t, "lookback", result.Params[0].Name)
	assert.Equal(t, "int", result.Params[0].Type)
	assert.Equal(t, 20, result.Params[0].Default)
	assert.Equal(t, "threshold", result.Params[1].Name)
	assert.Equal(t, "float", result.Params[1].Type)
	assert.Equal(t, 0.0, result.Params[1].Default)
}

func TestGenerateAgent_generateFallbackStrategy_Factors(t *testing.T) {
	agent := NewGenerateAgent()

	result := agent.generateFallbackStrategy("test")

	require.Len(t, result.Factors, 2)
	assert.Equal(t, "ts_mean(close, 20)", result.Factors[0])
	assert.Equal(t, "close", result.Factors[1])
}

func TestGenerateAgent_generateFallbackStrategy_UniqueIDs(t *testing.T) {
	agent := NewGenerateAgent()

	id1 := agent.generateFallbackStrategy("test1").ID
	id2 := agent.generateFallbackStrategy("test2").ID
	id3 := agent.generateFallbackStrategy("test3").ID

	assert.NotEqual(t, id1, id2)
	assert.NotEqual(t, id2, id3)
	assert.NotEqual(t, id1, id3)
}

func TestGenerateAgent_generateFallbackStrategy_ConfidenceRange(t *testing.T) {
	agent := NewGenerateAgent()

	result := agent.generateFallbackStrategy("test")

	assert.GreaterOrEqual(t, result.Confidence, 0.0)
	assert.LessOrEqual(t, result.Confidence, 1.0)
}

// ---- S7-P0-4 (ODR-043-4): LLM output JSON parsing (generate.go) ----
//
// parseStrategyTemplateJSON replaces the extractField string-scanner
// in generate.go. These tests verify it handles the JSON edge cases
// that extractField truncated: commas, escaped quotes, nested arrays.

func TestParseStrategyTemplateJSON_Simple(t *testing.T) {
	resp := `{"name":"dual_ma","type":"momentum","logic":"Buy when fast_ma > slow_ma","params":[],"factors":["ts_mean(close,20)"]}`

	tmpl, err := parseStrategyTemplateJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, "dual_ma", tmpl.Name)
	assert.Equal(t, "momentum", tmpl.Type)
	assert.Equal(t, "Buy when fast_ma > slow_ma", tmpl.Code)
	assert.Len(t, tmpl.Factors, 1)
}

// TestParseStrategyTemplateJSON_CommaInLogic verifies that the logic
// field (which often contains commas in pseudocode) is not truncated.
func TestParseStrategyTemplateJSON_CommaInLogic(t *testing.T) {
	resp := `{"name":"combo","type":"custom","logic":"rank(close), ts_mean(returns, 5), filter"}`

	tmpl, err := parseStrategyTemplateJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, "rank(close), ts_mean(returns, 5), filter", tmpl.Code,
		"logic with commas must not be truncated")
}

// TestParseStrategyTemplateJSON_FactorsArray verifies that the factors
// array (which extractField could not parse at all) is correctly
// extracted as a slice.
func TestParseStrategyTemplateJSON_FactorsArray(t *testing.T) {
	resp := `{"name":"multi","type":"multi_factor","logic":"combine","factors":["ts_mean(close,20)","ts_std(close,60)","rank(volume)"]}`

	tmpl, err := parseStrategyTemplateJSON(resp)
	require.NoError(t, err)
	require.Len(t, tmpl.Factors, 3)
	assert.Equal(t, "ts_mean(close,20)", tmpl.Factors[0])
	assert.Equal(t, "ts_std(close,60)", tmpl.Factors[1])
	assert.Equal(t, "rank(volume)", tmpl.Factors[2])
}

// TestParseStrategyTemplateJSON_EscapedQuotes verifies escaped quotes
// in the logic field are preserved.
func TestParseStrategyTemplateJSON_EscapedQuotes(t *testing.T) {
	resp := `{"name":"test","type":"custom","logic":"use \"momentum\" signal"}`

	tmpl, err := parseStrategyTemplateJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, `use "momentum" signal`, tmpl.Code)
}

// TestParseStrategyTemplateJSON_InvalidJSON returns an error for
// non-JSON input so the caller can fall back.
func TestParseStrategyTemplateJSON_InvalidJSON(t *testing.T) {
	_, err := parseStrategyTemplateJSON("not json")
	assert.Error(t, err)
}

// TestParseStrategyTemplateJSON_MarkdownFenceStripped verifies the
// parser handles ```json fenced responses.
func TestParseStrategyTemplateJSON_MarkdownFenceStripped(t *testing.T) {
	resp := "```json\n{\"name\":\"fenced\",\"type\":\"momentum\",\"logic\":\"ok\"}\n```"

	tmpl, err := parseStrategyTemplateJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, "fenced", tmpl.Name)
}

// TestGenerate_ExtractField_Removed is a regression guard: extractField
// must not be re-introduced in generate.go.
func TestGenerate_ExtractField_Removed(t *testing.T) {
	source, err := os.ReadFile("generate.go")
	require.NoError(t, err)
	assert.NotContains(t, string(source), "func extractField(",
		"extractField must be removed from generate.go (S7-P0-4); use json.Unmarshal")
}
