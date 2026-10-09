package tools

import "context"

// Tool is the composite interface every capability must implement.
//
// It is split into 3 single-responsibility sub-interfaces (ISP) so a
// caller can depend on only the facet it needs — e.g. the HTTP
// GET /api/tools handler uses only ToolCore + SchemaProvider to render
// the discovery list, without forcing Executable's Execute method.
//
// The decomposition mirrors pkg/strategy/interfaces.go (StrategyCore /
// Configurable / SignalGenerator / ResourceManaged).
type Tool interface {
	ToolCore
	SchemaProvider
	Executable
}

// ToolCore is the identity facet: every Tool has a stable Name and a
// human-readable Description.
//
// Name conventions (S7-P3-3):
//   - kebab-case + dot namespace: "backtest.run", "factor.compute",
//     "data.ohlcv", "strategy.list"
//   - globally unique within a Registry
//   - HTTP-safe (used as path parameter: POST /api/tools/:name)
type ToolCore interface {
	Name() string
	Description() string
}

// SchemaProvider exposes the Tool's input/output contract so an
// external agent can decide how to call it without trial-and-error.
//
// The schema is a simplified JSON-Schema: a flat list of Parameter
// structs (no nested objects) plus an OutputSchema describing the
// return shape. This is intentionally weaker than full JSON-Schema —
// it covers everything our 4 builtin Tools need and stays trivial to
// marshal to JSON for the HTTP API.
type SchemaProvider interface {
	// Parameters returns the input parameters in stable order.
	// Required parameters should come first for readability.
	Parameters() []Parameter

	// OutputSchema describes the value returned by Execute.
	// Type "object" should populate Fields; other types may leave it nil.
	OutputSchema() OutputSchema
}

// Executable is the facet that actually runs the Tool.
//
// args is a map from parameter Name to value. The concrete Tool is
// responsible for type-asserting each value (e.g.
// `args["strategy_name"].(string)`) and returning ErrInvalidArgs
// (wrapped with the field name) when a required value is missing or
// has the wrong type.
//
// result is any JSON-marshalable value: a struct, a map, a slice, a
// number, a string. The HTTP handler marshals it directly to the
// response body.
type Executable interface {
	Execute(ctx context.Context, args map[string]interface{}) (interface{}, error)
}

// Parameter describes one input argument to a Tool.
//
// Type is a lowercase string tag rather than a reflect.Kind to keep
// the schema JSON-friendly and to match the conventions used in
// strategy.Parameter (pkg/strategy/strategy.go). Accepted values:
//
//	"string"   — a single string
//	"[]string" — a string slice
//	"float"    — a float64
//	"int"      — an int
//	"bool"     — a bool
//
// Default is the value used when the caller omits the parameter.
// It is interface{} so any of the above types can be stored; nil means
// "no default" (combined with Required=true this means the caller must
// supply the value).
type Parameter struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Description string      `json:"description"`
	Required    bool        `json:"required"`
	Default     interface{} `json:"default,omitempty"`
}

// OutputSchema describes the shape of the value returned by Execute.
//
// Type is one of: "object", "array", "number", "string", "bool".
// When Type=="object", Fields should list the top-level keys an agent
// can expect in the result. Other Types may leave Fields nil.
type OutputSchema struct {
	Type        string        `json:"type"`
	Description string        `json:"description"`
	Fields      []OutputField `json:"fields,omitempty"`
}

// OutputField describes one top-level key in an object-typed Tool result.
type OutputField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// ToolInfo is the JSON-serializable summary returned by Registry.List
// and the HTTP GET /api/tools endpoint. It captures a Tool's identity
// + schema without its Executable facet, so it's safe to send to an
// external agent that has no Go type information.
type ToolInfo struct {
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	Parameters   []Parameter  `json:"parameters"`
	OutputSchema OutputSchema `json:"output_schema"`
}
