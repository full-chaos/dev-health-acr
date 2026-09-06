package graphrank

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestSlogResolutionTracer_StageLinesVisibleAtProductionLogLevel is the
// rig-visibility fix's acceptance test. The tracer is invisible on the kiac rig
// today, for a reason that has nothing to do with wiring: the tracer IS
// constructed and wired on the real rig path
// (internal/runtime/hosted/open.go: graphConfig.ResolutionTracer =
// graphrank.NewSlogResolutionTracer(...)) -- it just logs the
// ranked_cut/reserved_kind_admitted/kind_offer stages at DebugContext,
// while production defaults to slog.LevelInfo
// (internal/sidecar/config.go's defaultLogLevel). Any zero for one of
// these tokens on the rig is therefore absence of INSTRUMENT, not
// evidence the stage never fired.
//
// This drives the SAME construction path the rig uses -- the real
// NewSlogResolutionTracer (not a hand-rolled capture fake) wired into the
// real ResolveSubjectsWithCommitBasis entry point via deps.ResolutionTracer,
// exactly like open.go's own GraphConfig wiring -- at the REAL production
// default log level (slog.LevelInfo), not a Debug-level handler that would
// hide the exact defect this test exists to catch.
//
// The fixture reuses TestResolveSubjects_AnchorKindSurvivesFlatTruncation's
// own shape (subject_anchor_kind_test.go): a 90-candidate lexical crowd
// under a MaxSubjectCandidates=20 budget forces a ranked cut, and an
// anchor-reserved team survives it only via the kind reserve
// (reservedPrefix, resolve.go) -- so ranked_cut and reserved_kind_admitted
// both fire for real, not by construction of a synthetic event. kind_offer
// fires unconditionally on every resolution that reaches the offer
// builder (its own doc comment in tracer.go), which resolveSubjects
// always does outside the offers-only bypass this call does not take.
//
// ranked_cut's own per-candidate line stays Debug (team-lead's volume
// gate, measured 2026-09-06: up to 91 lines for one resolution against a
// 25-per-resolution ceiling) -- what must reach Info is the ONE
// RankedCutSummary line per resolution (ResolutionTraceEvent's own doc
// comment). This test asserts exactly that: the stage token appears
// exactly once at Info, carrying the folded candidate/survivor counts.
func TestSlogResolutionTracer_StageLinesVisibleAtProductionLogLevel(t *testing.T) {
	t.Parallel()
	const crowd = 90
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{"platform": lexicalCrowd("platform", crowd)},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"platform": {
				contextfabric.SubjectTeam:       {anchorTeamNode("platform", "Platform Team")},
				contextfabric.SubjectRepository: nil,
			},
		},
	}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20 // 20 < 90: forces the cut the reserve must survive.

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(logger)

	_, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
		deps, nil, nil, anchorScopedFrame("platform"), contextfabric.SubjectTeam)
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log := buf.String()
	if log == "" {
		t.Fatal("captured log is EMPTY -- the tracer produced no output at all, which would make every assertion below vacuous")
	}
	for _, stage := range []string{"ranked_cut", "reserved_kind_admitted", "kind_offer"} {
		if !strings.Contains(log, `"stage":"`+stage+`"`) {
			t.Errorf("no %q stage line at the production default log level (Info) -- this stage logs at DebugContext, invisible on the rig", stage)
		}
	}
	// The volume gate's whole point: exactly ONE ranked_cut line reaches
	// Info for this resolution (the summary), not one per pool candidate --
	// a regression back to per-candidate Info logging would silently
	// reproduce the >25-lines-per-resolution volume this design avoids.
	if got := strings.Count(log, `"stage":"ranked_cut"`); got != 1 {
		t.Errorf("ranked_cut Info lines = %d, want exactly 1 (the per-resolution summary) -- a value >1 means the per-candidate line leaked back to Info", got)
	}
	if !strings.Contains(log, `"msg":"context fabric resolution trace: ranked cut summary"`) {
		t.Error("the ranked_cut Info line is not the summary shape (msg mismatch) -- the folded-array design")
	}
	if !strings.Contains(log, `"candidate_count":91`) {
		t.Errorf("ranked_cut summary missing/wrong candidate_count -- log: %s", log)
	}
	if !strings.Contains(log, `"survived_count":20`) {
		t.Errorf("ranked_cut summary missing/wrong survived_count (want the MaxSubjectCandidates budget, 20) -- log: %s", log)
	}
}

// TestRankedCutSummary_SurvivedIDsCappedIndependentlyOfCutBudget is the
// scale-safety half of the same fix: a scale ruling (chris, 100/1000-org
// growth) requires that ONE Info line never grows unbounded with the size
// of a large-org resolution. The configured cut budget `max` has no upper
// bound of its own -- a deployment could legitimately set it to 1000 -- so
// bounding RankedCutSurvivedIDs by `max` (as the first cut of this fix did)
// is not enough on its own. This test forces 40 survivors under an
// UNBOUNDED cut (max=0, so every candidate survives, same shape as
// TestCHAOS4234_RankedCutTrace_UnboundedMaxMarksEverySurvivor) and asserts:
// RankedCutSurvivedCount is the TRUE count (40, never truncated) while
// RankedCutSurvivedIDs is capped at 25 and holds exactly the first 25
// survivors IN RANK ORDER (highest confidence first) -- a truncation that
// silently dropped the top-ranked survivors, or that also shrank the count
// field, would be a worse regression than the one this fix closes.
func TestRankedCutSummary_SurvivedIDsCappedIndependentlyOfCutBudget(t *testing.T) {
	t.Parallel()
	const crowd = 40
	pool := map[string]contextfabric.SubjectCandidate{}
	wantOrder := make([]string, crowd)
	for i := 0; i < crowd; i++ {
		id := "c_" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		// Strictly descending confidence so rank order is deterministic and
		// distinct from insertion order.
		confidence := 1.0 - float64(i)*0.01
		pool[id] = chaos4234Candidate(contextfabric.SubjectWorkItem, id, confidence)
		wantOrder[i] = id
	}
	tracer := &captureResolutionTracer{}
	ResolveFromMergedCandidatesWithGate(
		pool, map[string]string{}, map[string]bool{}, 0, true,
		true, nil, 0, false, 2, 20, true,
		DefaultCommitGatePolicy(), identityClaimants{}, identityMatchTerms{},
		false, tracer, "request_5222_cap", "",
	)
	var summary ResolutionTraceEvent
	found := false
	for _, event := range tracer.eventsForStage("ranked_cut") {
		if event.RankedCutSummary {
			if found {
				t.Fatalf("more than one RankedCutSummary event for a single resolution")
			}
			summary = event
			found = true
		}
	}
	if !found {
		t.Fatal("no RankedCutSummary event found for the resolution")
	}
	if summary.RankedCutCandidateCount != crowd {
		t.Errorf("RankedCutCandidateCount = %d, want %d", summary.RankedCutCandidateCount, crowd)
	}
	if summary.RankedCutSurvivedCount != crowd {
		t.Errorf("RankedCutSurvivedCount = %d, want %d (the TRUE count, never truncated by the id cap)", summary.RankedCutSurvivedCount, crowd)
	}
	const idCap = 25
	if len(summary.RankedCutSurvivedIDs) != idCap {
		t.Fatalf("len(RankedCutSurvivedIDs) = %d, want exactly %d regardless of %d survivors", len(summary.RankedCutSurvivedIDs), idCap, crowd)
	}
	for i, id := range summary.RankedCutSurvivedIDs {
		if id != wantOrder[i] {
			t.Errorf("RankedCutSurvivedIDs[%d] = %q, want %q (first %d survivors in rank order)", i, id, wantOrder[i], idCap)
		}
	}
}
