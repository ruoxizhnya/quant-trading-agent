// Tests for ratelimit.go — Limiter (S7-P1-5).
//
// Coverage goal: 100% of ratelimit.go. The limiter wraps
// golang.org/x/time/rate.Limiter; we test our wrapper's contract
// (defaults, custom rate/burst, nil-receiver safety, Wait cancellation,
// concurrent safety) rather than the underlying library's behaviour.
package ai

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLimiter_ReturnsNonNil(t *testing.T) {
	l := NewLimiter()
	require.NotNil(t, l)
	assert.NotNil(t, l.l, "underlying rate.Limiter must be initialised")
	assert.NotNil(t, l.Now, "Now func must be initialised (defaults to time.Now)")
}

func TestNewLimiter_DefaultsMatchConstants(t *testing.T) {
	l := NewLimiter()
	// Allow should succeed for up to DefaultBurst (4) immediate calls.
	for i := 0; i < DefaultBurst; i++ {
		assert.True(t, l.Allow(),
			"call %d/%d should be allowed within burst window", i+1, DefaultBurst)
	}
	// The very next call should be denied — burst exhausted, no time
	// has elapsed to refill tokens.
	assert.False(t, l.Allow(), "call beyond burst should be denied")
}

func TestNewLimiterWithRate_CustomRateAndBurst(t *testing.T) {
	l := NewLimiterWithRate(60.0, 2) // 1/sec, burst 2
	require.NotNil(t, l)
	assert.True(t, l.Allow(), "first call within burst should be allowed")
	assert.True(t, l.Allow(), "second call within burst should be allowed")
	assert.False(t, l.Allow(), "third call beyond burst should be denied")
}

func TestNewLimiterWithRate_ZeroRateFallsBackToDefault(t *testing.T) {
	// A zero (or negative) rate would cause rate.Limit(0) which means
	// "block all". The constructor falls back to DefaultRatePerMin so
	// callers can't accidentally build a permanently-blocked limiter.
	l := NewLimiterWithRate(0, 4)
	require.NotNil(t, l)
	// Default rate still allows burst through immediately.
	for i := 0; i < DefaultBurst; i++ {
		assert.True(t, l.Allow())
	}
}

func TestNewLimiterWithRate_NegativeRateFallsBackToDefault(t *testing.T) {
	l := NewLimiterWithRate(-5, 4)
	require.NotNil(t, l)
	assert.True(t, l.Allow(), "fallback rate should still allow burst")
}

func TestNewLimiterWithRate_ZeroBurstFallsBackToDefault(t *testing.T) {
	l := NewLimiterWithRate(60.0, 0)
	require.NotNil(t, l)
	for i := 0; i < DefaultBurst; i++ {
		assert.True(t, l.Allow(), "fallback burst should allow %d calls", i+1)
	}
}

func TestNewLimiterWithRate_NegativeBurstFallsBackToDefault(t *testing.T) {
	l := NewLimiterWithRate(60.0, -1)
	require.NotNil(t, l)
	assert.True(t, l.Allow())
}

func TestLimiter_Wait_ReturnsNilWhenTokenAvailable(t *testing.T) {
	// Fresh limiter with a generous burst — Wait should return
	// immediately.
	l := NewLimiterWithRate(1000.0, 10)
	err := l.Wait(context.Background())
	require.NoError(t, err)
}

func TestLimiter_Wait_ReturnsErrorOnCancelledContext(t *testing.T) {
	// Build a limiter that has already exhausted its burst, then
	// cancel the context before calling Wait. The Wait must surface
	// the context error rather than blocking forever.
	l := NewLimiterWithRate(0.01, 1) // very slow refill
	// Drain the single burst token.
	require.NoError(t, l.Wait(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	err := l.Wait(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limiter",
		"error should be wrapped with the 'ai rate limiter' prefix")
}

func TestLimiter_Wait_NilReceiverReturnsNil(t *testing.T) {
	// nil limiter = "no rate limiting" — used by tests that exercise
	// single-call latency. Must not panic.
	var l *Limiter
	err := l.Wait(context.Background())
	require.NoError(t, err)
}

func TestLimiter_Allow_NilReceiverReturnsTrue(t *testing.T) {
	var l *Limiter
	assert.True(t, l.Allow(), "nil limiter should allow all calls")
}

func TestLimiter_Reserve_ReturnsNonNilForNonEmptyLimiter(t *testing.T) {
	l := NewLimiter()
	r := l.Reserve()
	require.NotNil(t, r, "non-nil limiter must return a non-nil reservation")
}

func TestLimiter_Reserve_NilReceiverReturnsNil(t *testing.T) {
	var l *Limiter
	assert.Nil(t, l.Reserve())
}

func TestLimiter_Tokens_NilReceiverReturnsZero(t *testing.T) {
	var l *Limiter
	assert.Equal(t, 0, l.Tokens())
}

func TestLimiter_Tokens_NonNilReturnsReservedValue(t *testing.T) {
	// Tokens() currently returns -1 (reserved for future use) per the
	// source comment. Pin this so a future change to expose real token
	// count is intentional.
	l := NewLimiter()
	assert.Equal(t, -1, l.Tokens())
}

func TestLimiter_ConcurrentWait_NoDataRace(t *testing.T) {
	// -race flag would catch any data race in the underlying
	// rate.Limiter. Use a high rate + large burst so all goroutines
	// complete quickly.
	l := NewLimiterWithRate(10000.0, 100)

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_ = l.Wait(context.Background())
		}()
	}
	wg.Wait()
}

func TestLimiter_Wait_PropagatesContextError(t *testing.T) {
	// Use a context with a very short timeout and a slow limiter so
	// Wait blocks long enough to hit the deadline. This verifies the
	// error is propagated verbatim (not swallowed).
	l := NewLimiterWithRate(0.001, 1)                // refill every ~16 minutes
	require.NoError(t, l.Wait(context.Background())) // drain burst

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	err := l.Wait(ctx)
	require.Error(t, err)
}
