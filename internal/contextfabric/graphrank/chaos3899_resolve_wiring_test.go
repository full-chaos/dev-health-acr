package graphrank

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveSubjects_ShadowEvidenceRoundNeverChangesResolution is
// CHAOS-3899's own zero-behavior-change proof (design brief v5 §6 Slice A:
// "decisions suppressed... production outcomes byte-identical"), STILL
// VALID after CHAOS-3896 Slice B: this scenario's single candidate means
// SurvivorsFirstOrder has nothing to reorder (a 1-element list's order is
// invariant) even though the fake CensusFunc reports Count==1 -- the
// scenario purposely does NOT populate SatisfierCanonicalID (the field
// only devhealthsource.NewCensusFunc's own bridging populates), so
// classifyCandidate would land on verdictNeutral even with more
// candidates. Slice B's OWN reorder proof lives in
// TestResolveSubjects_SurvivorsFirstOrderReordersLiveThroughResolveSubjects
// below; THIS test's job stays what it always was -- proving the round's
// DECISIVE would_commit/would_no_match/would_clarify verdict never
// contaminates resolution.Status/Committed, the one half of "zero
// behavior change" that remains absolute post-Slice-B. It runs
// the IDENTICAL stalled-resolution scenario twice -- once with
// deps.CensusFunc nil (today's production wiring), once with a CensusFunc
// that would answer would_commit (Count==1) if the shadow round's output
// were EVER allowed to influence a real decision -- and asserts the
// returned contextfabric.SubjectResolution is byte-identical either way.
func TestResolveSubjects_ShadowEvidenceRoundNeverChangesResolution(t *testing.T) {
	t.Parallel()
	// A low-confidence, single-candidate, TRUNCATED search: aliasIdentityComplete
	// is false (no AliasLookup wired), so the commit switch's `case
	// searchTruncated:` fires before LoneFloor is ever consulted --
	// "stalled" per brief §0's own definition (search_truncated=true, pool
	// == cap, nothing committed).
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.6, "*")
	buildBackend := func() *fakeGraphBackend {
		return &fakeGraphBackend{
			searchResults:   map[string][]CandidateNode{"PR 532": {target}},
			searchTruncated: true,
		}
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"

	runOnce := func(censusWired bool) (contextfabric.SubjectResolution, *captureResolutionTracer) {
		backend := buildBackend()
		deps := backend.deps()
		tracer := &captureResolutionTracer{}
		deps.ResolutionTracer = tracer
		if censusWired {
			deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
				return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "repo-1:532"}, nil
			}
		}
		resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps, nil, nil)
		if err != nil {
			t.Fatalf("ResolveSubjects(nil) error = %v", err)
		}
		return resolution, tracer
	}

	withoutShadow, tracerWithout := runOnce(false)
	withShadow, tracerWith := runOnce(true)

	if len(withoutShadow.Committed) != 0 {
		t.Fatalf("baseline resolution.Committed = %#v, want nothing committed (this scenario must actually be stalled for the proof to mean anything)", withoutShadow.Committed)
	}
	if !reflect.DeepEqual(withoutShadow, withShadow) {
		t.Fatalf("resolution differs when deps.CensusFunc is wired:\n  without shadow: %#v\n  with shadow:    %#v", withoutShadow, withShadow)
	}

	if got := len(tracerWithout.eventsForStage("evidence_round")); got != 0 {
		t.Fatalf("evidence_round events with CensusFunc nil = %d, want 0 (the round must not run at all)", got)
	}
	if got := len(tracerWith.eventsForStage("evidence_round")); got != 1 {
		t.Fatalf("evidence_round events with CensusFunc wired = %d, want 1 (the round DID run -- non-vacuity)", got)
	}
	decision := tracerWith.eventsForStage("evidence_round")[0]
	if decision.ShadowOutcome != string(ShadowWouldCommit) {
		t.Fatalf("shadow outcome = %q, want %q (proving the round genuinely reached a would-commit verdict it was still forbidden from acting on)", decision.ShadowOutcome, ShadowWouldCommit)
	}
}

// TestResolveSubjects_ConfirmedAnchorReachesShadowRoundThroughResolveSubjects
// is CHAOS-4042's own wiring proof at the exported ResolveSubjects boundary
// (codex xhigh review round 1 finding, confirmed and addressed: no test
// previously exercised a non-nil *contextfabric.ConfirmedAnchorSelection
// through this function's own conversion into *AnchorBinding --
// runShadowEvidenceRoundForResolution's `if confirmedAnchor != nil { ... }`
// block, resolve.go -- a regression dropping that conversion would leave
// every existing test green). This scenario deliberately sets NO
// AliasClaimants at all, so BindAnchor alone would find nothing (anchorOK
// stays false, no anchor FK ever applies to the census call) -- any
// anchor-bound census call in this test can only be explained by the
// CONFIRMED anchor's own override, never by a coincidental BindAnchor
// derivation.
func TestResolveSubjects_ConfirmedAnchorReachesShadowRoundThroughResolveSubjects(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.6, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"PR 532": {target}},
		searchTruncated: true,
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"
	deps := backend.deps()

	var gotAnchorKind contextfabric.SubjectKind
	var gotAnchorCanonicalID string
	var gotAnchorBound bool
	deps.CensusFunc = func(_ context.Context, _ string, _ CensusKind, _ string, _ bool, anchorKind contextfabric.SubjectKind, anchorCanonicalID string, anchorBound bool) (CensusOutcome, error) {
		gotAnchorKind, gotAnchorCanonicalID, gotAnchorBound = anchorKind, anchorCanonicalID, anchorBound
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "repo-1:532"}, nil
	}
	confirmedAnchor := &contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectRepository, CanonicalID: "repository_widget_service"}

	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps, nil, confirmedAnchor); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	if !gotAnchorBound || gotAnchorKind != contextfabric.SubjectRepository || gotAnchorCanonicalID != "repository_widget_service" {
		t.Fatalf("census anchor discriminator = (kind=%q, canonical_id=%q, bound=%v), want the CONFIRMED anchor selection (repository_widget_service) -- BindAnchor alone (no AliasClaimants wired) could never have produced a bound anchor here, so this proves confirmedAnchor's own conversion into the shadow round's AnchorBinding actually happened", gotAnchorKind, gotAnchorCanonicalID, gotAnchorBound)
	}
}

// TestResolveSubjects_SurvivorsFirstOrderReordersLiveThroughResolveSubjects
// is CHAOS-3896 Slice B's own end-to-end wiring proof: a two-candidate
// AMBIGUOUS, stalled, census-registered-kind resolution, with a CensusFunc
// that (as devhealthsource.NewCensusFunc's own bridging would) reports a
// SatisfierCanonicalID naming ONE of the two candidates -- proving the
// reorder actually reaches resolution.Candidates AND resolution.ClarificationPrompt
// through the real ResolveSubjects call path, not just SurvivorsFirstOrder
// called in isolation (chaos3896_slice_b_presentation_test.go). Status and
// Committed are asserted unchanged (still ambiguous, still nothing
// committed) -- membership and decision are exactly as
// ResolveFromMergedCandidatesWithGate produced; only order and prompt
// text move.
func TestResolveSubjects_SurvivorsFirstOrderReordersLiveThroughResolveSubjects(t *testing.T) {
	t.Parallel()
	// Two close-confidence PR candidates -- SubjectPullRequest IS a
	// registered census kind (unlike TestResolveSubjectsMarksCloseCandidatesAmbiguousAndOffersClarification's
	// own SubjectProject fixture, which is not).
	loser := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:1", "PR #1", 0.75, "*")
	winner := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:2", "PR #2", 0.70, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"Which PR": {loser, winner}},
		searchTruncated: true, // required for the shadow round's own "stalled" gate
	}
	deps := backend.deps()
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		// As devhealthsource.NewCensusFunc's own bridging would report: the
		// census names PR #2 as the sole real satisfier.
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: "pull_request:repo-1:2"}, nil
	}
	// The question text must contain a handle the closed grammar registry
	// recognizes (design brief §1.2's "PR 532"/"PR #532" pattern) -- without
	// a bound handle (or a unique-claimant anchor, neither of which this
	// fixture wires) the round refuses at D2(a) (no_discriminators) before
	// ever reaching CensusFunc at all.
	request := testRequest()
	request.Question = "Why did PR #2 fail?"
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Which PR"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none -- Slice B must never change the decision", resolution.Committed)
	}
	if len(resolution.Candidates) != 2 {
		t.Fatalf("resolution.Candidates = %#v, want both candidates still present (membership unchanged)", resolution.Candidates)
	}
	if resolution.Candidates[0].Subject.CanonicalID != "pull_request:repo-1:2" {
		t.Fatalf("resolution.Candidates[0] = %q, want the census-attested survivor (%q) first", resolution.Candidates[0].Subject.CanonicalID, "pull_request:repo-1:2")
	}
	if resolution.Candidates[1].Subject.CanonicalID != "pull_request:repo-1:1" {
		t.Fatalf("resolution.Candidates[1] = %q, want the census-eliminated candidate (%q) last", resolution.Candidates[1].Subject.CanonicalID, "pull_request:repo-1:1")
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("resolution.ClarificationPrompt is empty for an ambiguous, clarification-allowed request")
	}
	if !strings.Contains(resolution.ClarificationPrompt, "PR #2") || strings.Index(resolution.ClarificationPrompt, "PR #2") > strings.Index(resolution.ClarificationPrompt, "PR #1") {
		t.Fatalf("resolution.ClarificationPrompt = %q, want the survivor (PR #2) mentioned before the eliminated candidate (PR #1)", resolution.ClarificationPrompt)
	}
}

// TestResolveSubjects_SurvivorsFirstOrderReordersLiveViaSatisfierSet is
// TestResolveSubjects_SurvivorsFirstOrderReordersLiveThroughResolveSubjects's
// own companion (codex review finding, addressed): that test's CensusFunc
// only ever exercises the Count==1 witness path -- this one exercises the
// NEW 2<=count<=CensusBudget satisfier-SET path end-to-end through
// ResolveSubjects, not just at the SurvivorsFirstOrder/RunCensus unit
// level. Three candidates; the census names two of them (via
// SatisfierCanonicalIDs) as real, the third as census-eliminated.
func TestResolveSubjects_SurvivorsFirstOrderReordersLiveViaSatisfierSet(t *testing.T) {
	t.Parallel()
	eliminated := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:3", "PR #3", 0.80, "*")
	survivor1 := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:1", "PR #1", 0.75, "*")
	survivor2 := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:2", "PR #2", 0.70, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"Which PR": {eliminated, survivor1, survivor2}},
		searchTruncated: true,
	}
	deps := backend.deps()
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		// As devhealthsource.NewCensusFunc's own bridging would report for
		// a closure-verified 2<=count<=CensusBudget enrichment fetch: PR #1
		// and PR #2 are real, PR #3 is not among the satisfiers.
		return CensusOutcome{
			Count: 2, CensusReadAt: time.Now().UTC(),
			SatisfierCanonicalIDs: []string{"pull_request:repo-1:1", "pull_request:repo-1:2"},
		}, nil
	}
	request := testRequest()
	request.Question = "Why did PR #2 fail?"
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Which PR"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none -- Slice B must never change the decision", resolution.Committed)
	}
	if len(resolution.Candidates) != 3 {
		t.Fatalf("resolution.Candidates = %#v, want all three candidates still present (membership unchanged)", resolution.Candidates)
	}
	if resolution.Candidates[2].Subject.CanonicalID != "pull_request:repo-1:3" {
		t.Fatalf("resolution.Candidates[2] = %q, want the census-eliminated candidate (PR #3) last", resolution.Candidates[2].Subject.CanonicalID)
	}
	survivors := map[string]bool{
		resolution.Candidates[0].Subject.CanonicalID: true,
		resolution.Candidates[1].Subject.CanonicalID: true,
	}
	if !survivors["pull_request:repo-1:1"] || !survivors["pull_request:repo-1:2"] {
		t.Fatalf("resolution.Candidates[0:2] = %#v, want both satisfier-set survivors ahead of the eliminated candidate", resolution.Candidates[:2])
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("resolution.ClarificationPrompt is empty for an ambiguous, clarification-allowed request")
	}
	if strings.Index(resolution.ClarificationPrompt, "PR #3") < strings.Index(resolution.ClarificationPrompt, "PR #1") ||
		strings.Index(resolution.ClarificationPrompt, "PR #3") < strings.Index(resolution.ClarificationPrompt, "PR #2") {
		t.Fatalf("resolution.ClarificationPrompt = %q, want the eliminated candidate (PR #3) mentioned after both survivors", resolution.ClarificationPrompt)
	}
}

// TestResolveSubjects_ShadowEvidenceRoundUsesInterpretedAxisNotRequestAxis
// is an adversarial review regression pin: the shadow round must read
// interpreted.TimeContext.Axis (the ENGINE's own authoritative axis --
// engine.go clamps request.TimeContext.Axis into interpreted.TimeContext
// separately) rather than the raw request.TimeContext.Axis. A historical
// question submitted with a current-axis REQUEST context must still be
// refused via historical_axis_skip (D7), never silently run a
// current-state census against a historical question.
func TestResolveSubjects_ShadowEvidenceRoundUsesInterpretedAxisNotRequestAxis(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.6, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"PR 532": {target}}, searchTruncated: true}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		t.Fatalf("CensusFunc called -- historical_axis_skip must refuse before any source read")
		return CensusOutcome{}, nil
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"
	request.TimeContext.Axis = contextfabric.TemporalCurrent // the RAW request context claims current...
	interpreted := testInterpreted("PR 532")
	interpreted.TimeContext.Axis = contextfabric.TemporalValidTime // ...but the INTERPRETED axis (what the engine actually treats as authoritative) is historical.

	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted, deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	events := tracer.eventsForStage("evidence_round")
	if len(events) != 1 || events[0].ShadowReason != string(ReasonHistoricalAxisSkip) {
		t.Fatalf("evidence_round events = %#v, want exactly 1 with Reason=historical_axis_skip", events)
	}
}

// TestResolveSubjects_ShadowEvidenceRoundPanicIsolation is an adversarial
// review regression pin: a panic inside a caller-supplied CensusFunc must
// never escape into a real ResolveSubjects call -- the shadow round is
// purely observational, and its own hardening must be structural, not
// merely "true while nothing panics".
func TestResolveSubjects_ShadowEvidenceRoundPanicIsolation(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.6, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"PR 532": {target}}, searchTruncated: true}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		panic("simulated CensusFunc panic")
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v, want the panic to be isolated, not propagated as an error either", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want nothing committed", resolution.Committed)
	}
	events := tracer.eventsForStage("evidence_round")
	if len(events) != 1 || events[0].ShadowReason != string(ReasonProbeError) {
		t.Fatalf("evidence_round events = %#v, want exactly 1 recovered probe_error event", events)
	}
}

// TestResolveSubjects_ShadowEvidenceRoundSkipsNonStalledResolutions pins the
// cost discipline (brief's own pricing note: "Committed resolutions pay
// nothing"): a resolution that commits outright must never trigger the
// shadow round at all, even with deps.CensusFunc wired.
func TestResolveSubjects_ShadowEvidenceRoundSkipsNonStalledResolutions(t *testing.T) {
	t.Parallel()
	exact := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 0.4, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Ask Dev": {exact}}}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	censusCalls := 0
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		censusCalls++
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC()}, nil
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("resolution.Committed = %#v, want exactly 1 (this scenario must actually commit for the proof to mean anything)", resolution.Committed)
	}
	if censusCalls != 0 {
		t.Fatalf("CensusFunc called %d times for a COMMITTED resolution, want 0", censusCalls)
	}
	if got := len(tracer.eventsForStage("evidence_round")); got != 0 {
		t.Fatalf("evidence_round events for a committed resolution = %d, want 0", got)
	}
}
