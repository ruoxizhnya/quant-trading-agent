// Tests for MockClient — the deterministic in-memory LLMClient
// implementation used by unit tests in downstream packages (currently
// pkg/strategy). Coverage goal: ≥ 80% for pkg/ai.
package ai

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time assertion: *MockClient must satisfy LLMClient.
// (Mocked again here to catch the regression at test-time even if
// the production-side assertion in mock.go is removed.)
var _ LLMClient = (*MockClient)(nil)

func TestMockClient_ImplementsLLMClient(t *testing.T) {
	// Same as the var assertion but as a runtime sanity check.
	var c LLMClient = &MockClient{Configured: true}
	assert.True(t, c.IsConfigured())
}

func TestMockClient_IsConfigured_True(t *testing.T) {
	m := &MockClient{Configured: true}
	assert.True(t, m.IsConfigured())
}

func TestMockClient_IsConfigured_False(t *testing.T) {
	m := &MockClient{Configured: false}
	assert.False(t, m.IsConfigured())
}

// ---- Chat -----------------------------------------------------------------

func TestMockClient_Chat_ReturnsCannedResponse(t *testing.T) {
	m := &MockClient{ChatResponse: "hello"}
	got, err := m.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}})
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}

func TestMockClient_Chat_ReturnsCannedError(t *testing.T) {
	want := errors.New("chat down")
	m := &MockClient{ChatErr: want}
	_, err := m.Chat(context.Background(), nil)
	assert.ErrorIs(t, err, want)
}

func TestMockClient_Chat_FuncTakesPrecedence(t *testing.T) {
	m := &MockClient{
		ChatResponse: "canned",
		ChatFunc: func(ctx context.Context, msgs []ChatMessage) (string, error) {
			return "from-func", nil
		},
	}
	got, err := m.Chat(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "from-func", got,
		"Func override must take precedence over CannedResponse")
}

func TestMockClient_Chat_RecordsCalls(t *testing.T) {
	m := &MockClient{ChatResponse: "ok"}

	_, _ = m.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "a"}})
	_, _ = m.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "b"}})

	require.Len(t, m.ChatCalls, 2)
	assert.Equal(t, "a", m.ChatCalls[0][0].Content)
	assert.Equal(t, "b", m.ChatCalls[1][0].Content)
}

func TestMockClient_Chat_NoResponseConfiguredReturnsError(t *testing.T) {
	// No Func, no Response, no Err — defensible behavior is to error.
	m := &MockClient{}
	_, err := m.Chat(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no response configured")
}

func TestMockClient_Chat_ConcurrentSafe(t *testing.T) {
	m := &MockClient{ChatResponse: "ok"}

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, err := m.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "x"}})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, N, len(m.ChatCalls),
		"concurrent Chat calls must all be recorded without data race")
}

// ---- GenerateStrategyCode -------------------------------------------------

func TestMockClient_GenerateStrategyCode_ReturnsCannedResponse(t *testing.T) {
	m := &MockClient{GenerateResponse: "package plugins\n// stub"}
	got, err := m.GenerateStrategyCode(context.Background(), "desc")
	require.NoError(t, err)
	assert.Equal(t, "package plugins\n// stub", got)
}

func TestMockClient_GenerateStrategyCode_ReturnsCannedError(t *testing.T) {
	want := errors.New("llm rate limited")
	m := &MockClient{GenerateErr: want}
	_, err := m.GenerateStrategyCode(context.Background(), "x")
	assert.ErrorIs(t, err, want)
}

func TestMockClient_GenerateStrategyCode_FuncTakesPrecedence(t *testing.T) {
	m := &MockClient{
		GenerateResponse: "canned",
		GenerateFunc: func(ctx context.Context, description string) (string, error) {
			return "func-out", nil
		},
	}
	got, err := m.GenerateStrategyCode(context.Background(), "desc")
	require.NoError(t, err)
	assert.Equal(t, "func-out", got)
}

func TestMockClient_GenerateStrategyCode_RecordsCalls(t *testing.T) {
	m := &MockClient{GenerateResponse: "ok"}

	_, _ = m.GenerateStrategyCode(context.Background(), "alpha")
	_, _ = m.GenerateStrategyCode(context.Background(), "beta")

	require.Len(t, m.GenerateCalls, 2)
	assert.Equal(t, "alpha", m.GenerateCalls[0])
	assert.Equal(t, "beta", m.GenerateCalls[1])
}

// ---- FixStrategyCode ------------------------------------------------------

func TestMockClient_FixStrategyCode_ReturnsCannedResponse(t *testing.T) {
	m := &MockClient{FixResponse: "package plugins // fixed"}
	got, err := m.FixStrategyCode(context.Background(), "old code", "syntax err")
	require.NoError(t, err)
	assert.Equal(t, "package plugins // fixed", got)
}

func TestMockClient_FixStrategyCode_ReturnsCannedError(t *testing.T) {
	want := errors.New("fix failed")
	m := &MockClient{FixErr: want}
	_, err := m.FixStrategyCode(context.Background(), "x", "y")
	assert.ErrorIs(t, err, want)
}

func TestMockClient_FixStrategyCode_FuncTakesPrecedence(t *testing.T) {
	m := &MockClient{
		FixResponse: "canned",
		FixFunc: func(ctx context.Context, code, buildErrors string) (string, error) {
			return "from-func", nil
		},
	}
	got, err := m.FixStrategyCode(context.Background(), "x", "y")
	require.NoError(t, err)
	assert.Equal(t, "from-func", got)
}

func TestMockClient_FixStrategyCode_RecordsCalls(t *testing.T) {
	m := &MockClient{FixResponse: "ok"}

	_, _ = m.FixStrategyCode(context.Background(), "code1", "err1")
	_, _ = m.FixStrategyCode(context.Background(), "code2", "err2")

	require.Len(t, m.FixCalls, 2)
	assert.Equal(t, "code1", m.FixCalls[0].Code)
	assert.Equal(t, "err1", m.FixCalls[0].BuildErrors)
	assert.Equal(t, "code2", m.FixCalls[1].Code)
	assert.Equal(t, "err2", m.FixCalls[1].BuildErrors)
}
