package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════
//  Mock implementation
// ═══════════════════════════════════════════════════════════════════════

// mockResearchProfileClient satisfies ResearchProfileClient for testing.
// It returns a preset profile/error and records the ticker it was asked for,
// so tests can assert the tool normalised the caller's input.
type mockResearchProfileClient struct {
	profile   *storage.ResearchProfile
	err       error
	gotTicker string
	calls     int
}

func (m *mockResearchProfileClient) GetResearchProfile(_ context.Context, ticker string) (*storage.ResearchProfile, error) {
	m.calls++
	m.gotTicker = ticker
	if m.err != nil {
		return nil, m.err
	}
	return m.profile, nil
}

// ── Fixtures ────────────────────────────────────────────────────────

// freshProfile returns a schema_version 1 profile generated "now", so the
// staleness check does not fire unless a test deliberately ages it.
func freshProfile(ticker string) *storage.ResearchProfile {
	return &storage.ResearchProfile{
		Ticker:        ticker,
		Name:          "贵州茅台",
		SchemaVersion: 1,
		UpdatedAt:     time.Now().Add(-time.Hour),
		Conclusions: []storage.ResearchConclusion{
			{
				Ticker:     ticker,
				ID:         "C1",
				Title:      "高端白酒需求韧性强",
				Status:     "active",
				Citations:  json.RawMessage(`[{"content_hash":"` + strings.Repeat("a", 64) + `","pointer":"/revenue"}]`),
				UpdatedAt:  time.Now(),
				Confidence: strPtr("高"),
			},
		},
		Questions: []storage.ResearchQuestion{
			{Ticker: ticker, ID: "Q1", Text: "系列酒放量能否持续？", Status: "open"},
		},
	}
}

// writeMirror writes a contract C2 mirror to {vault}/{ticker}/_profile.json
// and returns the vault root.
func writeMirror(t *testing.T, ticker, body string) string {
	t.Helper()
	vault := t.TempDir()
	dir := filepath.Join(vault, ticker)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, profileMirrorFileName), []byte(body), 0o644))
	return vault
}

// mirrorJSON renders a minimal valid contract C2 document.
func mirrorJSON(t *testing.T, ticker string, generatedAt time.Time) string {
	t.Helper()
	body := map[string]interface{}{
		"schema_version": 1,
		"ticker":         ticker,
		"name":           "贵州茅台",
		"generated_at":   generatedAt.Format(time.RFC3339),
		"needs_review":   false,
		"conclusions": []map[string]interface{}{
			{
				"id":         "C1",
				"title":      "高端白酒需求韧性强",
				"body":       "...",
				"status":     "active",
				"citations":  []map[string]interface{}{{"content_hash": strings.Repeat("b", 64), "pointer": "/revenue"}},
				"as_of":      nil,
				"confidence": nil,
			},
		},
		"questions": []map[string]interface{}{
			{"id": "Q1", "text": "系列酒放量能否持续？", "status": "open", "raised_at": nil},
		},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return string(raw)
}

func strPtr(s string) *string { return &s }

// ═══════════════════════════════════════════════════════════════════════
//  Construction & metadata
// ═══════════════════════════════════════════════════════════════════════

func TestNewResearchProfileTool_PanicsOnNilClient(t *testing.T) {
	assert.Panics(t, func() { NewResearchProfileTool(nil, "") })
}

func TestResearchProfileTool_Metadata(t *testing.T) {
	tool := NewResearchProfileTool(&mockResearchProfileClient{}, "")

	assert.Equal(t, "research.profile", tool.Name())

	params := tool.Parameters()
	require.Len(t, params, 2)
	assert.Equal(t, "ticker", params[0].Name)
	assert.True(t, params[0].Required)
	assert.Equal(t, "sections", params[1].Name)
	assert.False(t, params[1].Required)

	schema := tool.OutputSchema()
	names := make([]string, 0, len(schema.Fields))
	for _, f := range schema.Fields {
		names = append(names, f.Name)
	}
	for _, want := range []string{"ticker", "stale", "stale_reason", "source", "conclusions", "questions"} {
		assert.Contains(t, names, want)
	}
}

// ══════════════════════════════════════════════════════════════════════
//  Argument validation
// ═══════════════════════════════════════════════════════════════════════

func TestResearchProfileTool_InvalidArgs(t *testing.T) {
	cases := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing ticker", map[string]interface{}{}},
		{"empty ticker", map[string]interface{}{"ticker": ""}},
		{"non-string ticker", map[string]interface{}{"ticker": 600519}},
		{"unrecognisable ticker", map[string]interface{}{"ticker": "AAPL"}},
		{"short ticker", map[string]interface{}{"ticker": "60051"}},
		{"unknown section", map[string]interface{}{"ticker": "600519", "sections": []string{"bogus"}}},
		{"non-string sections", map[string]interface{}{"ticker": "600519", "sections": 42}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &mockResearchProfileClient{}
			tool := NewResearchProfileTool(client, "")

			out, err := tool.Execute(context.Background(), tc.args)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tools.ErrInvalidArgs), "want ErrInvalidArgs, got %v", err)
			assert.Nil(t, out)
			assert.Zero(t, client.calls, "argument validation must fail before touching the store")
		})
	}
}

// TestResearchProfileTool_NormalizesTicker verifies the contract C2 ticker
// form: the archive is keyed by the bare 6-digit code, so suffixed and
// prefixed spellings must resolve to the same row.
func TestResearchProfileTool_NormalizesTicker(t *testing.T) {
	for _, raw := range []string{"600519", "600519.SH", "600519.SZ", "sh600519", " 600519 "} {
		t.Run(raw, func(t *testing.T) {
			client := &mockResearchProfileClient{profile: freshProfile("600519")}
			tool := NewResearchProfileTool(client, "")

			out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": raw})
			require.NoError(t, err)
			assert.Equal(t, "600519", client.gotTicker)

			res, ok := out.(*researchProfileResult)
			require.True(t, ok)
			assert.Equal(t, "600519", res.Ticker)
		})
	}
}

// ══════════════════════════════════════════════════════════════════════
//  Source resolution: PG projection first, vault mirror as fallback
// ══════════════════════════════════════════════════════════════════════

func TestResearchProfileTool_PrefersProjection(t *testing.T) {
	// A vault mirror exists AND the projection has a row: the projection wins.
	client := &mockResearchProfileClient{profile: freshProfile("600519")}
	vault := writeMirror(t, "600519", mirrorJSON(t, "600519", time.Now()))
	tool := NewResearchProfileTool(client, vault)

	out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
	require.NoError(t, err)

	res, ok := out.(*researchProfileResult)
	require.True(t, ok)
	assert.Equal(t, "postgres", res.Source)
	assert.Equal(t, 1, client.calls)
}

func TestResearchProfileTool_FallsBackToVault(t *testing.T) {
	// GetResearchProfile returns (nil, nil) — the ticker was never projected.
	client := &mockResearchProfileClient{profile: nil}
	vault := writeMirror(t, "600519", mirrorJSON(t, "600519", time.Now()))
	tool := NewResearchProfileTool(client, vault)

	out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519.SH"})
	require.NoError(t, err)

	res, ok := out.(*researchProfileResult)
	require.True(t, ok)
	assert.Equal(t, "vault", res.Source)
	assert.Equal(t, "600519", res.Ticker)
	require.Len(t, res.Conclusions, 1)
	assert.Equal(t, "C1", res.Conclusions[0].ID)
	require.Len(t, res.Questions, 1)
	assert.Equal(t, "Q1", res.Questions[0].ID)
}

// TestResearchProfileTool_NoProfile_NotFound covers the three ways a ticker
// can have no archive at all — all of them are "valid request, no record",
// which ToolsHandler maps to HTTP 404, never to a 5xx.
func TestResearchProfileTool_NoProfile_NotFound(t *testing.T) {
	t.Run("no vault configured", func(t *testing.T) {
		tool := NewResearchProfileTool(&mockResearchProfileClient{}, "")
		_, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, tools.ErrNotFound))
		assert.Contains(t, err.Error(), "no_profile")
	})

	t.Run("vault configured but ticker directory absent", func(t *testing.T) {
		tool := NewResearchProfileTool(&mockResearchProfileClient{}, t.TempDir())
		_, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, tools.ErrNotFound))
	})

	t.Run("vault has the directory but no mirror file", func(t *testing.T) {
		vault := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(vault, "600519"), 0o755))
		tool := NewResearchProfileTool(&mockResearchProfileClient{}, vault)
		_, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, tools.ErrNotFound))
	})
}

// TestResearchProfileTool_StoreError verifies a downstream failure is NOT
// reported as a 404 — the archive may well exist, we just could not read it.
func TestResearchProfileTool_StoreError(t *testing.T) {
	client := &mockResearchProfileClient{err: errors.New("connection refused")}
	tool := NewResearchProfileTool(client, "")

	_, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
	require.Error(t, err)
	assert.False(t, errors.Is(err, tools.ErrNotFound))
	assert.Contains(t, err.Error(), "connection refused")
}

// ═══════════════════════════════════════════════════════════════════════
//  Sections
// ═══════════════════════════════════════════════════════════════════════

func TestResearchProfileTool_Sections(t *testing.T) {
	t.Run("omitted means both", func(t *testing.T) {
		tool := NewResearchProfileTool(&mockResearchProfileClient{profile: freshProfile("600519")}, "")
		out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
		require.NoError(t, err)
		res := out.(*researchProfileResult)
		assert.NotNil(t, res.Conclusions)
		assert.NotNil(t, res.Questions)
	})

	t.Run("empty list means both", func(t *testing.T) {
		tool := NewResearchProfileTool(&mockResearchProfileClient{profile: freshProfile("600519")}, "")
		out, err := tool.Execute(context.Background(), map[string]interface{}{
			"ticker": "600519", "sections": []string{},
		})
		require.NoError(t, err)
		res := out.(*researchProfileResult)
		assert.NotNil(t, res.Conclusions)
		assert.NotNil(t, res.Questions)
	})

	t.Run("conclusions only", func(t *testing.T) {
		tool := NewResearchProfileTool(&mockResearchProfileClient{profile: freshProfile("600519")}, "")
		out, err := tool.Execute(context.Background(), map[string]interface{}{
			"ticker": "600519", "sections": []string{"conclusions"},
		})
		require.NoError(t, err)
		res := out.(*researchProfileResult)
		assert.NotNil(t, res.Conclusions)
		assert.Nil(t, res.Questions, "an unrequested section must be null, not an empty array")
	})

	t.Run("questions only", func(t *testing.T) {
		tool := NewResearchProfileTool(&mockResearchProfileClient{profile: freshProfile("600519")}, "")
		out, err := tool.Execute(context.Background(), map[string]interface{}{
			"ticker": "600519", "sections": []interface{}{"questions"},
		})
		require.NoError(t, err)
		res := out.(*researchProfileResult)
		assert.Nil(t, res.Conclusions)
		assert.NotNil(t, res.Questions)
	})
}

// TestResearchProfileTool_EmptyArchive verifies the distinction the output
// schema promises: null = not requested, [] = requested and there are none.
func TestResearchProfileTool_EmptyArchive(t *testing.T) {
	empty := freshProfile("600519")
	empty.Conclusions = nil
	empty.Questions = nil
	tool := NewResearchProfileTool(&mockResearchProfileClient{profile: empty}, "")

	out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
	require.NoError(t, err)
	res := out.(*researchProfileResult)
	assert.NotNil(t, res.Conclusions)
	assert.Empty(t, res.Conclusions)
	assert.NotNil(t, res.Questions)
	assert.Empty(t, res.Questions)
}

// ══════════════════════════════════════════════════════════════════════
//  Contract C2 frozen semantics: schema version + staleness
// ══════════════════════════════════════════════════════════════════════

// TestResearchProfileTool_RefusesUnknownSchemaVersion locks in the frozen C2
// rule: a consumer that only understands v1 must refuse a v2 archive rather
// than guess at an incompatible structure.
func TestResearchProfileTool_RefusesUnknownSchemaVersion(t *testing.T) {
	t.Run("projection", func(t *testing.T) {
		p := freshProfile("600519")
		p.SchemaVersion = 2
		tool := NewResearchProfileTool(&mockResearchProfileClient{profile: p}, "")

		_, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
		require.Error(t, err)
		assert.False(t, errors.Is(err, tools.ErrNotFound), "an unreadable archive is not a missing one")
		assert.Contains(t, err.Error(), "schema_version 2")
	})

	t.Run("vault mirror", func(t *testing.T) {
		vault := writeMirror(t, "600519", `{"schema_version":2,"ticker":"600519","generated_at":"2026-01-01T00:00:00Z"}`)
		tool := NewResearchProfileTool(&mockResearchProfileClient{}, vault)

		_, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "schema_version 2")
	})
}

// TestResearchProfileTool_Staleness covers both frozen C2 staleness tests:
// the source was edited after the mirror was generated, or the mirror is
// simply too old to trust.
func TestResearchProfileTool_Staleness(t *testing.T) {
	t.Run("projection: source_mtime newer than updated_at", func(t *testing.T) {
		p := freshProfile("600519")
		newer := p.UpdatedAt.Add(time.Hour)
		p.SourceMtime = &newer
		tool := NewResearchProfileTool(&mockResearchProfileClient{profile: p}, "")

		out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
		require.NoError(t, err)
		res := out.(*researchProfileResult)
		assert.True(t, res.Stale)
		assert.Contains(t, res.StaleReason, "newer than generated_at")
	})

	t.Run("projection: older than the freshness limit", func(t *testing.T) {
		p := freshProfile("600519")
		p.UpdatedAt = time.Now().Add(-DefaultProfileMaxAge - 24*time.Hour)
		tool := NewResearchProfileTool(&mockResearchProfileClient{profile: p}, "")

		out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
		require.NoError(t, err)
		res := out.(*researchProfileResult)
		assert.True(t, res.Stale)
		assert.Contains(t, res.StaleReason, "freshness limit")
	})

	t.Run("projection: fresh archive is not stale", func(t *testing.T) {
		tool := NewResearchProfileTool(&mockResearchProfileClient{profile: freshProfile("600519")}, "")
		out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
		require.NoError(t, err)
		res := out.(*researchProfileResult)
		assert.False(t, res.Stale)
		assert.Empty(t, res.StaleReason)
	})

	t.Run("vault: stale mirror is annotated", func(t *testing.T) {
		stale := time.Now().Add(-DefaultProfileMaxAge - 24*time.Hour)
		vault := writeMirror(t, "600519", mirrorJSON(t, "600519", stale))
		tool := NewResearchProfileTool(&mockResearchProfileClient{}, vault)

		out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
		require.NoError(t, err)
		res := out.(*researchProfileResult)
		assert.True(t, res.Stale)
	})
}

// ═══════════════════════════════════════════════════════════════════════
//  Vault mirror integrity
// ═══════════════════════════════════════════════════════════════════════

// TestResearchProfileTool_MirrorInWrongDirectory guards against a mis-synced
// vault: a file whose declared ticker disagrees with its directory would
// otherwise be served as the wrong company's research.
func TestResearchProfileTool_MirrorInWrongDirectory(t *testing.T) {
	vault := writeMirror(t, "600519", mirrorJSON(t, "000001", time.Now()))
	tool := NewResearchProfileTool(&mockResearchProfileClient{}, vault)

	_, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrong directory")
}

// TestResearchProfileTool_MalformedMirror verifies a broken mirror surfaces as
// an error rather than silently degrading to "no profile" — that would hide a
// failed sync behind a 404.
func TestResearchProfileTool_MalformedMirror(t *testing.T) {
	vault := writeMirror(t, "600519", "{not json")
	tool := NewResearchProfileTool(&mockResearchProfileClient{}, vault)

	_, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
	require.Error(t, err)
	assert.False(t, errors.Is(err, tools.ErrNotFound))
	assert.Contains(t, err.Error(), "decode vault mirror")
}

// TestResearchProfileTool_MirrorCitationsArray verifies the contract's
// citations key is always an array — a null would break consumers that
// iterate anchors.
func TestResearchProfileTool_MirrorCitationsArray(t *testing.T) {
	body := `{"schema_version":1,"ticker":"600519","name":"贵州茅台","generated_at":"` +
		time.Now().Format(time.RFC3339) + `","needs_review":false,"conclusions":[{"id":"C1","title":"t","body":"b","status":"active","citations":null}],"questions":[]}`
	vault := writeMirror(t, "600519", body)
	tool := NewResearchProfileTool(&mockResearchProfileClient{}, vault)

	out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
	require.NoError(t, err)
	res := out.(*researchProfileResult)
	require.Len(t, res.Conclusions, 1)
	assert.JSONEq(t, "[]", string(res.Conclusions[0].Citations))
}

// TestResearchProfileTool_PassesCitationsThrough verifies the archive is
// returned verbatim: the citation anchors (ADR-022 §5) reach the consumer
// byte-for-byte, unmodified by this layer.
func TestResearchProfileTool_PassesCitationsThrough(t *testing.T) {
	hash := strings.Repeat("c", 64)
	body := `{"schema_version":1,"ticker":"600519","name":"贵州茅台","generated_at":"` +
		time.Now().Format(time.RFC3339) + `","needs_review":true,"review_reason":"年报发布",` +
		`"conclusions":[{"id":"C1","title":"t","body":"b","status":"superseded","citations":[{"source":"akshare","dataset":"fundamentals.income","key":"600519/2024Q1","as_of":"2024-03-31","content_hash":"` + hash + `","pointer":"/revenue"}]}],"questions":[]}`
	vault := writeMirror(t, "600519", body)
	tool := NewResearchProfileTool(&mockResearchProfileClient{}, vault)

	out, err := tool.Execute(context.Background(), map[string]interface{}{"ticker": "600519"})
	require.NoError(t, err)
	res := out.(*researchProfileResult)
	require.Len(t, res.Conclusions, 1)

	var anchors []map[string]string
	require.NoError(t, json.Unmarshal(res.Conclusions[0].Citations, &anchors))
	require.Len(t, anchors, 1)
	assert.Equal(t, hash, anchors[0]["content_hash"])
	assert.Equal(t, "/revenue", anchors[0]["pointer"])
	assert.Equal(t, "superseded", res.Conclusions[0].Status)
	assert.True(t, res.NeedsReview)
	assert.Equal(t, "年报发布", res.ReviewReason)
}

// ═══════════════════════════════════════════════════════════════════════
//  Pure helpers
// ══════════════════════════════════════════════════════════════════════

func TestNormalizeTicker(t *testing.T) {
	ok := map[string]string{
		"600519":     "600519",
		"600519.SH":  "600519",
		"600519.sz":  "600519",
		"000001.BJ":  "000001",
		"sh600519":   "600519",
		"SZ000001":   "000001",
		"  600519  ": "600519",
	}
	for raw, want := range ok {
		got, err := normalizeTicker(raw)
		require.NoError(t, err, "input %q", raw)
		assert.Equal(t, want, got, "input %q", raw)
	}

	for _, bad := range []string{"", "60051", "6005190", "AAPL", "600519.HK", "sh60051"} {
		_, err := normalizeTicker(bad)
		require.Error(t, err, "input %q should be rejected", bad)
		assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	}
}

func TestProfileStaleness_MissingGeneratedAt(t *testing.T) {
	stale, reason := profileStaleness(time.Time{}, nil, time.Now())
	assert.True(t, stale)
	assert.Contains(t, reason, "generated_at is missing")
}
