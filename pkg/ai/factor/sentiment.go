package factor

import (
	"math"
	"sort"
	"time"

	"github.com/ruoxizhnya/quant-trading/pkg/data/source"
)

// HotSearchRow is a normalized hot-search / news record from
// Xueqiu (or any other social-signal source).
type HotSearchRow struct {
	Symbol    string
	TradeTime time.Time
	Rank      int
	Keyword   string
	Heat      float64
	Title     string
	Sentiment float64 // -1 (bearish) to +1 (bullish), 0 if unknown
}

// HotSearchFromPoints projects xueqiu hot_search / news records.
func HotSearchFromPoints(points []source.UnifiedDataPoint) []HotSearchRow {
	out := make([]HotSearchRow, 0, len(points))
	for _, p := range points {
		switch p.DataType {
		case source.DataTypeHotSearch:
			out = append(out, HotSearchRow{
				Symbol:    p.Symbol,
				TradeTime: p.TradeTime,
				Rank:      int(floatField(p.Data, "rank")),
				Keyword:   stringField(p.Data, "keyword"),
				Heat:      floatField(p.Data, "heat"),
			})
		case source.DataTypeNews:
			out = append(out, HotSearchRow{
				Symbol:    p.Symbol,
				TradeTime: p.TradeTime,
				Title:     stringField(p.Data, "title"),
				Sentiment: floatField(p.Data, "sentiment"),
			})
		}
	}
	return out
}

// SentimentFactor combines hot-search rank and news sentiment into a
// per-symbol score in [0, 1]. Higher = more positive buzz.
//
// The intuition:
//   - A keyword in the top-20 hot search is "trending"; a stock
//     associated with it deserves attention.
//   - News with a positive sentiment lifts the score; bearish
//     news drags it down.
//
// The lookback window is implicit (the caller passes a time window
// when fetching from the registry). Aggregation across multiple
// records per symbol uses a weighted sum: more recent events count
// more (linearly decaying weight over 1 / 7 days).
//
// NaN/Inf handling: rows with non-finite Sentiment or Heat are dropped
// before they enter the accumulator. CR-53 (ODR-012) regression test
// pins this; a single NaN row would otherwise void the entire
// cross-section's IC computation.
func SentimentFactor(rows []HotSearchRow, refTime time.Time) map[string]float64 {
	if len(rows) == 0 {
		return map[string]float64{}
	}
	// decayPerDay = 1/7 means a 7-day-old event contributes half of
	// a 1-day-old event. Tunable from a config in a future PR.
	const decayPerDay = 1.0 / 7.0

	type accum struct {
		score  float64
		weight float64
	}
	bySym := make(map[string]*accum, 16)
	for _, r := range rows {
		if r.Symbol == "" {
			continue
		}
		// CR-53 (ODR-012): drop non-finite signals before they reach
		// the accumulator. The Heat field is also checked because
		// future logic may incorporate it; better to be defensive
		// now than chase a regression when someone adds that path.
		if math.IsNaN(r.Sentiment) || math.IsInf(r.Sentiment, 0) {
			continue
		}
		if math.IsNaN(r.Heat) || math.IsInf(r.Heat, 0) {
			continue
		}
		daysOld := refTime.Sub(r.TradeTime).Hours() / 24
		if daysOld < 0 {
			daysOld = 0
		}
		// weight = 1 / (1 + decayPerDay * daysOld)
		// This is a soft half-life decay: at daysOld = 1/decayPerDay
		// the weight is 1/2.
		w := 1.0 / (1.0 + decayPerDay*daysOld)
		var raw float64
		hasSignal := false
		switch {
		case r.Rank > 0 && r.Rank <= 20:
			// Top 20 hot search: rank 1 → +1, rank 20 → ~0.05.
			raw = 1.0 - float64(r.Rank-1)/20.0
			if raw < 0 {
				raw = 0
			}
			hasSignal = true
		case r.Title != "" || r.Keyword != "" || r.Sentiment != 0:
			// News or out-of-top20 hot search with a body. Map
			// sentiment in [-1, 1] to [0, 1]. Missing sentiment
			// (Title/Keyword present, Sentiment==0) defaults to
			// neutral 0.5 so a stock with buzz but no sentiment
			// score still gets a moderate signal.
			raw = (r.Sentiment + 1) / 2
			hasSignal = true
		}
		if !hasSignal {
			continue
		}
		// Belt-and-braces: even with the early filter above, an
		// upstream parser may one day produce a non-finite `raw`
		// (e.g. a divide-by-zero in a future weighting tweak). Skip
		// rather than poison the accumulator.
		if math.IsNaN(raw) || math.IsInf(raw, 0) {
			continue
		}
		a, ok := bySym[r.Symbol]
		if !ok {
			a = &accum{}
			bySym[r.Symbol] = a
		}
		a.score += raw * w
		a.weight += w
	}

	out := make(map[string]float64, len(bySym))
	for sym, a := range bySym {
		if a.weight == 0 {
			continue
		}
		v := a.score / a.weight
		// Clip to [0, 1] to bound the IC computation.
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		out[sym] = v
	}
	return out
}

// TopBuzzSymbols returns the top-N symbols by SentimentFactor. Used
// as a screening helper for "social buzz" strategies.
func TopBuzzSymbols(rows []HotSearchRow, refTime time.Time, n int) []string {
	scored := SentimentFactor(rows, refTime)
	type kv struct {
		sym string
		v   float64
	}
	pairs := make([]kv, 0, len(scored))
	for sym, v := range scored {
		pairs = append(pairs, kv{sym, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].v > pairs[j].v
	})
	if n > 0 && n < len(pairs) {
		pairs = pairs[:n]
	}
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.sym
	}
	return out
}

// IsHotSearchRowValid is the validation guard for ETL.
func IsHotSearchRowValid(r HotSearchRow) bool {
	if r.Symbol == "" {
		return false
	}
	if r.TradeTime.IsZero() {
		return false
	}
	if math.IsNaN(r.Heat) || math.IsInf(r.Heat, 0) {
		return false
	}
	return true
}
