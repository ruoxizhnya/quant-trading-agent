// Tests for tracer.go — NoopTracer / NoopSpan (S7-P1-5).
//
// Coverage goal: 100% of tracer.go. The tracer is the simplest of the
// five leaf subsystems (no I/O, no state), but the tests pin down the
// contract that downstream code relies on:
//   - StartSpan returns a non-nil span and a non-nil context.
//   - Span methods are no-ops that never panic.
//   - Span.End is idempotent (safe to call multiple times).
//   - The attribute constants and SpanName are stable strings.
package ai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface assertions. These are duplicated from
// tracer.go to catch accidental removal of the var _ declarations.
var (
	_ Tracer = NoopTracer{}
	_ Span   = NoopSpan{}
)

func TestNoopTracer_SatisfiesTracerInterface(t *testing.T) {
	// Runtime check that a NoopTracer value (not pointer) satisfies
	// Tracer. The interface is structural; if a method is renamed,
	// this line fails to compile.
	var tr Tracer = NoopTracer{}
	assert.NotNil(t, tr)
}

func TestNoopSpan_SatisfiesSpanInterface(t *testing.T) {
	var s Span = NoopSpan{}
	assert.NotNil(t, s)
}

func TestNoopTracer_StartSpan_ReturnsNonNilSpanAndContext(t *testing.T) {
	tr := NoopTracer{}
	ctxIn := context.Background()
	ctxOut, span := tr.StartSpan(ctxIn, "test.span", map[string]any{"k": "v"})
	require.NotNil(t, span, "span must never be nil — Chat() calls span.End() unconditionally")
	require.NotNil(t, ctxOut, "returned context must never be nil")
}

func TestNoopTracer_StartSpan_PreservesInputContext(t *testing.T) {
	// The NoopTracer must return the same context it was given so
	// downstream context-derived values (deadlines, request IDs) are
	// not lost. This is documented in the StartSpan contract.
	tr := NoopTracer{}
	ctxIn := context.WithValue(context.Background(), ctxKey("test"), "v")
	ctxOut, _ := tr.StartSpan(ctxIn, "test", nil)
	assert.Same(t, ctxIn, ctxOut,
		"NoopTracer must return the input context unchanged so context values are preserved")
}

func TestNoopTracer_StartSpan_HandlesNilAttrs(t *testing.T) {
	// StartSpan is called with nil attrs in some client paths; verify
	// it doesn't panic.
	tr := NoopTracer{}
	_, span := tr.StartSpan(context.Background(), "test", nil)
	span.End()
}

func TestNoopSpan_RecordError_NeverPanics(t *testing.T) {
	s := NoopSpan{}
	// Must not panic regardless of error / status inputs.
	assert.NotPanics(t, func() {
		s.RecordError(nil, 0)
		s.RecordError(errBoom, 500)
		s.RecordError(errBoom, 0)
	})
}

func TestNoopSpan_SetAttribute_NeverPanics(t *testing.T) {
	s := NoopSpan{}
	assert.NotPanics(t, func() {
		s.SetAttribute("string", "v")
		s.SetAttribute("int", 42)
		s.SetAttribute("nil", nil)
		s.SetAttribute("", nil)
	})
}

func TestNoopSpan_End_Idempotent(t *testing.T) {
	s := NoopSpan{}
	// End must be safe to call multiple times — Chat() calls End()
	// once via defer, and a caller that also calls End() must not
	// crash.
	assert.NotPanics(t, func() {
		s.End()
		s.End()
		s.End()
	})
}

func TestAttributeConstants_HaveExpectedValues(t *testing.T) {
	// Pin the attribute key strings — these are referenced by
	// external tracers (e.g. OTel wrappers in cmd/ai/main.go) and
	// by dashboards. Renaming them silently breaks downstream
	// consumers.
	assert.Equal(t, "ai.model", AttrAIModel)
	assert.Equal(t, "ai.prompt_tokens", AttrAIPromptTok)
	assert.Equal(t, "ai.completion_tokens", AttrAICompletionTok)
	assert.Equal(t, "ai.cost_usd", AttrAITotalCost)
	assert.Equal(t, "ai.http_status_code", AttrAIStatusCode)
	assert.Equal(t, "ai.duration_ms", AttrAIDurationMS)
	assert.Equal(t, "ai.retry_count", AttrAIRetryCount)
	assert.Equal(t, "ai.rate_limited", AttrAIRateLimited)
}

func TestSpanName_HasExpectedValue(t *testing.T) {
	// The span name follows the OTel `<package>.<operation>` convention.
	// Dashboards filter on this exact string; do not change.
	assert.Equal(t, "ai.client.chat", SpanName)
}

// ctxKey is a private type used only by context value tests in this
// package. Declared here (not in production code) so tests can verify
// NoopTracer.StartSpan preserves context values without polluting the
// production API.
type ctxKey string

// errBoom is a sentinel error reused by multiple test files. Declared
// here so tracer_test.go is self-contained, but kept as a package-level
// var so other tests can reference it if needed.
var errBoom = &boomErr{}

type boomErr struct{}

func (boomErr) Error() string { return "boom" }
