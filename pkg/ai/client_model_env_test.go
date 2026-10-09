// AI_MODEL environment wiring for the OpenAI-compatible client.
//
// WHY THIS FILE: AI_API_URL already let you repoint the client at any
// OpenAI-compatible gateway, but the model id was hardcoded to
// "gpt-4o-mini". A gateway that accepts /v1/chat/completions still rejects a
// foreign model id (MiniMax answers "model not found"), so URL-only config
// is half a feature: switching provider needs BOTH knobs.
//
// These legs assert the model id that actually goes out on the wire — not
// just the struct field — because "the field was set" and "the request
// carried it" can diverge, and only the latter decides whether a real
// provider answers or 400s.
//
// None of these touch the network: the endpoint is an httptest server.
package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modelCapturingServer serves chat completions and records the model id of
// the last request it saw. Also counts calls so a test can prove the server
// really ran (otherwise `got` would stay "" and a lazy assertion could pass
// by accident).
func modelCapturingServer(t *testing.T, got *string, calls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		*got = req.Model
		*calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatResponseJSON("ok", nil))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// chatOnce drives one Chat call and returns the model the server received.
func chatOnce(t *testing.T, opts ...ClientOption) (model string, calls int) {
	t.Helper()
	srv := modelCapturingServer(t, &model, &calls)
	c := newTestClient(t, srv, opts...)
	_, err := c.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}})
	require.NoError(t, err)
	return model, calls
}

func TestNewClient_ModelComesFromAI_MODEL(t *testing.T) {
	t.Setenv("AI_MODEL", "MiniMax-M3")

	model, calls := chatOnce(t)

	require.Equal(t, 1, calls, "服务端必须真的被调用，否则下面的断言等于没跑")
	assert.Equal(t, "MiniMax-M3", model,
		"AI_MODEL 必须真的进到请求体 —— 否则换供应商会把 gpt-4o-mini 发给对方，必然 model-not-found")
}

func TestNewClient_ModelFallsBackToDefaultWhenEnvBlank(t *testing.T) {
	// 两种「没配」都等价于默认：未设置、以及设成空白。
	// 空白必须回落，否则会发出 "model":"" ，供应商一律 400。
	for name, value := range map[string]string{
		"unset":      "",
		"whitespace": "   \t ",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("AI_MODEL", value)

			model, calls := chatOnce(t)

			require.Equal(t, 1, calls, "服务端必须真的被调用")
			assert.Equal(t, DefaultModel, model, "AI_MODEL 空白时必须回落到 DefaultModel")
			assert.NotEmpty(t, model, "绝不能把空模型名发出去")
		})
	}
}

func TestNewClient_WithModelBeatsAI_MODEL(t *testing.T) {
	// 显式选项优先于环境变量 —— 否则调用方没法在运行时覆盖配置。
	t.Setenv("AI_MODEL", "MiniMax-M3")

	model, calls := chatOnce(t, WithModel("explicit-override"))

	require.Equal(t, 1, calls, "服务端必须真的被调用")
	assert.Equal(t, "explicit-override", model,
		"WithModel 必须压过 AI_MODEL，否则调用方无法覆盖环境配置")
}

func TestNewClient_DefaultModelIsUsableAsAFallbackSentinel(t *testing.T) {
	// 自检腿：证明上面那几条不是「断言了空字符串」。
	// 若 DefaultModel 哪天被改成空，前面所有 assert.Equal(DefaultModel, ...)
	// 会跟着一起变绿 —— 这条腿独立把它钉住。
	assert.NotEmpty(t, DefaultModel, "DefaultModel 不能为空：空模型名会被所有供应商拒绝")
	assert.Equal(t, "gpt-4o-mini", DefaultModel,
		"DefaultModel 变了要同步 .env.example 与 docker-compose.yml 的注释")
}
