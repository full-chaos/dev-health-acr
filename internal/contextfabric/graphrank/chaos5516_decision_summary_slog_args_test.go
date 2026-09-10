package graphrank

// CHAOS-5516, chris's binding condition on the typed-construction pilot
// (2026-09-10 21:1xZ): "a pin proves tracer.go's decision_summary emission
// carries exactly the generated key set (certify sweep against the real
// emitted line) so a hand-added key in tracer.go or a field added to spec.go
// without regeneration fails."
//
// tracer.go's "decision_summary" case is now one line --
// event.DecisionSummaryFields.SlogArgs()... -- so the two ways this could
// still drift are: (a) a key hand-added or hand-removed IN TRACER.GO beside
// that spread (e.g. an extra "debug_hint" key/value pair before or after the
// generated ones), which TestGeneratedArtifactsRegenerateByteIdentically
// cannot see (it only proves zz_generated.go matches spec.go, never that
// tracer.go's own call site limits itself to what SlogArgs() returns); (b) a
// field added to spec.go's DecisionSummary without regenerating
// zz_generated.go, which the OTHER pin already covers -- included here too
// so this test states its own completeness rather than assuming a sibling
// file's coverage.
//
// This drives the REAL production entry point (the same pilot pattern
// chaos5515_eventspec_certify_test.go uses), parses the REAL emitted JSON
// line, and asserts its key set is EXACTLY eventspec.FieldKeys(DecisionSummary)
// -- no more (a hand-added key), no fewer (a hand-removed key or an
// unregenerated field).

import (
	"bytes"
	"context"
	"log/slog"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestDecisionSummaryEmissionCarriesExactlyTheGeneratedKeySet(t *testing.T) {
	t.Parallel()
	backend := anchorOnlyByKindBackend("chaos", saturatedCrowd)
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20

	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("chaos"), deps,
		nil, nil, scopedProjectsFrame("chaos"), contextfabric.SubjectTeam); err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production slog output error = %v", err)
	}
	lines := log.LinesWithMsg(eventspec.DecisionSummary.Msg)
	if len(lines) != 1 {
		t.Fatalf("captured %d decision_summary lines, want exactly 1", len(lines))
	}
	line := lines[0]

	// The envelope keys (time/level/msg) are slog's own, not this event's
	// declared fields -- excluded from the comparison the same way
	// certify.Parse's own envelope requirement is separate from an event's
	// Fields.
	gotKeys := make([]string, 0, len(line))
	for k := range line {
		if k == "time" || k == "level" || k == "msg" {
			continue
		}
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)

	wantKeys := append([]string{}, eventspec.FieldKeys(eventspec.DecisionSummary)...)
	sort.Strings(wantKeys)

	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("real emitted decision_summary line carries %d keys %v, want exactly the %d generated keys %v -- a hand-added or hand-removed key in tracer.go's case, or an unregenerated spec.go field, would show up here",
			len(gotKeys), gotKeys, len(wantKeys), wantKeys)
	}
	for i := range gotKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("real emitted decision_summary line's key set %v does not match the generated key set %v", gotKeys, wantKeys)
		}
	}
}
