package tools

// AsExecutable returns t as an Executable, or nil if t does not
// implement Executable. This is the safe downcast helper for callers
// that hold a ToolCore (e.g. from Registry.Get) and want to invoke
// Execute — it avoids a type-switch in the caller.
//
// Mirrors the strategy.AsConfigurable / AsSignalGenerator pattern
// in pkg/strategy/interfaces.go.
func AsExecutable(t any) Executable {
	if t == nil {
		return nil
	}
	e, ok := t.(Executable)
	if !ok {
		return nil
	}
	return e
}

// AsSchemaProvider returns t as a SchemaProvider, or nil if t does
// not implement SchemaProvider. Used by the HTTP handler when it
// needs to render a Tool's schema but only has a ToolCore reference.
func AsSchemaProvider(t any) SchemaProvider {
	if t == nil {
		return nil
	}
	s, ok := t.(SchemaProvider)
	if !ok {
		return nil
	}
	return s
}
