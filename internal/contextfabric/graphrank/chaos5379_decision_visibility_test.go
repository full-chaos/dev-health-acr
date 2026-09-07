package graphrank

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// infoCapture builds the sink under proof the way a deployment does: the
// real production tracer over a real slog handler at the production
// default level (slog.LevelInfo, internal/sidecar/config.go's
// defaultLogLevel). A Debug-level handler here would hide the exact defect
// these tests exist to catch.
func infoCapture() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	return &buf, slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// decisionSummaryLines returns every folded decision-summary line the
// capture holds, decoded. Lines are matched on the record's own stage
// FIELD by EXACT equality -- a substring test over the log text would
// match the per-subject "decision" stage too, and a message-text match
// would pin prose rather than the contract.
func decisionSummaryLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("captured line is not JSON: %v -- line: %s", err, line)
		}
		if stage, _ := rec["stage"].(string); stage != "decision_summary" {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func numField(t *testing.T, rec map[string]any, key string) float64 {
	t.Helper()
	value, ok := rec[key]
	if !ok {
		t.Fatalf("the decision summary carries no %q field at all -- an absent count and a measured zero must never read alike; line: %v", key, rec)
	}
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("%q = %v (%T), want a number", key, value, value)
	}
	return number
}

// TestDecisionReachesTheProductionLogLevel is the emission-path proof for
// the one stage that names the decision a resolution took. Every other
// stage answers what the resolver LOOKED AT; this one answers what it
// DECIDED -- the outcome, which gate admitted it, which basis it committed
// on, which population it decided over. Without it at Info, an operator
// bisecting a wrong or missing answer on the rig can see the retrieval and
// the folds and still cannot say what the resolver concluded.
//
// The per-subject decision line stays Debug (it fires once per committed
// subject, so a large commit set is unbounded on one resolution); what must
// reach Info is the fold.
//
// Fixture: the exact-label-on-a-truncated-search shape
// (exact_truncation_test.go's own), which commits ONE subject through the
// ordinary commit gate -- a real decision taken by the production entry
// point, not a synthesized event.
func TestDecisionReachesTheProductionLogLevel(t *testing.T) {
	t.Parallel()
	const term = "Ask Dev"
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{term: exactMatchSearchResults(term, 11)},
		searchTruncated: true,
	}
	buf, logger := infoCapture()
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(logger)

	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term),
		deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("the fixture committed %d subjects, want 1 -- this arm asserts a COMMITTED decision reaches Info, so a non-committing fixture would make it vacuous", len(resolution.Committed))
	}
	if buf.Len() == 0 {
		t.Fatal("captured log is EMPTY -- the tracer produced no output at all, which would make every assertion below vacuous")
	}

	summaries := decisionSummaryLines(t, buf)
	if len(summaries) != 1 {
		t.Fatalf("decision summary lines at Info = %d, want exactly 1 per resolution -- log: %s", len(summaries), buf.String())
	}
	summary := summaries[0]
	if got := numField(t, summary, "committed_count"); int(got) != len(resolution.Committed) {
		t.Errorf("committed_count = %v, want %d (the resolution's own committed set)", got, len(resolution.Committed))
	}
	// Explicit zeros: every count is present on every pass, so a reader
	// never has to decide whether a missing field means zero or means the
	// resolver never got there.
	for _, key := range []string{"decision_event_count", "committed_count", "ambiguous_count", "no_commit_count"} {
		numField(t, summary, key)
	}
	if got := numField(t, summary, "ambiguous_count"); got != 0 {
		t.Errorf("ambiguous_count = %v, want 0 on a committing resolution", got)
	}
	// The decision is only IDENTIFIED if the gate and basis that took it
	// travel with it -- an outcome alone cannot tell a caller-hint commit
	// from a statistical one.
	for _, key := range []string{"commit_gates", "commit_bases"} {
		if _, ok := summary[key]; !ok {
			t.Errorf("the decision summary carries no %q -- the outcome alone does not identify which decision was taken", key)
		}
	}
}

// TestAnAmbiguousDecisionIsDistinguishableAtTheProductionLogLevel keeps the
// three outcomes apart at Info. "Committed nothing" is not one state: a
// resolver that found two equally exact subjects and refused to choose has
// taken a DIFFERENT decision from one that found nothing at all, and the
// operator response differs (disambiguate the question vs. fix the pool).
// Folding them into one zero would answer the question the rig cannot
// afford to have answered wrong.
func TestAnAmbiguousDecisionIsDistinguishableAtTheProductionLogLevel(t *testing.T) {
	t.Parallel()
	const term = "Ask Dev"
	nodes := exactMatchSearchResults(term, 11)
	nodes[0] = candidateNode(contextfabric.SubjectProject, "project_duplicate_label", term, 0.35, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{term: nodes},
		searchTruncated: true,
	}
	buf, logger := infoCapture()
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(logger)

	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term),
		deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(resolution.Committed) != 0 || resolution.ClarificationPrompt == "" {
		t.Fatalf("the fixture is not the ambiguous shape (committed=%d, prompt=%q) -- this arm would assert nothing", len(resolution.Committed), resolution.ClarificationPrompt)
	}

	summaries := decisionSummaryLines(t, buf)
	if len(summaries) != 1 {
		t.Fatalf("decision summary lines at Info = %d, want exactly 1 -- log: %s", len(summaries), buf.String())
	}
	summary := summaries[0]
	if got := numField(t, summary, "ambiguous_count"); got < 1 {
		t.Errorf("ambiguous_count = %v, want at least 1 -- the outcome the resolver actually took", got)
	}
	if got := numField(t, summary, "committed_count"); got != 0 {
		t.Errorf("committed_count = %v, want an explicit 0", got)
	}
	if got := numField(t, summary, "no_commit_count"); got != 0 {
		t.Errorf("no_commit_count = %v, want 0 -- an ambiguous refusal must not read as an empty pool", got)
	}
}

// TestADecisionThatCommitsNothingStillReachesTheProductionLogLevel is the
// other half of the same gate, and the half the rig actually needs: a
// resolution that commits nothing must emit the SAME line with an explicit
// zero, so a measured "the resolver decided not to commit" can never read
// the same as "the resolver never reached the decision at all". Absence of
// this line has exactly one meaning: the site was never reached.
//
// Fixture: a backend with no search results at all -- the empty-pool
// no_commit path (resolution.go's own early return), which is the shape a
// named-subject question whose candidate pool holds none of the asked-for
// kind takes on the rig.
func TestADecisionThatCommitsNothingStillReachesTheProductionLogLevel(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{}
	buf, logger := infoCapture()
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(logger)

	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("platform"),
		deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("the fixture committed %d subjects -- this arm asserts the MEASURED-ZERO shape, so a committing fixture would prove the wrong thing", len(resolution.Committed))
	}

	summaries := decisionSummaryLines(t, buf)
	if len(summaries) != 1 {
		t.Fatalf("decision summary lines at Info = %d, want exactly 1 -- a resolution that commits nothing must still say so; log: %s", len(summaries), buf.String())
	}
	summary := summaries[0]
	if got := numField(t, summary, "committed_count"); got != 0 {
		t.Errorf("committed_count = %v, want an explicit 0", got)
	}
	if got := numField(t, summary, "decision_event_count"); got < 1 {
		t.Errorf("decision_event_count = %v, want at least 1 -- the resolver DID take a decision (it declined to commit); a zero here would say the decision site was never reached", got)
	}
	if got := numField(t, summary, "no_commit_count"); got < 1 {
		t.Errorf("no_commit_count = %v, want at least 1 -- the outcome the resolver actually took", got)
	}
}
