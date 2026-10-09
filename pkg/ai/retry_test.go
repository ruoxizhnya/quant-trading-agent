// Tests for retry.go — RetryPolicy.Do, backoff, IsRetryableStatus
// (S7-P1-5).
//
// Coverage goal: 100% of retry.go. The retry loop is the core of the
// AI client's resilience layer; we test success-on-first-attempt,
// retry-then-success on 5xx/429/network error, non-retry on 4xx,
// max-attempts exhaustion, context cancellation, attempt counter
// semantics, backoff growth/cap/zero, jitter, and concurrent safety.
//
// All backoff values are kept tiny (1ms) so the suite stays fast.
package ai

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastPolicy returns a RetryPolicy with the same retry semantics as
// DefaultRetryPolicy but with a 1ms backoff so tests don't wait.
func fastPolicy(maxAttempts int) RetryPolicy {
	return RetryPolicy{
		MaxAttempts:     maxAttempts,
		InitialBackoff:  1 * time.Millisecond,
		MaxBackoff:      5 * time.Millisecond,
		Jitter:          0.0,
		RetryableStatus: DefaultRetryPolicy.RetryableStatus,
	}
}

func TestRetryPolicy_Do_SuccessOnFirstAttempt(t *testing.T) {
	p := fastPolicy(3)
	var calls int32
	res, status, err := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		atomic.AddInt32(&calls, 1)
		return "ok", 200, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", res)
	assert.Equal(t, 200, status)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"success on first attempt must not retry")
}

func TestRetryPolicy_Do_RetriesOn5xxThenSucceeds(t *testing.T) {
	p := fastPolicy(3)
	var calls int32
	res, status, err := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return "", 500, nil
		}
		return "ok", 200, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", res)
	assert.Equal(t, 200, status)
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls),
		"two 500s then a 200 → 3 total attempts")
}

func TestRetryPolicy_Do_RetriesOn429ThenSucceeds(t *testing.T) {
	p := fastPolicy(3)
	var calls int32
	_, _, err := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			return "", http.StatusTooManyRequests, nil // 429
		}
		return "ok", 200, nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls),
		"429 must be retried (not treated as a hard 4xx failure)")
}

func TestRetryPolicy_Do_RetriesOnNetworkError(t *testing.T) {
	// Network errors arrive as err != nil, status == 0. The policy
	// must retry them (transient).
	p := fastPolicy(3)
	var calls int32
	_, _, err := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			return "", 0, errors.New("connection refused")
		}
		return "ok", 200, nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestRetryPolicy_Do_DoesNotRetryOn4xx(t *testing.T) {
	// 4xx (except 429) is non-retryable: caller error, won't fix itself.
	p := fastPolicy(3)
	var calls int32
	_, status, err := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		atomic.AddInt32(&calls, 1)
		return "", 400, nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNonRetryable, "4xx must surface as ErrNonRetryable")
	assert.Equal(t, 400, status, "status from the final attempt must be returned")
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"4xx must not be retried")
}

func TestRetryPolicy_Do_DoesNotRetryOn401(t *testing.T) {
	// Auth errors (401) are non-retryable — retrying with the same
	// key just burns attempts. Pin this so a future "retry on all
	// 4xx except 429" refactor doesn't accidentally retry auth.
	p := fastPolicy(3)
	var calls int32
	_, _, err := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		atomic.AddInt32(&calls, 1)
		return "", 401, nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNonRetryable)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestRetryPolicy_Do_MaxAttemptsExhausted(t *testing.T) {
	p := fastPolicy(3)
	var calls int32
	_, status, err := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		atomic.AddInt32(&calls, 1)
		return "", 503, nil // always 503
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max retries exhausted",
		"exhausted retries must surface a clear 'max retries exhausted' message")
	assert.Equal(t, 503, status, "final status must be the last attempt's status")
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls))
}

func TestRetryPolicy_Do_ContextCancelledStopsRetry(t *testing.T) {
	// Pre-cancel the context, then have the function return a
	// retryable 500. Do must honour the cancelled context and stop
	// retrying quickly, surfacing context.Canceled.
	//
	// Implementation note: backoff(1) returns 0, so on the first
	// retry the select is between a closed ctx.Done() and
	// time.After(0) — both ready. Go's select picks randomly, so Do
	// may make 1 or 2 attempts before ctx.Done() wins on the second
	// select (where backoff(2) > 0). Either way, the error must be
	// context.Canceled and the call count must be tiny (≤ 2), not
	// MaxAttempts (10).
	p := fastPolicy(10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int32
	_, _, err := p.Do(ctx, func(ctx context.Context, attempt int) (string, int, error) {
		atomic.AddInt32(&calls, 1)
		return "", 500, nil // retryable failure → Do tries to back off
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled,
		"cancelled context must surface as context.Canceled when Do tries to retry")
	assert.LessOrEqual(t, atomic.LoadInt32(&calls), int32(2),
		"Do must stop after at most 2 attempts when context is pre-cancelled")
}

func TestRetryPolicy_Do_AlreadyCancelledContext(t *testing.T) {
	p := fastPolicy(3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := p.Do(ctx, func(ctx context.Context, attempt int) (string, int, error) {
		return "ok", 200, nil
	})
	// First attempt succeeds (returns 200), so Do returns nil error.
	// This pins the contract: Do does not check ctx before the first
	// attempt — it relies on the function or HTTP layer to honour the
	// context. If the function succeeds, Do returns the success.
	require.NoError(t, err)
}

func TestRetryPolicy_Do_AttemptCounterStartsAt1(t *testing.T) {
	p := fastPolicy(3)
	var firstAttempt int32
	p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		if atomic.LoadInt32(&firstAttempt) == 0 {
			atomic.StoreInt32(&firstAttempt, int32(attempt))
		}
		return "", 500, nil // exhaust attempts
	})
	assert.Equal(t, int32(1), atomic.LoadInt32(&firstAttempt),
		"attempt counter must start at 1, not 0")
}

func TestRetryPolicy_Do_StatusReturnedFromFinalAttempt(t *testing.T) {
	// Even on exhaustion, the returned status is the final attempt's
	// status, not 0 or the first failure's status.
	p := fastPolicy(2)
	_, status, _ := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		if attempt == 1 {
			return "", 500, nil
		}
		return "", 503, nil
	})
	assert.Equal(t, 503, status)
}

func TestRetryPolicy_Do_ReturnsErrNonRetryableForNonRetryableStatus(t *testing.T) {
	// When the function returns an explicit err with a non-retryable
	// status (4xx except 429), Do wraps the result with ErrNonRetryable
	// so callers can errors.Is(err, ErrNonRetryable).
	//
	// Note: the original function error is currently NOT preserved in
	// the error chain — the code uses %v (not %w) when wrapping with
	// ErrNonRetryable. This pins the actual behaviour; if a future
	// change switches to %w, this test should be updated to also
	// assert ErrorIs(err, sentinel).
	p := fastPolicy(3)
	sentinel := errors.New("bad request body")
	_, _, err := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		return "", 400, sentinel
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNonRetryable,
		"non-retryable 4xx must surface as ErrNonRetryable for errors.Is")
	// The sentinel should at least appear in the error message (via %v).
	assert.Contains(t, err.Error(), "bad request body",
		"original error message must be included even if not in the chain")
}

func TestRetryPolicy_backoff_ZeroForFirstAttempt(t *testing.T) {
	p := fastPolicy(3)
	assert.Equal(t, time.Duration(0), p.backoff(1),
		"backoff(1) must be 0 — no wait before the first attempt")
}

func TestRetryPolicy_backoff_GrowsExponentially(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts:    5,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     1 * time.Second,
		Jitter:         0,
	}
	// attempt 2 → 1.0x = 10ms, attempt 3 → 2.0x = 20ms, attempt 4 → 4.0x = 40ms
	assert.Equal(t, 10*time.Millisecond, p.backoff(2))
	assert.Equal(t, 20*time.Millisecond, p.backoff(3))
	assert.Equal(t, 40*time.Millisecond, p.backoff(4))
}

func TestRetryPolicy_backoff_CappedAtMaxBackoff(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts:    10,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     250 * time.Millisecond,
		Jitter:         0,
	}
	// attempt 5 → 100ms * 2^3 = 800ms → capped at 250ms
	assert.Equal(t, 250*time.Millisecond, p.backoff(5))
	// attempt 10 → even bigger, still capped
	assert.Equal(t, 250*time.Millisecond, p.backoff(10))
}

func TestRetryPolicy_backoff_JitterStaysWithinBounds(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts:    5,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     1 * time.Second,
		Jitter:         0.25,
	}
	// Run the backoff calculation many times — every result must be
	// within ±25% of the base value (with 100ms base for attempt 2).
	base := 100 * time.Millisecond
	for i := 0; i < 100; i++ {
		got := p.backoff(2)
		min := time.Duration(float64(base) * 0.75)
		max := time.Duration(float64(base) * 1.25)
		assert.GreaterOrEqual(t, int64(got), int64(min),
			"jittered backoff %v must be >= %v", got, min)
		assert.LessOrEqual(t, int64(got), int64(max),
			"jittered backoff %v must be <= %v", got, max)
	}
}

func TestRetryPolicy_IsRetryableStatus_KnownStatuses(t *testing.T) {
	p := DefaultRetryPolicy
	for _, s := range []int{429, 500, 502, 503, 504} {
		assert.True(t, p.IsRetryableStatus(s),
			"status %d must be retryable per DefaultRetryPolicy", s)
	}
}

func TestRetryPolicy_IsRetryableStatus_NonRetryableStatuses(t *testing.T) {
	p := DefaultRetryPolicy
	for _, s := range []int{200, 400, 401, 403, 404, 422} {
		assert.False(t, p.IsRetryableStatus(s),
			"status %d must NOT be retryable", s)
	}
}

func TestRetryPolicy_IsRetryableStatus_EmptySetReturnsFalse(t *testing.T) {
	// A custom policy with no RetryableStatus — nothing is retryable.
	p := RetryPolicy{RetryableStatus: nil}
	assert.False(t, p.IsRetryableStatus(500))
}

func TestRetryPolicy_Do_DefaultsAppliedWhenZero(t *testing.T) {
	// A zero-value RetryPolicy must not panic and must apply sensible
	// defaults (MaxAttempts=1, InitialBackoff=500ms, MaxBackoff=4s).
	p := RetryPolicy{}
	var calls int32
	_, _, err := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		atomic.AddInt32(&calls, 1)
		return "", 500, nil
	})
	require.Error(t, err)
	// MaxAttempts defaults to 1 when < 1.
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"zero-value policy must default to MaxAttempts=1")
}

func TestRetryPolicy_Do_MaxAttemptsOneMeansNoRetry(t *testing.T) {
	p := fastPolicy(1)
	var calls int32
	_, _, err := p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
		atomic.AddInt32(&calls, 1)
		return "", 500, nil
	})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestRetryPolicy_ConcurrentDo_NoDataRace(t *testing.T) {
	// -race flag catches any data race in the RetryPolicy. The policy
	// is passed by value to Do, so each call gets its own copy — but
	// the receiver method backoff() reads p.InitialBackoff etc., so a
	// shared *RetryPolicy must be safe for concurrent reads.
	p := fastPolicy(3)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _, _ = p.Do(context.Background(), func(ctx context.Context, attempt int) (string, int, error) {
				return "ok", 200, nil
			})
		}()
	}
	wg.Wait()
}

func TestDefaultRetryPolicy_HasExpectedDefaults(t *testing.T) {
	// Pin the production defaults — operators rely on these for
	// capacity planning (3 attempts, 500ms→4s backoff, 25% jitter).
	assert.Equal(t, 3, DefaultRetryPolicy.MaxAttempts)
	assert.Equal(t, 500*time.Millisecond, DefaultRetryPolicy.InitialBackoff)
	assert.Equal(t, 4*time.Second, DefaultRetryPolicy.MaxBackoff)
	assert.InDelta(t, 0.25, DefaultRetryPolicy.Jitter, 1e-9)
	assert.Contains(t, DefaultRetryPolicy.RetryableStatus, http.StatusTooManyRequests)
	assert.Contains(t, DefaultRetryPolicy.RetryableStatus, http.StatusInternalServerError)
	assert.Contains(t, DefaultRetryPolicy.RetryableStatus, http.StatusBadGateway)
	assert.Contains(t, DefaultRetryPolicy.RetryableStatus, http.StatusServiceUnavailable)
	assert.Contains(t, DefaultRetryPolicy.RetryableStatus, http.StatusGatewayTimeout)
}
