package ai

import (
	"context"
	"time"
)

// Tracer is a minimal interface for AI call observability
// (Sprint 6 P1-14, AR-008). It is intentionally decoupled from any
// specific tracing library (OTel, Datadog, etc.) so the AI client does
// not gain a heavy dependency. Downstream packages can wrap an OTel
// tracer with this interface and inject it via WithTracer.
//
// Three entry points cover the lifecycle of a single AI call:
//   - StartSpan begins a span and returns its ID (typically a trace ID).
//   - RecordError annotates the span with an error (and its status code).
//   - EndSpan closes the span, recording the duration.
//
// Implementations should be non-blocking — RecordError / EndSpan must
// not block the calling goroutine on a network call. Buffer or
// fire-and-forget.
//
// The no-op default (`NoopTracer{}`) is used when no tracer is
// configured; it does nothing and is allocation-free.
type Tracer interface {
	StartSpan(ctx context.Context, name string, attrs map[string]any) (ctx2 context.Context, span Span)
}

// Span is a single traced operation. It exposes the most common
// observability hooks without committing to a specific library.
type Span interface {
	// RecordError annotates the span with an error.
	RecordError(err error, statusCode int)
	// SetAttribute sets a key/value attribute on the span.
	SetAttribute(key string, value any)
	// End closes the span. Must be safe to call multiple times.
	End()
}

// NoopTracer returns a no-op span. Used by default and in tests.
type NoopTracer struct{}

// StartSpan implements Tracer. The returned context is the same as ctx.
func (NoopTracer) StartSpan(ctx context.Context, _ string, _ map[string]any) (context.Context, Span) {
	return ctx, NoopSpan{}
}

// NoopSpan is a no-op span.
type NoopSpan struct{}

// RecordError implements Span.
func (NoopSpan) RecordError(_ error, _ int) {}

// SetAttribute implements Span.
func (NoopSpan) SetAttribute(_ string, _ any) {}

// End implements Span.
func (NoopSpan) End() {}

// Compile-time assertions.
var (
	_ Tracer = NoopTracer{}
	_ Span   = NoopSpan{}
)

// Common span attribute keys. These are kept as constants so the
// service / pipeline can reference them without typo risk. The values
// are deliberately untyped (string / int / time.Duration) so they
// can flow through any tracer implementation.
const (
	AttrAIModel         = "ai.model"
	AttrAIPromptTok     = "ai.prompt_tokens"
	AttrAICompletionTok = "ai.completion_tokens"
	AttrAITotalCost     = "ai.cost_usd"
	AttrAIStatusCode    = "ai.http_status_code"
	AttrAIDurationMS    = "ai.duration_ms"
	AttrAIRetryCount    = "ai.retry_count"
	AttrAIRateLimited   = "ai.rate_limited"
)

// SpanName is the conventional span name for AI client calls. The
// pattern `<package>.<operation>` follows OTel semantic conventions
// and gives dashboards a stable label to filter on.
const SpanName = "ai.client.chat"

// Now is a wallclock function exposed for tests to override. Defaults
// to time.Now via init() in client.go.
var Now = time.Now
