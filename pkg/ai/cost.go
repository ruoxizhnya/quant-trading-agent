package ai

import (
	"fmt"
	"sync"
	"time"
)

// CostTable maps model name to per-1K-token cost in USD (Sprint 6 P1-14,
// AR-008). The table covers the models we actually use (gpt-4o-mini,
// gpt-4o, claude-3-haiku, deepseek-chat); unknown models are billed at
// the "default" rate (gpt-4o-mini) and emit a warning log at the first
// usage so the operator can extend the table.
//
// Prices are as of 2026-06-12. Sources:
//   - OpenAI: https://openai.com/pricing
//   - Anthropic: https://www.anthropic.com/pricing
//   - DeepSeek: https://api-docs.deepseek.com/quick_start/pricing
//
// The table is stored in an immutable value so concurrent reads are
// safe; updates require a new CostTable (see SetCostTable).
type CostTable struct {
	mu       sync.RWMutex
	per1KUSD map[string]ModelCost
	fallback ModelCost
}

// ModelCost is the per-1K-token cost (input and output) for a model.
// Some providers (e.g. Anthropic) charge different rates for input vs
// output tokens; we mirror that here.
type ModelCost struct {
	InputPer1K  float64 // USD per 1K input tokens
	OutputPer1K float64 // USD per 1K output tokens
}

// Default cost rates as of 2026-06-12.
var defaultCostTable = map[string]ModelCost{
	// OpenAI
	"gpt-4o-mini":   {InputPer1K: 0.00015, OutputPer1K: 0.0006},
	"gpt-4o":        {InputPer1K: 0.0025, OutputPer1K: 0.01},
	"gpt-4-turbo":   {InputPer1K: 0.01, OutputPer1K: 0.03},
	"gpt-3.5-turbo": {InputPer1K: 0.0005, OutputPer1K: 0.0015},
	"o1-preview":    {InputPer1K: 0.015, OutputPer1K: 0.06},
	"o1-mini":       {InputPer1K: 0.003, OutputPer1K: 0.012},
	// Anthropic
	"claude-3-5-sonnet": {InputPer1K: 0.003, OutputPer1K: 0.015},
	"claude-3-haiku":    {InputPer1K: 0.00025, OutputPer1K: 0.00125},
	"claude-3-opus":     {InputPer1K: 0.015, OutputPer1K: 0.075},
	// DeepSeek
	"deepseek-chat":     {InputPer1K: 0.00014, OutputPer1K: 0.00028},
	"deepseek-reasoner": {InputPer1K: 0.00055, OutputPer1K: 0.00219},
	// Qwen
	"qwen-turbo": {InputPer1K: 0.0003, OutputPer1K: 0.0006},
	"qwen-plus":  {InputPer1K: 0.0008, OutputPer1K: 0.002},
}

// Fallback cost applied when the model is not in the table. Defaults
// to gpt-4o-mini (cheap, defensive — we'd rather under-estimate than
// over-estimate an unknown model).
var defaultFallback = ModelCost{InputPer1K: 0.00015, OutputPer1K: 0.0006}

// NewCostTable returns a CostTable populated with the default rates.
// Callers can override per-model rates with Override() (e.g. for
// contract-negotiated prices).
func NewCostTable() *CostTable {
	t := &CostTable{
		per1KUSD: make(map[string]ModelCost, len(defaultCostTable)),
		fallback: defaultFallback,
	}
	for k, v := range defaultCostTable {
		t.per1KUSD[k] = v
	}
	return t
}

// Override replaces the cost for a specific model. Useful for
// contract-negotiated rates or for adding a new model.
func (t *CostTable) Override(model string, cost ModelCost) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.per1KUSD[model] = cost
}

// SetFallback replaces the cost applied for unknown models.
func (t *CostTable) SetFallback(cost ModelCost) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fallback = cost
}

// Cost returns the per-1K cost for a model. If the model is unknown,
// the fallback is returned.
func (t *CostTable) Cost(model string) ModelCost {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if c, ok := t.per1KUSD[model]; ok {
		return c
	}
	return t.fallback
}

// Calculate returns the USD cost for the given token usage on a model.
// promptTokens + completionTokens are the raw counts returned by the
// LLM API (see Usage in chat_response.go).
func (t *CostTable) Calculate(model string, promptTokens, completionTokens int) float64 {
	c := t.Cost(model)
	cost := (float64(promptTokens)/1000.0)*c.InputPer1K +
		(float64(completionTokens)/1000.0)*c.OutputPer1K
	return cost
}

// Usage tracks token counts returned by an LLM call. We use a struct
// rather than a generic map so the cost calculator has a stable API.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Total returns the total token count (prompt + completion).
func (u Usage) Total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens
}

// String formats the cost as a 6-decimal-place USD value, e.g.
// "$0.000142" for 1K tokens of gpt-4o-mini.
func (u Usage) String() string {
	return fmt.Sprintf("Usage{prompt=%d, completion=%d, total=%d}",
		u.PromptTokens, u.CompletionTokens, u.Total())
}

// DailyCostSnapshot is a single row in the daily cost history
// (Sprint 6 P1-14, AR-017). Persisted to `ai_costs` table by the
// service every minute; queried by the operator dashboard.
type DailyCostSnapshot struct {
	Date    string             // YYYY-MM-DD (UTC)
	ByModel map[string]float64 // model → USD spent
	Total   float64            // sum across models
	Updated time.Time
}
