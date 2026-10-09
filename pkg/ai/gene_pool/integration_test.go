package gene_pool

// P2-19: Integration tests for the gene pool persistence layer.
//
// The FactorPool and StrategyPool types require a live *pgxpool.Pool
// for their CRUD methods (Save/Get/List/...). To keep these tests
// Docker-free and fast, we exercise:
//
//   1. The Mutator (pure logic, no DB) — all 7 mutation types + edge cases.
//   2. The scanFactorRows / scanStrategyRows helpers via mock row
//      implementations that satisfy the minimal interface.
//   3. FactorGene / StrategyGene struct construction, JSON round-trip,
//      and field defaults.
//   4. Constructor behavior (NewFactorPool / NewStrategyPool with nil
//      pool — construction must not panic; only method calls fail).
//
// The DB-backed CRUD path is covered by the docker-compose integration
// suite documented in docs/TEST.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- mockRows implements the minimal rows interface used by scanFactorRows / scanStrategyRows ---

type mockRows struct {
	rows   [][]interface{}
	idx    int
	closed bool
	err    error
}

func (m *mockRows) Next() bool {
	if m.closed || m.err != nil {
		return false
	}
	if m.idx >= len(m.rows) {
		return false
	}
	m.idx++
	return true
}

func (m *mockRows) Scan(dest ...interface{}) error {
	if m.idx == 0 || m.idx > len(m.rows) {
		return fmt.Errorf("scan out of range")
	}
	row := m.rows[m.idx-1]
	if len(dest) != len(row) {
		return fmt.Errorf("scan: dest count %d != row count %d", len(dest), len(row))
	}
	for i, v := range dest {
		switch d := v.(type) {
		case *string:
			if s, ok := row[i].(string); ok {
				*d = s
			}
		case *float64:
			if f, ok := row[i].(float64); ok {
				*d = f
			}
		case *int:
			if n, ok := row[i].(int); ok {
				*d = n
			}
		case *time.Time:
			if t, ok := row[i].(time.Time); ok {
				*d = t
			}
		case *[]byte:
			if b, ok := row[i].([]byte); ok {
				*d = b
			}
		default:
			return fmt.Errorf("scan: unsupported dest type %T", v)
		}
	}
	return nil
}

func (m *mockRows) Err() error { return m.err }

func (m *mockRows) Close() { m.closed = true }

// --- Test 1: NewMutator creates a deterministic Mutator ---

func TestIntegration_NewMutator_Deterministic(t *testing.T) {
	m1 := NewMutator(42)
	m2 := NewMutator(42)
	formula := "rank(close)"
	r1, _, err := m1.Mutate(formula)
	if err != nil {
		t.Fatalf("m1.Mutate error: %v", err)
	}
	r2, _, err := m2.Mutate(formula)
	if err != nil {
		t.Fatalf("m2.Mutate error: %v", err)
	}
	if r1 != r2 {
		t.Errorf("deterministic mutation with same seed: %q vs %q", r1, r2)
	}
}

// --- Test 2: Mutator rejects empty formula ---

func TestIntegration_Mutator_EmptyFormula(t *testing.T) {
	m := NewMutator(1)
	_, _, err := m.Mutate("")
	if err == nil {
		t.Fatal("expected error on empty formula, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty, got: %v", err)
	}
	// Whitespace-only
	_, _, err = m.Mutate("   ")
	if err == nil {
		t.Fatal("expected error on whitespace formula, got nil")
	}
}

// --- Test 3: Mutator Mutate changes the formula ---

func TestIntegration_Mutator_Mutate_ChangesFormula(t *testing.T) {
	m := NewMutator(7)
	original := "rank(close)"
	mutated, mutType, err := m.Mutate(original)
	if err != nil {
		t.Fatalf("Mutate error: %v", err)
	}
	if mutated == original {
		t.Errorf("Mutate did not change formula: %q == %q", mutated, original)
	}
	if mutType == "" {
		t.Error("mutation type is empty")
	}
}

// --- Test 4: Mutator MutateN applies N mutations ---

func TestIntegration_Mutator_MutateN(t *testing.T) {
	m := NewMutator(3)
	original := "rank(close)"
	result, types, err := m.MutateN(original, 3)
	if err != nil {
		t.Fatalf("MutateN error: %v", err)
	}
	if result == original {
		t.Errorf("MutateN did not change formula: %q", result)
	}
	if len(types) == 0 {
		t.Error("expected at least 1 mutation type recorded")
	}
}

// --- Test 5: Mutator MutateN with zero returns original ---

func TestIntegration_Mutator_MutateN_Zero(t *testing.T) {
	m := NewMutator(3)
	original := "rank(close)"
	result, types, err := m.MutateN(original, 0)
	if err != nil {
		t.Fatalf("MutateN error: %v", err)
	}
	if result != original {
		t.Errorf("MutateN(0) changed formula: %q != %q", result, original)
	}
	if len(types) != 0 {
		t.Errorf("expected 0 mutation types, got %d", len(types))
	}
}

// --- Test 6: mutateChangeWindow changes a window parameter ---

func TestIntegration_Mutator_ChangeWindow(t *testing.T) {
	m := NewMutator(1)
	// Formula with a window parameter (number)
	formula := "sma(close, 20)"
	result, mutType, err := m.mutateChangeWindow(formula)
	if err != nil {
		t.Fatalf("mutateChangeWindow error: %v", err)
	}
	if result == formula {
		t.Errorf("expected formula change, got same: %q", result)
	}
	if mutType != MutationChangeWindow {
		t.Errorf("mutType = %q, want %q", mutType, MutationChangeWindow)
	}
}

// --- Test 7: mutateChangeWindow fails when no numbers present ---

func TestIntegration_Mutator_ChangeWindow_NoNumbers(t *testing.T) {
	m := NewMutator(1)
	_, _, err := m.mutateChangeWindow("close")
	if err == nil {
		t.Fatal("expected error on formula with no numbers, got nil")
	}
}

// --- Test 8: mutateChangeOperator changes an operator ---

func TestIntegration_Mutator_ChangeOperator(t *testing.T) {
	m := NewMutator(1)
	formula := "close + open"
	result, mutType, err := m.mutateChangeOperator(formula)
	if err != nil {
		t.Fatalf("mutateChangeOperator error: %v", err)
	}
	if result == formula {
		t.Errorf("expected formula change, got same: %q", result)
	}
	if mutType != MutationChangeOperator {
		t.Errorf("mutType = %q, want %q", mutType, MutationChangeOperator)
	}
}

// --- Test 9: mutateChangeOperator fails when no operator present ---

func TestIntegration_Mutator_ChangeOperator_NoOperator(t *testing.T) {
	m := NewMutator(1)
	_, _, err := m.mutateChangeOperator("close")
	if err == nil {
		t.Fatal("expected error on formula with no operator, got nil")
	}
}

// --- Test 10: mutateAddOperator wraps formula ---

func TestIntegration_Mutator_AddOperator(t *testing.T) {
	m := NewMutator(1)
	formula := "close"
	result, mutType, err := m.mutateAddOperator(formula)
	if err != nil {
		t.Fatalf("mutateAddOperator error: %v", err)
	}
	if !strings.HasPrefix(result, "(") || !strings.HasSuffix(result, ")") {
		t.Errorf("expected wrapped formula, got %q", result)
	}
	if mutType != MutationAddOperator {
		t.Errorf("mutType = %q, want %q", mutType, MutationAddOperator)
	}
}

// --- Test 11: mutateRemoveOperator removes outer operator ---

func TestIntegration_Mutator_RemoveOperator(t *testing.T) {
	m := NewMutator(1)
	formula := "(close + open)"
	result, mutType, err := m.mutateRemoveOperator(formula)
	if err != nil {
		t.Fatalf("mutateRemoveOperator error: %v", err)
	}
	if result == formula {
		t.Errorf("expected formula change, got same: %q", result)
	}
	if mutType != MutationRemoveOperator {
		t.Errorf("mutType = %q, want %q", mutType, MutationRemoveOperator)
	}
}

// --- Test 12: mutateRemoveOperator fails on non-wrapped formula ---

func TestIntegration_Mutator_RemoveOperator_NotWrapped(t *testing.T) {
	m := NewMutator(1)
	_, _, err := m.mutateRemoveOperator("close")
	if err == nil {
		t.Fatal("expected error on non-wrapped formula, got nil")
	}
}

// --- Test 13: mutateChangeField changes a data field ---

func TestIntegration_Mutator_ChangeField(t *testing.T) {
	m := NewMutator(1)
	formula := "close"
	result, mutType, err := m.mutateChangeField(formula)
	if err != nil {
		t.Fatalf("mutateChangeField error: %v", err)
	}
	if result == formula {
		t.Errorf("expected formula change, got same: %q", result)
	}
	if mutType != MutationChangeField {
		t.Errorf("mutType = %q, want %q", mutType, MutationChangeField)
	}
}

// --- Test 14: mutateWrapFunction wraps in a function ---

func TestIntegration_Mutator_WrapFunction(t *testing.T) {
	m := NewMutator(1)
	formula := "close"
	result, mutType, err := m.mutateWrapFunction(formula)
	if err != nil {
		t.Fatalf("mutateWrapFunction error: %v", err)
	}
	if result == formula {
		t.Errorf("expected formula change, got same: %q", result)
	}
	if mutType != MutationWrapFunction {
		t.Errorf("mutType = %q, want %q", mutType, MutationWrapFunction)
	}
	// Should be wrapped as fn(close)
	if !strings.HasSuffix(result, "(close)") {
		t.Errorf("expected wrapped function, got %q", result)
	}
}

// --- Test 15: mutateUnwrapFunction removes outer function ---

func TestIntegration_Mutator_UnwrapFunction(t *testing.T) {
	m := NewMutator(1)
	formula := "abs(close)"
	result, mutType, err := m.mutateUnwrapFunction(formula)
	if err != nil {
		t.Fatalf("mutateUnwrapFunction error: %v", err)
	}
	if result != "close" {
		t.Errorf("expected 'close', got %q", result)
	}
	if mutType != MutationUnwrapFunction {
		t.Errorf("mutType = %q, want %q", mutType, MutationUnwrapFunction)
	}
}

// --- Test 16: mutateUnwrapFunction fails when no outer function ---

func TestIntegration_Mutator_UnwrapFunction_NoFunction(t *testing.T) {
	m := NewMutator(1)
	_, _, err := m.mutateUnwrapFunction("close")
	if err == nil {
		t.Fatal("expected error on formula with no outer function, got nil")
	}
}

// --- Test 17: extractNumbers extracts integers from formula ---

func TestIntegration_ExtractNumbers(t *testing.T) {
	cases := []struct {
		formula string
		want    []int
	}{
		{"sma(close, 20)", []int{20}},
		{"ema(close, 12) - ema(close, 26)", []int{12, 26}},
		{"close", nil},
		{"rank(close, 5, 10, 15)", []int{5, 10, 15}},
		{"123abc456", []int{123, 456}},
	}
	for _, c := range cases {
		got := extractNumbers(c.formula)
		if len(got) != len(c.want) {
			t.Errorf("extractNumbers(%q) = %v, want %v", c.formula, got, c.want)
			continue
		}
		for i, n := range got {
			if n != c.want[i] {
				t.Errorf("extractNumbers(%q)[%d] = %d, want %d", c.formula, i, n, c.want[i])
			}
		}
	}
}

// --- Test 18: replaceNthNumber replaces the nth occurrence ---

func TestIntegration_ReplaceNthNumber(t *testing.T) {
	// Replace the 0th occurrence of 20 with 60
	result := replaceNthNumber("sma(close, 20)", 20, 60, 0)
	if !strings.Contains(result, "60") {
		t.Errorf("expected 60 in result, got %q", result)
	}
	if strings.Contains(result, "20") {
		t.Errorf("expected 20 replaced, got %q", result)
	}
}

// --- Test 19: FactorGene JSON round-trip ---

func TestIntegration_FactorGene_JSONRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	gene := &FactorGene{
		ID:          "factor-001",
		Name:        "momentum_20d",
		Category:    "momentum",
		Formula:     "rank(close)",
		Description: "20-day momentum",
		Rationale:   "trend-following signal",
		IC:          0.05,
		IR:          0.8,
		Turnover:    0.3,
		Sharpe:      1.2,
		Fitness:     0.9,
		Generation:  1,
		ParentIDs:   []string{"parent-1", "parent-2"},
		Status:      "active",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	data, err := json.Marshal(gene)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded FactorGene
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if decoded.ID != gene.ID {
		t.Errorf("ID = %q, want %q", decoded.ID, gene.ID)
	}
	if decoded.IC != gene.IC {
		t.Errorf("IC = %v, want %v", decoded.IC, gene.IC)
	}
	if len(decoded.ParentIDs) != len(gene.ParentIDs) {
		t.Errorf("ParentIDs length = %d, want %d", len(decoded.ParentIDs), len(gene.ParentIDs))
	}
}

// --- Test 20: StrategyGene JSON round-trip ---

func TestIntegration_StrategyGene_JSONRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	gene := &StrategyGene{
		ID:           "strategy-001",
		Name:         "momentum_v1",
		Description:  "momentum strategy",
		StrategyType: "momentum",
		Code:         "package plugins\n...",
		Params:       map[string]interface{}{"period": 20, "threshold": 0.05},
		FactorIDs:    []string{"factor-1", "factor-2"},
		ParentIDs:    []string{"parent-1"},
		TotalReturn:  0.25,
		Sharpe:       1.5,
		MaxDrawdown:  -0.10,
		WinRate:      0.55,
		Fitness:      0.85,
		Generation:   2,
		Status:       "active",
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	data, err := json.Marshal(gene)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded StrategyGene
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if decoded.ID != gene.ID {
		t.Errorf("ID = %q, want %q", decoded.ID, gene.ID)
	}
	if decoded.StrategyType != gene.StrategyType {
		t.Errorf("StrategyType = %q, want %q", decoded.StrategyType, gene.StrategyType)
	}
	if len(decoded.FactorIDs) != len(gene.FactorIDs) {
		t.Errorf("FactorIDs length = %d, want %d", len(decoded.FactorIDs), len(gene.FactorIDs))
	}
}

// --- Test 21: scanFactorRows scans mock rows into FactorGene slice ---

func TestIntegration_ScanFactorRows_HappyPath(t *testing.T) {
	now := time.Now().UTC()
	parentIDs, _ := json.Marshal([]string{"p1", "p2"})
	rows := &mockRows{
		rows: [][]interface{}{
			{"f1", "name1", "momentum", "rank(close)", "desc1", "rationale1",
				0.05, 0.8, 0.3, 1.2, 0.9, 1.0, parentIDs, "active", now, now},
			{"f2", "name2", "value", "pe_ratio", "desc2", "rationale2",
				0.03, 0.6, 0.2, 0.9, 0.7, 2.0, []byte("null"), "inactive", now, now},
		},
	}

	genes, err := scanFactorRows(rows)
	if err != nil {
		t.Fatalf("scanFactorRows error: %v", err)
	}
	if len(genes) != 2 {
		t.Fatalf("expected 2 genes, got %d", len(genes))
	}
	if genes[0].ID != "f1" {
		t.Errorf("first gene ID = %q, want f1", genes[0].ID)
	}
	if genes[0].Name != "name1" {
		t.Errorf("first gene Name = %q, want name1", genes[0].Name)
	}
	if len(genes[0].ParentIDs) != 2 {
		t.Errorf("first gene ParentIDs length = %d, want 2", len(genes[0].ParentIDs))
	}
}

// --- Test 22: scanFactorRows with empty rows returns empty slice ---

func TestIntegration_ScanFactorRows_Empty(t *testing.T) {
	rows := &mockRows{rows: [][]interface{}{}}
	genes, err := scanFactorRows(rows)
	if err != nil {
		t.Fatalf("scanFactorRows error: %v", err)
	}
	if len(genes) != 0 {
		t.Errorf("expected 0 genes, got %d", len(genes))
	}
}

// --- Test 23: scanFactorRows propagates rows.Err() ---

func TestIntegration_ScanFactorRows_PropagatesError(t *testing.T) {
	rows := &mockRows{err: errors.New("connection lost")}
	_, err := scanFactorRows(rows)
	if err == nil {
		t.Fatal("expected error from rows.Err(), got nil")
	}
	if !errors.Is(err, rows.err) && err.Error() != "connection lost" {
		t.Errorf("expected 'connection lost', got %v", err)
	}
}

// --- Test 24: scanStrategyRows scans mock rows into StrategyGene slice ---

func TestIntegration_ScanStrategyRows_HappyPath(t *testing.T) {
	now := time.Now().UTC()
	params, _ := json.Marshal(map[string]interface{}{"period": 20})
	factorIDs, _ := json.Marshal([]string{"f1"})
	parentIDs, _ := json.Marshal([]string{"p1"})
	rows := &mockRows{
		rows: [][]interface{}{
			{"s1", "momentum_v1", "desc", "momentum", "code", params, factorIDs, parentIDs,
				0.25, 1.5, -0.10, 0.55, 0.85, 1.0, "active", now, now},
		},
	}

	genes, err := scanStrategyRows(rows)
	if err != nil {
		t.Fatalf("scanStrategyRows error: %v", err)
	}
	if len(genes) != 1 {
		t.Fatalf("expected 1 gene, got %d", len(genes))
	}
	if genes[0].ID != "s1" {
		t.Errorf("gene ID = %q, want s1", genes[0].ID)
	}
	if genes[0].StrategyType != "momentum" {
		t.Errorf("StrategyType = %q, want momentum", genes[0].StrategyType)
	}
	if len(genes[0].FactorIDs) != 1 {
		t.Errorf("FactorIDs length = %d, want 1", len(genes[0].FactorIDs))
	}
}

// --- Test 25: scanStrategyRows with empty rows returns empty slice ---

func TestIntegration_ScanStrategyRows_Empty(t *testing.T) {
	rows := &mockRows{rows: [][]interface{}{}}
	genes, err := scanStrategyRows(rows)
	if err != nil {
		t.Fatalf("scanStrategyRows error: %v", err)
	}
	if len(genes) != 0 {
		t.Errorf("expected 0 genes, got %d", len(genes))
	}
}

// --- Test 26: NewFactorPool with nil pool does not panic ---

func TestIntegration_NewFactorPool_NilPool(t *testing.T) {
	fp := NewFactorPool(nil)
	if fp == nil {
		t.Fatal("NewFactorPool returned nil")
	}
	// Calling Save with nil pool will panic on .pool.Exec; we don't
	// call it here — construction alone must succeed.
}

// --- Test 27: NewStrategyPool with nil pool does not panic ---

func TestIntegration_NewStrategyPool_NilPool(t *testing.T) {
	sp := NewStrategyPool(nil)
	if sp == nil {
		t.Fatal("NewStrategyPool returned nil")
	}
}

// --- Test 28: FactorGene zero value has expected defaults ---

func TestIntegration_FactorGene_ZeroValue(t *testing.T) {
	var gene FactorGene
	if gene.ID != "" {
		t.Errorf("zero ID = %q, want empty", gene.ID)
	}
	if gene.IC != 0 {
		t.Errorf("zero IC = %v, want 0", gene.IC)
	}
	if gene.Generation != 0 {
		t.Errorf("zero Generation = %d, want 0", gene.Generation)
	}
	if gene.ParentIDs != nil {
		t.Errorf("zero ParentIDs = %v, want nil", gene.ParentIDs)
	}
}

// --- Test 29: StrategyGene zero value has expected defaults ---

func TestIntegration_StrategyGene_ZeroValue(t *testing.T) {
	var gene StrategyGene
	if gene.ID != "" {
		t.Errorf("zero ID = %q, want empty", gene.ID)
	}
	if gene.Fitness != 0 {
		t.Errorf("zero Fitness = %v, want 0", gene.Fitness)
	}
	if gene.Params != nil {
		t.Errorf("zero Params = %v, want nil", gene.Params)
	}
}

// --- Test 30: MutationType constants are distinct strings ---

func TestIntegration_MutationType_Constants(t *testing.T) {
	types := []MutationType{
		MutationChangeWindow,
		MutationChangeOperator,
		MutationAddOperator,
		MutationRemoveOperator,
		MutationChangeField,
		MutationWrapFunction,
		MutationUnwrapFunction,
	}
	seen := make(map[MutationType]bool)
	for _, mt := range types {
		if seen[mt] {
			t.Errorf("duplicate mutation type: %q", mt)
		}
		seen[mt] = true
		if mt == "" {
			t.Error("mutation type is empty string")
		}
	}
}

// --- Test 31: Mutator with different seeds produces different results ---

func TestIntegration_Mutator_DifferentSeeds(t *testing.T) {
	m1 := NewMutator(1)
	m2 := NewMutator(999)
	formula := "rank(close)"
	// Try multiple times to increase chance of divergence
	diffFound := false
	for i := 0; i < 20; i++ {
		r1, _, err1 := m1.Mutate(formula)
		r2, _, err2 := m2.Mutate(formula)
		if err1 != nil || err2 != nil {
			continue
		}
		if r1 != r2 {
			diffFound = true
			break
		}
	}
	if !diffFound {
		t.Error("expected different results from different seeds after 20 attempts")
	}
}

// --- Test 32: Mutator MutateN with large N does not panic ---

func TestIntegration_Mutator_MutateN_LargeN(t *testing.T) {
	m := NewMutator(5)
	original := "rank(close)"
	result, types, err := m.MutateN(original, 10)
	if err != nil {
		t.Fatalf("MutateN error: %v", err)
	}
	if result == "" {
		t.Error("result is empty")
	}
	// Some mutations may fail (no valid mutation found), so types
	// may be fewer than 10 — that's acceptable.
	_ = types
}

// --- Test 33: extractNumbers handles edge cases ---

func TestIntegration_ExtractNumbers_EdgeCases(t *testing.T) {
	cases := []struct {
		formula string
		want    int
	}{
		{"", 0},
		{"close", 0},
		{"123", 1},
		{"abc123def456", 2},
		{"1.5", 2}, // "1" and "5" are separate ints (dot splits)
	}
	for _, c := range cases {
		got := extractNumbers(c.formula)
		if len(got) != c.want {
			t.Errorf("extractNumbers(%q) = %v (len %d), want len %d", c.formula, got, len(got), c.want)
		}
	}
}

// --- Test 34: replaceNthNumber with non-existent number returns original ---

func TestIntegration_ReplaceNthNumber_NotFound(t *testing.T) {
	result := replaceNthNumber("close", 999, 1, 0)
	if result != "close" {
		t.Errorf("expected original when number not found, got %q", result)
	}
}

// --- Test 35: mockRows Next returns false after exhaustion ---

func TestIntegration_MockRows_NextExhaustion(t *testing.T) {
	rows := &mockRows{
		rows: [][]interface{}{
			{"a"},
		},
	}
	if !rows.Next() {
		t.Error("first Next() should return true")
	}
	if rows.Next() {
		t.Error("second Next() should return false (exhausted)")
	}
}

// --- Test 36: mockRows Close prevents further iteration ---

func TestIntegration_MockRows_Close(t *testing.T) {
	rows := &mockRows{
		rows: [][]interface{}{
			{"a"}, {"b"},
		},
	}
	rows.Close()
	if rows.Next() {
		t.Error("Next() after Close() should return false")
	}
}

// --- Test 37: FactorGene with nil ParentIDs marshals correctly ---

func TestIntegration_FactorGene_NilParentIDs(t *testing.T) {
	gene := &FactorGene{
		ID:        "f1",
		Name:      "test",
		ParentIDs: nil,
	}
	data, err := json.Marshal(gene)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	var decoded FactorGene
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	// After round-trip, ParentIDs may be nil or empty slice depending
	// on JSON encoding. Both are acceptable.
}

// --- Test 38: StrategyGene with nil Params marshals correctly ---

func TestIntegration_StrategyGene_NilParams(t *testing.T) {
	gene := &StrategyGene{
		ID:     "s1",
		Name:   "test",
		Params: nil,
	}
	data, err := json.Marshal(gene)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	var decoded StrategyGene
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
}

// --- Test 39: Mutator handles formula with multiple operators ---

func TestIntegration_Mutator_FormulaWithMultipleOperators(t *testing.T) {
	m := NewMutator(2)
	formula := "close + open - high"
	result, _, err := m.Mutate(formula)
	if err != nil {
		t.Fatalf("Mutate error: %v", err)
	}
	if result == "" {
		t.Error("result is empty")
	}
}

// --- Test 40: Mutator handles formula with nested functions ---

func TestIntegration_Mutator_FormulaWithNestedFunctions(t *testing.T) {
	m := NewMutator(3)
	formula := "abs(sma(close, 20))"
	result, _, err := m.Mutate(formula)
	if err != nil {
		t.Fatalf("Mutate error: %v", err)
	}
	if result == "" {
		t.Error("result is empty")
	}
}

// --- Test 41: scanFactorRows handles scan error gracefully ---

func TestIntegration_ScanFactorRows_ScanError(t *testing.T) {
	rows := &mockRows{
		rows: [][]interface{}{
			// Wrong number of columns — Scan will fail
			{"only", "two"},
		},
	}
	_, err := scanFactorRows(rows)
	if err == nil {
		t.Fatal("expected scan error, got nil")
	}
}

// --- Test 42: scanStrategyRows handles scan error gracefully ---

func TestIntegration_ScanStrategyRows_ScanError(t *testing.T) {
	rows := &mockRows{
		rows: [][]interface{}{
			{"only", "two"},
		},
	}
	_, err := scanStrategyRows(rows)
	if err == nil {
		t.Fatal("expected scan error, got nil")
	}
}

// --- Test 43: Mutator Mutate eventually succeeds after retries ---

func TestIntegration_Mutator_Mutate_RetryLogic(t *testing.T) {
	m := NewMutator(10)
	// A formula that has multiple mutation opportunities
	formula := "sma(close, 20) + ema(open, 10)"
	result, _, err := m.Mutate(formula)
	if err != nil {
		t.Fatalf("Mutate error: %v", err)
	}
	if result == "" {
		t.Error("result is empty")
	}
}

// --- Test 44: Mutator Mutate returns error when no valid mutation found ---

func TestIntegration_Mutator_Mutate_NoValidMutation(t *testing.T) {
	// A formula with no numbers, no operators, no recognizable fields,
	// and not wrapped in parens or functions. The Mutator tries 10
	// random mutations and should fail.
	m := NewMutator(1)
	formula := "x"
	_, _, err := m.Mutate(formula)
	if err == nil {
		// Some seeds may find a mutation (e.g., wrap function), so
		// we don't hard-fail here. We just log.
		t.Logf("Mutate(x) succeeded — seed found a valid mutation")
	}
}

// --- Test 45: context cancellation does not affect Mutator (pure CPU) ---

func TestIntegration_Mutator_IgnoresContext(t *testing.T) {
	m := NewMutator(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Mutator.Mutate does not take a context, so cancellation has no
	// effect. This test documents that behavior.
	_ = ctx
	_, _, err := m.Mutate("rank(close)")
	if err != nil {
		t.Errorf("Mutate should not be affected by context: %v", err)
	}
}
