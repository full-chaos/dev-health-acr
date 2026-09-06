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
// 25-per-pass ceiling) -- what must reach Info is the RankedCutSummary
// line, one per PASS through ResolveFromMergedCandidatesWithGateAndBasis,
// paired 1:1 with that pass's own "decision" event (ResolutionTraceEvent's
// own doc comment; a resolution running more than one pass gets more than
// one summary -- see TestRankedCutSummary_PairedOneToOneWithDecisionAcrossReDecisionPasses
// for that shape measured directly). This fixture takes exactly ONE pass
// (no confirmed kind, no evidence census configured), so 1 decision means
// exactly 1 summary here -- this test asserts that, plus the folded
// candidate/survivor counts.
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
	// Info for this SINGLE-PASS resolution (the summary), not one per pool
	// candidate -- a regression back to per-candidate Info logging would
	// silently reproduce the >25-lines-per-pass volume this design avoids.
	// (This fixture takes exactly one pass; see
	// TestRankedCutSummary_PairedOneToOneWithDecisionAcrossReDecisionPasses
	// for the multi-pass count, which is NOT 1.)
	if got := strings.Count(log, `"stage":"ranked_cut"`); got != 1 {
		t.Errorf("ranked_cut Info lines = %d, want exactly 1 for this single-pass fixture -- a value >1 means the per-candidate line leaked back to Info", got)
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

// TestRankedCutSummary_PairedOneToOneWithDecisionAcrossReDecisionPasses is
// the measured half of the pairing rule (ResolutionTraceEvent.RankedCutSummary's
// own doc comment): a RankedCutSummary is emitted once per PASS through
// ResolveFromMergedCandidatesWithGateAndBasis, not once per resolution --
// exactly the same multiplicity contract this file already applies to the
// "decision" stage (discardableDecisionTracer's own doc comment: "several
// decision events per resolution is normal; the LAST one describes the
// returned resolution"). A codex review round reproduced a resolution
// emitting 2 RankedCutSummary events under this exact shape (a confirmed-kind
// scoped re-decision superseding the first pass) and initially read that as
// a defect against an "exactly once per resolution" claim; the claim was
// wrong, not the emission -- this test measures and pins the CORRECT
// invariant: count(ranked_cut summaries) == count(decision events),
// deliberately on a fixture that exercises a re-decision (reusing
// TestResolveSubjects_ConfirmedKindScope_Case57ShapeClearsStaleGlobalTruncation's
// own shape, chaos4154_confirmed_kind_scope_test.go), so the pairing is
// proven where it actually matters, not just in the trivial single-pass
// case TestSlogResolutionTracer_StageLinesVisibleAtProductionLogLevel
// already covers.
func TestRankedCutSummary_PairedOneToOneWithDecisionAcrossReDecisionPasses(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	node := candidateNode(kind, subject.CanonicalID, subject.Label, 0.9, "*")
	// rival: a same-kind, unscoped-only candidate for the SAME term -- see
	// Case57's own doc comment for why its presence matters (without it, a
	// broken implementation that skips the scoped re-decision entirely
	// would still happen to commit the right subject here).
	rival := candidateNode(kind, "wi_rival", "Something Else Entirely", 0.85, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{term: {rival}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {kind: {node}},
		},
		// The earlier, unrelated unscoped stage that trips the
		// resolution-wide truncation bit -- Case57's own shape, needed so
		// the confirmed-kind scoped re-decision actually runs.
		searchTruncated: true,
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution.Committed = %#v, want the scoped re-decision's own subject -- this test's pairing count is only meaningful if a real re-decision actually fired and committed", resolution.Committed)
	}
	decisions := 0
	summaries := 0
	for _, event := range tracer.events {
		if event.Stage == "decision" {
			decisions++
		}
		if event.Stage == "ranked_cut" && event.RankedCutSummary {
			summaries++
		}
	}
	if decisions < 2 {
		t.Fatalf("decision events = %d, want >= 2 -- this fixture must exercise a re-decision (first pass + scoped pass) for the pairing count to be meaningful; got %d", decisions, decisions)
	}
	if summaries != decisions {
		t.Errorf("ranked_cut summary events = %d, decision events = %d -- want exactly 1:1 pairing (one summary per pass, matching one decision per pass)", summaries, decisions)
	}
}
