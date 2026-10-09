package main

// Integration tests for the Hermes/MCP tool bridge (Phase 2.7).
//
// Unlike handlers_tools_test.go (which uses fake echoTool/strictTool/
// failingTool fixtures to test the HTTP plumbing in isolation), these
// tests exercise REAL builtin tools through the HTTP layer with mock
// dependencies. They verify:
//
//   1. GateDecision struct survives JSON serialization round-trip
//      through POST /api/tools/:name (the L1-L4 gate contract that
//      Hermes depends on).
//   2. Discovery schema (GET /api/tools/:name) matches what execution
//      (POST /api/tools/:name) actually expects.
//   3. save_factor → list_factors round-trip works through HTTP.
//   4. The L1→save chain: validate a factor, then save it.
//
// The builtin tools have their own unit-level coverage in
// pkg/ai/tools/builtin/*_test.go; these tests add the HTTP-layer
// integration dimension.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools/builtin"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
)

// ──────────────────────────────────────────────────────────────────────
// mock FactorPoolClient for HTTP integration tests
// ──────────────────────────────────────────────────────────────────────

// httpTestFactorPool is a minimal in-memory FactorPoolClient that
// records Save calls and returns them on List. This lets us test the
// save→list round-trip through HTTP without a database.
type httpTestFactorPool struct {
	saved   []*gene_pool.FactorGene
	listErr error
	saveErr error
}

func (m *httpTestFactorPool) List(_ context.Context, _, _ string, _ float64, _ int) ([]*gene_pool.FactorGene, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	if m.saved == nil {
		return []*gene_pool.FactorGene{}, nil
	}
	return m.saved, nil
}

func (m *httpTestFactorPool) Save(_ context.Context, gene *gene_pool.FactorGene) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saved = append(m.saved, gene)
	return nil
}

// ──────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────

// newIntegrationRegistry builds a tools.Registry with real builtin
// gate tools (validate_factor, save_factor, list_factors) wired to
// mock dependencies. Other tools are omitted — these tests focus on
// the L1 gate + gene-pool round-trip.
func newIntegrationRegistry(t *testing.T) (*tools.Registry, *httpTestFactorPool) {
	t.Helper()
	reg := tools.NewRegistry()

	// L1 gate tool — self-contained (no external deps).
	require.NoError(t, reg.Register(builtin.NewValidateFactorTool()))

	// Gene pool tools — backed by an in-memory mock.
	pool := &httpTestFactorPool{}
	require.NoError(t, reg.Register(builtin.NewSaveFactorTool(pool)))
	require.NoError(t, reg.Register(builtin.NewListFactorsTool(pool)))

	return reg, pool
}

// newIntegrationHandler builds a ToolsHandler with the given registry
// and returns a gin engine with routes mounted, ready for httptest.
func newIntegrationHandler(t *testing.T, reg *tools.Registry) *gin.Engine {
	t.Helper()
	router := gin.New()
	h := NewToolsHandler(reg, zerolog.Nop())
	h.RegisterRoutes(router)
	return router
}

// doToolExecute sends POST /api/tools/:name with the given args and
// returns the raw response recorder.
func doToolExecute(t *testing.T, router *gin.Engine, toolName string, args map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{"args": args})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/tools/"+toolName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// doToolGet sends GET /api/tools/:name and returns the raw response.
func doToolGet(t *testing.T, router *gin.Engine, toolName string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/tools/"+toolName, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// decodeExecuteResult decodes the {result: ...} envelope from a
// successful POST /api/tools/:name response.
func decodeExecuteResult(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, "expected 200, got %d: %s", w.Code, w.Body.String())

	var envelope struct {
		Result map[string]interface{} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), "failed to decode response: %s", w.Body.String())
	require.NotNil(t, envelope.Result, "result should be non-nil")
	return envelope.Result
}

// ──────────────────────────────────────────────────────────────────────
// Test 1: L1 gate — valid expression through HTTP
// ──────────────────────────────────────────────────────────────────────

// TestIntegration_L1Gate_ValidExpression_HTTP verifies that the L1
// gate tool (validate_factor) returns a correct GateDecision when
// called through the HTTP layer with a valid expression. This is the
// contract Hermes depends on: POST /api/tools/validate_factor →
// {result: {valid: true, passed: true, level: "L1", reason: "passed", ...}}.
func TestIntegration_L1Gate_ValidExpression_HTTP(t *testing.T) {
	reg, _ := newIntegrationRegistry(t)
	router := newIntegrationHandler(t, reg)

	w := doToolExecute(t, router, "validate_factor", map[string]interface{}{
		"expression": "ts_rank(close, 20)",
	})

	result := decodeExecuteResult(t, w)

	// Core validation fields.
	assert.True(t, result["valid"].(bool), "expression should be valid")
	inputs, ok := result["inputs"].([]interface{})
	require.True(t, ok, "inputs should be array, got %T", result["inputs"])
	// inputs may contain "close" — verify at least one element.
	assert.NotEmpty(t, inputs, "inputs should be non-empty for valid expression")
	ast, ok := result["ast"].(string)
	require.True(t, ok, "ast should be string, got %T", result["ast"])
	assert.NotEmpty(t, ast, "ast should be non-empty")

	// GateDecision fields — the critical HTTP round-trip contract.
	assert.Equal(t, "L1", result["level"], "level should be L1")
	passed, ok := result["passed"].(bool)
	require.True(t, ok, "passed should be bool, got %T", result["passed"])
	assert.True(t, passed, "passed should be true for valid expression")
	assert.Equal(t, builtin.GateReasonPassed, result["reason"], "reason should be 'passed'")
	recommendation, ok := result["recommendation"].(string)
	require.True(t, ok, "recommendation should be string, got %T", result["recommendation"])
	assert.NotEmpty(t, recommendation, "recommendation should be non-empty")
}

// ──────────────────────────────────────────────────────────────────────
// Test 2: L1 gate — syntax error through HTTP
// ──────────────────────────────────────────────────────────────────────

// TestIntegration_L1Gate_SyntaxError_HTTP verifies that the L1 gate
// returns GateDecision {passed: false, reason: "syntax_error"} when
// the expression is malformed. Parse failure is a VALIDATION RESULT,
// not an HTTP error — the response should still be 200 OK.
func TestIntegration_L1Gate_SyntaxError_HTTP(t *testing.T) {
	reg, _ := newIntegrationRegistry(t)
	router := newIntegrationHandler(t, reg)

	w := doToolExecute(t, router, "validate_factor", map[string]interface{}{
		"expression": "ts_rank(close, 20", // missing closing paren
	})

	result := decodeExecuteResult(t, w)

	assert.False(t, result["valid"].(bool), "malformed expression should have valid=false")

	errStr, ok := result["error"].(string)
	require.True(t, ok, "error should be string, got %T", result["error"])
	assert.NotEmpty(t, errStr, "error message should explain the syntax problem")

	// GateDecision: syntax error → passed=false, reason=syntax_error.
	assert.Equal(t, "L1", result["level"])
	passed, ok := result["passed"].(bool)
	require.True(t, ok, "passed should be bool")
	assert.False(t, passed, "passed should be false for syntax error")
	assert.Equal(t, builtin.GateReasonSyntaxError, result["reason"], "reason should be 'syntax_error'")
	recommendation, ok := result["recommendation"].(string)
	require.True(t, ok, "recommendation should be string")
	assert.NotEmpty(t, recommendation, "recommendation should guide retry")
}

// ──────────────────────────────────────────────────────────────────────
// Test 3: Discovery schema matches execution (validate_factor)
// ──────────────────────────────────────────────────────────────────────

// TestIntegration_DiscoverySchema_Matches_ValidateFactor verifies
// that the schema returned by GET /api/tools/validate_factor accurately
// describes the parameters that POST /api/tools/validate_factor expects.
// Hermes uses the discovery schema to construct tool calls — if the
// schema lies, the agent will send wrong arguments.
func TestIntegration_DiscoverySchema_Matches_ValidateFactor(t *testing.T) {
	reg, _ := newIntegrationRegistry(t)
	router := newIntegrationHandler(t, reg)

	// Step 1: GET schema.
	wGet := doToolGet(t, router, "validate_factor")
	require.Equal(t, http.StatusOK, wGet.Code, "GET should return 200")

	var toolInfo struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Parameters  []tools.Parameter `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal(wGet.Body.Bytes(), &toolInfo))
	assert.Equal(t, "validate_factor", toolInfo.Name)
	require.Len(t, toolInfo.Parameters, 1, "validate_factor should have 1 parameter")

	param := toolInfo.Parameters[0]
	assert.Equal(t, "expression", param.Name)
	assert.Equal(t, "string", param.Type)
	assert.True(t, param.Required, "expression should be required")

	// Step 2: POST with the parameter documented in the schema.
	wPost := doToolExecute(t, router, "validate_factor", map[string]interface{}{
		param.Name: "ts_rank(close, 20)",
	})
	result := decodeExecuteResult(t, wPost)
	assert.True(t, result["valid"].(bool), "expression from schema-guided call should be valid")

	// Step 3: POST without the required parameter → 400 INVALID_ARGS.
	wErr := doToolExecute(t, router, "validate_factor", map[string]interface{}{})
	require.Equal(t, http.StatusBadRequest, wErr.Code, "missing required arg should return 400")
}

// ──────────────────────────────────────────────────────────────────────
// Test 4: save_factor → list_factors round-trip through HTTP
// ──────────────────────────────────────────────────────────────────────

// TestIntegration_SaveFactor_ListFactors_RoundTrip_HTTP verifies the
// gene-pool persistence contract: after POST /api/tools/save_factor
// succeeds, POST /api/tools/list_factors returns the saved factor.
// This is the HTTP-layer equivalent of the unit test in
// gene_pool_tools_test.go but exercises the full HTTP + JSON path.
func TestIntegration_SaveFactor_ListFactors_RoundTrip_HTTP(t *testing.T) {
	reg, pool := newIntegrationRegistry(t)
	router := newIntegrationHandler(t, reg)

	// Step 1: Before save, list_factors returns empty.
	wListBefore := doToolExecute(t, router, "list_factors", map[string]interface{}{})
	require.Equal(t, http.StatusOK, wListBefore.Code)

	var listEnvelopeBefore struct {
		Result []*gene_pool.FactorGene `json:"result"`
	}
	require.NoError(t, json.Unmarshal(wListBefore.Body.Bytes(), &listEnvelopeBefore))
	assert.Empty(t, listEnvelopeBefore.Result, "pool should be empty before save")

	// Step 2: save_factor with a validated factor.
	wSave := doToolExecute(t, router, "save_factor", map[string]interface{}{
		"name":        "price_momentum_20d",
		"category":    "momentum",
		"formula":     "ts_rank(close, 20)",
		"description": "20-day price momentum rank factor",
		"rationale":   "Trend-following: stocks with high recent rank tend to outperform.",
		"ic":          0.045,
		"turnover":    0.28,
		"sharpe":      0.8,
	})

	var saveEnvelope struct {
		Result map[string]interface{} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(wSave.Body.Bytes(), &saveEnvelope), "save response: %s", wSave.Body.String())
	require.Equal(t, http.StatusOK, wSave.Code, "save should return 200")

	factorID, ok := saveEnvelope.Result["factor_id"].(string)
	require.True(t, ok, "factor_id should be string, got %T", saveEnvelope.Result["factor_id"])
	assert.Contains(t, factorID, "fg_", "factor_id should start with 'fg_'")

	// Step 3: Verify the mock pool recorded the save.
	require.Len(t, pool.saved, 1, "mock pool should have 1 saved gene")
	savedGene := pool.saved[0]
	assert.Equal(t, "price_momentum_20d", savedGene.Name)
	assert.Equal(t, "ts_rank(close, 20)", savedGene.Formula)
	assert.InDelta(t, 0.045, savedGene.IC, 1e-9)

	// Step 4: list_factors now returns the saved factor.
	wListAfter := doToolExecute(t, router, "list_factors", map[string]interface{}{
		"category": "momentum",
	})

	var listEnvelopeAfter struct {
		Result []map[string]interface{} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(wListAfter.Body.Bytes(), &listEnvelopeAfter), "list response: %s", wListAfter.Body.String())
	require.Equal(t, http.StatusOK, wListAfter.Code)

	require.Len(t, listEnvelopeAfter.Result, 1, "list should return 1 factor after save")
	listedFactor := listEnvelopeAfter.Result[0]
	assert.Equal(t, "price_momentum_20d", listedFactor["name"])
	assert.Equal(t, "ts_rank(close, 20)", listedFactor["formula"])
	assert.InDelta(t, 0.045, listedFactor["ic"].(float64), 1e-9)
	assert.Equal(t, "momentum", listedFactor["category"])
}

// ──────────────────────────────────────────────────────────────────────
// Test 5: L1→save chain (validate then save a factor)
// ──────────────────────────────────────────────────────────────────────

// TestIntegration_L1Gate_Then_SaveFactor_Chain_HTTP verifies the
// minimal end-to-end chain: validate a factor expression (L1 gate),
// then save the validated factor to the gene pool. This mirrors the
// first 3 steps of the autonomous_factor_mining skill workflow.
func TestIntegration_L1Gate_Then_SaveFactor_Chain_HTTP(t *testing.T) {
	reg, _ := newIntegrationRegistry(t)
	router := newIntegrationHandler(t, reg)

	expr := "(close - ts_mean(close, 20)) / ts_std(close, 20)"

	// Step 1: L1 gate — validate the expression.
	wValidate := doToolExecute(t, router, "validate_factor", map[string]interface{}{
		"expression": expr,
	})
	validateResult := decodeExecuteResult(t, wValidate)

	// Assert L1 passed — this is the gate decision Hermes reads.
	passed, ok := validateResult["passed"].(bool)
	require.True(t, ok, "passed should be bool")
	require.True(t, passed, "L1 gate must pass before save (Hermes contract)")
	assert.Equal(t, builtin.GateReasonPassed, validateResult["reason"])

	// Step 2: Save the validated factor.
	wSave := doToolExecute(t, router, "save_factor", map[string]interface{}{
		"name":        "zscore_20d",
		"category":    "mean_reversion",
		"formula":     expr,
		"description": "20-day z-score of close price",
		"ic":          0.052,
		"turnover":    0.35,
	})

	var saveEnvelope struct {
		Result map[string]interface{} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(wSave.Body.Bytes(), &saveEnvelope))
	require.Equal(t, http.StatusOK, wSave.Code)

	factorID, ok := saveEnvelope.Result["factor_id"].(string)
	require.True(t, ok)
	assert.Contains(t, factorID, "fg_")
}

// ──────────────────────────────────────────────────────────────────────
// Test 6: GateDecision all fields survive HTTP JSON round-trip
// ──────────────────────────────────────────────────────────────────────

// TestIntegration_GateDecision_AllFields_Survive_HTTP verifies that
// every GateDecision field (level, passed, reason, recommendation)
// is present and correctly typed in the HTTP response JSON. Hermes
// reads these fields to decide whether to proceed to the next gate —
// if any field is missing or mistyped, the autonomous loop breaks.
func TestIntegration_GateDecision_AllFields_Survive_HTTP(t *testing.T) {
	reg, _ := newIntegrationRegistry(t)
	router := newIntegrationHandler(t, reg)

	// Use a valid expression — tests the "passed=true" path.
	w := doToolExecute(t, router, "validate_factor", map[string]interface{}{
		"expression": "ts_rank(close, 20) * cs_rank(volume)",
	})

	// Decode into a generic map to inspect raw JSON structure.
	var envelope struct {
		Result map[string]interface{} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.Equal(t, http.StatusOK, w.Code)

	r := envelope.Result

	// All 4 GateDecision fields must be present.
	requiredFields := []string{"level", "passed", "reason", "recommendation"}
	for _, field := range requiredFields {
		_, exists := r[field]
		assert.True(t, exists, "GateDecision field %q must be present in HTTP response", field)
	}

	// Type assertions — JSON unmarshaling produces specific Go types.
	assert.IsType(t, "string", r["level"], "level should be string")
	assert.IsType(t, true, r["passed"], "passed should be bool")
	assert.IsType(t, "string", r["reason"], "reason should be string")
	assert.IsType(t, "string", r["recommendation"], "recommendation should be string")

	// Value assertions.
	assert.Equal(t, "L1", r["level"])
	assert.Equal(t, true, r["passed"])
	assert.Equal(t, builtin.GateReasonPassed, r["reason"])
}

// ─────────────────────────────────────────────────────────────────────
// Test 7: dotted tool name routes through gin (research.profile)
// ──────────────────────────────────────────────────────────────────────

// httpTestResearchProfile is a minimal in-memory ResearchProfileClient.
// Returning a nil profile with no vault path configured is the "never
// researched" case → the tool reports ErrNotFound.
type httpTestResearchProfile struct {
	profile   *storage.ResearchProfile
	gotTicker string
}

func (m *httpTestResearchProfile) GetResearchProfile(_ context.Context, ticker string) (*storage.ResearchProfile, error) {
	m.gotTicker = ticker
	return m.profile, nil
}

// TestIntegration_DottedToolName_RoutesThroughGin pins two things the
// dot-namespace convention depends on:
//
//  1. gin's "/:name" parameter captures a name containing a dot, so
//     POST /api/tools/research.profile reaches the tool (path params are
//     split on "/" only). research.profile (EQD-P2-1) is the first dotted
//     name exercised through the HTTP layer, so the behaviour is pinned
//     here rather than assumed.
//  2. The ErrNotFound → 404 "NOT_FOUND" mapping added for the tool's
//     "no_profile" outcome, distinct from 400 INVALID_ARGS.
func TestIntegration_DottedToolName_RoutesThroughGin(t *testing.T) {

	newRouter := func(t *testing.T, client *httpTestResearchProfile) *gin.Engine {
		t.Helper()
		reg := tools.NewRegistry()
		require.NoError(t, reg.Register(builtin.NewResearchProfileTool(client, "")))
		return newIntegrationHandler(t, reg)
	}

	t.Run("execute reaches the tool and normalises a suffixed ticker", func(t *testing.T) {
		client := &httpTestResearchProfile{profile: &storage.ResearchProfile{
			Ticker:        "600519",
			Name:          "贵州茅台",
			SchemaVersion: 1,
			UpdatedAt:     time.Now(),
		}}
		router := newRouter(t, client)

		w := doToolExecute(t, router, "research.profile", map[string]interface{}{"ticker": "600519.SH"})
		result := decodeExecuteResult(t, w)

		assert.Equal(t, "600519", client.gotTicker, "the dot-suffixed ticker must reach the tool, already normalised")
		assert.Equal(t, "600519", result["ticker"])
		assert.Equal(t, "postgres", result["source"])
	})

	t.Run("discovery returns the dotted tool schema", func(t *testing.T) {
		router := newRouter(t, &httpTestResearchProfile{})

		w := doToolGet(t, router, "research.profile")
		require.Equal(t, http.StatusOK, w.Code, "GET should return 200")

		var toolInfo struct {
			Name         string            `json:"name"`
			Parameters   []tools.Parameter `json:"parameters"`
			OutputSchema struct {
				Type   string              `json:"type"`
				Fields []tools.OutputField `json:"fields"`
			} `json:"output_schema"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &toolInfo))
		assert.Equal(t, "research.profile", toolInfo.Name)
		require.Len(t, toolInfo.Parameters, 2, "ticker + sections")
		assert.Equal(t, "ticker", toolInfo.Parameters[0].Name)
		assert.True(t, toolInfo.Parameters[0].Required)
		assert.NotEmpty(t, toolInfo.OutputSchema.Fields, "the tool advertises an output schema to Hermes")
	})

	t.Run("no_profile maps to 404 NOT_FOUND (not 400)", func(t *testing.T) {
		router := newRouter(t, &httpTestResearchProfile{}) // nil profile, empty vault

		w := doToolExecute(t, router, "research.profile", map[string]interface{}{"ticker": "600519"})
		require.Equal(t, http.StatusNotFound, w.Code, "no archive → 404, body: %s", w.Body.String())

		var body map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "NOT_FOUND", body["code"])
		assert.Contains(t, body["error"].(string), "no_profile")
	})

	t.Run("malformed ticker still maps to 400 INVALID_ARGS", func(t *testing.T) {
		router := newRouter(t, &httpTestResearchProfile{})

		w := doToolExecute(t, router, "research.profile", map[string]interface{}{"ticker": "600519.HK"})
		require.Equal(t, http.StatusBadRequest, w.Code, "unknown exchange suffix → 400, body: %s", w.Body.String())

		var body map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "INVALID_ARGS", body["code"])
	})
}
