package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
)

// ─── helpers ───────────────────────────────────────────────────────────

// newGene is a tiny constructor for test fixtures. Only the fields the
// lineage tool reads are populated; everything else is left zero.
func newGene(id, name string, parentIDs ...string) *gene_pool.StrategyGene {
	return &gene_pool.StrategyGene{
		ID:           id,
		Name:         name,
		StrategyType: "expression",
		FactorIDs:    []string{},
		ParentIDs:    parentIDs,
		Sharpe:       1.0,
		Fitness:      0.5,
		Generation:   0,
		Status:       "validated",
	}
}

// newLineagePool builds a mockStrategyPool pre-populated with the given
// strategies, addressable by ID. Convenience wrapper for the common
// test pattern.
func newLineagePool(genes ...*gene_pool.StrategyGene) *mockStrategyPool {
	byID := make(map[string]*gene_pool.StrategyGene, len(genes))
	for _, g := range genes {
		byID[g.ID] = g
	}
	return &mockStrategyPool{byID: byID}
}

// ─── Name / Description / Parameters / OutputSchema ───────────────────

func TestGetStrategyLineageTool_Name(t *testing.T) {
	tt := NewGetStrategyLineageTool(newLineagePool())
	assert.Equal(t, "get_strategy_lineage", tt.Name())
}

func TestGetStrategyLineageTool_Description(t *testing.T) {
	tt := NewGetStrategyLineageTool(newLineagePool())
	desc := tt.Description()
	assert.NotEmpty(t, desc)
	assert.Contains(t, desc, "lineage")
	assert.Contains(t, desc, "mutation history")
}

func TestGetStrategyLineageTool_Parameters(t *testing.T) {
	tt := NewGetStrategyLineageTool(newLineagePool())
	params := tt.Parameters()
	require.Len(t, params, 2)

	assert.Equal(t, "strategy_id", params[0].Name)
	assert.True(t, params[0].Required)
	assert.Equal(t, "string", params[0].Type)

	assert.Equal(t, "max_depth", params[1].Name)
	assert.False(t, params[1].Required)
	assert.Equal(t, "int", params[1].Type)
	assert.Equal(t, DefaultLineageMaxDepth, params[1].Default)
}

func TestGetStrategyLineageTool_OutputSchema(t *testing.T) {
	tt := NewGetStrategyLineageTool(newLineagePool())
	schema := tt.OutputSchema()
	assert.Equal(t, "object", schema.Type)
	require.NotEmpty(t, schema.Fields)

	expected := map[string]bool{
		"root":               true,
		"depth_reached":      true,
		"max_depth":          true,
		"cycles_detected":    true,
		"missing_parent_ids": true,
	}
	for _, f := range schema.Fields {
		delete(expected, f.Name)
	}
	assert.Empty(t, expected, "missing fields in schema: %v", expected)
}

// ─── Constructor nil-panic ──────────────────────────────────────────────

func TestNewGetStrategyLineageTool_NilPoolPanics(t *testing.T) {
	assert.Panics(t, func() { NewGetStrategyLineageTool(nil) })
}

// ─── Execute: happy path — linear lineage ──────────────────────────────

// A → B → C  (chain of 3 nodes, depth 2)
func TestGetStrategyLineageTool_Execute_LinearLineage(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B"),
		newGene("sg_B", "B", "sg_C"),
		newGene("sg_C", "C"), // leaf — no parents
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	r, ok := result.(*strategyLineageResult)
	require.True(t, ok, "expected *strategyLineageResult, got %T", result)

	// Root == A
	assert.Equal(t, "sg_A", r.Root.ID)
	assert.Equal(t, "A", r.Root.Name)
	// Depth 0 (root) + depth 1 (B) + depth 2 (C) → deepest is 2
	assert.Equal(t, 2, r.DepthReached)
	assert.Equal(t, DefaultLineageMaxDepth, r.MaxDepth)
	assert.Empty(t, r.CyclesDetected)
	assert.Empty(t, r.MissingParentIDs)

	// A.Parents == [B]
	require.Len(t, r.Root.Parents, 1, "A should have 1 parent (B)")
	b := r.Root.Parents[0]
	assert.Equal(t, "sg_B", b.ID)

	// B.Parents == [C]
	require.Len(t, b.Parents, 1, "B should have 1 parent (C)")
	c := b.Parents[0]
	assert.Equal(t, "sg_C", c.ID)

	// C is a leaf
	assert.Empty(t, c.Parents)
}

// Single node, no parents (depth 0).
func TestGetStrategyLineageTool_Execute_SingleNodeNoParents(t *testing.T) {
	pool := newLineagePool(newGene("sg_A", "A"))
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	assert.Equal(t, "sg_A", r.Root.ID)
	assert.Empty(t, r.Root.Parents)
	assert.Equal(t, 0, r.DepthReached)
	assert.Empty(t, r.CyclesDetected)
	assert.Empty(t, r.MissingParentIDs)
}

// ─── Execute: branching DAG — shared ancestor skipped silently ─────────
//
//	A
//	├── B → D   (D visited here)
//	└── C → D   (D already expanded → silently skipped, NOT a cycle)
func TestGetStrategyLineageTool_Execute_BranchingDAG_SharedAncestorSilentlySkipped(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B", "sg_C"),
		newGene("sg_B", "B", "sg_D"),
		newGene("sg_C", "C", "sg_D"),
		newGene("sg_D", "D"),
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	// A has 2 parents
	require.Len(t, r.Root.Parents, 2)
	b := r.Root.Parents[0]
	c := r.Root.Parents[1]
	assert.Equal(t, "sg_B", b.ID)
	assert.Equal(t, "sg_C", c.ID)

	// B has 1 parent (D) — D successfully expanded
	require.Len(t, b.Parents, 1)
	assert.Equal(t, "sg_D", b.Parents[0].ID)

	// C has 0 parents in tree — D was already visited via B, silently skipped.
	// (C.ParentIDs in the data still shows ["sg_D"], but the tree doesn't
	// duplicate D.)
	assert.Empty(t, c.Parents, "shared ancestor D should be silently skipped under C")

	// No cycles, no missing — D is just a shared ancestor, not pathological.
	assert.Empty(t, r.CyclesDetected, "shared ancestor is NOT a cycle")
	assert.Empty(t, r.MissingParentIDs)

	// Depth 2 reached (D via B)
	assert.Equal(t, 2, r.DepthReached)
}

// ─── Execute: true cycle A → B → A ──────────────────────────────────────
//
//	A → B → A  (back-edge to ancestor on stack)
func TestGetStrategyLineageTool_Execute_TrueCycle(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B"),
		newGene("sg_B", "B", "sg_A"), // back-edge to A
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	// Root is A with 1 parent (B)
	assert.Equal(t, "sg_A", r.Root.ID)
	require.Len(t, r.Root.Parents, 1)
	b := r.Root.Parents[0]
	assert.Equal(t, "sg_B", b.ID)

	// B has 0 parents in the tree — the back-edge A was a cycle.
	assert.Empty(t, b.Parents, "cycle back-edge should not be expanded")

	// Cycles detected contains A (the back-edge target)
	require.Len(t, r.CyclesDetected, 1)
	assert.Equal(t, "sg_A", r.CyclesDetected[0])

	// Depth 1 (B was reached)
	assert.Equal(t, 1, r.DepthReached)
	assert.Empty(t, r.MissingParentIDs)
}

// Self-parent: A → A.
func TestGetStrategyLineageTool_Execute_SelfParentCycle(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_A"), // self-reference
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	assert.Equal(t, "sg_A", r.Root.ID)
	assert.Empty(t, r.Root.Parents, "self-parent should be a cycle, not expanded")

	require.Len(t, r.CyclesDetected, 1)
	assert.Equal(t, "sg_A", r.CyclesDetected[0])
}

// ─── Execute: missing parent ────────────────────────────────────────────
//
//	A → B (exists) → MISSING (does not exist)
func TestGetStrategyLineageTool_Execute_MissingParent(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B"),
		newGene("sg_B", "B", "sg_MISSING"), // MISSING not in pool
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	// Tree: A → B (no further expansion under B)
	assert.Equal(t, "sg_A", r.Root.ID)
	require.Len(t, r.Root.Parents, 1)
	b := r.Root.Parents[0]
	assert.Equal(t, "sg_B", b.ID)
	assert.Empty(t, b.Parents, "missing parent should not produce a node")

	// MISSING recorded once
	require.Len(t, r.MissingParentIDs, 1)
	assert.Equal(t, "sg_MISSING", r.MissingParentIDs[0])

	// No cycles
	assert.Empty(t, r.CyclesDetected)
}

// Missing parent deduplication: same missing ID reached via multiple paths.
//
//	A → [B, C], B → MISSING, C → MISSING
func TestGetStrategyLineageTool_Execute_MissingParentDeduplicated(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B", "sg_C"),
		newGene("sg_B", "B", "sg_MISSING"),
		newGene("sg_C", "C", "sg_MISSING"),
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	// MISSING recorded only once even though reached via B and C
	require.Len(t, r.MissingParentIDs, 1, "missing parent should be deduplicated")
	assert.Equal(t, "sg_MISSING", r.MissingParentIDs[0])
}

// ─── Execute: max_depth limits expansion ────────────────────────────────

// max_depth=0 returns only the root, no parents expanded.
func TestGetStrategyLineageTool_Execute_MaxDepthZero(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B"),
		newGene("sg_B", "B"),
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
		"max_depth":   0,
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	assert.Equal(t, "sg_A", r.Root.ID)
	assert.Empty(t, r.Root.Parents, "max_depth=0 should not expand parents")
	// ParentIDs field still shows what WOULD be expanded
	assert.Equal(t, []string{"sg_B"}, r.Root.ParentIDs)
	assert.Equal(t, 0, r.DepthReached)
	assert.Equal(t, 0, r.MaxDepth)
}

// max_depth=1 returns root + immediate parents only.
func TestGetStrategyLineageTool_Execute_MaxDepthOne(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B"),
		newGene("sg_B", "B", "sg_C"),
		newGene("sg_C", "C"),
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
		"max_depth":   1,
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	assert.Equal(t, "sg_A", r.Root.ID)
	require.Len(t, r.Root.Parents, 1, "depth 1 should expand B")
	b := r.Root.Parents[0]
	assert.Equal(t, "sg_B", b.ID)
	assert.Empty(t, b.Parents, "depth 1 should NOT expand C (depth 2)")
	// B's ParentIDs field still shows C
	assert.Equal(t, []string{"sg_C"}, b.ParentIDs)

	assert.Equal(t, 1, r.DepthReached)
	assert.Equal(t, 1, r.MaxDepth)
}

// max_depth above MaxLineageMaxDepth is clamped to MaxLineageMaxDepth (10).
func TestGetStrategyLineageTool_Execute_MaxDepthClamped(t *testing.T) {
	pool := newLineagePool(newGene("sg_A", "A"))
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
		"max_depth":   100, // way above ceiling
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	assert.Equal(t, MaxLineageMaxDepth, r.MaxDepth, "max_depth should be clamped to %d", MaxLineageMaxDepth)
}

// Negative max_depth is treated as 0.
func TestGetStrategyLineageTool_Execute_NegativeMaxDepth(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B"),
		newGene("sg_B", "B"),
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
		"max_depth":   -5,
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	assert.Equal(t, 0, r.MaxDepth, "negative max_depth should be treated as 0")
	assert.Empty(t, r.Root.Parents)
}

// ─── Execute: missing root → error ─────────────────────────────────────

func TestGetStrategyLineageTool_Execute_RootNotFound(t *testing.T) {
	pool := newLineagePool() // empty pool
	tt := NewGetStrategyLineageTool(pool)

	_, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_NONEXISTENT",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sg_NONEXISTENT")
	assert.Contains(t, err.Error(), "not found in gene pool")
	// Not an ErrInvalidArgs — args were valid, the lookup just failed.
	assert.False(t, errors.Is(err, tools.ErrInvalidArgs))
}

// ─── Execute: invalid args ─────────────────────────────────────────────

func TestGetStrategyLineageTool_Execute_MissingStrategyID(t *testing.T) {
	tt := NewGetStrategyLineageTool(newLineagePool())
	_, err := tt.Execute(context.Background(), map[string]interface{}{
		"max_depth": 5,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "strategy_id")
}

func TestGetStrategyLineageTool_Execute_EmptyStrategyID(t *testing.T) {
	tt := NewGetStrategyLineageTool(newLineagePool())
	_, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "strategy_id")
}

func TestGetStrategyLineageTool_Execute_WrongTypeStrategyID(t *testing.T) {
	tt := NewGetStrategyLineageTool(newLineagePool())
	_, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": 42,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "strategy_id")
}

func TestGetStrategyLineageTool_Execute_WrongTypeMaxDepth(t *testing.T) {
	tt := NewGetStrategyLineageTool(newLineagePool(newGene("sg_A", "A")))
	_, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
		"max_depth":   "5", // string, not int
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "max_depth")
}

// JSON-decoded numbers come as float64 — max_depth should accept this.
func TestGetStrategyLineageTool_Execute_MaxDepthAsFloat(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B"),
		newGene("sg_B", "B"),
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
		"max_depth":   float64(1), // simulates json.Unmarshal output
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	assert.Equal(t, 1, r.MaxDepth)
	require.Len(t, r.Root.Parents, 1, "depth 1 should expand B")
}

// ─── Execute: default max_depth used when omitted ───────────────────────

func TestGetStrategyLineageTool_Execute_DefaultMaxDepth(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B"),
		newGene("sg_B", "B"),
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
		// no max_depth — should default
	})
	require.NoError(t, err)
	r := result.(*strategyLineageResult)

	assert.Equal(t, DefaultLineageMaxDepth, r.MaxDepth)
}

// ─── buildLineage direct unit test ──────────────────────────────────────

// Verify the helper works without going through Execute's arg parsing.
func TestGetStrategyLineageTool_buildLineage_DeepChain(t *testing.T) {
	// 6-deep chain: A → B → C → D → E → F (depth 5)
	// With maxDepth=3, only A/B/C/D should be in the tree, E/F truncated.
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B"),
		newGene("sg_B", "B", "sg_C"),
		newGene("sg_C", "C", "sg_D"),
		newGene("sg_D", "D", "sg_E"),
		newGene("sg_E", "E", "sg_F"),
		newGene("sg_F", "F"),
	)
	tt := NewGetStrategyLineageTool(pool)

	r, err := tt.buildLineage(context.Background(), "sg_A", 3)
	require.NoError(t, err)
	assert.Equal(t, "sg_A", r.Root.ID)
	assert.Equal(t, 3, r.DepthReached, "deepest expanded should be D at depth 3")
	assert.Equal(t, 3, r.MaxDepth)
	assert.Empty(t, r.CyclesDetected)
	assert.Empty(t, r.MissingParentIDs)

	// Walk the chain: A → B → C → D (truncated)
	node := r.Root
	for _, expected := range []string{"sg_B", "sg_C", "sg_D"} {
		require.Len(t, node.Parents, 1, "expected %s to have 1 parent", node.ID)
		node = node.Parents[0]
		assert.Equal(t, expected, node.ID)
	}
	// D's parents should NOT be expanded (maxDepth=3 reached at D)
	assert.Empty(t, node.Parents)
	// D's ParentIDs field still shows E would have been expanded
	assert.Equal(t, []string{"sg_E"}, node.ParentIDs)
}

// ─── Registry integration ───────────────────────────────────────────────

func TestGetStrategyLineageTool_RegisterInRegistry(t *testing.T) {
	reg := tools.NewRegistry()
	tt := NewGetStrategyLineageTool(newLineagePool())
	require.NoError(t, reg.Register(tt))

	fetched, err := reg.Get("get_strategy_lineage")
	require.NoError(t, err)
	assert.Same(t, tt, fetched)

	info := reg.List()
	found := false
	for _, ti := range info {
		if ti.Name == "get_strategy_lineage" {
			found = true
			break
		}
	}
	assert.True(t, found, "get_strategy_lineage should appear in registry list")
}

// ─── JSON marshaling sanity ─────────────────────────────────────────────

// Verify the result is JSON-marshalable (no recursive struct cycles
// after the visited/onStack pruning).
func TestGetStrategyLineageTool_Execute_JSONMarshalable(t *testing.T) {
	pool := newLineagePool(
		newGene("sg_A", "A", "sg_B", "sg_C"),
		newGene("sg_B", "B", "sg_D"),
		newGene("sg_C", "C", "sg_D"),
		newGene("sg_D", "D"),
	)
	tt := NewGetStrategyLineageTool(pool)

	result, err := tt.Execute(context.Background(), map[string]interface{}{
		"strategy_id": "sg_A",
	})
	require.NoError(t, err)

	// Should marshal without infinite recursion error.
	_, err = json.Marshal(result)
	require.NoError(t, err, "result must be JSON-marshalable")
}
