// Tests for cost.go — CostTable, Usage, DailyCostSnapshot (S7-P1-5).
//
// Coverage goal: 100% of cost.go. The CostTable is a leaf module
// (no I/O) so we test the full surface: defaults, Override,
// SetFallback, Cost lookup, Calculate arithmetic, Usage.Total /
// Usage.String, and concurrent safety.
package ai

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCostTable_PopulatedWithDefaults(t *testing.T) {
	t1 := NewCostTable()
	require.NotNil(t, t1)
	// Every default entry should be present.
	for model := range defaultCostTable {
		got := t1.Cost(model)
		assert.Equal(t, defaultCostTable[model], got,
			"model %q should have the default cost", model)
	}
}

func TestNewCostTable_IsIndependentCopy(t *testing.T) {
	// Mutating the returned table must not affect future tables.
	t1 := NewCostTable()
	t1.Override("gpt-4o-mini", ModelCost{InputPer1K: 999, OutputPer1K: 999})

	t2 := NewCostTable()
	got := t2.Cost("gpt-4o-mini")
	assert.Equal(t, defaultCostTable["gpt-4o-mini"], got,
		"Override on one table must not leak into a fresh table")
}

func TestCostTable_Cost_KnownModel(t *testing.T) {
	t1 := NewCostTable()
	got := t1.Cost("gpt-4o")
	assert.Equal(t, ModelCost{InputPer1K: 0.0025, OutputPer1K: 0.01}, got)
}

func TestCostTable_Cost_UnknownModelReturnsFallback(t *testing.T) {
	t1 := NewCostTable()
	got := t1.Cost("some-future-model")
	assert.Equal(t, defaultFallback, got,
		"unknown models should be billed at the gpt-4o-mini fallback rate")
}

func TestCostTable_Override_ReplacesCost(t *testing.T) {
	t1 := NewCostTable()
	newCost := ModelCost{InputPer1K: 0.5, OutputPer1K: 1.5}
	t1.Override("gpt-4o", newCost)
	assert.Equal(t, newCost, t1.Cost("gpt-4o"))
}

func TestCostTable_Override_AddsNewModel(t *testing.T) {
	t1 := NewCostTable()
	newModel := ModelCost{InputPer1K: 0.0001, OutputPer1K: 0.0002}
	t1.Override("internal-finetune", newModel)
	assert.Equal(t, newModel, t1.Cost("internal-finetune"))
}

func TestCostTable_SetFallback_ReplacesFallback(t *testing.T) {
	t1 := NewCostTable()
	newFallback := ModelCost{InputPer1K: 0.99, OutputPer1K: 0.99}
	t1.SetFallback(newFallback)
	assert.Equal(t, newFallback, t1.Cost("unknown-model"))
}

func TestCostTable_Calculate_KnownModel(t *testing.T) {
	t1 := NewCostTable()
	// gpt-4o-mini: 0.00015 input / 0.0006 output per 1K
	// 1000 prompt + 500 completion → 0.00015*1 + 0.0006*0.5 = 0.00015 + 0.0003 = 0.00045
	got := t1.Calculate("gpt-4o-mini", 1000, 500)
	assert.InDelta(t, 0.00045, got, 1e-9)
}

func TestCostTable_Calculate_UnknownModelUsesFallback(t *testing.T) {
	t1 := NewCostTable()
	// Fallback is gpt-4o-mini rates.
	got := t1.Calculate("unknown-model", 1000, 1000)
	want := (float64(1000)/1000.0)*defaultFallback.InputPer1K +
		(float64(1000)/1000.0)*defaultFallback.OutputPer1K
	assert.InDelta(t, want, got, 1e-9)
}

func TestCostTable_Calculate_ZeroTokens(t *testing.T) {
	t1 := NewCostTable()
	got := t1.Calculate("gpt-4o", 0, 0)
	assert.InDelta(t, 0.0, got, 1e-9)
}

func TestCostTable_Calculate_NegativeTokensHandled(t *testing.T) {
	// Negative tokens shouldn't happen in practice, but Calculate is
	// pure arithmetic — verify it doesn't panic and produces a
	// sensible (negative) result rather than crashing.
	t1 := NewCostTable()
	got := t1.Calculate("gpt-4o", -100, 0)
	assert.InDelta(t, -0.00025, got, 1e-9)
}

func TestUsage_Total_UsesTotalTokensWhenSet(t *testing.T) {
	u := Usage{PromptTokens: 100, CompletionTokens: 200, TotalTokens: 350}
	assert.Equal(t, 350, u.Total(),
		"Total must prefer the server-reported TotalTokens over the sum")
}

func TestUsage_Total_SumsWhenTotalTokensZero(t *testing.T) {
	// Some providers (e.g. local LLMs) omit total_tokens. Total must
	// fall back to prompt + completion.
	u := Usage{PromptTokens: 100, CompletionTokens: 200, TotalTokens: 0}
	assert.Equal(t, 300, u.Total())
}

func TestUsage_Total_ZeroUsage(t *testing.T) {
	u := Usage{}
	assert.Equal(t, 0, u.Total())
}

func TestUsage_String_FormatsCorrectly(t *testing.T) {
	u := Usage{PromptTokens: 100, CompletionTokens: 200, TotalTokens: 300}
	got := u.String()
	assert.Equal(t, "Usage{prompt=100, completion=200, total=300}", got)
}

func TestUsage_String_UsesCalculatedTotalWhenZero(t *testing.T) {
	u := Usage{PromptTokens: 50, CompletionTokens: 50, TotalTokens: 0}
	got := u.String()
	assert.Contains(t, got, "total=100",
		"String must reflect the calculated total when TotalTokens is 0")
}

func TestDailyCostSnapshot_FieldsAccessible(t *testing.T) {
	// Pin the field names — the operator dashboard reads these via
	// JSON reflection. Renaming would silently break the dashboard.
	snap := DailyCostSnapshot{
		Date:    "2026-06-30",
		ByModel: map[string]float64{"gpt-4o": 1.23},
		Total:   1.23,
	}
	assert.Equal(t, "2026-06-30", snap.Date)
	assert.Equal(t, 1.23, snap.ByModel["gpt-4o"])
	assert.Equal(t, 1.23, snap.Total)
}

func TestCostTable_ConcurrentOverrideAndCost_NoDataRace(t *testing.T) {
	// -race flag catches any data race in the RWMutex usage. Mix
	// writes (Override) and reads (Cost, Calculate) across goroutines.
	t1 := NewCostTable()

	const N = 30
	var wg sync.WaitGroup
	wg.Add(N * 2)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			t1.Override(fmt.Sprintf("model-%d", i), ModelCost{
				InputPer1K:  float64(i),
				OutputPer1K: float64(i),
			})
		}(i)
	}
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_ = t1.Cost(fmt.Sprintf("model-%d", i))
			_ = t1.Calculate(fmt.Sprintf("model-%d", i), 100, 100)
		}(i)
	}
	wg.Wait()
}
