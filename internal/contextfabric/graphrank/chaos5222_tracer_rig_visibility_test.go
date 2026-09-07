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
	// CHAOS-5393 changed the SHAPE this fixture drives, and the contract is
	// updated deliberately rather than the assertions loosened. On a
	// scope-anchored frame the SCOPE ANCHOR now decides in its own contest
	// (design: SubjectPlan is "group axis, member axis, scope anchor"; I11
	// requires the graph to COMMIT the anchor, which a shared contest makes
	// unsatisfiable whenever a member outranks it). So:
	//
	//   - there are TWO ranked cuts on such a turn, one per contest, not one;
	//   - `reserved_kind_admitted` no longer fires for the anchor, because the
	//     anchor is no longer competing in the member pool for a slot to be
	//     admitted into -- the reserve still exists for every other kind and
	//     on every non-scope-anchored frame;
	//   - the member cut sees the crowd minus nothing and survives the budget
	//     less the one slot the anchor holds.
	//
	// The rig-visibility PROPERTY this test exists for is unchanged and still
	// asserted: the per-candidate lines stay Debug, the summaries reach Info,
	// and the counts on them are real rather than absent.
	for _, stage := range []string{"ranked_cut", "kind_offer", "anchor_pool"} {
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
	// TWO contests, so two summaries -- and the number is asserted exactly,
	// because "more than one" is also what a per-candidate leak back to Info
	// would look like, and 91 candidates would produce 91.
	if got := strings.Count(log, `"stage":"ranked_cut"`); got != 2 {
		t.Errorf("ranked_cut Info lines = %d, want exactly 2 (one per contest: members, then the scope anchor) -- a much larger value means the per-candidate line leaked back to Info", got)
	}
	if !strings.Contains(log, `"msg":"context fabric resolution trace: ranked cut summary"`) {
		t.Error("the ranked_cut Info line is not the summary shape (msg mismatch) -- the folded-array design")
	}
	// The MEMBER contest sees the 90-strong crowd and survives the budget
	// less the single slot the anchor holds: the budget is SHARED between
	// the two contests, never doubled.
	if !strings.Contains(log, `"candidate_count":90`) {
		t.Errorf("member ranked_cut summary missing/wrong candidate_count (want the 90 member-kind candidates) -- log: %s", log)
	}
	if !strings.Contains(log, `"survived_count":19`) {
		t.Errorf("member ranked_cut summary missing/wrong survived_count (want the 20-candidate budget less the anchor's one slot) -- log: %s", log)
	}
	// And the ANCHOR's own contest is visible as its own cut, over its one
	// candidate. Without this the two-contest shape would be indistinguishable
	// from a single cut that happened to log twice.
	if !strings.Contains(log, `"candidate_count":1`) {
		t.Errorf("anchor ranked_cut summary missing -- the scope contest must be visible on the rig as its own cut; log: %s", log)
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

// TestRankedCutSummary_LastSummaryDescribesTheKeptPassAcrossReDecisionPasses
// pins the ACTUAL guarantee (ResolutionTraceEvent.RankedCutSummary's own doc
// comment), after two rounds of a WRONG count-based formalization of it:
//
// Round 1 of this ticket's own codex review claimed/tested "exactly once per
// resolution" -- reproduced false (a scoped re-decision produces a second
// summary). The fix-of-the-fix claimed "1:1 paired with decision" instead --
// round 2 reproduced THAT false too, three ways (see the three regression
// pins below this test). Both formalizations tried to turn "the last event
// describes the returned resolution" (decision's own established rule, which
// IS true) into a COUNT relationship, which is not what the rule says or
// needs.
//
// This test asserts the rule as it actually is, on Case57's own fixture
// (chaos4154_confirmed_kind_scope_test.go) extended with a SECOND unscoped
// rival so the first pass's own cut numbers are visibly DIFFERENT from the
// kept (scoped) pass's -- proving "last" is doing real work, not vacuously
// matching a single-summary case:
//  1. The first pass's own summary and the LAST (kept) summary have
//     DIFFERENT candidate_count/survived_count -- quoted, not just asserted
//     unequal, so a future reader can see the actual numbers.
//  2. Every id in the returned resolution.Committed appears in the LAST
//     summary's RankedCutSurvivedIDs -- the property an Info-only reader
//     actually needs (find your committed subject in the last summary you
//     see for its request_id), not a raw count comparison.
func TestRankedCutSummary_LastSummaryDescribesTheKeptPassAcrossReDecisionPasses(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	node := candidateNode(kind, subject.CanonicalID, subject.Label, 0.9, "*")
	// TWO same-kind, unscoped-only rivals for the SAME term (Case57 itself
	// uses only one) -- so the first pass's own candidate_count (2) is
	// visibly different from the kept scoped pass's (1), making "last
	// summary's numbers differ from the first's" a real, non-vacuous check.
	rival1 := candidateNode(kind, "wi_rival1", "Something Else Entirely", 0.85, "*")
	rival2 := candidateNode(kind, "wi_rival2", "Yet Another Rival", 0.75, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{term: {rival1, rival2}},
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
		t.Fatalf("resolution.Committed = %#v, want the scoped re-decision's own subject -- this test's cross-pass comparison is only meaningful if a real re-decision actually fired and committed", resolution.Committed)
	}
	var summaries []ResolutionTraceEvent
	for _, event := range tracer.events {
		if event.Stage == "ranked_cut" && event.RankedCutSummary {
			summaries = append(summaries, event)
		}
	}
	if len(summaries) < 2 {
		t.Fatalf("ranked_cut summary events = %d, want >= 2 -- this fixture must exercise a re-decision (first pass + scoped pass) for the cross-pass comparison to be meaningful", len(summaries))
	}
	first, last := summaries[0], summaries[len(summaries)-1]
	if first.RankedCutCandidateCount == last.RankedCutCandidateCount && first.RankedCutSurvivedCount == last.RankedCutSurvivedCount {
		t.Fatalf("first summary %+v and last summary %+v have IDENTICAL counts -- this fixture no longer distinguishes the passes, so the assertions below would be vacuous", first, last)
	}
	t.Logf("first pass summary: candidate_count=%d survived_count=%d survived_ids=%v", first.RankedCutCandidateCount, first.RankedCutSurvivedCount, first.RankedCutSurvivedIDs)
	t.Logf("last (kept) pass summary: candidate_count=%d survived_count=%d survived_ids=%v", last.RankedCutCandidateCount, last.RankedCutSurvivedCount, last.RankedCutSurvivedIDs)
	survived := map[string]bool{}
	for _, id := range last.RankedCutSurvivedIDs {
		survived[id] = true
	}
	for _, committed := range resolution.Committed {
		if !survived[committed.CanonicalID] {
			t.Errorf("committed subject %q not found in the LAST ranked_cut summary's survived_ids %v -- an Info-only reader could not confirm the commit from the last summary they see", committed.CanonicalID, last.RankedCutSurvivedIDs)
		}
	}
}

// TestRankedCutSummary_MultiSubjectCommitEmitsOneSummaryForManyDecisions
// documents (not fixes -- this is CORRECT, established behavior) round 2's
// first counter-example to the discarded "1:1 with decision" claim: a pass
// that commits MULTIPLE subjects traces one "decision" event PER committed
// subject (CHAOS-4096, TestChaos4096_MultiSubjectCommitEmitsOneDecisionEventPerSubject's
// own fixture, reused here) but still cuts its pool exactly once, so it
// emits exactly ONE ranked_cut summary regardless of how many subjects that
// one cut committed.
func TestRankedCutSummary_MultiSubjectCommitEmitsOneSummaryForManyDecisions(t *testing.T) {
	t.Parallel()
	first := corroborationCandidate("multi_first", 1, contextfabric.MatchExact)
	first.State = contextfabric.ResolutionCommitted
	second := corroborationCandidate("multi_second", 1, contextfabric.MatchAlias)
	second.State = contextfabric.ResolutionCommitted
	tracer := &recordingTracer{}
	bySubject := map[string]contextfabric.SubjectCandidate{
		SubjectKey(first.Subject):  first,
		SubjectKey(second.Subject): second,
	}
	resolution, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(
		bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
		nil, 0, false, 10, 20, true,
		DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-multi-summary", "", false, false, nil)
	if len(resolution.Committed) != 2 {
		t.Fatalf("both pre-committed hints must still commit, got %v", resolution.Committed)
	}
	decisions := tracer.decisions()
	if len(decisions) != 2 {
		t.Fatalf("decision events = %d, want 2 (one per committed subject, CHAOS-4096) -- this fixture's own precondition", len(decisions))
	}
	summaries := 0
	for _, event := range tracer.events {
		if event.Stage == "ranked_cut" && event.RankedCutSummary {
			summaries++
		}
	}
	if summaries != 1 {
		t.Errorf("ranked_cut summary events = %d, want exactly 1 -- one pass cuts its pool once regardless of how many subjects that cut committed (2 decisions here); NOT a 1:1 count with decision", summaries)
	}
}

// TestRankedCutSummary_EmptyCandidatePoolEmitsNoSummary documents round 2's
// second counter-example: a pass whose candidate pool is EMPTY still
// decides (typically a stalled/no-candidate outcome -- one "decision" event
// fires) but has nothing to rank or cut, so ResolutionTraceEvent's own
// ranked_cut summary code path never runs for that pass. Zero summaries for
// one decision is correct, not a gap.
func TestRankedCutSummary_EmptyCandidatePoolEmitsNoSummary(t *testing.T) {
	t.Parallel()
	term := "nothing here"
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{term: {}},
	}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want empty -- nothing was ever found for this term", resolution.Committed)
	}
	decisions := len(tracer.decisions())
	if decisions == 0 {
		t.Fatal("decision events = 0 -- this fixture's own precondition (an empty pool must still decide, e.g. a stalled outcome) did not hold; the test below would be vacuous")
	}
	summaries := 0
	for _, event := range tracer.events {
		if event.Stage == "ranked_cut" && event.RankedCutSummary {
			summaries++
		}
	}
	if summaries != 0 {
		t.Errorf("ranked_cut summary events = %d, want 0 for an empty candidate pool (nothing to cut) alongside %d decision event(s)", summaries, decisions)
	}
}

// TestRankedCutSummary_DiscardedScopedPassContributesNothingToInfo documents
// round 2's third counter-example, reusing
// TestResolveSubjects_ConfirmedKindScope_DiscardedScopedDecisionNeverTraces's
// own fixture (chaos4154_confirmed_kind_scope_test.go): a scoped re-decision
// that RUNS but is not KEPT (does not commit) withholds its summary and its
// decision event TOGETHER via discardableDecisionTracer -- neither reaches
// the real tracer. A resolution that attempts more than one pass does NOT
// necessarily show more than one summary at Info; it shows one only for
// each pass that was actually retained.
func TestRankedCutSummary_DiscardedScopedPassContributesNothingToInfo(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	backend := &fakeGraphBackend{
		enableSearchKind:  true,
		searchResults:     map[string][]CandidateNode{term: {}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {}}},
		searchTruncated:   true,
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want empty -- nothing was ever found for this term", resolution.Committed)
	}
	decisions := len(tracer.decisions())
	if decisions != 1 {
		t.Fatalf("decision events = %d, want exactly 1 (the discarded scoped pass's own decision must never reach the tracer) -- this pins the SAME invariant TestResolveSubjects_ConfirmedKindScope_DiscardedScopedDecisionNeverTraces already asserts; if this drifts, that test should also be failing", decisions)
	}
	summaries := 0
	for _, event := range tracer.events {
		if event.Stage == "ranked_cut" && event.RankedCutSummary {
			summaries++
		}
	}
	if summaries != 0 {
		t.Errorf("ranked_cut summary events = %d, want 0 -- both passes here have empty candidate pools, so neither the retained first pass nor the discarded scoped pass has anything to cut", summaries)
	}
}
