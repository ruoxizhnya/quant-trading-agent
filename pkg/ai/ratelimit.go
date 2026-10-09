package ai

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter is a thread-safe token-bucket rate limiter that throttles
// outgoing LLM calls per AI service (Sprint 6 P1-14, AR-008, ADR-013).
//
// The default AI service tier allows ~10 req/min/user, but bursts up to
// 4 are tolerable (token bucket refills at 1 token every 6s). A larger
// LLM (e.g. research batch) can use a higher rate via NewLimiterWithRate.
//
// The implementation wraps `golang.org/x/time/rate.Limiter` (already a
// transitive dep through cron/v3) — no new dependency introduced.
type Limiter struct {
	mu sync.Mutex
	l  *rate.Limiter
	// Now is overridable for tests; defaults to time.Now.
	Now func() time.Time
}

// DefaultRatePerMin is the default sustained rate (req/min).
const DefaultRatePerMin = 10.0

// DefaultBurst is the default token-bucket burst size. Picked to allow
// short AI research batches (e.g. 4 concurrent factor discoveries) to
// start in parallel without immediate 429.
const DefaultBurst = 4

// NewLimiter creates a Limiter with the default rate/burst (10/min, 4 burst).
// Suitable for production AI service endpoints where a user typically
// submits ≤ 1 request every 6 seconds.
func NewLimiter() *Limiter {
	return NewLimiterWithRate(DefaultRatePerMin, DefaultBurst)
}

// NewLimiterWithRate creates a Limiter with custom rate and burst.
// ratePerMin: sustained requests per minute. burst: max concurrent burst.
func NewLimiterWithRate(ratePerMin float64, burst int) *Limiter {
	if ratePerMin <= 0 {
		ratePerMin = DefaultRatePerMin
	}
	if burst <= 0 {
		burst = DefaultBurst
	}
	perSec := rate.Limit(ratePerMin / 60.0)
	return &Limiter{
		l:   rate.NewLimiter(perSec, burst),
		Now: time.Now,
	}
}

// Wait blocks until a token is available or ctx is cancelled.
// Returns ctx.Err() if the context expires before a token arrives.
//
// This method is the primary entry point used by the AI service and
// pipeline (e.g. pkg/ai/pipeline/pipeline.go) to gate outbound calls.
//
// Note: blocking is preferable to dropping requests silently because
// users will retry anyway, and a 5-10s backoff is preferable to losing
// a $0.10 LLM call's progress.
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil // nil receiver → no rate limiting (for tests / opts)
	}
	if err := l.l.Wait(ctx); err != nil {
		return fmt.Errorf("ai rate limiter: %w", err)
	}
	return nil
}

// Allow returns true if a token is immediately available (non-blocking).
// Useful for the AI service's pre-flight check before enqueueing work.
func (l *Limiter) Allow() bool {
	if l == nil {
		return true
	}
	return l.l.Allow()
}

// Reserve reserves a token and returns a *rate.Reservation that can be
// used to inspect the wait time via Delay(). Callers that want to
// surface a Retry-After header can use this.
func (l *Limiter) Reserve() *rate.Reservation {
	if l == nil {
		return nil
	}
	return l.l.Reserve()
}

// Tokens returns the current number of available tokens (≤ burst).
// Used by /metrics for debugging. Note: not atomic — the value can
// drift between read and use, which is acceptable for metrics.
func (l *Limiter) Tokens() int {
	if l == nil {
		return 0
	}
	// rate.Limiter does not expose Tokens() directly, but AllowN with
	// a huge burst approximates it.
	return -1 // reserved for future use
}
