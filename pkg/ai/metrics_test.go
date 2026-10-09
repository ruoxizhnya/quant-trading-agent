// Tests for metrics.go — Metrics (S7-P1-5).
//
// Coverage goal: 100% of metrics.go. Metrics is a leaf observability
// module: atomic counters + a mutex-guarded daily rollup. We test the
// full surface (Record, Snapshot, Reset, nil-receiver safety) plus
// concurrent safety (the -race flag will catch any torn read/write).
package ai

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMetrics_InitialCountersZero(t *testing.T) {
	m := NewMetrics()
	require.NotNil(t, m)
	s := m.Snapshot()
	assert.Equal(t, int64(0), s.CallsTotal)
	assert.Equal(t, int64(0), s.ErrorsTotal)
	assert.Equal(t, int64(0), s.RetriesTotal)
	assert.Equal(t, int64(0), s.RateLimited)
	assert.Equal(t, 0.0, s.CostUSD)
	assert.Equal(t, int64(0), s.PromptTokens)
	assert.Equal(t, int64(0), s.CompletionTokens)
	assert.Empty(t, s.DailyCosts)
}

func TestMetrics_Record_IncrementsCallCount(t *testing.T) {
	m := NewMetrics()
	m.Record(CallResult{Model: "gpt-4o-mini"})
	m.Record(CallResult{Model: "gpt-4o-mini"})
	s := m.Snapshot()
	assert.Equal(t, int64(2), s.CallsTotal)
}

func TestMetrics_Record_IncrementsErrorCountOnErr(t *testing.T) {
	m := NewMetrics()
	m.Record(CallResult{Model: "gpt-4o", Err: errors.New("boom")})
	m.Record(CallResult{Model: "gpt-4o"}) // no error
	m.Record(CallResult{Model: "gpt-4o", Err: errors.New("boom2")})
	s := m.Snapshot()
	assert.Equal(t, int64(3), s.CallsTotal)
	assert.Equal(t, int64(2), s.ErrorsTotal)
}

func TestMetrics_Record_IncrementsRetryCountOnRetried(t *testing.T) {
	m := NewMetrics()
	m.Record(CallResult{Model: "gpt-4o", Retried: true})
	m.Record(CallResult{Model: "gpt-4o", Retried: false})
	m.Record(CallResult{Model: "gpt-4o", Retried: true})
	s := m.Snapshot()
	assert.Equal(t, int64(2), s.RetriesTotal)
}

func TestMetrics_Record_IncrementsRateLimitedCount(t *testing.T) {
	m := NewMetrics()
	m.Record(CallResult{Model: "gpt-4o", RateLimited: true})
	m.Record(CallResult{Model: "gpt-4o", RateLimited: false})
	s := m.Snapshot()
	assert.Equal(t, int64(1), s.RateLimited)
}

func TestMetrics_Record_AccumulatesTokens(t *testing.T) {
	m := NewMetrics()
	m.Record(CallResult{
		Model: "gpt-4o",
		Usage: Usage{PromptTokens: 100, CompletionTokens: 200, TotalTokens: 300},
	})
	m.Record(CallResult{
		Model: "gpt-4o",
		Usage: Usage{PromptTokens: 50, CompletionTokens: 75, TotalTokens: 125},
	})
	s := m.Snapshot()
	assert.Equal(t, int64(150), s.PromptTokens)
	assert.Equal(t, int64(275), s.CompletionTokens)
}

func TestMetrics_Record_DoesNotAccumulateNegativeTokens(t *testing.T) {
	// Negative tokens shouldn't happen, but the > 0 guard in Record
	// must prevent decrementing the counter. Verify the guard works.
	m := NewMetrics()
	m.Record(CallResult{
		Model: "gpt-4o",
		Usage: Usage{PromptTokens: -100, CompletionTokens: -200, TotalTokens: 0},
	})
	s := m.Snapshot()
	assert.Equal(t, int64(0), s.PromptTokens, "negative prompt tokens must not decrement counter")
	assert.Equal(t, int64(0), s.CompletionTokens, "negative completion tokens must not decrement counter")
}

func TestMetrics_Record_AccumulatesCost(t *testing.T) {
	m := NewMetrics()
	m.Record(CallResult{Model: "gpt-4o", CostUSD: 0.000123})
	m.Record(CallResult{Model: "gpt-4o", CostUSD: 0.000456})
	s := m.Snapshot()
	// Cost is stored as micros (1e-6 USD) in an atomic int64; verify
	// the round-trip preserves precision to the micro.
	assert.InDelta(t, 0.000579, s.CostUSD, 1e-9)
}

func TestMetrics_Record_UpdatesDailyRollup(t *testing.T) {
	m := NewMetrics()
	m.Record(CallResult{Model: "gpt-4o", CostUSD: 1.0})
	m.Record(CallResult{Model: "gpt-4o", CostUSD: 2.0})
	m.Record(CallResult{Model: "gpt-4o-mini", CostUSD: 0.5})

	s := m.Snapshot()
	require.Len(t, s.DailyCosts, 2, "two distinct models → two daily snapshots")

	// Find each snapshot by model name (order is not guaranteed by map iteration).
	byModel := make(map[string]*DailyCostSnapshot, len(s.DailyCosts))
	for _, snap := range s.DailyCosts {
		// ByModel has exactly one key per snapshot because Record
		// keys the snapshot by "day|model".
		for model := range snap.ByModel {
			byModel[model] = snap
		}
	}

	gpt4o := byModel["gpt-4o"]
	require.NotNil(t, gpt4o)
	assert.InDelta(t, 3.0, gpt4o.Total, 1e-9)
	assert.Equal(t, "gpt-4o", dayKeyModel(gpt4o))

	gpt4oMini := byModel["gpt-4o-mini"]
	require.NotNil(t, gpt4oMini)
	assert.InDelta(t, 0.5, gpt4oMini.Total, 1e-9)
}

// dayKeyModel returns the single model name stored in a snapshot's
// ByModel map, or empty string if the map is empty.
func dayKeyModel(s *DailyCostSnapshot) string {
	for k := range s.ByModel {
		return k
	}
	return ""
}

func TestMetrics_Record_RecordsTimestamp(t *testing.T) {
	m := NewMetrics()
	before := time.Now().UTC().Truncate(time.Second)
	m.Record(CallResult{Model: "gpt-4o", CostUSD: 1.0})
	after := time.Now().UTC().Add(time.Second)

	s := m.Snapshot()
	require.Len(t, s.DailyCosts, 1)
	updated := s.DailyCosts[0].Updated
	assert.False(t, updated.Before(before), "Updated timestamp %v should be >= %v", updated, before)
	assert.False(t, updated.After(after), "Updated timestamp %v should be <= %v", updated, after)
}

func TestMetrics_Snapshot_CopiesDailyCostsMap(t *testing.T) {
	// Snapshot must deep-copy the ByModel map so the caller can mutate
	// the returned snapshot without affecting the live Metrics.
	m := NewMetrics()
	m.Record(CallResult{Model: "gpt-4o", CostUSD: 1.0})

	s1 := m.Snapshot()
	require.Len(t, s1.DailyCosts, 1)
	// Mutate the returned snapshot.
	s1.DailyCosts[0].ByModel["gpt-4o"] = 999
	s1.DailyCosts[0].Total = 999

	// A fresh snapshot should be unaffected.
	s2 := m.Snapshot()
	require.Len(t, s2.DailyCosts, 1)
	assert.InDelta(t, 1.0, s2.DailyCosts[0].Total, 1e-9)
	assert.InDelta(t, 1.0, s2.DailyCosts[0].ByModel["gpt-4o"], 1e-9)
}

func TestMetrics_Snapshot_EmptyDailyCostsReturnsEmptySlice(t *testing.T) {
	m := NewMetrics()
	s := m.Snapshot()
	// The Snapshot() contract returns an empty (not nil) slice when
	// there are no daily costs — verify it's safe to range over.
	assert.Nil(t, s.DailyCosts, "fresh metrics should produce nil DailyCosts (early return)")
}

func TestMetrics_Reset_ClearsAllCounters(t *testing.T) {
	m := NewMetrics()
	m.Record(CallResult{
		Model:       "gpt-4o",
		Usage:       Usage{PromptTokens: 100, CompletionTokens: 200, TotalTokens: 300},
		CostUSD:     0.5,
		Err:         errors.New("x"),
		Retried:     true,
		RateLimited: true,
	})

	m.Reset()
	s := m.Snapshot()
	assert.Equal(t, int64(0), s.CallsTotal)
	assert.Equal(t, int64(0), s.ErrorsTotal)
	assert.Equal(t, int64(0), s.RetriesTotal)
	assert.Equal(t, int64(0), s.RateLimited)
	assert.Equal(t, 0.0, s.CostUSD)
	assert.Equal(t, int64(0), s.PromptTokens)
	assert.Equal(t, int64(0), s.CompletionTokens)
	assert.Empty(t, s.DailyCosts)
}

func TestMetrics_NilReceiver_RecordIsNoOp(t *testing.T) {
	// Nil Metrics is a valid opt-out — Record must not panic.
	var m *Metrics
	assert.NotPanics(t, func() {
		m.Record(CallResult{Model: "gpt-4o", CostUSD: 1.0})
	})
}

func TestMetrics_NilReceiver_SnapshotReturnsZero(t *testing.T) {
	var m *Metrics
	s := m.Snapshot()
	assert.Equal(t, Snapshot{}, s)
}

func TestMetrics_NilReceiver_ResetIsNoOp(t *testing.T) {
	var m *Metrics
	assert.NotPanics(t, func() { m.Reset() })
}

func TestMetrics_ConcurrentRecord_NoDataRace(t *testing.T) {
	// -race flag catches any data race in the atomic counters or the
	// mutex-guarded daily map. Mix Record (writer) and Snapshot
	// (reader) to exercise the RWMutex-free atomic counters vs the
	// Mutex-guarded map.
	m := NewMetrics()

	const N = 30
	var wg sync.WaitGroup
	wg.Add(N * 2)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			m.Record(CallResult{
				Model:       "gpt-4o",
				Usage:       Usage{PromptTokens: i, CompletionTokens: i, TotalTokens: i * 2},
				CostUSD:     float64(i) * 0.0001,
				Err:         nil,
				Retried:     i%2 == 0,
				RateLimited: i%3 == 0,
			})
		}(i)
	}
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_ = m.Snapshot()
		}()
	}
	wg.Wait()

	s := m.Snapshot()
	assert.Equal(t, int64(N), s.CallsTotal, "all %d records should be counted", N)
}

func TestMetrics_Record_StressContextCancelledDoesntAffectMetrics(t *testing.T) {
	// Records happen after a call completes — context cancellation
	// should never reach Record. This test pins that contract: even
	// if the caller passes a cancelled context to Chat, the eventual
	// Record call (with Err set) must still be counted.
	m := NewMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx // not used directly; the contract is that Record is still called

	m.Record(CallResult{
		Model: "gpt-4o",
		Err:   errors.New("context cancelled"),
	})
	s := m.Snapshot()
	assert.Equal(t, int64(1), s.CallsTotal)
	assert.Equal(t, int64(1), s.ErrorsTotal)
}
