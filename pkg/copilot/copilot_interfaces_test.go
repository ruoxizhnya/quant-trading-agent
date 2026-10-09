// S7-P1-2 regression tests: verify the local interfaces break the
// strategy → ai / strategy → internal/sandbox reverse dependencies.
//
// This test file is INTENTIONALLY self-contained: it does NOT import
// pkg/ai, pkg/ai/drift, or internal/sandbox/*. All stubs are defined
// locally so the test binary's import graph proves the reverse dep is
// gone from production code.
//
// What these tests lock in:
//  1. Local LLMClient / CodeChecker / BuildExecutor interfaces exist
//     in pkg/strategy and can be referenced without importing higher
//     layers.
//  2. NewCopilotService() returns a service with nil deps — callers
//     MUST inject via WithLLMClient / WithCodeChecker / WithBuildExecutor
//     (dependency injection, fail-closed).
//  3. run() fails closed when codeChecker is nil (no silent skip of
//     the sandbox gate).
//  4. run() fails closed when buildExecutor is nil (no silent skip
//     of the build step).
//  5. Injected stubs are actually invoked (the wiring is real, not
//     a no-op).
package copilot

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- Local stubs (NO pkg/ai import) -------------------------------------

// stubLLMClient is a deterministic LLMClient implementation that does
// NOT import pkg/ai — proving the local interface breaks the reverse
// dependency. Tests in copilot_test.go still use *ai.MockClient (which
// structurally satisfies LLMClient); this stub covers the case where
// we want to verify the interface without the pkg/ai import.
type stubLLMClient struct {
	configured       bool
	generateResponse string
	generateErr      error
	fixResponse      string
	fixErr           error

	mu            sync.Mutex
	generateCalls int32
	fixCalls      int32
}

func (s *stubLLMClient) IsConfigured() bool { return s.configured }

func (s *stubLLMClient) GenerateStrategyCode(ctx context.Context, description string) (string, error) {
	atomic.AddInt32(&s.generateCalls, 1)
	return s.generateResponse, s.generateErr
}

func (s *stubLLMClient) FixStrategyCode(ctx context.Context, code string, buildErrors string) (string, error) {
	atomic.AddInt32(&s.fixCalls, 1)
	return s.fixResponse, s.fixErr
}

// stubCodeChecker is a test-only CodeChecker.
type stubCodeChecker struct {
	err     error
	calls   int32
	allowed int32 // number of times CheckOrError returned nil before switching to err
}

func (s *stubCodeChecker) CheckOrError(code string) error {
	n := atomic.AddInt32(&s.calls, 1)
	if s.allowed > 0 && n <= s.allowed {
		return nil
	}
	return s.err
}

// stubBuildExecutor is a test-only BuildExecutor.
type stubBuildExecutor struct {
	runCalls  int32
	stdout    *bytes.Buffer
	stderr    *bytes.Buffer
	err       error
	isTimeout bool
}

func (s *stubBuildExecutor) Run(ctx context.Context, name string, args []string, workingDir string) (*bytes.Buffer, *bytes.Buffer, error) {
	atomic.AddInt32(&s.runCalls, 1)
	if s.stdout == nil {
		s.stdout = &bytes.Buffer{}
	}
	if s.stderr == nil {
		s.stderr = &bytes.Buffer{}
	}
	return s.stdout, s.stderr, s.err
}

func (s *stubBuildExecutor) IsTimeout(err error) bool {
	return s.isTimeout && errors.Is(err, s.err)
}

// ---- Tests ---------------------------------------------------------------

// TestS7P1_2_LocalInterfacesExist verifies the three local interfaces
// can be referenced from this test file without importing pkg/ai or
// internal/sandbox. The compile-time var-assertions below are the real
// check — if the interfaces are removed or renamed, this file fails to
// compile.
func TestS7P1_2_LocalInterfacesExist(t *testing.T) {
	var _ LLMClient = (*stubLLMClient)(nil)
	var _ CodeChecker = (*stubCodeChecker)(nil)
	var _ BuildExecutor = (*stubBuildExecutor)(nil)
	t.Log("local interfaces exist: LLMClient, CodeChecker, BuildExecutor")
}

// TestS7P1_2_NewCopilotService_ReturnsNilDeps verifies the default
// constructor returns a service with nil dependencies — the caller
// MUST inject via WithLLMClient / WithCodeChecker / WithBuildExecutor.
// This is the fail-closed DI pattern that breaks the strategy → ai
// reverse dependency at construction time.
func TestS7P1_2_NewCopilotService_ReturnsNilDeps(t *testing.T) {
	svc := NewCopilotService()
	require.NotNil(t, svc)
	// nil aiClient → IsConfigured returns false (fail-closed).
	assert.False(t, svc.IsConfigured(),
		"NewCopilotService() must return service with nil aiClient")
}

// TestS7P1_2_WithLLMClient_Injects verifies the WithLLMClient setter
// wires a local LLMClient implementation.
func TestS7P1_2_WithLLMClient_Injects(t *testing.T) {
	svc := NewCopilotService()
	llm := &stubLLMClient{configured: true}
	svc.WithLLMClient(llm)
	assert.True(t, svc.IsConfigured(),
		"WithLLMClient must wire the client so IsConfigured delegates to it")
}

// TestS7P1_2_WithCodeChecker_Injects verifies the WithCodeChecker setter
// wires a CodeChecker and run() actually invokes it.
func TestS7P1_2_WithCodeChecker_Injects(t *testing.T) {
	llm := &stubLLMClient{
		configured:       true,
		generateResponse: "package x\nfunc f() {}\n",
	}
	checker := &stubCodeChecker{err: errors.New("blocked by stub")}
	svc := NewCopilotService().
		WithLLMClient(llm).
		WithCodeChecker(checker).
		WithWorkingDir(t.TempDir())

	res := svc.Generate(context.Background(), GenerateParams{Description: "x"}, nil)
	require.NotNil(t, res)
	waitTerminal(t, svc, res.JobID, 2*time.Second)

	job := svc.GetJob(res.JobID)
	require.NotNil(t, job)
	job.Lock()
	status, buildErr := job.Status, job.BuildErr
	job.Unlock()

	assert.Equal(t, "sandbox_rejected", status,
		"injected stub CodeChecker must reject code via run()")
	assert.Contains(t, buildErr, "blocked by stub",
		"BuildErr must carry the stub's error message")
	assert.GreaterOrEqual(t, atomic.LoadInt32(&checker.calls), int32(1),
		"stub CodeChecker.CheckOrError must be invoked by run()")
}

// TestS7P1_2_NilCodeChecker_FailsClosed verifies that when codeChecker
// is nil (caller forgot to inject), run() fails closed with a clear
// error naming the missing dep — NOT silently allowing the code through
// the sandbox gate.
func TestS7P1_2_NilCodeChecker_FailsClosed(t *testing.T) {
	llm := &stubLLMClient{
		configured:       true,
		generateResponse: "package x\nfunc f() {}\n", // safe code
	}
	svc := NewCopilotService().
		WithLLMClient(llm).
		WithWorkingDir(t.TempDir())
	// codeChecker is nil — must fail closed.

	res := svc.Generate(context.Background(), GenerateParams{Description: "x"}, nil)
	require.NotNil(t, res)
	waitTerminal(t, svc, res.JobID, 2*time.Second)

	job := svc.GetJob(res.JobID)
	require.NotNil(t, job)
	job.Lock()
	status, buildErr := job.Status, job.BuildErr
	job.Unlock()

	assert.Equal(t, "sandbox_rejected", status,
		"nil codeChecker must fail closed (reject), not silently allow")
	assert.Contains(t, buildErr, "code checker not configured",
		"BuildErr must name the missing dep so operator can fix it")
}

// TestS7P1_2_NilBuildExecutor_FailsClosed verifies that when
// buildExecutor is nil, run() fails closed at the build step with a
// clear error — NOT silently skipping the build.
func TestS7P1_2_NilBuildExecutor_FailsClosed(t *testing.T) {
	llm := &stubLLMClient{
		configured:       true,
		generateResponse: "package x\nfunc f() {}\n", // safe code
	}
	svc := NewCopilotService().
		WithLLMClient(llm).
		WithCodeChecker(&stubCodeChecker{err: nil}). // allow
		WithWorkingDir(t.TempDir())
	// buildExecutor is nil — must fail closed at build step.

	res := svc.Generate(context.Background(), GenerateParams{Description: "x"}, nil)
	require.NotNil(t, res)
	waitTerminal(t, svc, res.JobID, 2*time.Second)

	job := svc.GetJob(res.JobID)
	require.NotNil(t, job)
	job.Lock()
	status, buildErr := job.Status, job.BuildErr
	job.Unlock()

	assert.Equal(t, "build_failed", status,
		"nil buildExecutor must fail closed, not silently skip build")
	assert.Contains(t, buildErr, "build executor not configured",
		"BuildErr must name the missing dep")
}

// TestS7P1_2_BuildExecutor_IsInvoked verifies that an injected
// BuildExecutor is actually called by run() (not silently bypassed).
func TestS7P1_2_BuildExecutor_IsInvoked(t *testing.T) {
	llm := &stubLLMClient{
		configured:       true,
		generateResponse: "package x\nfunc f() {}\n",
	}
	exec := &stubBuildExecutor{err: errors.New("synthetic build failure")}
	svc := NewCopilotService().
		WithLLMClient(llm).
		WithCodeChecker(&stubCodeChecker{err: nil}).
		WithBuildExecutor(exec).
		WithWorkingDir(t.TempDir())

	res := svc.Generate(context.Background(), GenerateParams{Description: "x"}, nil)
	require.NotNil(t, res)
	waitTerminal(t, svc, res.JobID, 2*time.Second)

	assert.GreaterOrEqual(t, atomic.LoadInt32(&exec.runCalls), int32(1),
		"stub BuildExecutor.Run must be invoked by run()")
}

// TestS7P1_2_NewCopilotServiceWithLLM_NilLeavesClientNil verifies the
// post-refactor behavior: NewCopilotServiceWithLLM(nil) no longer falls
// back to ai.NewClient() (which would re-introduce the reverse dep).
// Instead, aiClient stays nil and the service is "not configured" —
// the caller MUST inject a real client via WithLLMClient.
//
// This test SUPERSEDES the old TestCopilotService_NewCopilotServiceWithLLM_NilFallsBackToReal
// which documented the now-removed fallback behavior.
func TestS7P1_2_NewCopilotServiceWithLLM_NilLeavesClientNil(t *testing.T) {
	svc := NewCopilotServiceWithLLM(nil)
	require.NotNil(t, svc)
	assert.False(t, svc.IsConfigured(),
		"NewCopilotServiceWithLLM(nil) must leave aiClient nil (no fallback) — "+
			"caller must inject via WithLLMClient")
}
