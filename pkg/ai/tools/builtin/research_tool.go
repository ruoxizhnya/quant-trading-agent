package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
)

// ═══════════════════════════════════════════════════════════════════════
//  ResearchProfileTool (EQD-P2-1, bridge B2)
// ══════════════════════════════════════════════════════════════════════
//
// ResearchProfileTool gives Hermes read access to an EquityDeep research
// archive: the conclusions (with their citation anchors) and the open
// questions recorded for one ticker. It is the read side of the flywheel —
// B1 (pkg/data/factor_equitydeep.go) turns deep financials into factors,
// this tool turns "which company did we learn this from, and what did we
// still not know?" into a queryable call.
//
// Source resolution (PG projection first, vault mirror as fallback):
//  1. research.* projection in PostgreSQL (ADR-022 §2 class E — the
//     authoritative structured store once EquityDeep syncs into it);
//  2. the contract C2 mirror `{EQUITYDEEP_VAULT_PATH}/{ticker}/_profile.json`
//     when the projection has no row for the ticker.
//
// The fallback exists only because the EquityDeep side of the projection
// (TASKS.md P2-1) is not landed yet; once it is, the fallback branch can be
// deleted without touching the response shape.
//
// The archive is returned verbatim — this tool performs NO LLM-side
// rewriting and invents no claim. Every conclusion carries its citation
// anchors to ingest.raw (ADR-022 §5), so a downstream consumer traces a
// number back to the exact archived response it came from.
//
// Tool name: "research.profile"
// Input: ticker (required, 600519 or 600519.SH), sections (optional)
// Output: researchProfileResult — profile header + stale verdict + children

const (
	// ResearchProfileToolName is the registered tool name. It follows the
	// dot-namespace convention (as factor.compute / data.ohlcv do) and is
	// HTTP-safe: POST /api/tools/research.profile.
	ResearchProfileToolName = "research.profile"

	// DefaultProfileMaxAge is how old a generated archive may be before the
	// tool flags it stale. EquityDeep's research cadence is quarterly (the
	// US-2 story re-researches a company ~every three months), so an
	// archive older than 90 days is due for a refresh even if nobody
	// edited the markdown.
	DefaultProfileMaxAge = 90 * 24 * time.Hour

	// profileMirrorFileName is the contract C2 mirror inside a ticker
	// directory. Only this JSON is parsed — never the markdown: the md is
	// hand-edited in Obsidian, and parsing it in Go is brittle by design.
	profileMirrorFileName = "_profile.json"

	// sectionConclusions / sectionQuestions are the accepted values of the
	// "sections" argument.
	sectionConclusions = "conclusions"
	sectionQuestions   = "questions"
)

// ResearchProfileClient is the narrow read contract satisfied by
// *storage.PostgresStore. Extracted so the tool can be unit-tested with a
// mock instead of a live PostgreSQL pool.
type ResearchProfileClient interface {
	// GetResearchProfile returns the archived profile of a bare 6-digit
	// ticker plus its conclusions and questions, or (nil, nil) when the
	// ticker was never projected.
	GetResearchProfile(ctx context.Context, ticker string) (*storage.ResearchProfile, error)
}

// Compile-time assertion that the concrete store satisfies the narrow
// interface. A failure here is a wiring bug — failing at build time beats
// failing inside setup.go at startup.
var _ ResearchProfileClient = (*storage.PostgresStore)(nil)

// bareTickerPattern matches the contract C2 ticker form (contracts/profile.schema.json).
var bareTickerPattern = regexp.MustCompile(`^[0-9]{6}$`)

// prefixedTickerPattern matches the exchange-prefixed A-share form (sh600519).
var prefixedTickerPattern = regexp.MustCompile(`^(?:SH|SZ|BJ)[0-9]{6}$`)

// exchangeSuffixes is the closed set of exchange suffixes the A-share market
// uses (contract C2's ts_code form).
var exchangeSuffixes = map[string]bool{"SH": true, "SZ": true, "BJ": true}

// profileConclusion is one conclusion of the archive, shaped per contract C2
// (id/title/body/as_of/confidence/status/citations). It is both the decode
// target for the vault mirror and the value returned to the caller — the
// archive is passed through, not re-modelled.
type profileConclusion struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	AsOf       *string         `json:"as_of"`
	Confidence *string         `json:"confidence"`
	Status     string          `json:"status"`
	Citations  json.RawMessage `json:"citations"`
}

// profileQuestion is one question of the archive, shaped per contract C2.
type profileQuestion struct {
	ID       string  `json:"id"`
	Text     string  `json:"text"`
	Status   string  `json:"status"`
	RaisedAt *string `json:"raised_at"`
}

// profileDocument is the contract C2 mirror as written by EquityDeep. Only the
// fields the tool needs are decoded; additionalProperties is validated on the
// producer side, not here.
type profileDocument struct {
	SchemaVersion  int                 `json:"schema_version"`
	Ticker         string              `json:"ticker"`
	Name           string              `json:"name"`
	GeneratedAt    time.Time           `json:"generated_at"`
	SourceMtime    *time.Time          `json:"source_mtime"`
	LastResearched *string             `json:"last_researched"`
	NeedsReview    bool                `json:"needs_review"`
	ReviewReason   string              `json:"review_reason"`
	Conclusions    []profileConclusion `json:"conclusions"`
	Questions      []profileQuestion   `json:"questions"`
}

// researchProfileResult is the return value. It is deliberately flat and
// small: profile header, the staleness verdict (which every consumer MUST
// act on — C2 frozen semantics), and the requested child sections.
//
// A nil Conclusions / Questions means "that section was not requested"
// (vs. an empty array = "requested, and the archive has none").
type researchProfileResult struct {
	Ticker         string              `json:"ticker"`
	Name           string              `json:"name"`
	SchemaVersion  int                 `json:"schema_version"`
	LastResearched *string             `json:"last_researched"`
	NeedsReview    bool                `json:"needs_review"`
	ReviewReason   string              `json:"review_reason,omitempty"`
	Stale          bool                `json:"stale"`
	StaleReason    string              `json:"stale_reason,omitempty"`
	GeneratedAt    string              `json:"generated_at"`
	Source         string              `json:"source"`
	Conclusions    []profileConclusion `json:"conclusions"`
	Questions      []profileQuestion   `json:"questions"`
}

// ResearchProfileTool reads EquityDeep research archives. See the package
// comment above for the source-resolution and no-rewriting design.
type ResearchProfileTool struct {
	client ResearchProfileClient
	// vaultPath is the root of the Obsidian vault (ticker directories live
	// directly under it). Empty disables the mirror fallback — the tool
	// then answers only from the PG projection.
	vaultPath string
}

var _ tools.Tool = (*ResearchProfileTool)(nil)

// NewResearchProfileTool constructs a ResearchProfileTool backed by the
// research.* projection, with vaultPath as the contract C2 mirror fallback.
// Panics on a nil client — fail loud at the composition root, not at the
// first Execute call.
func NewResearchProfileTool(client ResearchProfileClient, vaultPath string) *ResearchProfileTool {
	if client == nil {
		panic("builtin: NewResearchProfileTool called with nil ResearchProfileClient")
	}
	return &ResearchProfileTool{client: client, vaultPath: vaultPath}
}

func (t *ResearchProfileTool) Name() string { return ResearchProfileToolName }

func (t *ResearchProfileTool) Description() string {
	return "Read the EquityDeep research archive for a ticker: its recorded conclusions " +
		"(each carrying the ingest.raw citation anchors that prove the numbers) and its open questions. " +
		"Use this before designing fundamental/quality factors to learn what is already known about a company " +
		"and what is still unverified. Returns the archive verbatim plus a 'stale' verdict — when stale is true " +
		"or needs_review is true, the conclusions must be re-verified against fresh data before use. " +
		"Accepts '600519' or '600519.SH'. Returns 404 (no_profile) when the ticker was never researched."
}

func (t *ResearchProfileTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "ticker",
			Type:        "string",
			Description: "Stock code, either the bare 6-digit form ('600519') or with an exchange suffix ('600519.SH').",
			Required:    true,
		},
		{
			Name:        "sections",
			Type:        "[]string",
			Description: "Which sections to include: any of 'conclusions', 'questions'. Omitted or empty = both.",
			Required:    false,
		},
	}
}

func (t *ResearchProfileTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "One EquityDeep research archive: profile header, staleness verdict, and the requested sections.",
		Fields: []tools.OutputField{
			{Name: "ticker", Type: "string", Description: "Bare 6-digit ticker code."},
			{Name: "name", Type: "string", Description: "Company short name from the archive front-matter."},
			{Name: "schema_version", Type: "int", Description: "Contract C2 version of the archive (this consumer understands 1 only)."},
			{Name: "last_researched", Type: "string", Description: "Date of the last research run (YYYY-MM-DD), or null if never recorded."},
			{Name: "needs_review", Type: "bool", Description: "True when the archive was invalidated by a report/announcement — the conclusions must be re-verified."},
			{Name: "review_reason", Type: "string", Description: "Why the archive needs review; empty when needs_review is false."},
			{Name: "stale", Type: "bool", Description: "True when the mirror is older than its source, or older than the max age. Consumers must degrade (annotate) when true."},
			{Name: "stale_reason", Type: "string", Description: "Why the archive is considered stale; empty when stale is false."},
			{Name: "generated_at", Type: "string", Description: "When the archive was generated (RFC 3339)."},
			{Name: "source", Type: "string", Description: "Where the archive was read from: 'postgres' (research.* projection) or 'vault' (contract C2 mirror)."},
			{Name: "conclusions", Type: "array", Description: "Recorded conclusions with citation anchors; null when not requested, [] when the archive has none."},
			{Name: "questions", Type: "array", Description: "Recorded questions (open/closed); null when not requested, [] when the archive has none."},
		},
	}
}

// Execute reads the archive for one ticker. Errors are returned when:
//   - ticker is missing/empty/not a string (wrapped ErrInvalidArgs)
//   - ticker is not a recognisable A-share code (wrapped ErrInvalidArgs)
//   - sections contains an unknown section name (wrapped ErrInvalidArgs)
//   - both sources are unavailable (downstream error, wrapped)
//   - the ticker has no archive in either source (wrapped ErrNotFound)
//   - the archive declares a schema_version this consumer cannot read
//   - the vault mirror exists but is unreadable or malformed
func (t *ResearchProfileTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	rawTicker, err := requireString(args, "ticker")
	if err != nil {
		return nil, err
	}
	sections, err := optionalStringSlice(args, "sections")
	if err != nil {
		return nil, err
	}
	wantConclusions, wantQuestions, err := parseSections(sections)
	if err != nil {
		return nil, err
	}

	ticker, err := normalizeTicker(rawTicker)
	if err != nil {
		return nil, err
	}

	profile, err := t.client.GetResearchProfile(ctx, ticker)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ResearchProfileToolName, err)
	}
	if profile != nil {
		return t.fromProjection(ticker, profile, wantConclusions, wantQuestions)
	}

	// The projection has no row for this ticker — fall back to the C2 mirror.
	doc, err := t.loadMirror(ticker)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("%w: %s: no_profile (ticker %s has no research archive in research.profile or the vault mirror)",
			tools.ErrNotFound, ResearchProfileToolName, ticker)
	}
	return t.fromMirror(ticker, doc, wantConclusions, wantQuestions)
}

// fromProjection shapes a research.* row set into the tool result.
func (t *ResearchProfileTool) fromProjection(
	ticker string,
	profile *storage.ResearchProfile,
	wantConclusions, wantQuestions bool,
) (interface{}, error) {
	if err := checkSchemaVersion(profile.SchemaVersion, ticker); err != nil {
		return nil, err
	}

	stale, reason := profileStaleness(profile.UpdatedAt, profile.SourceMtime, time.Now())

	result := &researchProfileResult{
		Ticker:         ticker,
		Name:           profile.Name,
		SchemaVersion:  profile.SchemaVersion,
		LastResearched: formatNullableDate(profile.LastResearched),
		NeedsReview:    profile.NeedsReview,
		Stale:          stale,
		StaleReason:    reason,
		GeneratedAt:    formatTimestamp(profile.UpdatedAt),
		Source:         "postgres",
	}
	if profile.ReviewReason != nil {
		result.ReviewReason = *profile.ReviewReason
	}
	if wantConclusions {
		result.Conclusions = conclusionsFromProjection(profile.Conclusions)
	}
	if wantQuestions {
		result.Questions = questionsFromProjection(profile.Questions)
	}
	return result, nil
}

// fromMirror shapes a decoded contract C2 document into the tool result.
func (t *ResearchProfileTool) fromMirror(
	ticker string,
	doc *profileDocument,
	wantConclusions, wantQuestions bool,
) (interface{}, error) {
	if err := checkSchemaVersion(doc.SchemaVersion, ticker); err != nil {
		return nil, err
	}
	if doc.Ticker != "" {
		docTicker, err := normalizeTicker(doc.Ticker)
		if err != nil || docTicker != ticker {
			return nil, fmt.Errorf("%s: vault mirror for %s declares ticker %q — the file is in the wrong directory",
				ResearchProfileToolName, ticker, doc.Ticker)
		}
	}

	stale, reason := profileStaleness(doc.GeneratedAt, doc.SourceMtime, time.Now())

	result := &researchProfileResult{
		Ticker:         ticker,
		Name:           doc.Name,
		SchemaVersion:  doc.SchemaVersion,
		LastResearched: doc.LastResearched,
		NeedsReview:    doc.NeedsReview,
		ReviewReason:   doc.ReviewReason,
		Stale:          stale,
		StaleReason:    reason,
		GeneratedAt:    formatTimestamp(doc.GeneratedAt),
		Source:         "vault",
	}
	if wantConclusions {
		result.Conclusions = conclusionsFromMirror(doc.Conclusions)
	}
	if wantQuestions {
		result.Questions = doc.Questions
		if result.Questions == nil {
			result.Questions = []profileQuestion{}
		}
	}
	return result, nil
}

// loadMirror reads and decodes the contract C2 mirror of ticker. It returns
// (nil, nil) when the mirror does not exist (or no vault path is configured) —
// "not mirrored" is not an error, it is the normal state of an un-researched
// ticker. An existing-but-unreadable or malformed mirror IS an error: silently
// treating it as absent would hide a broken sync.
func (t *ResearchProfileTool) loadMirror(ticker string) (*profileDocument, error) {
	if t.vaultPath == "" {
		return nil, nil
	}

	// ticker is a validated 6-digit code by now, so this join cannot escape
	// the vault root.
	path := filepath.Join(t.vaultPath, ticker, profileMirrorFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: read vault mirror %s: %w", ResearchProfileToolName, path, err)
	}

	var doc profileDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: decode vault mirror %s: %w", ResearchProfileToolName, path, err)
	}
	return &doc, nil
}

// ─── Internal helpers ─────────────────────────────────────────────────

// normalizeTicker reduces a caller-supplied code to the bare 6-digit form the
// contract C2 (and research.profile.ticker) uses. It accepts '600519',
// '600519.SH'/'600519.SZ'/'600519.BJ', and 'sh600519'. Anything else is a bad
// argument rather than a missing profile — the caller mistyped, not the archive.
// The suffix set is closed on purpose: silently dropping a '.HK' would serve
// an A-share archive for a Hong Kong listing.
func normalizeTicker(raw string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(raw))

	if i := strings.Index(code, "."); i >= 0 {
		suffix := code[i+1:]
		if !exchangeSuffixes[suffix] {
			return "", fmt.Errorf("%w: ticker %q has an unknown exchange suffix %q (expected .SH, .SZ or .BJ)",
				tools.ErrInvalidArgs, raw, suffix)
		}
		code = code[:i]
	}
	if prefixedTickerPattern.MatchString(code) {
		code = code[2:]
	}
	if !bareTickerPattern.MatchString(code) {
		return "", fmt.Errorf("%w: ticker %q is not a recognisable A-share code (expected e.g. 600519 or 600519.SH)",
			tools.ErrInvalidArgs, raw)
	}
	return code, nil
}

// parseSections validates the requested sections and reports which ones were
// asked for. An omitted or empty list means "both".
func parseSections(sections []string) (conclusions, questions bool, err error) {
	if len(sections) == 0 {
		return true, true, nil
	}
	for _, s := range sections {
		switch s {
		case sectionConclusions:
			conclusions = true
		case sectionQuestions:
			questions = true
		default:
			return false, false, fmt.Errorf("%w: unknown section %q (expected %q or %q)",
				tools.ErrInvalidArgs, s, sectionConclusions, sectionQuestions)
		}
	}
	return conclusions, questions, nil
}

// checkSchemaVersion enforces the frozen contract C2 rule: this consumer
// understands version 1 only, and must refuse (not guess at) a newer structure.
func checkSchemaVersion(version int, ticker string) error {
	if version != 1 {
		return fmt.Errorf("%s: ticker %s declares schema_version %d, this consumer reads version 1 only — refusing to serve a structure it cannot interpret",
			ResearchProfileToolName, ticker, version)
	}
	return nil
}

// profileStaleness applies the two frozen staleness tests: the mirror is
// stale when its source (the markdown) was modified after it was generated,
// or when it is simply too old to trust.
func profileStaleness(generatedAt time.Time, sourceMtime *time.Time, now time.Time) (bool, string) {
	if generatedAt.IsZero() {
		return true, "generated_at is missing from the archive"
	}
	if sourceMtime != nil && sourceMtime.After(generatedAt) {
		return true, fmt.Sprintf("source_mtime (%s) is newer than generated_at (%s) — the markdown was edited after the mirror was generated",
			formatTimestamp(*sourceMtime), formatTimestamp(generatedAt))
	}
	if age := now.Sub(generatedAt); age > DefaultProfileMaxAge {
		return true, fmt.Sprintf("generated_at (%s) is %.0f days old, older than the %d-day freshness limit",
			formatTimestamp(generatedAt), age.Hours()/24, int(DefaultProfileMaxAge.Hours()/24))
	}
	return false, ""
}

// conclusionsFromProjection maps research.conclusion rows onto the contract C2
// output shape, leaving the citations JSONB untouched.
func conclusionsFromProjection(rows []storage.ResearchConclusion) []profileConclusion {
	out := make([]profileConclusion, 0, len(rows))
	for _, r := range rows {
		out = append(out, profileConclusion{
			ID:         r.ID,
			Title:      r.Title,
			Body:       derefString(r.Body),
			AsOf:       r.AsOf,
			Confidence: r.Confidence,
			Status:     r.Status,
			Citations:  normalizeCitations(r.Citations),
		})
	}
	return out
}

// questionsFromProjection maps research.question rows onto the output shape.
func questionsFromProjection(rows []storage.ResearchQuestion) []profileQuestion {
	out := make([]profileQuestion, 0, len(rows))
	for _, r := range rows {
		out = append(out, profileQuestion{
			ID:       r.ID,
			Text:     r.Text,
			Status:   r.Status,
			RaisedAt: r.RaisedAt,
		})
	}
	return out
}

// conclusionsFromMirror passes the decoded mirror conclusions through, only
// normalising an absent citations array to [] so consumers always see an array.
func conclusionsFromMirror(rows []profileConclusion) []profileConclusion {
	out := make([]profileConclusion, 0, len(rows))
	for _, r := range rows {
		r.Citations = normalizeCitations(r.Citations)
		out = append(out, r)
	}
	return out
}

// normalizeCitations turns an absent or null citations value into an empty JSON
// array — the contract requires the key to be an array, and null is not one.
// (json.RawMessage keeps the literal `null` rather than becoming nil, so the
// length check alone is not enough.)
func normalizeCitations(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("[]")
	}
	return raw
}

// derefString returns the pointed-to string, or "" when nil.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// formatNullableDate renders a nullable date column as YYYY-MM-DD.
func formatNullableDate(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.Format("2006-01-02")
	return &formatted
}

// formatTimestamp renders a timestamp as RFC 3339 (contract C2's date-time
// form), mapping the zero value to "" so callers never see year 1.
func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
