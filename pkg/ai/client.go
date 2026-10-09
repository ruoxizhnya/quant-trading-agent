package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// LLMClient is the contract that downstream packages (e.g. pkg/strategy) depend
// on. Extracting an interface lets tests inject a deterministic mock without
// hitting a real LLM endpoint, which is required for CI to pass without
// network access or shared credentials (Sprint 6 P0-1).
//
// Implementations:
//   - *Client  — real OpenAI-compatible HTTP client with timeout/retry/rate/cost
//   - *MockClient (in mock.go) — deterministic fake for unit tests
type LLMClient interface {
	IsConfigured() bool
	Chat(ctx context.Context, messages []ChatMessage) (string, error)
	GenerateStrategyCode(ctx context.Context, description string) (string, error)
	FixStrategyCode(ctx context.Context, code string, buildErrors string) (string, error)
}

// Compile-time assertion: *Client must satisfy LLMClient. If a method signature
// on *Client changes, this line fails to compile and the breakage is caught
// at the boundary rather than at runtime in a consumer.
var _ LLMClient = (*Client)(nil)

// Default HTTP timeout applied to the underlying transport. Without an
// explicit timeout a misbehaving LLM server can hang the caller's goroutine
// indefinitely (Sprint 6 P0-1 / TQ-003).
const defaultHTTPTimeout = 30 * time.Second

// Client wraps an HTTP client for LLM chat completions with production-grade
// resilience (timeout, retry, rate-limit, cost tracking, tracing) and
// observability (metrics). All knobs are exposed as functional options.
type Client struct {
	apiKey     string
	apiURL     string
	model      string
	httpClient *http.Client

	// Resilience layer (Sprint 6 P1-14, AR-008).
	limiter   *Limiter    // token-bucket rate limiter
	retry     RetryPolicy // exponential backoff with jitter
	costTable *CostTable  // per-model token cost
	metrics   *Metrics    // per-call observability
	tracer    Tracer      // pluggable tracer (default: NoopTracer)

	// mu protects limiter/retry/costTable/metrics/tracer swaps.
	// Field assignments during NewClientWithOptions are safe without
	// the lock because no other goroutine can see the client until
	// construction returns. The lock is held only by option setters
	// and the Chat() call site, which is also a single point of truth.
	mu sync.Mutex
}

// DefaultModel is the chat model used when AI_MODEL is unset or blank.
//
// Exported (rather than an inline literal) so callers and tests can assert
// the fallback without duplicating the string — a hardcoded literal in two
// places drifts the moment someone changes the default.
const DefaultModel = "gpt-4o-mini"

// resolveModel picks the chat model from the environment, falling back to
// DefaultModel when AI_MODEL is unset or whitespace-only.
//
// WHY AI_MODEL EXISTS: AI_API_URL already lets you point the client at any
// OpenAI-compatible gateway, but the *model* was hardcoded. A provider that
// speaks /v1/chat/completions still rejects a foreign model id (MiniMax
// answers "model not found" for "gpt-4o-mini"), so URL-only configuration is
// half a feature. Any OpenAI-compatible backend — OpenAI, MiniMax CN,
// DeepSeek, Qwen, OpenRouter, a local Ollama/vLLM shim — needs both knobs.
func resolveModel() string {
	if m := strings.TrimSpace(os.Getenv("AI_MODEL")); m != "" {
		return m
	}
	return DefaultModel
}

// NewClient creates a new AI client reading config from environment.
// A 30s default HTTP timeout is applied to prevent indefinite hangs.
func NewClient() *Client {
	c, _ := NewClientWithOptions()
	return c
}

// NewClientWithOptions builds a Client with functional options. Useful for
// tests that need to inject a custom model, custom HTTP client, or fixed
// API key/URL without touching environment variables.
//
// Precedence for each setting is: explicit option > environment variable >
// built-in default. WithModel therefore beats AI_MODEL, which beats
// DefaultModel.
func NewClientWithOptions(opts ...ClientOption) (*Client, error) {
	c := &Client{
		apiKey:     os.Getenv("AI_API_KEY"),
		apiURL:     os.Getenv("AI_API_URL"),
		model:      resolveModel(),
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
		limiter:    NewLimiter(),
		retry:      DefaultRetryPolicy,
		costTable:  NewCostTable(),
		metrics:    NewMetrics(),
		tracer:     NoopTracer{},
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, fmt.Errorf("ai client option: %w", err)
		}
	}
	return c, nil
}

// ClientOption configures a Client at construction time. Follows the
// functional-options pattern recommended by ADR-020 (avoiding Setter soup).
//
// Options may return an error to fail-fast on invalid input (e.g. negative
// rate). They run in order; later options override earlier ones.
type ClientOption func(*Client) error

// WithAPIKey overrides AI_API_KEY for this client instance.
func WithAPIKey(key string) ClientOption {
	return func(c *Client) error { c.apiKey = key; return nil }
}

// WithAPIURL overrides AI_API_URL for this client instance.
func WithAPIURL(url string) ClientOption {
	return func(c *Client) error { c.apiURL = url; return nil }
}

// WithModel overrides the model — it beats both AI_MODEL and DefaultModel.
func WithModel(model string) ClientOption {
	return func(c *Client) error { c.model = model; return nil }
}

// WithHTTPClient replaces the underlying *http.Client (e.g. with a custom
// transport for testing or for an OTel-instrumented round tripper).
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) error { c.httpClient = hc; return nil }
}

// WithTimeout overrides the default 30s HTTP timeout. Use 0 to disable.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) error {
		if d <= 0 {
			c.httpClient = &http.Client{}
			return nil
		}
		c.httpClient = &http.Client{Timeout: d}
		return nil
	}
}

// WithLimiter sets a custom rate limiter. Pass nil to disable rate limiting
// (useful for tests that exercise single-call latency in isolation).
func WithLimiter(l *Limiter) ClientOption {
	return func(c *Client) error { c.limiter = l; return nil }
}

// WithRetryPolicy sets a custom retry policy. Pass DefaultRetryPolicy (or
// a copy with adjustments) for production use.
func WithRetryPolicy(p RetryPolicy) ClientOption {
	return func(c *Client) error { c.retry = p; return nil }
}

// WithCostTable sets a custom cost table. Defaults to NewCostTable()
// populated with 2026-06-12 OpenAI/Anthropic/DeepSeek rates.
func WithCostTable(t *CostTable) ClientOption {
	return func(c *Client) error {
		if t == nil {
			return errors.New("WithCostTable: nil table")
		}
		c.costTable = t
		return nil
	}
}

// WithMetrics sets a custom metrics tracker. Pass nil to disable metrics.
func WithMetrics(m *Metrics) ClientOption {
	return func(c *Client) error { c.metrics = m; return nil }
}

// WithTracer sets a custom tracer. Defaults to NoopTracer. To plug in OTel,
// wrap otel.Tracer into the Tracer interface in your service init code and
// pass it here — this keeps the AI package free of OTel dependencies.
func WithTracer(t Tracer) ClientOption {
	return func(c *Client) error {
		if t == nil {
			c.tracer = NoopTracer{}
			return nil
		}
		c.tracer = t
		return nil
	}
}

// IsConfigured returns true when both AI_API_KEY and AI_API_URL are set.
//
// nil receiver 返回 false：调用方常把 *Client 装进 ai.LLMClient 接口，
// 而「装了类型的 nil 指针」不等于 nil 接口 —— 没有这层防御，
// `p.aiClient != nil && p.aiClient.IsConfigured()` 会在传 nil 时 panic。
func (c *Client) IsConfigured() bool {
	return c != nil && c.apiKey != "" && c.apiURL != ""
}

// Limiter returns the rate limiter (read-only handle). Useful for the
// /metrics endpoint to expose token availability.
func (c *Client) Limiter() *Limiter { return c.limiter }

// CostTable returns the cost table (read-only handle). Useful for the
// /pricing endpoint.
func (c *Client) CostTable() *CostTable { return c.costTable }

// Metrics returns the metrics tracker. Safe to call from any goroutine.
func (c *Client) Metrics() *Metrics { return c.metrics }

// ChatMessage represents a single message in a chat conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the request body for OpenAI-compatible chat completions.
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
}

// ChatResponse is the top-level response from OpenAI-compatible chat completions.
type ChatResponse struct {
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"` // OpenAI returns this; other providers may omit
}

// Choice represents one completion choice.
type Choice struct {
	Message ChatMessage `json:"message"`
}

// Chat sends a chat completion request and returns the assistant's reply text.
// The call goes through the full resilience stack: rate-limit wait →
// retry-with-backoff → per-call observability (tracer + metrics + cost).
func (c *Client) Chat(ctx context.Context, messages []ChatMessage) (string, error) {
	if !c.IsConfigured() {
		return "", fmt.Errorf("AI_API_KEY or AI_API_URL not configured")
	}

	// Snapshot the resilience config (lock-free) so concurrent option
	// changes (e.g. for hot-reload) don't tear the call state.
	c.mu.Lock()
	limiter := c.limiter
	retry := c.retry
	costTable := c.costTable
	metrics := c.metrics
	tracer := c.tracer
	c.mu.Unlock()

	// Defensive defaults: a hand-rolled Client (e.g. from tests) may not
	// have these wired. Treat nil as a no-op rather than panicking.
	if tracer == nil {
		tracer = NoopTracer{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	if costTable == nil {
		costTable = NewCostTable()
	}

	// Start span (default: no-op).
	ctx, span := tracer.StartSpan(ctx, SpanName, map[string]any{
		AttrAIModel: c.model,
	})
	defer span.End()

	// Rate-limit wait (blocks if bucket is empty, respects ctx).
	rateLimited := false
	if limiter != nil {
		if err := limiter.Wait(ctx); err != nil {
			span.RecordError(err, 0)
			return "", fmt.Errorf("rate limit: %w", err)
		}
		rateLimited = true
	}

	start := time.Now()
	attemptCount := 0
	result, status, err := retry.Do(ctx, func(ctx context.Context, attempt int) (string, int, error) {
		attemptCount = attempt
		span.SetAttribute(AttrAIRetryCount, attempt-1)
		return c.doChatOnce(ctx, messages)
	})
	duration := time.Since(start)

	// Compute cost (best-effort; if the response didn't include usage,
	// the cost is 0 — caller can still log the call).
	var usage Usage
	if err == nil {
		// Re-fetch usage — doChatOnce returned a stripped result string.
		// To keep the API simple, we re-parse the body in the caller path.
		// (See doChatWithUsage below for the version that returns usage.)
		// For now, we record zero usage when the simple path is used.
		_ = usage
	}

	cost := 0.0
	if costTable != nil && err == nil {
		// Best-effort: if the underlying call returned usage, we'd
		// record it here. The simple Chat() path doesn't, so cost is 0.
		// Use ChatWithUsage() to get cost tracking.
		_ = cost
	}

	// Span attributes.
	span.SetAttribute(AttrAIStatusCode, status)
	span.SetAttribute(AttrAIDurationMS, duration.Milliseconds())
	span.SetAttribute(AttrAIRateLimited, rateLimited)
	if err != nil {
		span.RecordError(err, status)
	}

	// Metrics.
	if metrics != nil {
		metrics.Record(CallResult{
			Model:       c.model,
			Usage:       usage,
			CostUSD:     cost,
			Duration:    duration,
			StatusCode:  status,
			Err:         err,
			Retried:     attemptCount > 1,
			RateLimited: rateLimited,
		})
	}

	return result, err
}

// ChatWithUsage is like Chat but returns the token usage and computes
// the cost. Use this for production paths where cost tracking is
// required; use Chat() for tests / quick paths.
func (c *Client) ChatWithUsage(ctx context.Context, messages []ChatMessage) (string, Usage, error) {
	if !c.IsConfigured() {
		return "", Usage{}, fmt.Errorf("AI_API_KEY or AI_API_URL not configured")
	}
	c.mu.Lock()
	limiter := c.limiter
	retry := c.retry
	costTable := c.costTable
	metrics := c.metrics
	tracer := c.tracer
	c.mu.Unlock()

	ctx, span := tracer.StartSpan(ctx, SpanName+".with_usage", map[string]any{
		AttrAIModel: c.model,
	})
	defer span.End()

	rateLimited := false
	if limiter != nil {
		if err := limiter.Wait(ctx); err != nil {
			span.RecordError(err, 0)
			return "", Usage{}, fmt.Errorf("rate limit: %w", err)
		}
		rateLimited = true
	}

	start := time.Now()
	attemptCount := 0
	var lastUsage Usage
	type chatResult struct {
		text  string
		usage Usage
	}
	result, status, err := retry.Do(ctx, func(ctx context.Context, attempt int) (string, int, error) {
		attemptCount = attempt
		span.SetAttribute(AttrAIRetryCount, attempt-1)
		text, usage, status, callErr := c.doChatOnceWithUsage(ctx, messages)
		lastUsage = usage
		return text, status, callErr
	})
	duration := time.Since(start)

	cost := 0.0
	if costTable != nil {
		cost = costTable.Calculate(c.model, lastUsage.PromptTokens, lastUsage.CompletionTokens)
	}

	span.SetAttribute(AttrAIStatusCode, status)
	span.SetAttribute(AttrAIPromptTok, lastUsage.PromptTokens)
	span.SetAttribute(AttrAICompletionTok, lastUsage.CompletionTokens)
	span.SetAttribute(AttrAITotalCost, cost)
	span.SetAttribute(AttrAIDurationMS, duration.Milliseconds())
	span.SetAttribute(AttrAIRateLimited, rateLimited)
	if err != nil {
		span.RecordError(err, status)
	}

	if metrics != nil {
		metrics.Record(CallResult{
			Model:       c.model,
			Usage:       lastUsage,
			CostUSD:     cost,
			Duration:    duration,
			StatusCode:  status,
			Err:         err,
			Retried:     attemptCount > 1,
			RateLimited: rateLimited,
		})
	}

	return result, lastUsage, err
}

// doChatOnce performs a single HTTP request (no retry, no rate limit).
// Used internally by Chat() and ChatWithUsage(); exported via the
// retryable signature.
func (c *Client) doChatOnce(ctx context.Context, messages []ChatMessage) (string, int, error) {
	text, _, status, err := c.doChatOnceFull(ctx, messages)
	return text, status, err
}

// doChatOnceWithUsage is the version that returns usage. Returns
// (text, usage, status, err).
func (c *Client) doChatOnceWithUsage(ctx context.Context, messages []ChatMessage) (string, Usage, int, error) {
	text, usage, status, err := c.doChatOnceFull(ctx, messages)
	return text, usage, status, err
}

// doChatOnceFull does the actual HTTP call. Returns (text, usage, status, err).
func (c *Client) doChatOnceFull(ctx context.Context, messages []ChatMessage) (string, Usage, int, error) {
	reqBody := ChatRequest{Model: c.model, Messages: messages}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", Usage{}, 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, 0, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", Usage{}, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", Usage{}, resp.StatusCode, fmt.Errorf("AI API returned status %d", resp.StatusCode)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", Usage{}, resp.StatusCode, fmt.Errorf("failed to decode response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", Usage{}, resp.StatusCode, fmt.Errorf("no response choices from LLM")
	}
	var usage Usage
	if chatResp.Usage != nil {
		usage = *chatResp.Usage
	}
	return chatResp.Choices[0].Message.Content, usage, resp.StatusCode, nil
}

// GenerateStrategyCode calls the LLM with system + user prompts and returns
// the raw Go code, stripping any markdown fences.
func (c *Client) GenerateStrategyCode(ctx context.Context, description string) (string, error) {
	messages := []ChatMessage{
		{Role: "system", Content: SystemPrompt},
		{Role: "user", Content: fmt.Sprintf(UserPromptTemplate, description)},
	}
	resp, err := c.Chat(ctx, messages)
	if err != nil {
		return "", err
	}
	return stripFences(resp), nil
}

// FixStrategyCode asks the LLM to fix compilation errors in the given code.
func (c *Client) FixStrategyCode(ctx context.Context, code string, buildErrors string) (string, error) {
	messages := []ChatMessage{
		{Role: "system", Content: SystemPrompt},
		{Role: "user", Content: fmt.Sprintf(FixPromptTemplate, code, buildErrors)},
	}
	resp, err := c.Chat(ctx, messages)
	if err != nil {
		return "", err
	}
	return stripFences(resp), nil
}

func stripFences(s string) string {
	s = strings.TrimPrefix(s, "```go")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
