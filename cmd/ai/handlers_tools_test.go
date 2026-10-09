package main

// HTTP tests for ToolsHandler (S7-P3-3, ODR-043).
//
// These tests cover the three /api/tools/* endpoints (list / get /
// execute) and the full error-classification matrix:
//   - 404 TOOL_NOT_REGISTERED  (unknown tool name)
//   - 400 INVALID_ARGS         (tool returned ErrInvalidArgs)
//   - 400 INVALID_BODY         (request body is not valid JSON)
//   - 500 TOOL_EXECUTION_FAILED (tool returned a generic error)
//
// The tests use fake Tool fixtures (echoTool / strictTool / failingTool)
// rather than real builtin tools so the handler layer is exercised in
// isolation — builtin tools have their own coverage in
// pkg/ai/tools/builtin/*_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
)

// ============================================================
// fake Tool fixtures
// ============================================================

// echoTool has a real schema and echoes args["msg"] back as the result.
type echoTool struct{}

func (echoTool) Name() string { return "echo" }
func (echoTool) Description() string {
	return "echoes the msg argument verbatim"
}
func (echoTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{Name: "msg", Type: "string", Description: "message to echo", Required: true},
	}
}
func (echoTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{Type: "string", Description: "the echoed message"}
}
func (echoTool) Execute(_ context.Context, args map[string]interface{}) (interface{}, error) {
	return args["msg"], nil
}

// strictTool always returns ErrInvalidArgs — exercises the 400 path.
type strictTool struct{}

func (strictTool) Name() string        { return "strict" }
func (strictTool) Description() string { return "always rejects its arguments" }
func (strictTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{Name: "foo", Type: "string", Description: "required field", Required: true},
	}
}
func (strictTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{Type: "string"}
}
func (strictTool) Execute(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("%w: missing required field foo", tools.ErrInvalidArgs)
}

// failingTool always returns a generic (non-sentinel) error — exercises
// the 500 path.
type failingTool struct{}

func (failingTool) Name() string        { return "failing" }
func (failingTool) Description() string { return "always fails with a generic error" }
func (failingTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{Name: "unused", Type: "string", Description: "ignored", Required: false},
	}
}
func (failingTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{Type: "string"}
}
func (failingTool) Execute(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	return nil, errors.New("boom: downstream service unavailable")
}

// ============================================================
// test helpers
// ============================================================

// newTestRegistry builds a Registry pre-loaded with the given tools.
func newTestRegistry(t *testing.T, toolList ...tools.Tool) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	for _, tl := range toolList {
		if err := reg.Register(tl); err != nil {
			t.Fatalf("Register(%q) failed: %v", tl.Name(), err)
		}
	}
	return reg
}

// doToolsRequest mounts a ToolsHandler on a fresh gin engine and issues
// a request. body is JSON-marshaled; pass nil for no body.
func doToolsRequest(t *testing.T, reg *tools.Registry, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	NewToolsHandler(reg, zerolog.Nop()).RegisterRoutes(r)

	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// decodeToolsError pulls the structured error body {error, code} from a
// failed response.
func decodeToolsError(t *testing.T, w *httptest.ResponseRecorder) (code string) {
	t.Helper()
	var resp struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error body: %v (body=%q)", err, w.Body.String())
	}
	return resp.Code
}

// ============================================================
// GET /api/tools — list
// ============================================================

func TestHandler_Tools_ListTools(t *testing.T) {
	reg := newTestRegistry(t, echoTool{}, strictTool{})
	w := doToolsRequest(t, reg, "GET", "/api/tools", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Tools []tools.ToolInfo `json:"tools"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Count != 2 {
		t.Fatalf("expected count=2, got %d", resp.Count)
	}
	if len(resp.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(resp.Tools))
	}
	// Sorted by name → echo comes first, then strict.
	if resp.Tools[0].Name != "echo" {
		t.Fatalf("expected first tool=echo, got %q", resp.Tools[0].Name)
	}
	if resp.Tools[1].Name != "strict" {
		t.Fatalf("expected second tool=strict, got %q", resp.Tools[1].Name)
	}
	// echoTool has 1 parameter; verify it survives JSON round-trip.
	if len(resp.Tools[0].Parameters) != 1 {
		t.Fatalf("expected echo to have 1 parameter, got %d", len(resp.Tools[0].Parameters))
	}
	if resp.Tools[0].Parameters[0].Name != "msg" {
		t.Fatalf("expected parameter name=msg, got %q", resp.Tools[0].Parameters[0].Name)
	}
	// output_schema should be populated.
	if resp.Tools[0].OutputSchema.Type != "string" {
		t.Fatalf("expected echo output_schema.type=string, got %q", resp.Tools[0].OutputSchema.Type)
	}
}

func TestHandler_Tools_ListTools_Empty(t *testing.T) {
	reg := newTestRegistry(t) // no tools
	w := doToolsRequest(t, reg, "GET", "/api/tools", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Tools []tools.ToolInfo `json:"tools"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Count != 0 {
		t.Fatalf("expected count=0, got %d", resp.Count)
	}
	if len(resp.Tools) != 0 {
		t.Fatalf("expected empty tools slice, got %d items", len(resp.Tools))
	}
}

// TestHandler_Tools_ListTools_TrailingSlash verifies the "/" variant
// route also resolves (registered as a separate route in RegisterRoutes).
func TestHandler_Tools_ListTools_TrailingSlash(t *testing.T) {
	reg := newTestRegistry(t, echoTool{})
	w := doToolsRequest(t, reg, "GET", "/api/tools/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for trailing-slash variant, got %d: %s", w.Code, w.Body.String())
	}
}

// ============================================================
// GET /api/tools/:name — get single tool schema
// ============================================================

func TestHandler_Tools_GetTool(t *testing.T) {
	reg := newTestRegistry(t, echoTool{})
	w := doToolsRequest(t, reg, "GET", "/api/tools/echo", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var info tools.ToolInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Name != "echo" {
		t.Fatalf("expected name=echo, got %q", info.Name)
	}
	if info.Description != "echoes the msg argument verbatim" {
		t.Fatalf("unexpected description: %q", info.Description)
	}
	if len(info.Parameters) != 1 || info.Parameters[0].Name != "msg" {
		t.Fatalf("expected 1 parameter named msg, got %+v", info.Parameters)
	}
	if info.OutputSchema.Type != "string" {
		t.Fatalf("expected output_schema.type=string, got %q", info.OutputSchema.Type)
	}
}

func TestHandler_Tools_GetTool_NotFound(t *testing.T) {
	reg := newTestRegistry(t, echoTool{})
	w := doToolsRequest(t, reg, "GET", "/api/tools/nonexistent", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	if code := decodeToolsError(t, w); code != "TOOL_NOT_REGISTERED" {
		t.Fatalf("expected code=TOOL_NOT_REGISTERED, got %q", code)
	}
}

// ============================================================
// POST /api/tools/:name — execute
// ============================================================

func TestHandler_Tools_ExecuteTool_HappyPath(t *testing.T) {
	reg := newTestRegistry(t, echoTool{})
	w := doToolsRequest(t, reg, "POST", "/api/tools/echo",
		map[string]any{"args": map[string]any{"msg": "hello"}})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Result != "hello" {
		t.Fatalf("expected result=hello, got %q", resp.Result)
	}
}

func TestHandler_Tools_ExecuteTool_NotFound(t *testing.T) {
	reg := newTestRegistry(t) // empty
	w := doToolsRequest(t, reg, "POST", "/api/tools/nonexistent",
		map[string]any{"args": map[string]any{}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	if code := decodeToolsError(t, w); code != "TOOL_NOT_REGISTERED" {
		t.Fatalf("expected code=TOOL_NOT_REGISTERED, got %q", code)
	}
}

func TestHandler_Tools_ExecuteTool_InvalidArgs(t *testing.T) {
	reg := newTestRegistry(t, strictTool{})
	w := doToolsRequest(t, reg, "POST", "/api/tools/strict",
		map[string]any{"args": map[string]any{}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if code := decodeToolsError(t, w); code != "INVALID_ARGS" {
		t.Fatalf("expected code=INVALID_ARGS, got %q", code)
	}
}

func TestHandler_Tools_ExecuteTool_ToolError(t *testing.T) {
	reg := newTestRegistry(t, failingTool{})
	w := doToolsRequest(t, reg, "POST", "/api/tools/failing",
		map[string]any{"args": map[string]any{}})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	if code := decodeToolsError(t, w); code != "TOOL_EXECUTION_FAILED" {
		t.Fatalf("expected code=TOOL_EXECUTION_FAILED, got %q", code)
	}
}

// TestHandler_Tools_ExecuteTool_InvalidBody sends a malformed JSON body
// to verify the ShouldBindJSON error path returns 400 INVALID_BODY.
func TestHandler_Tools_ExecuteTool_InvalidBody(t *testing.T) {
	reg := newTestRegistry(t, echoTool{})
	r := gin.New()
	NewToolsHandler(reg, zerolog.Nop()).RegisterRoutes(r)

	req := httptest.NewRequest("POST", "/api/tools/echo", bytes.NewReader([]byte("not-json{")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if code := decodeToolsError(t, w); code != "INVALID_BODY" {
		t.Fatalf("expected code=INVALID_BODY, got %q", code)
	}
}

// ============================================================
// construction guard
// ============================================================

// TestHandler_Tools_NilRegistry_Panics verifies NewToolsHandler fails
// loud at startup when the registry is nil (wiring bug, not a runtime
// condition).
func TestHandler_Tools_NilRegistry_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected NewToolsHandler(nil, ...) to panic")
		}
	}()
	NewToolsHandler(nil, zerolog.Nop())
}
