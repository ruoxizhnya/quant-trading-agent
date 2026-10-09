package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Registry manages a set of Tool instances keyed by their Name().
//
// Design (S7-P3-3): follows the pkg/data/source/Registry pattern
// (factory injection, allow replacement, sentinel errors, sorted List)
// rather than the older pkg/strategy/Registry pattern (global instance,
// reject duplicates). The factory-injection style avoids global state
// and makes Registry safe to construct multiple instances in tests.
//
// Concurrency: Registry is safe for concurrent use. All mutations take
// a write lock; all reads take a read lock. Tools themselves are
// expected to be concurrency-safe.
//
// Replacement semantics: Registering a Tool with the same Name as an
// existing one replaces it. This supports hot-reload (re-registering
// a Tool with updated config) and simplifies tests that need to
// re-initialize state. The previous Tool is not notified — Tools are
// expected to be stateless or to manage their own lifecycle.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds t to the registry under t.Name().
//
// Returns ErrNilTool if t is nil, or ErrEmptyToolName if t.Name() is "".
// If a Tool with the same name is already registered, it is replaced
// (hot-reload semantics).
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return ErrNilTool
	}
	name := t.Name()
	if name == "" {
		return ErrEmptyToolName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = t
	return nil
}

// Get returns the Tool registered under name.
//
// Returns ErrToolNotRegistered (wrapped with the name) if no Tool
// matches. Callers can distinguish "not found" from "execution failed"
// via errors.Is(err, ErrToolNotRegistered).
func (r *Registry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrToolNotRegistered, name)
	}
	return t, nil
}

// List returns a snapshot of all registered Tools' ToolInfo, sorted by
// Name for stable output. The slice is a copy — callers may mutate it
// without affecting the Registry.
func (r *Registry) List() []ToolInfo {
	r.mu.RLock()
	names := make([]string, 0, len(r.tools))
	toolsByName := make(map[string]Tool, len(r.tools))
	for n, t := range r.tools {
		names = append(names, n)
		toolsByName[n] = t
	}
	r.mu.RUnlock()

	sort.Strings(names)

	out := make([]ToolInfo, 0, len(names))
	for _, n := range names {
		t := toolsByName[n]
		info := ToolInfo{
			Name:        t.Name(),
			Description: t.Description(),
		}
		if sp := AsSchemaProvider(t); sp != nil {
			info.Parameters = sp.Parameters()
			info.OutputSchema = sp.OutputSchema()
		}
		out = append(out, info)
	}
	return out
}

// Execute looks up the Tool by name and invokes its Execute method.
//
// Returns ErrToolNotRegistered (wrapped) if name is unknown. Otherwise
// the Tool's own error is returned verbatim — callers use errors.Is to
// distinguish the two cases.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]interface{}) (interface{}, error) {
	t, err := r.Get(name)
	if err != nil {
		return nil, err
	}
	exec := AsExecutable(t)
	if exec == nil {
		// Defensive: a Tool that satisfies ToolCore + SchemaProvider
		// but not Executable is registered. This shouldn't happen with
		// the composite Tool interface, but guard against it so we
		// return a typed error rather than panicking on a nil call.
		return nil, fmt.Errorf("tools: %q is not Executable", name)
	}
	return exec.Execute(ctx, args)
}
