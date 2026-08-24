package graphrank

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4234 trace shape: one "ranked_cut" event per candidate at the
// final MaxSubjectCandidates cut, in rank order, Survived marking the cut;
// plus the coverage-bypass companion and the offers-only kind_offer flag.

func chaos4234Candidate(kind contextfabric.SubjectKind, id string, confidence float64) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: "receipt_" + id,
		Subject:   contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id},
		// Confidence is a single-mechanism base: CorroboratedConfidence
		// returns it unchanged, so the ranking order below is exactly the
		// confidence order these literals encode.
		Confidence:      confidence,
		MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchLexical},
		MatchedTerms:    []string{"t"},
	}
}

func TestCHAOS4234_RankedCutTrace_OneEventPerCandidateInRankOrderWithSurvival(t *testing.T) {
	t.Parallel()
	pool := map[string]contextfabric.SubjectCandidate{}
	for _, c := range []contextfabric.SubjectCandidate{
		chaos4234Candidate(contextfabric.SubjectWorkItem, "wi_low", 0.30),
		chaos4234Candidate(contextfabric.SubjectPullRequest, "pr_top", 0.60),
		chaos4234Candidate(contextfabric.SubjectRepository, "repo_mid", 0.45),
		chaos4234Candidate(contextfabric.SubjectProject, "proj_floor", 0.20),
	} {
		pool[SubjectKey(c.Subject)] = c
	}
	tracer := &captureResolutionTracer{}

	resolution := ResolveFromMergedCandidatesWithGate(
		pool, map[string]string{}, map[string]bool{}, 2, true,
		true, nil, 0, false, 2, 20, true,
		DefaultCommitGatePolicy(), identityClaimants{}, identityMatchTerms{},
		false, tracer, "request_4234", "",
	)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want nothing committed (searchTruncated stall)", resolution.Committed)
	}
	if len(resolution.Candidates) != 2 {
		t.Fatalf("resolution.Candidates = %#v, want the 2 survivors of max=2", resolution.Candidates)
	}
	events := tracer.eventsForStage("ranked_cut")
	if len(events) != 4 {
		t.Fatalf("ranked_cut events = %d, want one per pool candidate (4), including the two the cut dropped", len(events))
	}
	wantOrder := []string{"pr_top", "repo_mid", "wi_low", "proj_floor"}
	for i, event := range events {
		if event.Subject.CanonicalID != wantOrder[i] {
			t.Fatalf("ranked_cut[%d].Subject = %q, want %q (confidence-desc order)", i, event.Subject.CanonicalID, wantOrder[i])
		}
		if event.Rank != i+1 {
			t.Fatalf("ranked_cut[%d].Rank = %d, want %d (1-based, Rank==1 marks a batch start)", i, event.Rank, i+1)
		}
		if event.Survived != (i < 2) {
			t.Fatalf("ranked_cut[%d].Survived = %v, want %v at max=2", i, event.Survived, i < 2)
		}
		if event.CoverageBypass {
			t.Fatalf("ranked_cut[%d].CoverageBypass = true, want false on a cut event", i)
		}
		if event.RequestID != "request_4234" {
			t.Fatalf("ranked_cut[%d].RequestID = %q, want request_4234", i, event.RequestID)
		}
	}
	for i, survivor := range resolution.Candidates {
		if survivor.Subject.CanonicalID != wantOrder[i] {
			t.Fatalf("resolution.Candidates[%d] = %q, want %q: the cut must take exactly the ranked prefix the trace reported as survived", i, survivor.Subject.CanonicalID, wantOrder[i])
		}
	}
}

func TestCHAOS4234_RankedCutTrace_UnboundedMaxMarksEverySurvivor(t *testing.T) {
	t.Parallel()
	pool := map[string]contextfabric.SubjectCandidate{}
	for _, c := range []contextfabric.SubjectCandidate{
		chaos4234Candidate(contextfabric.SubjectWorkItem, "wi_a", 0.30),
		chaos4234Candidate(contextfabric.SubjectPullRequest, "pr_b", 0.60),
	} {
		pool[SubjectKey(c.Subject)] = c
	}
	tracer := &captureResolutionTracer{}
	ResolveFromMergedCandidatesWithGate(
		pool, map[string]string{}, map[string]bool{}, 0, true,
		true, nil, 0, false, 2, 20, true,
		DefaultCommitGatePolicy(), identityClaimants{}, identityMatchTerms{},
		false, tracer, "request_4234", "",
	)
	for _, event := range tracer.eventsForStage("ranked_cut") {
		if !event.Survived {
			t.Fatalf("ranked_cut %#v: Survived=false under max=0 (no cut), want true", event)
		}
	}
}

func TestCHAOS4234_RankedCutTrace_CoverageBypassCompanionForFloorFindTheCutDropped(t *testing.T) {
	t.Parallel()
	floorFind := candidateNode(contextfabric.SubjectPullRequest, "pr_floor", "Outage PR", 0.10, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults: map[string][]CandidateNode{
			"outage": {
				candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.9, "*"),
				candidateNode(contextfabric.SubjectWorkItem, "wi_2", "Outage work item two", 0.8, "*"),
			},
		},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"outage": {contextfabric.SubjectPullRequest: {floorFind}},
		},
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	request := testRequest()
	request.Options.MaxSubjectCandidates = 2 // the two work items fill the cut; the floor's PR ranks third and is dropped

	resolution, offer, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("outage"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, c := range resolution.Candidates {
		if c.Subject.CanonicalID == "pr_floor" {
			t.Fatalf("resolution.Candidates = %#v, want the floor find OUTSIDE the max=2 cut for this test to mean anything", resolution.Candidates)
		}
	}
	sawPR := false
	for _, opt := range offer.KindOptions {
		if opt.Kind == contextfabric.SubjectPullRequest {
			sawPR = true
		}
	}
	if !sawPR {
		t.Fatalf("offer.KindOptions = %#v, want pull_request offered: the floor find bypasses the cut into the offer builders", offer.KindOptions)
	}
	var bypass []ResolutionTraceEvent
	for _, event := range tracer.eventsForStage("ranked_cut") {
		if event.CoverageBypass {
			bypass = append(bypass, event)
		}
	}
	if len(bypass) != 1 || bypass[0].Subject.CanonicalID != "pr_floor" || bypass[0].Rank != 0 || bypass[0].Survived {
		t.Fatalf("coverage-bypass ranked_cut events = %#v, want exactly one for pr_floor with Rank 0 and Survived=false", bypass)
	}
}

func TestCHAOS4234_OffersOnlyResolution_SkipsCensusAndFlagsTheKindOfferEvent(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.50, "*")
	sibling := candidateNode(contextfabric.SubjectWorkItem, "work_item:repo-1:9", "WI 9", 0.40, "*")
	build := func() (*fakeGraphBackend, ResolveDeps, *captureResolutionTracer, *int) {
		backend := &fakeGraphBackend{
			searchResults:   map[string][]CandidateNode{"PR 532": {target, sibling}},
			searchTruncated: true,
			exactHints: map[string]CandidateNode{
				SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}): target,
			},
		}
		deps := backend.deps()
		tracer := &captureResolutionTracer{}
		deps.ResolutionTracer = tracer
		censusCalls := 0
		deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
			censusCalls++
			return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: "pull_request:repo-1:532"}, nil
		}
		return backend, deps, tracer, &censusCalls
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"

	// Decisive turn: the stalled, truncated resolution enters the census
	// round -- the control that proves the fixture reaches the mechanism
	// offers-only mode must skip (a two-kind pool never commits through
	// it; what matters here is that CensusFunc was consulted at all).
	_, deps, tracer, censusCalls := build()
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps, nil, nil); err != nil {
		t.Fatalf("decisive ResolveSubjects() error = %v", err)
	}
	if *censusCalls == 0 {
		t.Fatal("decisive turn: censusCalls=0, want the census round to run (control)")
	}
	if len(tracer.eventsForStage("evidence_round")) == 0 {
		t.Fatal("decisive turn emitted no evidence_round event, want one (control)")
	}
	if event, ok := lastEventForStage(tracer, "kind_offer"); !ok || event.OfferedUnderWindowGate {
		t.Fatalf("decisive kind_offer event = %#v (ok=%v), want OfferedUnderWindowGate=false", event, ok)
	}

	// Offers-only turn: same fixture, marked ctx.
	_, deps, tracer, censusCalls = build()
	offersOnly, material, err := ResolveSubjects(contextfabric.WithOffersOnlyResolution(context.Background()), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps, nil, nil)
	if err != nil {
		t.Fatalf("offers-only ResolveSubjects() error = %v", err)
	}
	if *censusCalls != 0 {
		t.Fatalf("offers-only turn: censusCalls=%d, want 0 -- the census is a commit mechanism whose output the gate discards", *censusCalls)
	}
	if len(offersOnly.Committed) != 0 {
		t.Fatalf("offers-only turn committed %#v, want nothing (no census, no commit)", offersOnly.Committed)
	}
	if len(tracer.eventsForStage("evidence_round")) != 0 || len(tracer.eventsForStage("slice_b_survivor_verdict")) != 0 {
		t.Fatal("offers-only turn emitted evidence_round/slice_b_survivor_verdict events, want none (skipped mechanisms must not trace as if they ran)")
	}
	if len(material.KindOptions) != 2 {
		t.Fatalf("offers-only material.KindOptions = %#v, want both pool kinds offered exactly as a decisive turn would", material.KindOptions)
	}
	event, ok := lastEventForStage(tracer, "kind_offer")
	if !ok || !event.OfferedUnderWindowGate {
		t.Fatalf("offers-only kind_offer event = %#v (ok=%v), want OfferedUnderWindowGate=true", event, ok)
	}
	if len(tracer.eventsForStage("ranked_cut")) != 2 {
		t.Fatalf("offers-only ranked_cut events = %d, want 2: ranking and its trace still run under the gate", len(tracer.eventsForStage("ranked_cut")))
	}
}

func TestCHAOS4234_OffersOnlyResolution_TagsTheDecisionEventNotJustKindOffer(t *testing.T) {
	t.Parallel()
	// codex round-1 finding 3: the "decision" event Turn1CommitGate reads
	// from must itself carry OfferedUnderWindowGate under offers-only
	// mode -- the kind_offer flag alone left a reader with no way to
	// tell an offers-only pass's own "Outcome=committed" apart from a
	// real one.
	// Exact-label match via ordinary Search (NOT a caller-sourced hint --
	// that short-circuits BEFORE the decision-stage call this test
	// exercises, via FinalizeExactResolutionWithBasis, resolve.go's own
	// AnyCallerSourced branch). Same fixture shape as
	// TestResolveSubjectsWrongSubjectControlNeverCommitsTheDecoy.
	target := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 0.4, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Ask Dev": {target}}}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	request := testRequest()

	resolution, _, err := ResolveSubjects(contextfabric.WithOffersOnlyResolution(context.Background()), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Ask Dev"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	events := tracer.eventsForStage("decision")
	if len(events) == 0 {
		t.Fatal("no decision event traced -- fixture must reach a real commit/no-commit decision for this test to mean anything")
	}
	last := events[len(events)-1]
	if last.Outcome != "committed" {
		t.Fatalf("decision event Outcome = %q, want committed (control: the exact-hint fixture must actually decide to commit)", last.Outcome)
	}
	if !last.OfferedUnderWindowGate {
		t.Fatalf("decision event %#v: OfferedUnderWindowGate = false, want true under offers-only mode", last)
	}
	// graphrank.ResolveSubjects itself still returns whatever it committed
	// -- the DISCARD is an engine-level guarantee (chaos4234_offers_only.go's
	// gatedOfferMaterial), proven separately by the contextfabric-package
	// gate tests (e.g. TestCHAOS4234_ClassDefaultGate_ComposesKindAndHandleOffersBesideTheWindowOffer's
	// fake graph, whose ResolveSubjects returns a committed subject the
	// engine result never carries). This test's own scope is narrower: the
	// TAG on the event that would otherwise be misread as a real commit.
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "project_ask_dev" {
		t.Fatalf("resolution.Committed = %#v, want the exact-label match (control: this graphrank-level call genuinely committed, which is exactly why the tag matters)", resolution.Committed)
	}

	// Control: the SAME fixture decisive (no mark) must NOT carry the tag.
	decisiveTracer := &captureResolutionTracer{}
	deps.ResolutionTracer = decisiveTracer
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Ask Dev"), deps, nil, nil); err != nil {
		t.Fatalf("decisive ResolveSubjects() error = %v", err)
	}
	decisiveEvents := decisiveTracer.eventsForStage("decision")
	if len(decisiveEvents) == 0 || decisiveEvents[len(decisiveEvents)-1].OfferedUnderWindowGate {
		t.Fatalf("decisive decision event = %#v, want OfferedUnderWindowGate=false", decisiveEvents)
	}
}

func TestCHAOS4234_DiscardableDecisionTracer_DiscardsRankedCutWithItsDecision(t *testing.T) {
	t.Parallel()
	real := &captureResolutionTracer{}
	scoped := &discardableDecisionTracer{real: real}
	scoped.Trace(ResolutionTraceEvent{Stage: "ranked_cut", Rank: 1, Survived: true})
	scoped.Trace(ResolutionTraceEvent{Stage: "decision", Outcome: "ambiguous"})
	scoped.Trace(ResolutionTraceEvent{Stage: "search", TermHash: "passes-through"})
	if got := real.snapshot(); len(got) != 1 || got[0].Stage != "search" {
		t.Fatalf("real tracer saw %#v before keep(), want only the pass-through search event", got)
	}
	scoped.keep()
	got := real.snapshot()
	if len(got) != 3 || got[1].Stage != "ranked_cut" || got[2].Stage != "decision" {
		t.Fatalf("real tracer saw %#v after keep(), want the ranked_cut batch replayed BEFORE its decision", got)
	}
}

func lastEventForStage(tracer *captureResolutionTracer, stage string) (ResolutionTraceEvent, bool) {
	events := tracer.eventsForStage(stage)
	if len(events) == 0 {
		return ResolutionTraceEvent{}, false
	}
	return events[len(events)-1], true
}
