package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OPS-01-A 取证：SSE 的节奏（pumpStream）脱离网络计时确定测；HTTP 层只验真实帧
// 与 404，且只对**已完成**的 run 开流 —— 服务端 progress+complete 后立即收流，
// io.ReadAll 阻塞到连接关闭即返回，不依赖活体计时。

// —— 逻辑层：pumpStream ——

func TestPumpStream_CompletedRunEmitsProgressThenComplete(t *testing.T) {
	var events []string
	pumpStream(context.Background(), time.Hour, // 首帧即 done，用不到 ticker
		func() ExploreStatusResponse { return ExploreStatusResponse{RunID: "r", Running: false} },
		func(ev string, _ ExploreStatusResponse) { events = append(events, ev) })
	assert.Equal(t, []string{"progress", "complete"}, events, "已跑完的 run 应首帧判完、发完即停")
}

func TestPumpStream_RunningThenDoneEmitsProgressPerTick(t *testing.T) {
	var events []string
	calls := 0
	pumpStream(context.Background(), 5*time.Millisecond,
		func() ExploreStatusResponse {
			calls++
			return ExploreStatusResponse{RunID: "r", Running: calls < 3} // 第 3 次判为跑完
		},
		func(ev string, _ ExploreStatusResponse) { events = append(events, ev) })
	assert.Equal(t, []string{"progress", "progress", "progress", "complete"}, events)
}

// TestPumpStream_CtxCancelStopsPushWithoutComplete：客户端断开（ctx 取消）应让 pump
// 立即停 —— 停在 progress，绝不发 complete（发了就等于把「还在跑」谎报成「跑完了」）。
// 这是「断线 ≠ 取消探索」在 pump 这一侧的可测面：它只会停推，不会编造终止。
func TestPumpStream_CtxCancelStopsPushWithoutComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var events []string
	pumpStream(ctx, time.Hour,
		func() ExploreStatusResponse { return ExploreStatusResponse{RunID: "r", Running: true} },
		func(ev string, _ ExploreStatusResponse) {
			events = append(events, ev)
			cancel() // 模拟首帧之后客户端断开
		})
	assert.Equal(t, []string{"progress"}, events, "断线应停在 progress，不发 complete")
}

// —— HTTP 层：真实 SSE 帧 + not-found ——

func streamPostStart(t *testing.T, srv *httptest.Server, body string) string {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/explore/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var r struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
	require.NotEmpty(t, r.RunID)
	return r.RunID
}

func streamWaitDone(t *testing.T, srv *httptest.Server, runID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := http.Get(srv.URL + "/api/explore/runs/" + runID)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var st ExploreStatusResponse
		if json.NewDecoder(resp.Body).Decode(&st) != nil {
			return false
		}
		return !st.Running
	}, 5*time.Second, 10*time.Millisecond, "run 没在 5 秒内跑完")
}

func TestExploreHandler_StreamUnknownRunID404(t *testing.T) {
	srv := httptest.NewServer(newExploreRouter(NewExploreHandler(&fakeExploreRunner{}, nil)))
	defer srv.Close()

	// 不存在的 run 要在**开流之前**就 404，而不是给一个一连上就 error 的 EventSource。
	resp, err := http.Get(srv.URL + "/api/explore/runs/nope/stream")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestExploreHandler_StreamCompletedRunSSEFrames(t *testing.T) {
	srv := httptest.NewServer(newExploreRouter(NewExploreHandler(&fakeExploreRunner{}, nil)))
	defer srv.Close()

	runID := streamPostStart(t, srv, `{"description":"做一个动量策略","max_tries":3}`)
	streamWaitDone(t, srv, runID) // 先跑完，再开流 → 首帧即 done

	resp, err := http.Get(srv.URL + "/api/explore/runs/" + runID + "/stream")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// 服务端 progress+complete 后收流，连接关闭 → ReadAll 返回，确定性。
	// gin v1.10 的 SSE 帧是 `event:<name>` / `data:<json>`（冒号后无空格），按实测匹配。
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "event:progress")
	assert.Contains(t, string(body), "event:complete")
	// complete 帧带最终快照（轮询端点同形状），前端可复用类型。
	assert.Contains(t, string(body), `"run_id":"`+runID+`"`)
}
