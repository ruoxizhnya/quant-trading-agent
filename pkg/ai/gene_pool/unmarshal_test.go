package gene_pool

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- S7-P0-6 (ODR-043): stop swallowing json.Unmarshal errors ----
//
// The gene pool previously scanned JSON blob columns from PostgreSQL
// (params / factor_ids / parent_ids) and did `_ = json.Unmarshal(...)`,
// discarding the error. A truncated or corrupt blob would silently
// yield a gene with zero-valued fields and no signal — the caller would
// proceed as if the gene had no params / no parents.
//
// The fix extracts the unmarshaling into helpers that return the error.
// These tests verify the helpers surface parse failures instead of
// swallowing them.

// ---- unmarshalStrategyGeneFields ----

func TestUnmarshalStrategyGeneFields_AllValid(t *testing.T) {
	gene := &StrategyGene{}
	params := []byte(`{"lookback":20,"threshold":0.05}`)
	factorIDs := []byte(`["f1","f2"]`)
	parentIDs := []byte(`["p1","p2"]`)

	err := unmarshalStrategyGeneFields(gene, params, factorIDs, parentIDs)
	require.NoError(t, err)

	require.Len(t, gene.Params, 2)
	assert.Equal(t, 20.0, gene.Params["lookback"])
	assert.Equal(t, 0.05, gene.Params["threshold"])
	assert.Equal(t, []string{"f1", "f2"}, gene.FactorIDs)
	assert.Equal(t, []string{"p1", "p2"}, gene.ParentIDs)
}

func TestUnmarshalStrategyGeneFields_EmptyBytes(t *testing.T) {
	gene := &StrategyGene{Params: map[string]interface{}{"keep": 1.0}}

	err := unmarshalStrategyGeneFields(gene, nil, nil, nil)
	require.NoError(t, err)
	// Pre-existing fields must be untouched when the blob is empty.
	assert.Len(t, gene.Params, 1)
	assert.Nil(t, gene.FactorIDs)
	assert.Nil(t, gene.ParentIDs)
}

func TestUnmarshalStrategyGeneFields_InvalidParams(t *testing.T) {
	gene := &StrategyGene{}
	err := unmarshalStrategyGeneFields(gene, []byte(`{bad json`), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "params")
}

func TestUnmarshalStrategyGeneFields_InvalidFactorIDs(t *testing.T) {
	gene := &StrategyGene{}
	err := unmarshalStrategyGeneFields(gene, nil, []byte(`[bad`), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "factor_ids")
}

func TestUnmarshalStrategyGeneFields_InvalidParentIDs(t *testing.T) {
	gene := &StrategyGene{}
	err := unmarshalStrategyGeneFields(gene, nil, nil, []byte(`not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent_ids")
}

// TestUnmarshalStrategyGeneFields_PartialValid verifies that when one
// field is valid and a later one is invalid, the error is still
// returned (no short-circuit swallow).
func TestUnmarshalStrategyGeneFields_PartialValid(t *testing.T) {
	gene := &StrategyGene{}
	params := []byte(`{"lookback":20}`)
	parentIDs := []byte(`broken`)

	err := unmarshalStrategyGeneFields(gene, params, nil, parentIDs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent_ids")
	// The valid params field should still have been populated before the
	// error occurred.
	assert.Len(t, gene.Params, 1)
}

// ---- unmarshalFactorGeneFields ----

func TestUnmarshalFactorGeneFields_Valid(t *testing.T) {
	gene := &FactorGene{}
	err := unmarshalFactorGeneFields(gene, []byte(`["p1","p2"]`))
	require.NoError(t, err)
	assert.Equal(t, []string{"p1", "p2"}, gene.ParentIDs)
}

func TestUnmarshalFactorGeneFields_Empty(t *testing.T) {
	gene := &FactorGene{}
	err := unmarshalFactorGeneFields(gene, nil)
	require.NoError(t, err)
	assert.Nil(t, gene.ParentIDs)
}

func TestUnmarshalFactorGeneFields_Invalid(t *testing.T) {
	gene := &FactorGene{}
	err := unmarshalFactorGeneFields(gene, []byte(`broken`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent_ids")
}

// ---- Regression guards: _ = json.Unmarshal must not return ----

func TestStrategyPool_SourceHasNoSwallowedUnmarshal(t *testing.T) {
	source, err := os.ReadFile("strategy_pool.go")
	require.NoError(t, err)
	assert.NotContains(t, string(source), "_ = json.Unmarshal",
		"strategy_pool.go must not swallow json.Unmarshal errors (S7-P0-6)")
}

func TestFactorPool_SourceHasNoSwallowedUnmarshal(t *testing.T) {
	source, err := os.ReadFile("factor_pool.go")
	require.NoError(t, err)
	assert.NotContains(t, string(source), "_ = json.Unmarshal",
		"factor_pool.go must not swallow json.Unmarshal errors (S7-P0-6)")
}
