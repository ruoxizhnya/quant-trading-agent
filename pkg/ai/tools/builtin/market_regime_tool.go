package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/risk"
)

// ═══════════════════════════════════════════════════════════════════════
//  GetMarketRegimeTool (Hermes Phase 2.3)
// ═══════════════════════════════════════════════════════════════════════
//
// GetMarketRegimeTool wraps risk.RiskManager.DetectRegime to give Hermes
// a single-call view of the prevailing market regime (trend + volatility +
// sentiment) for a symbol and date range. It is the market-awareness
// companion to get_strategy_lineage (which is the history-awareness tool):
// together they let the agent answer "what regime was this strategy
// designed for, and what regime are we entering now?" before mutating or
// deploying a strategy.
//
// Why this tool fetches OHLCV internally: DetectRegime takes a []domain.OHLCV
// slice, but asking the agent to call data.ohlcv first and then pass the
// (potentially 200+ record) result inline would waste context tokens and
// add a round-trip. Instead, this tool takes a symbol + date range, fetches
// OHLCV from the data-service via the shared DataSourceClient, converts it
// to []domain.OHLCV, and hands it to the regime detector — one tool call,
// one result.
//
// Default symbol: "000300.SH" (CSI 300 index) — the standard A-share market
// benchmark. The agent can override with any stock or index symbol the
// data-service has OHLCV for.
//
// Tool name: "get_market_regime"
// Input: start_date (required), end_date (required), symbol (optional, default 000300.SH)
// Output: *marketRegimeResult — flattened regime fields + metadata

const (
	// DefaultMarketRegimeSymbol is the symbol used when the caller doesn't
	// specify one. CSI 300 (000300.SH) is the standard A-share large-cap
	// benchmark and aligns with the default "csi300" universe used by the
	// backtest engine.
	DefaultMarketRegimeSymbol = "000300.SH"
)

// RegimeDetectorClient is the narrow interface satisfied by
// *risk.RiskManager. Extracted so the tool can be unit-tested with a mock
// instead of constructing a real RiskManager (which needs a config + logger
// and would pull in the full risk package's initialization).
//
// Method signature matches (*risk.RiskManager).DetectRegime exactly —
// the compile-time assertion below verifies this.
type RegimeDetectorClient interface {
	// DetectRegime classifies the market regime from a slice of OHLCV bars.
	// Returns an error (typically errors.DataQuality) when len(ohlcv) is
	// below the detector's SlowMAPeriod (usually 200 bars).
	DetectRegime(ctx context.Context, ohlcv []domain.OHLCV) (*domain.MarketRegime, error)
}

// OHLCVFetcher is the narrow interface satisfied by *DataSourceClient.
// Extracted so the tool can be unit-tested with a mock that returns
// canned OHLCV records, without spinning up an httptest server.
//
// Method added to DataSourceClient in datafetch.go (FetchOHLCV) — the
// compile-time assertion below verifies it.
type OHLCVFetcher interface {
	// FetchOHLCV retrieves raw OHLCV records from the data-service for a
	// single symbol over a date range. Dates are YYYY-MM-DD. Returns the
	// raw data-service response as a slice of generic maps (same shape as
	// the data.ohlcv tool's output).
	FetchOHLCV(ctx context.Context, symbol, startDate, endDate string) ([]map[string]interface{}, error)
}

// Compile-time assertions that the concrete types satisfy the narrow
// interfaces. If these fail, the tool wiring in setup.go would also fail —
// but failing at compile time is strictly better than failing at startup.
var (
	_ RegimeDetectorClient = (*risk.RiskManager)(nil)
	_ OHLCVFetcher         = (*DataSourceClient)(nil)
)

// marketRegimeResult is the flattened, LLM-friendly return value of
// GetMarketRegimeTool.Execute. Rather than nesting *domain.MarketRegime
// inside a wrapper, we flatten its fields to the top level — this keeps
// the JSON one level shallower, which is easier for the agent to parse
// and reason about.
//
// Fields:
//   - Symbol / StartDate / EndDate / BarsAnalyzed: provenance metadata
//     so the agent can cite what data the regime was computed from.
//   - Trend / Volatility / Sentiment / AsOf: the regime classification
//     itself, flattened from *domain.MarketRegime.
type marketRegimeResult struct {
	Symbol       string    `json:"symbol"`
	StartDate    string    `json:"start_date"`
	EndDate      string    `json:"end_date"`
	BarsAnalyzed int       `json:"bars_analyzed"`
	Trend        string    `json:"trend"`      // "bull" | "bear" | "sideways"
	Volatility   string    `json:"volatility"` // "low" | "medium" | "high"
	Sentiment    float64   `json:"sentiment"`  // [-1.0, 1.0]
	AsOf         time.Time `json:"as_of"`      // timestamp of the regime (last bar)
}

// GetMarketRegimeTool analyzes the market regime for a symbol and date
// range. See the package-level doc comment above for the full design.
type GetMarketRegimeTool struct {
	detector RegimeDetectorClient
	fetcher  OHLCVFetcher
}

var _ tools.Tool = (*GetMarketRegimeTool)(nil)

// NewGetMarketRegimeTool constructs a GetMarketRegimeTool backed by the
// given regime detector and OHLCV fetcher. Panics on nil — fail-loud at
// wiring time (composition root), not at first Execute call.
func NewGetMarketRegimeTool(detector RegimeDetectorClient, fetcher OHLCVFetcher) *GetMarketRegimeTool {
	if detector == nil {
		panic("builtin: NewGetMarketRegimeTool called with nil RegimeDetectorClient")
	}
	if fetcher == nil {
		panic("builtin: NewGetMarketRegimeTool called with nil OHLCVFetcher")
	}
	return &GetMarketRegimeTool{detector: detector, fetcher: fetcher}
}

func (t *GetMarketRegimeTool) Name() string { return "get_market_regime" }

func (t *GetMarketRegimeTool) Description() string {
	return "Fetch market regime analysis (bull/bear/sideways trend + volatility level + sentiment score) " +
		"for a given symbol and date range. Defaults to CSI 300 index (000300.SH) as the A-share market benchmark. " +
		"Uses moving-average crossover + volatility classification. Requires sufficient OHLCV history (typically 200+ trading days). " +
		"Use this before strategy design to understand the prevailing market regime and pick strategies that fit it."
}

func (t *GetMarketRegimeTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "start_date",
			Type:        "string",
			Description: "Analysis start date in YYYY-MM-DD format. Inclusive.",
			Required:    true,
		},
		{
			Name:        "end_date",
			Type:        "string",
			Description: "Analysis end date in YYYY-MM-DD format. Inclusive.",
			Required:    true,
		},
		{
			Name:        "symbol",
			Type:        "string",
			Description: "Symbol to analyze. Defaults to '000300.SH' (CSI 300 index, the A-share market benchmark). Override with any stock or index symbol.",
			Required:    false,
			Default:     DefaultMarketRegimeSymbol,
		},
	}
}

func (t *GetMarketRegimeTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Market regime analysis: trend, volatility, sentiment, plus provenance metadata.",
		Fields: []tools.OutputField{
			{Name: "symbol", Type: "string", Description: "Analyzed symbol."},
			{Name: "start_date", Type: "string", Description: "Analysis start date (YYYY-MM-DD)."},
			{Name: "end_date", Type: "string", Description: "Analysis end date (YYYY-MM-DD)."},
			{Name: "bars_analyzed", Type: "int", Description: "Number of OHLCV bars used for the analysis."},
			{Name: "trend", Type: "string", Description: "Market trend classification: 'bull', 'bear', or 'sideways'."},
			{Name: "volatility", Type: "string", Description: "Volatility level: 'low', 'medium', or 'high'."},
			{Name: "sentiment", Type: "float", Description: "Sentiment score in [-1.0, 1.0]. Positive = bullish, negative = bearish."},
			{Name: "as_of", Type: "string", Description: "Timestamp of the regime (typically the last bar's date)."},
		},
	}
}

// Execute runs the market regime analysis. Errors are returned when:
//   - start_date or end_date is missing/empty/not a string (wrapped ErrInvalidArgs)
//   - symbol has the wrong type (wrapped ErrInvalidArgs)
//   - start_date or end_date is not in YYYY-MM-DD format (wrapped ErrInvalidArgs)
//   - start_date is after end_date (wrapped ErrInvalidArgs)
//   - the data-service returns no OHLCV for the symbol/range (error)
//   - the OHLCV fetch fails (network/HTTP error, wrapped)
//   - DetectRegime fails (e.g., insufficient bars — typically needs 200+)
func (t *GetMarketRegimeTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	startDate, err := requireString(args, "start_date")
	if err != nil {
		return nil, err
	}
	endDate, err := requireString(args, "end_date")
	if err != nil {
		return nil, err
	}
	symbol, err := optionalString(args, "symbol")
	if err != nil {
		return nil, err
	}
	if symbol == "" {
		symbol = DefaultMarketRegimeSymbol
	}

	// Validate date formats early — a bad format would otherwise produce a
	// confusing downstream error from the data-service's YYYYMMDD conversion.
	if err := validateDateFormat(startDate); err != nil {
		return nil, fmt.Errorf("get_market_regime: invalid start_date: %w", err)
	}
	if err := validateDateFormat(endDate); err != nil {
		return nil, fmt.Errorf("get_market_regime: invalid end_date: %w", err)
	}
	if startDate > endDate {
		return nil, fmt.Errorf("get_market_regime: start_date (%s) must not be after end_date (%s)", startDate, endDate)
	}

	// Fetch OHLCV from the data-service.
	records, err := t.fetcher.FetchOHLCV(ctx, symbol, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("get_market_regime: fetch OHLCV for %s: %w", symbol, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("get_market_regime: no OHLCV data for symbol %s in range [%s, %s]", symbol, startDate, endDate)
	}

	// Convert raw maps to []domain.OHLCV for the regime detector.
	bars := recordsToOHLCV(records, symbol)

	// Detect the regime. DetectRegime may return an error if len(bars) is
	// below the detector's SlowMAPeriod (typically 200) — pass it through.
	regime, err := t.detector.DetectRegime(ctx, bars)
	if err != nil {
		return nil, fmt.Errorf("get_market_regime: detect regime: %w", err)
	}

	return &marketRegimeResult{
		Symbol:       symbol,
		StartDate:    startDate,
		EndDate:      endDate,
		BarsAnalyzed: len(bars),
		Trend:        regime.Trend,
		Volatility:   regime.Volatility,
		Sentiment:    regime.Sentiment,
		AsOf:         regime.Timestamp,
	}, nil
}

// ─── Internal helpers ─────────────────────────────────────────────────

// validateDateFormat checks that s is in YYYY-MM-DD format and is a real
// calendar date. Returns nil if valid.
func validateDateFormat(s string) error {
	_, err := time.Parse("2006-01-02", s)
	if err != nil {
		return fmt.Errorf("expected YYYY-MM-DD format, got %q", s)
	}
	return nil
}

// recordsToOHLCV converts the data-service's generic map records into
// []domain.OHLCV. Only the fields DetectRegime actually reads (Close, Date,
// Symbol) are strictly needed, but we populate all standard OHLCV fields
// for completeness and forward-compatibility.
//
// The bars are sorted by Date ascending — the data-service usually returns
// them in order, but we sort defensively to guarantee DetectRegime sees
// chronological data (its MA/volatility calculations assume ordering).
func recordsToOHLCV(records []map[string]interface{}, defaultSymbol string) []domain.OHLCV {
	bars := make([]domain.OHLCV, 0, len(records))
	for _, r := range records {
		var bar domain.OHLCV
		if v, ok := r["symbol"].(string); ok && v != "" {
			bar.Symbol = v
		} else {
			bar.Symbol = defaultSymbol
		}
		bar.Open = toFloat64(r["open"])
		bar.High = toFloat64(r["high"])
		bar.Low = toFloat64(r["low"])
		bar.Close = toFloat64(r["close"])
		bar.Volume = toFloat64(r["volume"])
		bar.Turnover = toFloat64(r["turnover"])
		if d, ok := r["date"].(string); ok {
			bar.Date = parseFlexibleDate(d)
		}
		bars = append(bars, bar)
	}
	// Sort by Date ascending. Bars with zero Date (unparseable) sort first,
	// which is acceptable — they're typically at the start of the data.
	sort.SliceStable(bars, func(i, j int) bool {
		return bars[i].Date.Before(bars[j].Date)
	})
	return bars
}

// toFloat64 coerces a JSON-decoded value to float64. Handles the types
// encoding/json produces: float64 (default), json.Number (if UseNumber
// was set), and numeric ints. Returns 0 for nil or unrecognized types.
func toFloat64(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}

// parseFlexibleDate tries several common date formats and returns the
// first that parses. Returns time.Time{} (zero value) if none match —
// the caller treats this as "unparseable" and sorts accordingly.
func parseFlexibleDate(s string) time.Time {
	formats := []string{
		time.RFC3339,          // "2006-01-02T15:04:05Z07:00"
		"2006-01-02",          // ISO date (most common from data-service)
		"2006-01-02 15:04:05", // SQL timestamp
		"2006/01/02",          // Slash-separated
		"20060102",            // Compact (YYYYMMDD)
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
