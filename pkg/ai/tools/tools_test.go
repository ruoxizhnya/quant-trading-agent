package tools

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Test fixtures ────────────────────────────────────────────────────

// fakeTool is a minimal Tool implementation for Registry tests. It
// carries a name, a description, and a canned Execute result so tests
// can assert delegation behavior without depending on any builtin Tool.
type fakeTool struct {
	name        string
	description string
	params      []Parameter
	output      OutputSchema
	execResult  interface{}
	execErr     error
	// execCalls counts how many times Execute was invoked, for
	// concurrency tests that need to verify all calls landed.
	execCalls int
	mu        sync.Mutex
}

func (f *fakeTool) Name() string               { return f.name }
func (f *fakeTool) Description() string        { return f.description }
func (f *fakeTool) Parameters() []Parameter    { return f.params }
func (f *fakeTool) OutputSchema() OutputSchema { return f.output }

func (f *fakeTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	f.mu.Lock()
	f.execCalls++
	f.mu.Unlock()
	if f.execErr != nil {
		return nil, f.execErr
	}
	return f.execResult, nil
}

func (f *fakeTool) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.execCalls
}

// newFakeTool builds a fakeTool with sane defaults for tests.
func newFakeTool(name string) *fakeTool {
	return &fakeTool{
		name:        name,
		description: "fake tool: " + name,
		params: []Parameter{
			{Name: "x", Type: "string", Required: true, Description: "x param"},
		},
		output:     OutputSchema{Type: "string", Description: "echo result"},
		execResult: "ok",
	}
}

// ─── Register tests ───────────────────────────────────────────────────

// TestRegistry_RegisterAndGet verifies the happy path: a registered
// Tool is retrievable by Name and round-trips its identity fields.
func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	tt := newFakeTool("alpha")

	require.NoError(t, r.Register(tt))

	got, err := r.Get("alpha")
	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Name())
	assert.Equal(t, "fake tool: alpha", got.Description())
}

// TestRegistry_Register_NilTool verifies that passing nil to Register
// returns ErrNilTool rather than panicking later on Get/Execute.
func TestRegistry_Register_NilTool(t *testing.T) {
	r := NewRegistry()
	err := r.Register(nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNilTool), "expected ErrNilTool, got %v", err)
}

// TestRegistry_Register_EmptyName verifies that a Tool whose Name()
// returns "" is rejected. Empty names break HTTP routing
// (POST /api/tools/) and make List output ambiguous.
func TestRegistry_Register_EmptyName(t *testing.T) {
	r := NewRegistry()
	tt := newFakeTool("") // empty name
	err := r.Register(tt)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEmptyToolName), "expected ErrEmptyToolName, got %v", err)
}

// TestRegistry_Register_Replacement verifies the hot-reload semantics:
// registering a Tool with the same Name as an existing one replaces
// it (following the Source Registry pattern, not the Strategy
// Registry's reject-duplicates pattern).
func TestRegistry_Register_Replacement(t *testing.T) {
	r := NewRegistry()
	original := newFakeTool("dup")
	original.execResult = "original"
	require.NoError(t, r.Register(original))

	replacement := newFakeTool("dup")
	replacement.execResult = "replacement"
	require.NoError(t, r.Register(replacement))

	got, err := r.Get("dup")
	require.NoError(t, err)

	// The replacement should be the one stored.
	result, err := got.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "replacement", result)

	// The original should NOT have been called after replacement.
	assert.Equal(t, 0, original.callCount(), "replaced Tool should not be invoked")
}

// ─── Get tests ────────────────────────────────────────────────────────

// TestRegistry_Get_NotFound verifies that looking up an unregistered
// name returns ErrToolNotRegistered (wrapped with the name) so callers
// can distinguish "not found" from "execution failed".
func TestRegistry_Get_NotFound(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrToolNotRegistered), "expected ErrToolNotRegistered, got %v", err)
	assert.Contains(t, err.Error(), "missing", "error should name the missing tool")
}

// ─── List tests ───────────────────────────────────────────────────────

// TestRegistry_List_Sorted verifies that List returns ToolInfos sorted
// by Name. Stable ordering is required for deterministic HTTP API
// output and stable test assertions.
func TestRegistry_List_Sorted(t *testing.T) {
	r := NewRegistry()
	// Register in non-sorted order.
	for _, n := range []string{"charlie", "alpha", "bravo"} {
		require.NoError(t, r.Register(newFakeTool(n)))
	}

	list := r.List()
	require.Len(t, list, 3)
	assert.Equal(t, "alpha", list[0].Name)
	assert.Equal(t, "bravo", list[1].Name)
	assert.Equal(t, "charlie", list[2].Name)

	// ToolInfo should carry the schema fields.
	assert.NotEmpty(t, list[0].Parameters)
	assert.Equal(t, "string", list[0].OutputSchema.Type)
}

// TestRegistry_List_Empty verifies that List on an empty Registry
// returns a non-nil empty slice (not nil), so JSON marshaling
// produces "[]" rather than "null".
func TestRegistry_List_Empty(t *testing.T) {
	r := NewRegistry()
	list := r.List()
	assert.NotNil(t, list, "List on empty Registry should return non-nil slice")
	assert.Empty(t, list)
}

// TestRegistry_List_IsSnapshot verifies that mutating the returned
// slice does not affect the Registry's internal state.
func TestRegistry_List_IsSnapshot(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(newFakeTool("alpha")))

	list := r.List()
	list[0].Name = "mutated"

	// Registry should still return the original name.
	got, err := r.Get("alpha")
	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Name())
}

// ─── Execute tests ────────────────────────────────────────────────────

// TestRegistry_Execute_Delegates verifies that Execute looks up the
// Tool and invokes its Execute method, returning the result verbatim.
func TestRegistry_Execute_Delegates(t *testing.T) {
	r := NewRegistry()
	tt := newFakeTool("echo")
	tt.execResult = "hello"
	require.NoError(t, r.Register(tt))

	args := map[string]interface{}{"x": "world"}
	result, err := r.Execute(context.Background(), "echo", args)
	require.NoError(t, err)
	assert.Equal(t, "hello", result)
	assert.Equal(t, 1, tt.callCount(), "Execute should delegate exactly once")
}

// TestRegistry_Execute_NotFound verifies that executing an unregistered
// name returns ErrToolNotRegistered (not a generic error).
func TestRegistry_Execute_NotFound(t *testing.T) {
	r := NewRegistry()
	_, err := r.Execute(context.Background(), "ghost", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrToolNotRegistered), "expected ErrToolNotRegistered, got %v", err)
}

// TestRegistry_Execute_ToolError verifies that when a Tool's Execute
// returns an error, Registry.Execute propagates it verbatim (not
// wrapped as ErrToolNotRegistered). Callers use errors.Is to
// distinguish "not registered" from "execution failed".
func TestRegistry_Execute_ToolError(t *testing.T) {
	r := NewRegistry()
	toolErr := errors.New("downstream timeout")
	tt := newFakeTool("failing")
	tt.execErr = toolErr
	require.NoError(t, r.Register(tt))

	_, err := r.Execute(context.Background(), "failing", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, toolErr), "tool error should propagate verbatim")
	// Crucially, it should NOT be ErrToolNotRegistered.
	assert.False(t, errors.Is(err, ErrToolNotRegistered), "tool execution error must not be conflated with not-registered")
}

// ─── Concurrency tests ────────────────────────────────────────────────

// TestRegistry_Concurrent_RegisterGet verifies that concurrent Register
// and Get calls are safe under -race. This is the canary for the
// RWMutex protecting the tools map.
func TestRegistry_Concurrent_RegisterGet(t *testing.T) {
	r := NewRegistry()
	const n = 50

	var wg sync.WaitGroup
	wg.Add(n * 2)

	// Half the goroutines register Tools.
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_ = r.Register(newFakeTool(string(rune('a'+i%26)) + "-" + itoa(i)))
		}(i)
	}

	// The other half read.
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, _ = r.Get(string(rune('a'+i%26)) + "-" + itoa(i))
			_ = r.List()
		}(i)
	}

	wg.Wait()
	// If we got here without a race detector failure, the test passes.
}

// TestRegistry_Concurrent_Execute verifies that concurrent Execute
// calls on the same Tool are safe (the Tool itself must be
// concurrency-safe; the Registry only guarantees map safety).
func TestRegistry_Concurrent_Execute(t *testing.T) {
	r := NewRegistry()
	tt := newFakeTool("shared")
	require.NoError(t, r.Register(tt))

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = r.Execute(context.Background(), "shared", nil)
		}()
	}
	wg.Wait()

	assert.Equal(t, n, tt.callCount(), "all concurrent Execute calls should land")
}

// ─── Helpers tests ────────────────────────────────────────────────────

// TestAsExecutable verifies the type-assertion helper returns nil for
// non-Executable values and the concrete Executable for Tool values.
func TestAsExecutable(t *testing.T) {
	assert.Nil(t, AsExecutable(nil))
	assert.Nil(t, AsExecutable("not a tool"))

	tt := newFakeTool("x")
	e := AsExecutable(tt)
	require.NotNil(t, e)
	_, err := e.Execute(context.Background(), nil)
	require.NoError(t, err)
}

// TestAsSchemaProvider verifies the type-assertion helper returns nil
// for non-SchemaProvider values and the concrete SchemaProvider for
// Tool values.
func TestAsSchemaProvider(t *testing.T) {
	assert.Nil(t, AsSchemaProvider(nil))
	assert.Nil(t, AsSchemaProvider(42))

	tt := newFakeTool("x")
	sp := AsSchemaProvider(tt)
	require.NotNil(t, sp)
	assert.NotEmpty(t, sp.Parameters())
	assert.Equal(t, "string", sp.OutputSchema().Type)
}

// ─── Test helpers ─────────────────────────────────────────────────────

// itoa is a tiny dependency-free int→string to avoid importing fmt
// in the concurrency test's hot loop (keeps the test focused).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
