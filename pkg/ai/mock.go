package ai

import (
	"context"
	"fmt"
	"sync"
)

// MockClient is a deterministic in-memory LLMClient implementation for tests.
// It records every call and returns the configured canned response (or a
// configured error). Safe for concurrent use.
//
// Usage in tests:
//
//	mock := &ai.MockClient{
//	    Configured: true,
//	    GenerateFunc: func(ctx context.Context, description string) (string, error) {
//	        return "package plugins\n// generated", nil
//	    },
//	}
//	svc := copilot.NewCopilotServiceWithLLM(mock)
type MockClient struct {
	// Configured is returned by IsConfigured().
	Configured bool

	// ChatFunc, GenerateFunc, FixFunc — if set, the corresponding method
	// delegates to the function. If nil, the method returns the configured
	// canned response / error below.
	ChatFunc     func(ctx context.Context, messages []ChatMessage) (string, error)
	GenerateFunc func(ctx context.Context, description string) (string, error)
	FixFunc      func(ctx context.Context, code string, buildErrors string) (string, error)

	// CannedResponses are returned when the corresponding Func is nil.
	ChatResponse     string
	GenerateResponse string
	FixResponse      string

	// CannedErrors are returned by the methods when the corresponding Func
	// is nil and the response is empty. A non-nil error short-circuits the
	// canned response.
	ChatErr     error
	GenerateErr error
	FixErr      error

	mu sync.Mutex

	// Recorded calls — useful for asserting on what the SUT invoked.
	ChatCalls     [][]ChatMessage
	GenerateCalls []string
	FixCalls      []FixCall
}

// FixCall is one recorded FixStrategyCode invocation.
type FixCall struct {
	Code        string
	BuildErrors string
}

// IsConfigured implements LLMClient.
func (m *MockClient) IsConfigured() bool { return m.Configured }

// Chat implements LLMClient.
func (m *MockClient) Chat(ctx context.Context, messages []ChatMessage) (string, error) {
	m.mu.Lock()
	// Copy to avoid races with the test reading the slice.
	msgs := make([]ChatMessage, len(messages))
	copy(msgs, messages)
	m.ChatCalls = append(m.ChatCalls, msgs)
	m.mu.Unlock()

	if m.ChatFunc != nil {
		return m.ChatFunc(ctx, messages)
	}
	if m.ChatErr != nil {
		return "", m.ChatErr
	}
	if m.ChatResponse == "" {
		return "", fmt.Errorf("MockClient.Chat: no response configured")
	}
	return m.ChatResponse, nil
}

// GenerateStrategyCode implements LLMClient.
func (m *MockClient) GenerateStrategyCode(ctx context.Context, description string) (string, error) {
	m.mu.Lock()
	m.GenerateCalls = append(m.GenerateCalls, description)
	m.mu.Unlock()

	if m.GenerateFunc != nil {
		return m.GenerateFunc(ctx, description)
	}
	if m.GenerateErr != nil {
		return "", m.GenerateErr
	}
	return m.GenerateResponse, nil
}

// FixStrategyCode implements LLMClient.
func (m *MockClient) FixStrategyCode(ctx context.Context, code string, buildErrors string) (string, error) {
	m.mu.Lock()
	m.FixCalls = append(m.FixCalls, FixCall{Code: code, BuildErrors: buildErrors})
	m.mu.Unlock()

	if m.FixFunc != nil {
		return m.FixFunc(ctx, code, buildErrors)
	}
	if m.FixErr != nil {
		return "", m.FixErr
	}
	return m.FixResponse, nil
}

// Compile-time assertion that MockClient satisfies LLMClient.
var _ LLMClient = (*MockClient)(nil)
