package agents

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewResearchAgent(t *testing.T) {
	agent := NewResearchAgent()
	if agent == nil {
		t.Fatal("NewResearchAgent() returned nil")
	}
}

func TestResearchAgent_ValidateFormula(t *testing.T) {
	agent := NewResearchAgent()

	tests := []struct {
		name    string
		formula string
		wantErr bool
	}{
		{"valid simple", "close", false},
		{"valid function", "ts_mean(close, 20)", false},
		{"valid complex", "ts_pct_change(close, 20) / ts_std(close, 20)", false},
		{"invalid empty", "", true},
		{"invalid syntax", "close +", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := agent.ValidateFormula(tt.formula)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateFormula() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && expr == nil {
				t.Error("Expected non-nil expression")
			}
		})
	}
}

func TestGenerateFallbackHypothesis(t *testing.T) {
	agent := NewResearchAgent()

	tests := []struct {
		topic        string
		expectedType string
	}{
		{"momentum strategy", "momentum"},
		{"动量策略", "momentum"},
		{"value investing", "value"},
		{"价值投资", "value"},
		{"quality stocks", "quality"},
		{"质量因子", "quality"},
		{"volatility factor", "volatility"},
		{"波动率", "volatility"},
		{"custom idea", "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			h := agent.generateFallbackHypothesis(tt.topic)
			if h.Category != tt.expectedType {
				t.Errorf("Expected category %s, got %s", tt.expectedType, h.Category)
			}
			if h.Formula == "" {
				t.Error("Expected non-empty formula")
			}
			if h.Name == "" {
				t.Error("Expected non-empty name")
			}
		})
	}
}

func TestGenerateID(t *testing.T) {
	id1 := generateID()
	id2 := generateID()

	if id1 == id2 {
		t.Error("Expected unique IDs")
	}
	if id1 == "" {
		t.Error("Expected non-empty ID")
	}
}

// TestExtractField was removed in S7-P0-4 (ODR-043-4): the extractField
// function it tested has been deleted from research.go in favour of
// json.Unmarshal (parseFactorHypothesisJSON). See TestExtractField_Removed
// below for the regression guard that prevents re-introduction.

func TestCleanJSONResponse(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"```json\n{\"a\": 1}\n```", "{\"a\": 1}"},
		{"```\n{\"a\": 1}\n```", "{\"a\": 1}"},
		{"{\"a\": 1}", "{\"a\": 1}"},
		{"  {\"a\": 1}  ", "{\"a\": 1}"},
	}

	for _, tt := range tests {
		result := cleanJSONResponse(tt.input)
		if result != tt.expected {
			t.Errorf("cleanJSONResponse(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestFactorHypothesis_Structure(t *testing.T) {
	h := FactorHypothesis{
		ID:          "test-1",
		Name:        "test_factor",
		Description: "Test description",
		Formula:     "close / open",
		Category:    "momentum",
		Confidence:  0.8,
		Rationale:   "Test rationale",
	}

	if h.ID != "test-1" {
		t.Errorf("Expected ID test-1, got %s", h.ID)
	}
	if h.Name != "test_factor" {
		t.Errorf("Expected Name test_factor, got %s", h.Name)
	}
	if h.Confidence != 0.8 {
		t.Errorf("Expected Confidence 0.8, got %f", h.Confidence)
	}
}

// ---- S7-P0-4 (ODR-043-4): LLM output JSON parsing ----
//
// The following tests verify that parseFactorHypothesisJSON correctly
// handles JSON payloads that the former extractField string-scanner
// could not. extractField looked for "field": and read until the next
// ", \n, or , — which truncated values containing commas, escaped
// quotes, or nested objects.

func TestParseFactorHypothesisJSON_Simple(t *testing.T) {
	resp := `{"name":"momentum_20d","category":"momentum","formula":"ts_pct_change(close, 20)","rationale":"trend persistence"}`

	h, err := parseFactorHypothesisJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, "momentum_20d", h.Name)
	assert.Equal(t, "momentum", h.Category)
	assert.Equal(t, "ts_pct_change(close, 20)", h.Formula)
	assert.Equal(t, "trend persistence", h.Rationale)
}

// TestParseFactorHypothesisJSON_CommaInFormula reproduces the primary
// extractField bug: a formula containing a comma (very common in
// multi-argument expressions like "rank(close), ts_mean(returns, 5)")
// was truncated at the first comma.
func TestParseFactorHypothesisJSON_CommaInFormula(t *testing.T) {
	resp := `{"name":"combo","category":"custom","formula":"rank(close), ts_mean(returns, 5)","rationale":"combo"}`

	h, err := parseFactorHypothesisJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, "rank(close), ts_mean(returns, 5)", h.Formula,
		"formula with commas must not be truncated")
}

// TestParseFactorHypothesisJSON_EscapedQuotes verifies that escaped
// quotes inside a string value are preserved. extractField stopped at
// the first \" treating it as the closing quote.
func TestParseFactorHypothesisJSON_EscapedQuotes(t *testing.T) {
	// The rationale contains an escaped quote: factor "momentum"
	resp := `{"name":"test","category":"momentum","formula":"close","rationale":"factor \"momentum\" signal"}`

	h, err := parseFactorHypothesisJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, `factor "momentum" signal`, h.Rationale,
		"escaped quotes must be unescaped and preserved")
}

// TestParseFactorHypothesisJSON_NewlineInValue verifies that a value
// containing a newline (common in multi-line rationales) is preserved.
// extractField stopped at \n.
func TestParseFactorHypothesisJSON_NewlineInValue(t *testing.T) {
	resp := "{\"name\":\"test\",\"category\":\"momentum\",\"formula\":\"close\",\"rationale\":\"line1\\nline2\"}"

	h, err := parseFactorHypothesisJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, "line1\nline2", h.Rationale,
		"newlines inside JSON strings must be preserved")
}

// TestParseFactorHypothesisJSON_InvalidJSON returns an error for
// non-JSON input so the caller can fall back to the fallback hypothesis.
func TestParseFactorHypothesisJSON_InvalidJSON(t *testing.T) {
	_, err := parseFactorHypothesisJSON("not json at all")
	assert.Error(t, err, "invalid JSON must return an error")
}

// TestParseFactorHypothesisJSON_EmptyResponse returns an error for
// empty input.
func TestParseFactorHypothesisJSON_EmptyResponse(t *testing.T) {
	_, err := parseFactorHypothesisJSON("")
	assert.Error(t, err, "empty response must return an error")
}

// TestParseFactorHypothesisJSON_MarkdownFenceStripped verifies that
// the parser handles LLM responses wrapped in ```json fences (which
// cleanJSONResponse strips before parsing).
func TestParseFactorHypothesisJSON_MarkdownFenceStripped(t *testing.T) {
	resp := "```json\n{\"name\":\"fenced\",\"category\":\"momentum\",\"formula\":\"close\",\"rationale\":\"ok\"}\n```"

	h, err := parseFactorHypothesisJSON(resp)
	require.NoError(t, err)
	assert.Equal(t, "fenced", h.Name)
}

// TestExtractField_Removed is a regression guard verifying that the
// old extractField function has been removed from research.go. If
// someone re-introduces it, this test fails — the function must stay
// gone in favour of json.Unmarshal.
func TestExtractField_Removed(t *testing.T) {
	source, err := os.ReadFile("research.go")
	require.NoError(t, err)
	assert.NotContains(t, string(source), "func extractField(",
		"extractField must be removed from research.go (S7-P0-4); use json.Unmarshal")
}
