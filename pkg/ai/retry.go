package ai

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"time"
)

// RetryPolicy controls how transient failures are retried
// (Sprint 6 P1-14, AR-008). Defaults are conservative (3 attempts,
// 500ms→4s exponential backoff with 25% jitter).
//
// The policy distinguishes between retryable and non-retryable errors:
//   - 4xx (except 429) is NEVER retried — caller error, won't fix itself.
//   - 5xx and 429 are retried with backoff.
//   - Network errors (timeout, EOF, connection refused) are retried.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the first.
	// Must be >= 1. MaxAttempts = 1 disables retry.
	MaxAttempts int

	// InitialBackoff is the wait before the second attempt.
	// Subsequent attempts use exponential backoff (InitialBackoff * 2^(n-1)).
	InitialBackoff time.Duration

	// MaxBackoff caps the per-attempt wait time.
	MaxBackoff time.Duration

	// Jitter is the ± fraction of the backoff (0.0 = no jitter, 0.25 = ±25%).
	// Recommended: 0.25 to avoid thundering-herd when many clients
	// retry simultaneously.
	Jitter float64

	// RetryableStatus is the set of HTTP status codes that should be
	// retried. Defaults: {429, 500, 502, 503, 504}.
	RetryableStatus []int
}

// DefaultRetryPolicy is a sensible production default: 3 attempts,
// 500ms→2s exponential backoff with 25% jitter, retry on 429/5xx.
var DefaultRetryPolicy = RetryPolicy{
	MaxAttempts:     3,
	InitialBackoff:  500 * time.Millisecond,
	MaxBackoff:      4 * time.Second,
	Jitter:          0.25,
	RetryableStatus: []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
}

// ErrNonRetryable is returned by Do when a non-retryable error is
// observed (e.g. 4xx except 429). Wrapped with %w so callers can use
// errors.Is.
var ErrNonRetryable = errors.New("ai: non-retryable error")

// RetryableFunc is the signature of the function passed to Do. It must
// return (result, httpStatus, err). The httpStatus is used for
// retry-eligibility checks; err is returned to the caller if non-nil
// AND non-retryable.
type RetryableFunc func(ctx context.Context, attempt int) (result string, status int, err error)

// Do executes f with retry semantics. It is the core of the AI client's
// resilience layer.
//
// The attempt counter starts at 1 and is incremented for each retry.
// On the last attempt, Do returns the error from f regardless of
// retryability (the caller's last chance).
//
// The returned status is the HTTP status from the final attempt
// (0 if the call never reached the wire), so callers can attribute
// metrics/span attributes without re-plumbing the attempt loop.
func (p RetryPolicy) Do(ctx context.Context, f RetryableFunc) (string, int, error) {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = 500 * time.Millisecond
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = 4 * time.Second
	}
	if p.Jitter < 0 {
		p.Jitter = 0
	}

	var (
		lastErr    error
		lastStatus int
	)
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		result, status, err := f(ctx, attempt)
		lastStatus = status
		if err == nil && status < 400 {
			return result, status, nil
		}
		// Construct an error describing the failure.
		if err == nil {
			err = fmt.Errorf("ai: HTTP %d", status)
		} else {
			err = fmt.Errorf("ai: HTTP %d: %w", status, err)
		}
		lastErr = err

		// Don't retry non-retryable status codes (4xx except 429).
		if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
			return "", lastStatus, fmt.Errorf("%w: %v", ErrNonRetryable, err)
		}
		// Last attempt: return the error.
		if attempt == p.MaxAttempts {
			break
		}

		// Compute backoff with exponential growth + jitter.
		backoff := p.backoff(attempt)
		select {
		case <-ctx.Done():
			return "", lastStatus, ctx.Err()
		case <-time.After(backoff):
			// continue
		}
	}
	return "", lastStatus, fmt.Errorf("ai: max retries exhausted (%d attempts): %w", p.MaxAttempts, lastErr)
}

// backoff computes the wait time for the given attempt (1-indexed).
// attempt 1 → 0 wait (caller hasn't returned yet, so this is for the
// "wait before attempt 2" case). Internally called with attempt >= 2.
//
// Wait formula: min(InitialBackoff * 2^(attempt-2), MaxBackoff) ± jitter.
func (p RetryPolicy) backoff(attempt int) time.Duration {
	if attempt < 2 {
		return 0
	}
	// Shift so attempt 2 → 1.0x, attempt 3 → 2.0x, attempt 4 → 4.0x
	mult := 1 << (attempt - 2) // 2^(attempt-2), capped to avoid overflow
	d := p.InitialBackoff * time.Duration(mult)
	if d > p.MaxBackoff || d < 0 { // overflow check
		d = p.MaxBackoff
	}
	if p.Jitter > 0 {
		// ±Jitter fraction. Use math/rand (not crypto/rand) for
		// speed; this is not security-sensitive.
		jitterRange := float64(d) * p.Jitter
		jitter := (rand.Float64()*2 - 1) * jitterRange // [-J, +J]
		d = time.Duration(float64(d) + jitter)
		if d < 0 {
			d = 0
		}
	}
	return d
}

// IsRetryableStatus returns true if the HTTP status is in the
// retryable set (5xx or 429). Useful for callers that want to inspect
// the policy without running Do.
func (p RetryPolicy) IsRetryableStatus(status int) bool {
	for _, s := range p.RetryableStatus {
		if s == status {
			return true
		}
	}
	return false
}
