package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Note: TestMockClient_IsConfigured_True/False are tested in mock_test.go

func TestMockClient_IsConfigured_Default(t *testing.T) {
	m := &MockClient{}
	if m.IsConfigured() {
		t.Error("expected default IsConfigured to return false")
	}
}

func TestMockClient_Chat_CannedResponse(t *testing.T) {
	m := &MockClient{
		Configured:   true,
		ChatResponse: "Hello from mock LLM",
	}
	resp, err := m.Chat(context.Background(), []ChatMessage{
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp != "Hello from mock LLM" {
		t.Errorf("expected 'Hello from mock LLM', got %q", resp)
	}
}

func TestMockClient_Chat_NoResponseConfigured(t *testing.T) {
	m := &MockClient{Configured: true}
	_, err := m.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error when no response configured")
	}
}

func TestMockClient_Chat_CannedError(t *testing.T) {
	customErr := errors.New("LLM service unavailable")
	m := &MockClient{
		Configured: true,
		ChatErr:    customErr,
	}
	_, err := m.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}})
	if err != customErr {
		t.Errorf("expected custom error, got: %v", err)
	}
}

func TestMockClient_Chat_CustomFunc(t *testing.T) {
	m := &MockClient{
		Configured: true,
		ChatFunc: func(ctx context.Context, messages []ChatMessage) (string, error) {
			if len(messages) == 0 {
				return "", errors.New("no messages")
			}
			return fmt.Sprintf("Response to: %s", messages[0].Content), nil
		},
	}
	resp, err := m.Chat(context.Background(), []ChatMessage{
		{Role: "user", Content: "What is RSI?"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp != "Response to: What is RSI?" {
		t.Errorf("unexpected response: %s", resp)
	}
}

// Note: TestMockClient_Chat_RecordsCalls is tested in mock_test.go

func TestMockClient_Chat_FuncOverridesCannedResponse(t *testing.T) {
	m := &MockClient{
		Configured:   true,
		ChatResponse: "canned",
		ChatFunc: func(ctx context.Context, messages []ChatMessage) (string, error) {
			return "from func", nil
		},
	}
	resp, err := m.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp != "from func" {
		t.Errorf("expected 'from func', got %q", resp)
	}
}

func TestMockClient_Chat_FuncOverridesError(t *testing.T) {
	m := &MockClient{
		Configured: true,
		ChatErr:    errors.New("canned error"),
		ChatFunc: func(ctx context.Context, messages []ChatMessage) (string, error) {
			return "success", nil
		},
	}
	resp, err := m.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp != "success" {
		t.Errorf("expected 'success', got %q", resp)
	}
}

func TestMockClient_GenerateStrategyCode_CannedResponse(t *testing.T) {
	m := &MockClient{
		Configured:       true,
		GenerateResponse: "package plugins\n// generated code",
	}
	code, err := m.GenerateStrategyCode(context.Background(), "momentum strategy")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if code != "package plugins\n// generated code" {
		t.Errorf("unexpected code: %s", code)
	}
}

func TestMockClient_GenerateStrategyCode_NoResponse(t *testing.T) {
	m := &MockClient{Configured: true}
	code, err := m.GenerateStrategyCode(context.Background(), "test")
	if err != nil {
		t.Fatalf("expected no error for empty response, got: %v", err)
	}
	if code != "" {
		t.Errorf("expected empty code, got %q", code)
	}
}

func TestMockClient_GenerateStrategyCode_CannedError(t *testing.T) {
	m := &MockClient{
		Configured:  true,
		GenerateErr: errors.New("generation failed"),
	}
	_, err := m.GenerateStrategyCode(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMockClient_GenerateStrategyCode_CustomFunc(t *testing.T) {
	m := &MockClient{
		Configured: true,
		GenerateFunc: func(ctx context.Context, desc string) (string, error) {
			return fmt.Sprintf("// Strategy: %s", desc), nil
		},
	}
	code, err := m.GenerateStrategyCode(context.Background(), "RSI momentum")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if code != "// Strategy: RSI momentum" {
		t.Errorf("unexpected code: %s", code)
	}
}

// Note: TestMockClient_GenerateStrategyCode_RecordsCalls is tested in mock_test.go

func TestMockClient_FixStrategyCode_CannedResponse(t *testing.T) {
	m := &MockClient{
		Configured:  true,
		FixResponse: "package plugins\n// fixed code",
	}
	code, err := m.FixStrategyCode(context.Background(), "broken code", "syntax error")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if code != "package plugins\n// fixed code" {
		t.Errorf("unexpected code: %s", code)
	}
}

func TestMockClient_FixStrategyCode_CannedError(t *testing.T) {
	m := &MockClient{
		Configured: true,
		FixErr:     errors.New("fix failed"),
	}
	_, err := m.FixStrategyCode(context.Background(), "code", "error")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMockClient_FixStrategyCode_CustomFunc(t *testing.T) {
	m := &MockClient{
		Configured: true,
		FixFunc: func(ctx context.Context, code string, buildErrors string) (string, error) {
			return fmt.Sprintf("fixed(%s)", code), nil
		},
	}
	code, err := m.FixStrategyCode(context.Background(), "broken", "err")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if code != "fixed(broken)" {
		t.Errorf("unexpected code: %s", code)
	}
}

// Note: TestMockClient_FixStrategyCode_RecordsCalls is tested in mock_test.go

func TestMockClient_ConcurrentAccess(t *testing.T) {
	m := &MockClient{
		Configured:   true,
		ChatResponse: "ok",
	}

	var wg sync.WaitGroup
	const numGoroutines = 20
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}})
		}()
	}
	wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.ChatCalls) != numGoroutines {
		t.Errorf("expected %d recorded calls, got %d", numGoroutines, len(m.ChatCalls))
	}
}

func TestClient_IsConfigured_Unconfigured(t *testing.T) {
	c := NewClient()
	if c.IsConfigured() {
		t.Error("expected unconfigured client when no env vars set")
	}
}

func TestClient_IsConfigured_WithAPIKey(t *testing.T) {
	c, err := NewClientWithOptions(WithAPIKey("test-key"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.IsConfigured() {
		t.Error("expected unconfigured when only API key is set")
	}
}

func TestClient_IsConfigured_WithAPIKeyAndURL(t *testing.T) {
	c, err := NewClientWithOptions(
		WithAPIKey("test-key"),
		WithAPIURL("http://localhost:8080"),
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !c.IsConfigured() {
		t.Error("expected configured when both API key and URL are set")
	}
}

func TestClient_Options_WithModel(t *testing.T) {
	c, err := NewClientWithOptions(WithModel("gpt-4"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.model != "gpt-4" {
		t.Errorf("expected model gpt-4, got %s", c.model)
	}
}

func TestClient_Options_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{Timeout: 5 * time.Second}
	c, err := NewClientWithOptions(WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.httpClient != customClient {
		t.Error("expected custom HTTP client to be set")
	}
}

func TestClient_Options_WithTimeout(t *testing.T) {
	c, err := NewClientWithOptions(WithTimeout(10 * time.Second))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.httpClient.Timeout != 10*time.Second {
		t.Errorf("expected timeout 10s, got %v", c.httpClient.Timeout)
	}
}

func TestClient_Options_WithTimeoutZero(t *testing.T) {
	c, err := NewClientWithOptions(WithTimeout(0))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.httpClient.Timeout != 0 {
		t.Errorf("expected timeout 0, got %v", c.httpClient.Timeout)
	}
}

func TestClient_Options_WithLimiter(t *testing.T) {
	limiter := NewLimiter()
	c, err := NewClientWithOptions(WithLimiter(limiter))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.limiter != limiter {
		t.Error("expected custom limiter to be set")
	}
}

func TestClient_Options_WithRetryPolicy(t *testing.T) {
	policy := DefaultRetryPolicy
	c, err := NewClientWithOptions(WithRetryPolicy(policy))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.retry.MaxAttempts != policy.MaxAttempts {
		t.Error("expected custom retry policy to be set")
	}
}

func TestClient_Options_WithCostTable(t *testing.T) {
	table := NewCostTable()
	c, err := NewClientWithOptions(WithCostTable(table))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.costTable != table {
		t.Error("expected custom cost table to be set")
	}
}

func TestClient_Options_WithCostTable_NilReturnsError(t *testing.T) {
	_, err := NewClientWithOptions(WithCostTable(nil))
	if err == nil {
		t.Fatal("expected error for nil cost table")
	}
}

func TestClient_Options_WithMetrics(t *testing.T) {
	metrics := NewMetrics()
	c, err := NewClientWithOptions(WithMetrics(metrics))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.metrics != metrics {
		t.Error("expected custom metrics to be set")
	}
}

func TestClient_Options_WithTracer(t *testing.T) {
	tracer := NoopTracer{}
	c, err := NewClientWithOptions(WithTracer(tracer))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.tracer != tracer {
		t.Error("expected custom tracer to be set")
	}
}

func TestClient_Options_WithTracer_NilUsesNoop(t *testing.T) {
	c, err := NewClientWithOptions(WithTracer(nil))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	// Verify tracer is not nil (should default to NoopTracer)
	if c.tracer == nil {
		t.Error("expected non-nil tracer when nil is passed")
	}
}

func TestClient_Limiter_ReturnsLimiter(t *testing.T) {
	c := NewClient()
	if c.Limiter() == nil {
		t.Error("expected non-nil limiter")
	}
}

func TestClient_CostTable_ReturnsCostTable(t *testing.T) {
	c := NewClient()
	if c.CostTable() == nil {
		t.Error("expected non-nil cost table")
	}
}

func TestClient_Metrics_ReturnsMetrics(t *testing.T) {
	c := NewClient()
	if c.Metrics() == nil {
		t.Error("expected non-nil metrics")
	}
}

func TestClient_Chat_UnconfiguredReturnsError(t *testing.T) {
	c := NewClient()
	_, err := c.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error when client is not configured")
	}
}

func TestClient_ChatWithUsage_UnconfiguredReturnsError(t *testing.T) {
	c := NewClient()
	_, _, err := c.ChatWithUsage(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error when client is not configured")
	}
}

func TestClient_GenerateStrategyCode_UnconfiguredReturnsError(t *testing.T) {
	c := NewClient()
	_, err := c.GenerateStrategyCode(context.Background(), "test strategy")
	if err == nil {
		t.Fatal("expected error when client is not configured")
	}
}

func TestClient_FixStrategyCode_UnconfiguredReturnsError(t *testing.T) {
	c := NewClient()
	_, err := c.FixStrategyCode(context.Background(), "code", "errors")
	if err == nil {
		t.Fatal("expected error when client is not configured")
	}
}

func TestLLMClient_InterfaceCompliance(t *testing.T) {
	// Compile-time check that both *Client and *MockClient satisfy LLMClient
	var _ LLMClient = (*Client)(nil)
	var _ LLMClient = (*MockClient)(nil)
}

func TestChatMessage_Struct(t *testing.T) {
	msg := ChatMessage{Role: "assistant", Content: "Hello!"}
	if msg.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msg.Role)
	}
	if msg.Content != "Hello!" {
		t.Errorf("expected content 'Hello!', got %q", msg.Content)
	}
}

func TestChatRequest_Struct(t *testing.T) {
	req := ChatRequest{
		Model:    "gpt-4",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}
	if req.Model != "gpt-4" {
		t.Errorf("expected model gpt-4, got %s", req.Model)
	}
	if len(req.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(req.Messages))
	}
}

func TestChatResponse_Struct(t *testing.T) {
	resp := ChatResponse{
		Choices: []Choice{
			{Message: ChatMessage{Role: "assistant", Content: "response"}},
		},
		Usage: &Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
	if len(resp.Choices) != 1 {
		t.Errorf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "response" {
		t.Errorf("expected content 'response', got %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.TotalTokens != 30 {
		t.Errorf("expected 30 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestUsage_Struct(t *testing.T) {
	u := Usage{PromptTokens: 100, CompletionTokens: 200, TotalTokens: 300}
	if u.PromptTokens != 100 {
		t.Errorf("expected 100 prompt tokens, got %d", u.PromptTokens)
	}
	if u.CompletionTokens != 200 {
		t.Errorf("expected 200 completion tokens, got %d", u.CompletionTokens)
	}
	if u.TotalTokens != 300 {
		t.Errorf("expected 300 total tokens, got %d", u.TotalTokens)
	}
}

func TestMockClient_MultipleMethodsCalled(t *testing.T) {
	m := &MockClient{
		Configured:       true,
		ChatResponse:     "chat response",
		GenerateResponse: "generate response",
		FixResponse:      "fix response",
	}

	_, _ = m.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}})
	_, _ = m.GenerateStrategyCode(context.Background(), "desc")
	_, _ = m.FixStrategyCode(context.Background(), "code", "err")

	if len(m.ChatCalls) != 1 {
		t.Errorf("expected 1 chat call, got %d", len(m.ChatCalls))
	}
	if len(m.GenerateCalls) != 1 {
		t.Errorf("expected 1 generate call, got %d", len(m.GenerateCalls))
	}
	if len(m.FixCalls) != 1 {
		t.Errorf("expected 1 fix call, got %d", len(m.FixCalls))
	}
}
