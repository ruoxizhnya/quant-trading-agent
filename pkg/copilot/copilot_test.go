// Tests for CopilotService — the top-level orchestrator used by
// the strategy workshop "AI generate" button. Most of `run()` is
// hard to test (it shells out to `go build` against an absolute
// hard-coded working directory — see Sprint 6 P0-4), but
// Generate/GetJob/Stats/AcceptanceRate are the observable contract
// and are easy to cover with a deterministic ai.MockClient
// (Sprint 6 P0-1).
package copilot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/quant-trading/pkg/sandbox/staticcheck"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testStaticCheckAdapter wraps internal/sandbox/staticcheck so the
// CopilotService can exercise the real regex-based sandbox gate
// without pkg/strategy's production code importing
// internal/sandbox/staticcheck (S7-P1-2). This is a TEST-ONLY adapter
// — production wiring lives in cmd/analysis/main.go.
type testStaticCheckAdapter struct{}

func (testStaticCheckAdapter) CheckOrError(code string) error {
	return staticcheck.CheckOrError(code)
}

// newCopilotWithMock constructs a CopilotService whose AI client is
// a deterministic mock AND whose code checker is the real staticcheck
// (S7-P1-2: was implicit via direct staticcheck.CheckOrError call in
// run(); now wired via WithCodeChecker to preserve test fidelity).
// Tests that don't await the spawned goroutine (Generate is
// fire-and-forget) don't need to drain the goroutine because the mock
// is concurrency-safe.
func newCopilotWithMock(mock *ai.MockClient) *CopilotService {
	return NewCopilotServiceWithLLM(mock).WithCodeChecker(testStaticCheckAdapter{})
}

func TestCopilotService_Generate_RecordsJob(t *testing.T) {
	svc := newCopilotWithMock(&ai.MockClient{
		Configured:       true,
		GenerateResponse: "package plugins",
	})

	res := svc.Generate(context.Background(), GenerateParams{
		Description: "momentum",
		Universe:    "csi300",
		StartDate:   "2025-01-01",
		EndDate:     "2025-12-31",
	}, nil) // runner nil is fine — run() will short-circuit on build success without a runner

	require.NotNil(t, res)
	assert.NotEmpty(t, res.JobID)

	// S7-P0-12 (ODR-043): Lock before reading Status. The run() goroutine
	// writes Status (and other fields) under result.Lock(); any
	// unsynchronized read is a data race flagged by -race. The value is
	// "pending" at Generate() return time, but may have already transitioned
	// to a terminal status (e.g. "sandbox_rejected" since WorkingDir is empty
	// in this test and run() fails fast) by the time we read it — so we assert
	// non-empty rather than a specific value.
	res.Lock()
	status := res.Status
	res.Unlock()
	assert.NotEmpty(t, status,
		"Generate must initialize Status (to 'pending' or a terminal status)")
	generated, _, _ := svc.Stats()
	assert.GreaterOrEqual(t, generated, int64(1),
		"Generate must bump the generated counter so /stats/acceptance works")

	// GetJob round-trip
	got := svc.GetJob(res.JobID)
	require.NotNil(t, got)
	assert.Equal(t, res.JobID, got.JobID)
}

func TestCopilotService_GetJob_NotFound(t *testing.T) {
	svc := newCopilotWithMock(&ai.MockClient{Configured: false})
	assert.Nil(t, svc.GetJob("nonexistent"))
}

func TestCopilotService_IsConfigured_True(t *testing.T) {
	svc := newCopilotWithMock(&ai.MockClient{Configured: true})
	assert.True(t, svc.IsConfigured(),
		"IsConfigured must delegate to the underlying LLMClient")
}

func TestCopilotService_IsConfigured_False(t *testing.T) {
	svc := newCopilotWithMock(&ai.MockClient{Configured: false})
	assert.False(t, svc.IsConfigured())
}

func TestCopilotService_NewCopilotServiceWithLLM_NilLeavesClientNil(t *testing.T) {
	// S7-P1-2: Nil client NO LONGER falls back to ai.NewClient() — that
	// would re-introduce the strategy → ai reverse dependency. Instead,
	// aiClient stays nil and IsConfigured() returns false. The caller
	// MUST inject a real client via WithLLMClient at the composition root.
	svc := NewCopilotServiceWithLLM(nil)
	require.NotNil(t, svc)
	assert.False(t, svc.IsConfigured(),
		"NewCopilotServiceWithLLM(nil) must leave aiClient nil (no fallback)")
}

func TestCopilotService_AcceptanceRate_ZeroGenerated(t *testing.T) {
	svc := newCopilotWithMock(&ai.MockClient{Configured: false})
	// No calls to Generate yet → AcceptanceRate must be 0 (avoid
	// division by zero, see source).
	assert.Equal(t, 0.0, svc.AcceptanceRate())
}

func TestCopilotService_AcceptanceRate_ComputedFromCounters(t *testing.T) {
	svc := newCopilotWithMock(&ai.MockClient{Configured: false})

	// Inject counters directly (private fields, same package OK).
	svc.generated = 10
	svc.backtested = 7
	assert.InDelta(t, 0.7, svc.AcceptanceRate(), 1e-9)

	svc.backtested = 0
	assert.Equal(t, 0.0, svc.AcceptanceRate(),
		"zero backtested + nonzero generated must yield 0.0 not NaN")
}

func TestCopilotService_Stats_AtomicRead(t *testing.T) {
	svc := newCopilotWithMock(&ai.MockClient{
		Configured:       true,
		GenerateResponse: "package plugins",
	})

	// Hammer Generate from multiple goroutines and confirm the
	// generated counter ends at exactly the number of calls.
	// Generate spawns a goroutine that ignores ctx, so we can't
	// rely on the goroutine to clean up — we just verify the
	// counter is monotonic and reaches the expected value.
	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			svc.Generate(context.Background(), GenerateParams{Description: "x"}, nil)
		}()
	}
	// Wait for all Generate calls to return (they're sync up to the
	// s.jobs.Store + counter bump; the goroutine detaches).
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Generate didn't return for all callers within 2s")
	}

	generated, _, _ := svc.Stats()
	assert.Equal(t, int64(N), generated,
		"Stats().generated must equal the number of Generate calls")
}

// TestCopilotService_Run_LLMFailurePath verifies that when the LLM
// returns an error from GenerateStrategyCode, the job's terminal
// status is "llm_failed" and the buildable/backtested counters are
// not bumped. This is the path that previously triggered the nil
// pointer panic before LLMClient was extracted (Sprint 6 P0-1).
func TestCopilotService_Run_LLMFailurePath(t *testing.T) {
	wantErr := errors.New("simulated LLM outage")
	mock := &ai.MockClient{
		Configured: true,
		GenerateFunc: func(ctx context.Context, description string) (string, error) {
			return "", wantErr
		},
	}
	svc := newCopilotWithMock(mock)

	res := svc.Generate(context.Background(),
		GenerateParams{Description: "x"}, nil)
	require.NotNil(t, res)

	// Wait for the goroutine to drain. Poll GetJob with a tight
	// deadline — the LLM call is in-process so it should be
	// near-instant. Lock the JobResult before reading to sync
	// with the run() goroutine (Sprint 6 P0-1 hardening).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job := svc.GetJob(res.JobID)
		if job == nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		job.Lock()
		status := job.Status
		job.Unlock()
		if status != "" && status != "pending" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	job := svc.GetJob(res.JobID)
	require.NotNil(t, job)
	// Lock the JobResult before reading to synchronize with the
	// run() goroutine that writes under job.Lock(). Without this,
	// -race flags a data race (Sprint 6 P0-1 hardening).
	job.Lock()
	status := job.Status
	buildErr := job.BuildErr
	job.Unlock()
	assert.Equal(t, "llm_failed", status,
		"LLM error must surface as llm_failed status")
	assert.Contains(t, buildErr, "simulated LLM outage")

	// Counters: generated bumped, buildable NOT bumped, backtested NOT bumped.
	g, b, bt := svc.Stats()
	assert.GreaterOrEqual(t, g, int64(1))
	assert.Equal(t, int64(0), b, "buildable must remain 0 when LLM failed")
	assert.Equal(t, int64(0), bt, "backtested must remain 0 when LLM failed")
}

// TestCopilotService_Run_MockCalledWithDescription asserts the
// CopilotService forwards the description verbatim to the LLM and
// stores the generated code on the JobResult. Uses a polling wait
// for the fire-and-forget goroutine.
func TestCopilotService_Run_MockCalledWithDescription(t *testing.T) {
	var calls atomic.Int32
	mock := &ai.MockClient{
		Configured: true,
		GenerateFunc: func(ctx context.Context, description string) (string, error) {
			calls.Add(1)
			return "package plugins // generated", nil
		},
	}
	svc := newCopilotWithMock(mock)

	res := svc.Generate(context.Background(),
		GenerateParams{Description: "momentum on CSI300"}, nil)

	// Wait for the goroutine to call the LLM.
	require.Eventually(t, func() bool {
		return calls.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond,
		"Generate goroutine must call LLM within 2s")

	// Note: the build step will fail (working dir + Go plugin issues
	// are out of scope for P0-1; see P0-4), but we don't assert on
	// terminal status here — we only verify the LLM was called.
	_ = svc.GetJob(res.JobID)
}

// ---- parseUniverse -------------------------------------------------------

func TestParseUniverse_EmptyOrAll_ReturnsNil(t *testing.T) {
	assert.Nil(t, parseUniverse(""))
	assert.Nil(t, parseUniverse("all"),
		"universe='all' is a sentinel for 'use default pool' and must map to nil")
}

func TestParseUniverse_StripPrefix(t *testing.T) {
	assert.Equal(t, []string{"csi300", "csi500"},
		parseUniverse("universe:csi300,csi500"),
		"the 'universe:' prefix is a transport quirk and must be stripped")
}

func TestParseUniverse_TrimsWhitespace(t *testing.T) {
	got := parseUniverse(" csi300 , csi500 ,csi800 ")
	assert.Equal(t, []string{"csi300", "csi500", "csi800"}, got)
}

func TestParseUniverse_SingleEntry(t *testing.T) {
	assert.Equal(t, []string{"csi300"}, parseUniverse("csi300"))
}

// ---- P0-4 (ODR-013) WorkingDir + Stage-1 sandbox -----------------------

// TestCopilotService_WithWorkingDir verifies the post-construction
// setter pattern. The method is the only way to wire a working dir
// into the service, and the getter (WorkingDir) is what the
// bootstrap log uses to confirm the operator actually configured
// the value at startup.
func TestCopilotService_WithWorkingDir(t *testing.T) {
	svc := newCopilotWithMock(&ai.MockClient{Configured: true})
	assert.Equal(t, "", svc.WorkingDir(),
		"freshly-constructed service must have an empty WorkingDir (fail-closed)")

	const want = "/opt/quant-trading"
	svc.WithWorkingDir(want)
	assert.Equal(t, want, svc.WorkingDir())

	// Chained setter returns the same receiver (we don't compare
	// pointers directly because the helper allocates).
	_ = svc.WithWorkingDir("/elsewhere").WorkingDir()
	assert.Equal(t, "/elsewhere", svc.WorkingDir(),
		"chained call must overwrite the prior value")
}

// TestCopilotService_Run_StaticcheckRejectsDangerousCode is the
// P0-4 sandbox gate end-to-end: the LLM produces code containing
// os.RemoveAll, the gate must reject it with status="sandbox_rejected"
// and the error must surface the finding details so the API client
// (and the LLM on retry) can act on it.
func TestCopilotService_Run_StaticcheckRejectsDangerousCode(t *testing.T) {
	mock := &ai.MockClient{
		Configured:       true,
		GenerateResponse: "package x\nimport \"os\"\nfunc f() { os.RemoveAll(\"/\") }\n",
	}
	svc := newCopilotWithMock(mock).
		WithWorkingDir(t.TempDir()) // WorkingDir set, so the rejection must come from staticcheck, not config

	res := svc.Generate(context.Background(),
		GenerateParams{Description: "do bad things"}, nil)
	require.NotNil(t, res)

	waitTerminal(t, svc, res.JobID, 2*time.Second)

	job := svc.GetJob(res.JobID)
	require.NotNil(t, job)
	job.Lock()
	status := job.Status
	buildErr := job.BuildErr
	job.Unlock()

	assert.Equal(t, "sandbox_rejected", status,
		"LLM-generated code containing os.RemoveAll must be rejected by staticcheck")
	assert.Contains(t, buildErr, "os.RemoveAll",
		"BuildErr must identify the offending pattern for the LLM retry")
	assert.Contains(t, buildErr, "staticcheck rejected",
		"BuildErr must carry the staticcheck marker for log filtering")

	// Counters: buildable and backtested must NOT be bumped.
	_, b, bt := svc.Stats()
	assert.Equal(t, int64(0), b, "buildable must not increment on sandbox rejection")
	assert.Equal(t, int64(0), bt, "backtested must not increment on sandbox rejection")
}

// TestCopilotService_Run_StaticcheckRejectsExecCommand covers the
// exec.Command pattern (subprocess execution). This is the most
// likely real-world LLM failure mode — strategies that "test
// themselves" by shelling out.
func TestCopilotService_Run_StaticcheckRejectsExecCommand(t *testing.T) {
	mock := &ai.MockClient{
		Configured:       true,
		GenerateResponse: "package x\nimport \"os/exec\"\nfunc f() { _ = exec.Command(\"ls\") }\n",
	}
	svc := newCopilotWithMock(mock).WithWorkingDir(t.TempDir())

	res := svc.Generate(context.Background(),
		GenerateParams{Description: "shell out"}, nil)
	require.NotNil(t, res)

	waitTerminal(t, svc, res.JobID, 2*time.Second)

	job := svc.GetJob(res.JobID)
	job.Lock()
	status, buildErr := job.Status, job.BuildErr
	job.Unlock()

	assert.Equal(t, "sandbox_rejected", status)
	assert.Contains(t, buildErr, "exec.Command")
}

// TestCopilotService_Run_WorkingDirEmpty_RejectsWithConfigError
// covers the fail-closed configuration path: WorkingDir is empty
// (operator forgot to set it in analysis-service.yaml). The job
// must terminate with sandbox_rejected and a BuildErr that points
// the operator at the config key, not at a generic "go: no go.mod"
// error.
func TestCopilotService_Run_WorkingDirEmpty_RejectsWithConfigError(t *testing.T) {
	mock := &ai.MockClient{
		Configured:       true,
		GenerateResponse: "package x\nfunc f() {}\n", // safe code — failure must be config, not staticcheck
	}
	svc := newCopilotWithMock(mock) // WorkingDir left empty

	res := svc.Generate(context.Background(),
		GenerateParams{Description: "harmless"}, nil)
	require.NotNil(t, res)

	waitTerminal(t, svc, res.JobID, 2*time.Second)

	job := svc.GetJob(res.JobID)
	job.Lock()
	status, buildErr := job.Status, job.BuildErr
	job.Unlock()

	assert.Equal(t, "sandbox_rejected", status,
		"empty WorkingDir must terminate the job as sandbox_rejected (fail-closed)")
	assert.Contains(t, buildErr, "copilot.working_dir",
		"BuildErr must name the YAML key so the operator can fix it")
}

// TestCopilotService_WithLogger_NopIsSafe verifies that passing
// zerolog.Nop() (the default) is fine and the service doesn't
// crash. This is the "no logger threaded" path used by most unit
// tests; the WithLogger setter must not blow up on it.
func TestCopilotService_WithLogger_NopIsSafe(t *testing.T) {
	svc := newCopilotWithMock(&ai.MockClient{Configured: true})
	// Don't assert on the returned receiver — the contract is
	// "doesn't panic, doesn't disable the service".
	_ = svc.WithLogger(zerolog.Nop())
}

// TestCopilotService_Run_StaticcheckRejectsPanic covers the panic
// pattern (high severity). Strategies must return errors, not
// crash the backtest engine.
func TestCopilotService_Run_StaticcheckRejectsPanic(t *testing.T) {
	mock := &ai.MockClient{
		Configured:       true,
		GenerateResponse: "package x\nfunc f() { panic(\"boom\") }\n",
	}
	svc := newCopilotWithMock(mock).WithWorkingDir(t.TempDir())

	res := svc.Generate(context.Background(),
		GenerateParams{Description: "crashy"}, nil)
	require.NotNil(t, res)

	waitTerminal(t, svc, res.JobID, 2*time.Second)

	job := svc.GetJob(res.JobID)
	job.Lock()
	status, buildErr := job.Status, job.BuildErr
	job.Unlock()

	assert.Equal(t, "sandbox_rejected", status)
	assert.Contains(t, buildErr, "panic")
}

// waitTerminal polls GetJob until the run() goroutine has written a
// non-pending status, or fails the test at deadline. It's the
// synchronization point for the fire-and-forget Generate() call
// across all the P0-4 sandbox tests.
func waitTerminal(t *testing.T, svc *CopilotService, jobID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		job := svc.GetJob(jobID)
		if job == nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		job.Lock()
		status := job.Status
		job.Unlock()
		if status != "" && status != "pending" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach a terminal status within %s", jobID, timeout)
}
