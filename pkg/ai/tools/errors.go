// Package tools provides a registry of capabilities (Tools) that can be
// discovered and invoked by external agents via HTTP API.
//
// Design (S7-P3-3, ODR-043): this package turns quant-trading into a
// "tool provider" — backtest/factor/data/strategy capabilities are
// exposed as Tools with explicit schemas, so an external agent service
// can list them (GET /api/tools) and invoke them (POST /api/tools/:name)
// without reading SPEC.md or hand-crafting HTTP calls.
//
// This is a LEAF-style package for interfaces + Registry: it imports
// only the standard library. Concrete Tool implementations live in
// pkg/ai/tools/builtin/ to keep this package free of reverse dependencies.
package tools

import "errors"

// Sentinel errors for the tools package. All Registry methods and
// Tool implementations should wrap these via fmt.Errorf("%w: ...", ...)
// so callers can distinguish failure modes with errors.Is.
var (
	// ErrToolNotRegistered is returned by Get/Execute when no Tool is
	// registered under the requested name. Distinct from a Tool's own
	// execution error — this means the name is unknown to the Registry.
	ErrToolNotRegistered = errors.New("tools: tool not registered")

	// ErrEmptyToolName is returned by Register when a Tool's Name()
	// returns "". Empty names break HTTP routing (POST /api/tools/)
	// and make List output ambiguous.
	ErrEmptyToolName = errors.New("tools: tool name is empty")

	// ErrNilTool is returned by Register when the Tool argument is nil.
	// Registering nil would cause nil-pointer dereferences in Get/Execute.
	ErrNilTool = errors.New("tools: nil tool")

	// ErrInvalidArgs is returned by a Tool's Execute when the args map
	// is missing a required parameter, has a wrong-typed value, or
	// fails validation. The wrapped message should name the bad field.
	ErrInvalidArgs = errors.New("tools: invalid arguments")

	// ErrNotFound is returned by a Tool's Execute when the args were
	// valid but the addressed entity does not exist — e.g.
	// research.profile asked for a ticker that has no archived profile.
	// Distinct from ErrInvalidArgs (the request was well-formed) and
	// from a downstream failure (nothing broke, there is simply no
	// record). ToolsHandler maps it to HTTP 404.
	ErrNotFound = errors.New("tools: not found")
)
