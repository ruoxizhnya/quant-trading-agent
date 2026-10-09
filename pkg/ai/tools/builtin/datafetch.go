package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
)

// DataSourceClient is a minimal HTTP client for the data-service
// (port :8081). It exists only to serve the DataFetch* tools — a
// future S7-P3-9 task may promote it to pkg/api/dataservice/ if more
// callers need it, but for now keeping it here avoids premature
// abstraction across packages.
//
// Concurrency: safe for concurrent use (the embedded *http.Client is
// goroutine-safe; baseURL is set once at construction).
type DataSourceClient struct {
	baseURL string
	http    *http.Client
}

// NewDataSourceClient constructs a DataSourceClient for the given
// data-service base URL. If hc is nil, a default *http.Client with a
// 30s timeout is used. In production, pass the shared httpClient from
// cmd/analysis/main.go so outbound calls get observability + X-Request-ID.
func NewDataSourceClient(baseURL string, hc *http.Client) *DataSourceClient {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &DataSourceClient{baseURL: strings.TrimRight(baseURL, "/"), http: hc}
}

// doGet performs a GET request and decodes the JSON response into out.
// The response body is expected to be a JSON object; the caller passes
// the top-level key to extract (e.g. "ohlcv", "stock", "fundamentals").
func (c *DataSourceClient) doGet(ctx context.Context, path string, key string, out interface{}) error {
	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("data-service: create request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("data-service: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("data-service: HTTP %d: %s", resp.StatusCode, string(body))
	}

	// The data-service wraps responses in {"<key>": <payload>}. Decode
	// the wrapper and then unmarshal the payload into out.
	var wrapper map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return fmt.Errorf("data-service: decode response: %w", err)
	}
	payload, ok := wrapper[key]
	if !ok {
		// Response wasn't wrapped — try decoding the whole body directly.
		// Re-marshal wrapper to get a raw blob. This branch handles
		// endpoints that return the payload without a wrapper key.
		raw, err := json.Marshal(wrapper)
		if err != nil {
			return fmt.Errorf("data-service: unexpected response shape (no %q key)", key)
		}
		payload = raw
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("data-service: decode %q payload: %w", key, err)
	}
	return nil
}

// toYYYYMMDD converts a YYYY-MM-DD string to YYYYMMDD (the format
// data-service's OHLCV/fundamentals endpoints expect). Returns the
// input unchanged if parsing fails — the downstream 400 error will
// carry the diagnostic, and we avoid masking it with our own.
func toYYYYMMDD(s string) string {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return s
	}
	return t.Format("20060102")
}

// FetchOHLCV retrieves OHLCV records for a single symbol over a date range
// from the data-service. This is the same fetch logic that DataOHLCVTool
// uses inline, extracted as a public method so other tools (notably
// GetMarketRegimeTool, Hermes Phase 2.3) can reuse it without duplicating
// the endpoint path construction.
//
// Dates must be in YYYY-MM-DD format (they are converted to YYYYMMDD
// internally for the data-service). Returns the raw data-service response
// as a slice of generic maps — the same shape as DataOHLCVTool's output.
func (c *DataSourceClient) FetchOHLCV(ctx context.Context, symbol, startDate, endDate string) ([]map[string]interface{}, error) {
	path := fmt.Sprintf("/ohlcv/%s?start_date=%s&end_date=%s",
		symbol, toYYYYMMDD(startDate), toYYYYMMDD(endDate))
	var out []map[string]interface{}
	if err := c.doGet(ctx, path, "ohlcv", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ─── DataOHLCVTool ────────────────────────────────────────────────────

// DataOHLCVTool fetches OHLCV candlestick data for a single symbol.
//
// Tool name: "data.ohlcv"
// Input: symbol, start_date, end_date (all required, YYYY-MM-DD)
// Output: []map[string]interface{} (the OHLCV records as JSON objects)
//
// The output is a slice of generic maps rather than []domain.OHLCV to
// keep this package free of a pkg/domain dependency — the HTTP layer
// marshals it directly to JSON for the external agent, and the schema
// documents the fields.
type DataOHLCVTool struct {
	c *DataSourceClient
}

var _ tools.Tool = (*DataOHLCVTool)(nil)

// NewDataOHLCVTool constructs a DataOHLCVTool backed by the given
// data-service client. Panics if c is nil.
func NewDataOHLCVTool(c *DataSourceClient) *DataOHLCVTool {
	if c == nil {
		panic("builtin: NewDataOHLCVTool called with nil DataSourceClient")
	}
	return &DataOHLCVTool{c: c}
}

func (t *DataOHLCVTool) Name() string { return "data.ohlcv" }

func (t *DataOHLCVTool) Description() string {
	return "Fetch daily OHLCV (candlestick) data for a single stock symbol over a date range. Returns open/high/low/close/volume/turnover per trading day. Dates are inclusive."
}

func (t *DataOHLCVTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "symbol",
			Type:        "string",
			Description: "Stock symbol, e.g. '000001.SZ' or '600000.SH'.",
			Required:    true,
		},
		{
			Name:        "start_date",
			Type:        "string",
			Description: "Start date in YYYY-MM-DD format. Inclusive.",
			Required:    true,
		},
		{
			Name:        "end_date",
			Type:        "string",
			Description: "End date in YYYY-MM-DD format. Inclusive.",
			Required:    true,
		},
	}
}

func (t *DataOHLCVTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "array",
		Description: "Array of daily OHLCV records, one per trading day in [start_date, end_date].",
		Fields: []tools.OutputField{
			{Name: "symbol", Type: "string", Description: "Stock symbol."},
			{Name: "date", Type: "string", Description: "Trading date (ISO 8601)."},
			{Name: "open", Type: "float", Description: "Open price."},
			{Name: "high", Type: "float", Description: "High price."},
			{Name: "low", Type: "float", Description: "Low price."},
			{Name: "close", Type: "float", Description: "Close price."},
			{Name: "volume", Type: "float", Description: "Volume (shares)."},
			{Name: "turnover", Type: "float", Description: "Turnover (CNY)."},
		},
	}
}

func (t *DataOHLCVTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	symbol, err := requireString(args, "symbol")
	if err != nil {
		return nil, err
	}
	startDate, err := requireString(args, "start_date")
	if err != nil {
		return nil, err
	}
	endDate, err := requireString(args, "end_date")
	if err != nil {
		return nil, err
	}

	// data-service expects YYYYMMDD; we accept YYYY-MM-DD for
	// consistency with other tools and convert here.
	path := fmt.Sprintf("/ohlcv/%s?start_date=%s&end_date=%s",
		symbol, toYYYYMMDD(startDate), toYYYYMMDD(endDate))

	var out []map[string]interface{}
	if err := t.c.doGet(ctx, path, "ohlcv", &out); err != nil {
		return nil, fmt.Errorf("data.ohlcv: %w", err)
	}
	return out, nil
}

// ─── DataStocksTool ───────────────────────────────────────────────────

// DataStocksTool fetches stock metadata. With a symbol arg, returns a
// single stock; without, returns the full list.
//
// Tool name: "data.stocks"
// Input: symbol (optional)
// Output: []map[string]interface{} (always a slice for JSON uniformity)
type DataStocksTool struct {
	c *DataSourceClient
}

var _ tools.Tool = (*DataStocksTool)(nil)

// NewDataStocksTool constructs a DataStocksTool. Panics if c is nil.
func NewDataStocksTool(c *DataSourceClient) *DataStocksTool {
	if c == nil {
		panic("builtin: NewDataStocksTool called with nil DataSourceClient")
	}
	return &DataStocksTool{c: c}
}

func (t *DataStocksTool) Name() string { return "data.stocks" }

func (t *DataStocksTool) Description() string {
	return "Fetch stock metadata. If 'symbol' is provided, returns that single stock; otherwise returns the full stock list. Each record contains symbol, name, exchange, market, sector, market cap, and status."
}

func (t *DataStocksTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "symbol",
			Type:        "string",
			Description: "Optional stock symbol (e.g. '000001.SZ'). If omitted, all stocks are returned.",
			Required:    false,
		},
	}
}

func (t *DataStocksTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "array",
		Description: "Array of stock records (one if symbol is given, all if omitted).",
		Fields: []tools.OutputField{
			{Name: "symbol", Type: "string", Description: "Stock symbol, e.g. '000001.SZ'."},
			{Name: "name", Type: "string", Description: "Chinese stock name."},
			{Name: "exchange", Type: "string", Description: "Exchange: 'SZ', 'SH', or 'BJ'."},
			{Name: "market", Type: "string", Description: "Market: 'A-share', 'US', etc."},
			{Name: "sector", Type: "string", Description: "Sector classification."},
			{Name: "status", Type: "string", Description: "'active', 'suspended', or 'delisted'."},
		},
	}
}

func (t *DataStocksTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	symbol, err := optionalString(args, "symbol")
	if err != nil {
		return nil, err
	}

	var path string
	if symbol != "" {
		path = "/stocks/" + symbol
	} else {
		path = "/stocks"
	}

	var out []map[string]interface{}
	// The /stocks/:symbol endpoint returns a single object under
	// "stock"; the /stocks endpoint returns an array under "stocks".
	// We normalize: always return a slice.
	if symbol != "" {
		var single map[string]interface{}
		if err := t.c.doGet(ctx, path, "stock", &single); err != nil {
			return nil, fmt.Errorf("data.stocks: %w", err)
		}
		out = []map[string]interface{}{single}
	} else {
		if err := t.c.doGet(ctx, path, "stocks", &out); err != nil {
			return nil, fmt.Errorf("data.stocks: %w", err)
		}
	}
	return out, nil
}

// ─── DataFundamentalsTool ─────────────────────────────────────────────

// DataFundamentalsTool fetches fundamental data (PE/PB/ROE/etc.) for
// a single symbol. Date range is optional.
//
// Tool name: "data.fundamentals"
// Input: symbol (required), start_date (optional), end_date (optional)
// Output: []map[string]interface{}
type DataFundamentalsTool struct {
	c *DataSourceClient
}

var _ tools.Tool = (*DataFundamentalsTool)(nil)

// NewDataFundamentalsTool constructs a DataFundamentalsTool. Panics if c is nil.
func NewDataFundamentalsTool(c *DataSourceClient) *DataFundamentalsTool {
	if c == nil {
		panic("builtin: NewDataFundamentalsTool called with nil DataSourceClient")
	}
	return &DataFundamentalsTool{c: c}
}

func (t *DataFundamentalsTool) Name() string { return "data.fundamentals" }

func (t *DataFundamentalsTool) Description() string {
	return "Fetch fundamental data (PE, PB, ROE, ROA, margins, revenue, etc.) for a single stock. Optionally filter by date range; if omitted, returns the latest snapshot."
}

func (t *DataFundamentalsTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "symbol",
			Type:        "string",
			Description: "Stock symbol, e.g. '000001.SZ'.",
			Required:    true,
		},
		{
			Name:        "start_date",
			Type:        "string",
			Description: "Optional start date in YYYY-MM-DD format. If omitted, returns the latest fundamentals snapshot.",
			Required:    false,
		},
		{
			Name:        "end_date",
			Type:        "string",
			Description: "Optional end date in YYYY-MM-DD format.",
			Required:    false,
		},
	}
}

func (t *DataFundamentalsTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "array",
		Description: "Array of fundamental records (one per reporting period in the date range, or a single record if no range given).",
		Fields: []tools.OutputField{
			{Name: "symbol", Type: "string", Description: "Stock symbol."},
			{Name: "date", Type: "string", Description: "Reporting date."},
			{Name: "pe", Type: "float", Description: "Price-to-Earnings ratio."},
			{Name: "pb", Type: "float", Description: "Price-to-Book ratio."},
			{Name: "roe", Type: "float", Description: "Return on Equity (%)."},
			{Name: "roa", Type: "float", Description: "Return on Assets (%)."},
			{Name: "revenue", Type: "float", Description: "Total revenue (CNY)."},
			{Name: "net_profit", Type: "float", Description: "Net profit (CNY)."},
		},
	}
}

func (t *DataFundamentalsTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	symbol, err := requireString(args, "symbol")
	if err != nil {
		return nil, err
	}
	startDate, err := optionalString(args, "start_date")
	if err != nil {
		return nil, err
	}
	endDate, err := optionalString(args, "end_date")
	if err != nil {
		return nil, err
	}

	path := "/fundamentals/" + symbol
	if startDate != "" && endDate != "" {
		path += fmt.Sprintf("?start_date=%s&end_date=%s", toYYYYMMDD(startDate), toYYYYMMDD(endDate))
	}

	var out []map[string]interface{}
	// The fundamentals endpoint may return either a single object or
	// an array depending on whether a date range was given. Decode into
	// a slice to normalize — if the response is a single object, we
	// wrap it.
	var raw json.RawMessage
	if err := t.c.doGet(ctx, path, "fundamentals", &raw); err != nil {
		return nil, fmt.Errorf("data.fundamentals: %w", err)
	}
	// Try array first.
	if err := json.Unmarshal(raw, &out); err != nil {
		// Fall back to single object.
		var single map[string]interface{}
		if err2 := json.Unmarshal(raw, &single); err2 != nil {
			return nil, fmt.Errorf("data.fundamentals: decode payload: %w", err)
		}
		out = []map[string]interface{}{single}
	}
	return out, nil
}
