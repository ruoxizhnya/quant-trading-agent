package ai

import (
	"sync"
	"sync/atomic"
	"time"
)

// Metrics tracks per-call observability for the AI client
// (Sprint 6 P1-14, AR-008, AR-017). It exposes:
//   - per-call latency, status code, token usage, cost
//   - aggregate counts (calls, errors, retries, rate-limited)
//   - per-model daily cost rollup
//
// All mutations are goroutine-safe; reads (Snapshot, DailyCost) are
// eventually consistent (eventual consistency is acceptable for
// observability — we never need a strict point-in-time view).
//
// The struct is intentionally lightweight: no Prometheus dependency,
// no reflection. Downstream code (cmd/ai/main.go) can wrap calls
// with a Prometheus collector if /metrics export is desired.
type Metrics struct {
	// Atomic counters — single-writer, multi-reader.
	callsTotal       atomic.Int64
	errorsTotal      atomic.Int64
	retriesTotal     atomic.Int64
	rateLimited      atomic.Int64
	costMicros       atomic.Int64 // total cost in micros (1e-6 USD) to avoid float atomics
	promptTokens     atomic.Int64
	completionTokens atomic.Int64

	// Per-model daily rollup. Mutex-guarded because the inner map
	// mutation is non-atomic.
	mu         sync.Mutex
	dailyCosts map[string]*DailyCostSnapshot // key = "YYYY-MM-DD|model"
}

// NewMetrics returns a fresh Metrics tracker.
func NewMetrics() *Metrics {
	return &Metrics{
		dailyCosts: make(map[string]*DailyCostSnapshot, 32),
	}
}

// CallResult is the per-call summary recorded by Record().
type CallResult struct {
	Model       string
	Usage       Usage
	CostUSD     float64
	Duration    time.Duration
	StatusCode  int   // HTTP status; 0 = network error
	Err         error // non-nil for failed calls
	Retried     bool  // true if this call was a retry (not the first attempt)
	RateLimited bool  // true if this call waited on the rate limiter
}

// Record persists one CallResult into the metrics tracker. Safe to call
// concurrently from any goroutine.
func (m *Metrics) Record(r CallResult) {
	if m == nil {
		return // nil-safe for tests / opt-out
	}
	m.callsTotal.Add(1)
	if r.Err != nil {
		m.errorsTotal.Add(1)
	}
	if r.Retried {
		m.retriesTotal.Add(1)
	}
	if r.RateLimited {
		m.rateLimited.Add(1)
	}
	if r.Usage.PromptTokens > 0 {
		m.promptTokens.Add(int64(r.Usage.PromptTokens))
	}
	if r.Usage.CompletionTokens > 0 {
		m.completionTokens.Add(int64(r.Usage.CompletionTokens))
	}
	// Cost stored in micros (1e-6 USD) for atomic int accumulation.
	m.costMicros.Add(int64(r.CostUSD * 1_000_000))

	// Per-model daily rollup.
	day := time.Now().UTC().Format("2006-01-02")
	key := day + "|" + r.Model
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, ok := m.dailyCosts[key]
	if !ok {
		snap = &DailyCostSnapshot{
			Date:    day,
			ByModel: map[string]float64{r.Model: 0},
		}
		m.dailyCosts[key] = snap
	}
	snap.ByModel[r.Model] += r.CostUSD
	snap.Total += r.CostUSD
	snap.Updated = time.Now().UTC()
}

// Snapshot is a point-in-time view of the metrics counters.
// Counters are read atomically; the daily-cost map is read under the
// mutex and copied to avoid races with the caller.
type Snapshot struct {
	CallsTotal       int64
	ErrorsTotal      int64
	RetriesTotal     int64
	RateLimited      int64
	CostUSD          float64
	PromptTokens     int64
	CompletionTokens int64
	// DailyCosts is a copy of the daily rollup at snapshot time.
	DailyCosts []*DailyCostSnapshot
}

// Snapshot returns a consistent-ish view of the metrics counters.
// "Consistent-ish" because counters and the daily map are not read
// under a single lock — but observability does not need strict
// point-in-time consistency.
func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	s := Snapshot{
		CallsTotal:       m.callsTotal.Load(),
		ErrorsTotal:      m.errorsTotal.Load(),
		RetriesTotal:     m.retriesTotal.Load(),
		RateLimited:      m.rateLimited.Load(),
		CostUSD:          float64(m.costMicros.Load()) / 1_000_000,
		PromptTokens:     m.promptTokens.Load(),
		CompletionTokens: m.completionTokens.Load(),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.dailyCosts) == 0 {
		return s
	}
	s.DailyCosts = make([]*DailyCostSnapshot, 0, len(m.dailyCosts))
	for _, snap := range m.dailyCosts {
		copy := *snap
		copy.ByModel = make(map[string]float64, len(snap.ByModel))
		for k, v := range snap.ByModel {
			copy.ByModel[k] = v
		}
		s.DailyCosts = append(s.DailyCosts, &copy)
	}
	return s
}

// Reset clears all counters and daily rollups. Used by tests; not
// intended for production use.
func (m *Metrics) Reset() {
	if m == nil {
		return
	}
	m.callsTotal.Store(0)
	m.errorsTotal.Store(0)
	m.retriesTotal.Store(0)
	m.rateLimited.Store(0)
	m.costMicros.Store(0)
	m.promptTokens.Store(0)
	m.completionTokens.Store(0)
	m.mu.Lock()
	m.dailyCosts = make(map[string]*DailyCostSnapshot, 32)
	m.mu.Unlock()
}
