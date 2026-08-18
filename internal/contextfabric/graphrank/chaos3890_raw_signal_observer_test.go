package graphrank

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// CHAOS-3890: SlogRawSignalObserver is the production wiring for the
// CHAOS-3858 RawSignalObserver port -- previously nil in every real
// deployment (RawSignalObserver.ObserveDeps' own doc comment), so "what
// similarity/margin actually decided this" never ran outside a
// measurement harness. These tests pin the three load-bearing properties
// runtime/hosted/open.go's default wiring depends on: it is silent above
// Debug (no separate config knob to remember to flip), it correlates back
// to the request via ctx, and it NEVER emits raw corpus text -- only the
// numeric/enum raw-signal fields RawSignalObserver's own doc comment
// scopes it to.

// testRequestID is a syntactically valid observability.RequestID
// ("req_" + 32 lowercase hex chars, parseRequestID's own format) used to
// prove ctx-based correlation actually reaches the log line.
const testRequestID = "req_0123456789abcdef0123456789abcdef"

func TestSlogRawSignalObserver_EmitsAtDebugWithNoRawText(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	observer := NewSlogRawSignalObserver(logger)

	sim := 0.87
	// Name/Attributes carry the kind of raw corpus text a RawSignalObserver
	// must never surface -- see RawSignalObserver's own doc comment (it may
	// read VectorSimilarity/LexicalMatchedTerms/LexicalTermCount, nothing
	// else). A leaking implementation would put this string in the log.
	node := CandidateNode{
		Mechanism:        contextfabric.MatchVector,
		VectorSimilarity: &sim,
		Name:             "Definitely Secret Project Codename Zeta",
		Attributes:       map[string]interface{}{"label": "Definitely Secret Project Codename Zeta"},
	}
	ctx := observability.WithRequestID(context.Background(), testRequestID)
	key := SubjectKey(contextfabric.SubjectRef{Kind: "project", CanonicalID: "p1"})

	observer.ObserveCandidate(ctx, key, node)

	out := buf.String()
	if out == "" {
		t.Fatal("ObserveCandidate() produced no log output at Debug level, want one line")
	}
	if strings.Contains(out, "Secret") || strings.Contains(out, "Zeta") {
		t.Fatalf("log line leaked raw candidate text: %q", out)
	}
	if !strings.Contains(out, "vector_similarity=0.87") {
		t.Fatalf("log line missing vector_similarity=0.87: %q", out)
	}
	if !strings.Contains(out, testRequestID) {
		t.Fatalf("log line missing request_id correlation %q: %q", testRequestID, out)
	}
	if !strings.Contains(out, "subject_key=") || !strings.Contains(out, "project") {
		t.Fatalf("log line missing a subject_key referencing %q: %q", key, out)
	}
}

// TestSlogRawSignalObserver_LexicalFieldsOnlyNumeric is the lexical-arm
// companion: only the matched/count pair and mechanism appear, never any
// text the lexical arm matched against.
func TestSlogRawSignalObserver_LexicalFieldsOnlyNumeric(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	observer := NewSlogRawSignalObserver(logger)

	matched, count := 2, 4
	node := CandidateNode{
		Mechanism:           contextfabric.MatchLexical,
		LexicalMatchedTerms: &matched,
		LexicalTermCount:    &count,
	}
	observer.ObserveCandidate(context.Background(), "project\x00p2", node)

	out := buf.String()
	if !strings.Contains(out, "lexical_matched_terms=2") || !strings.Contains(out, "lexical_term_count=4") {
		t.Fatalf("log line missing raw lexical pair (2,4): %q", out)
	}
	if strings.Contains(out, "vector_similarity") {
		t.Fatalf("log line must not report vector_similarity for a lexical-mechanism candidate: %q", out)
	}
}

// TestSlogRawSignalObserver_SilentAboveDebugLevel is the gating-level pin:
// runtime/hosted/open.go wires this UNCONDITIONALLY (no separate config
// knob) on the premise that it is silent at any level an operator normally
// runs and disclosed only when they raise it. A handler configured above
// Debug (the deployment default) must therefore see nothing.
func TestSlogRawSignalObserver_SilentAboveDebugLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	observer := NewSlogRawSignalObserver(logger)

	sim := 0.5
	observer.ObserveCandidate(context.Background(), "project\x00p3", CandidateNode{VectorSimilarity: &sim})

	if buf.Len() != 0 {
		t.Fatalf("expected no output above Debug level, got %q", buf.String())
	}
}

// TestSlogRawSignalObserver_NilLoggerFallsBackToDefault mirrors
// NewSlogResolutionTracer's identical nil-logger convention.
func TestSlogRawSignalObserver_NilLoggerFallsBackToDefault(t *testing.T) {
	observer := NewSlogRawSignalObserver(nil)
	// Must not panic -- the only thing this test can assert without
	// capturing slog.Default()'s own output.
	observer.ObserveCandidate(context.Background(), "k", CandidateNode{})
}
