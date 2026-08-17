package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// identityBySubject builds a candidatesBySubject map from candidates,
// keying each by SubjectKey -- the shape ResolveFromMergedCandidatesWithGate
// expects.
func identityBySubject(candidates ...contextfabric.SubjectCandidate) map[string]contextfabric.SubjectCandidate {
	m := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, c := range candidates {
		m[SubjectKey(c.Subject)] = c
	}
	return m
}

// identitySideChannels replays recordIdentityClaim for each candidate,
// producing the same identity/identityTerms maps mergeSearchResults would
// have built during a real merge -- lets these tests exercise
// ResolveFromMergedCandidatesWithGate directly (full control over the
// candidate pool) while still using the REAL side-channel-population code
// path, not a hand-rolled substitute.
func identitySideChannels(candidates ...contextfabric.SubjectCandidate) (identityClaimants, identityMatchTerms) {
	claimants := identityClaimants{}
	terms := identityMatchTerms{}
	for _, c := range candidates {
		recordIdentityClaim(c, claimants, terms)
	}
	return claimants, terms
}

func repoAliasCandidate(id, term string) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: "receipt_" + id,
		Subject:   contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: id, Label: "owner/" + id},
		State:     contextfabric.ResolutionProposed, Confidence: 1,
		MatchedTerms: []string{term}, MatchReasons: []string{"Repository/project alias matched."},
		MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchAlias},
	}
}

func teamAliasCandidate(id, term string) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: "receipt_" + id,
		Subject:   contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: id, Label: id},
		State:     contextfabric.ResolutionProposed, Confidence: 0.5,
		MatchedTerms: []string{term}, MatchReasons: []string{"Repository/project alias matched."},
		MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchAlias},
	}
}

// noiseCandidate is an ordinary, unrelated candidate (no identity
// mechanism) used to prove identityCollision never touches a population
// this ticket does not concern.
func noiseCandidate(id string, confidence float64) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: "receipt_" + id,
		Subject:   contextfabric.SubjectRef{Kind: contextfabric.SubjectKind("ci_pipeline_run"), CanonicalID: id, Label: id},
		State:     contextfabric.ResolutionProposed, Confidence: confidence,
		MatchedTerms: []string{"chaos"}, MatchReasons: []string{"Hybrid graph search matched the subject label or indexed context."},
		MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchLexical},
	}
}

// TestResolveFromMergedCandidatesWithGate_UniqueIdentityCommitsAlone is the
// identity fast-path positive case: a single eligible, uniquely-claimed
// alias candidate commits even though it is alone in the pool (no ordinary
// search noise at all).
func TestResolveFromMergedCandidatesWithGate_UniqueIdentityCommitsAlone(t *testing.T) {
	t.Parallel()
	repo := repoAliasCandidate("r1", "chaos-ops")
	identity, terms := identitySideChannels(repo)

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo), map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, true, nil, "")

	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "r1" {
		t.Fatalf("resolution.Committed = %#v, want r1 committed via the identity fast path", resolution.Committed)
	}
}

// TestResolveFromMergedCandidatesWithGate_TwoClaimantsNeverCommit is the
// collision-as-normal proof: a repository and a team both claim the same
// alias -- neither commits, and both survive to the clarification prompt.
func TestResolveFromMergedCandidatesWithGate_TwoClaimantsNeverCommit(t *testing.T) {
	t.Parallel()
	repo := repoAliasCandidate("r1", "chaos")
	team := teamAliasCandidate("t1", "chaos")
	identity, terms := identitySideChannels(repo, team)

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo, team), map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, true, nil, "")

	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING committed -- a genuine collision must clarify, never silently pick", resolution.Committed)
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("resolution.ClarificationPrompt is empty, want both claimants offered")
	}
}

// TestResolveFromMergedCandidatesWithGate_IncompleteLookupNeverCommitsViaIdentity
// pins MEDIUM-7's corrected rationale directly: aliasIdentityComplete=false
// (the lookup was unavailable or degraded) must disable the identity fast
// path even for an otherwise-unique claimant -- and, per this graph's own
// realistic shape (searchTruncated virtually always true), the candidate
// lands in ambiguous, NOT LoneFloor, so this also proves the "degraded
// alias candidates reach LoneFloor" v3 claim was false.
func TestResolveFromMergedCandidatesWithGate_IncompleteLookupNeverCommitsViaIdentity(t *testing.T) {
	t.Parallel()
	repo := repoAliasCandidate("r1", "chaos-ops")
	identity, terms := identitySideChannels(repo)

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo), map[string]string{}, map[string]bool{}, 10, true, true /* searchTruncated */, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, false /* aliasIdentityComplete */, nil, "")

	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want nothing committed when aliasIdentityComplete=false", resolution.Committed)
	}
}

// TestResolveFromMergedCandidatesWithGate_IdentityCollisionBlocksLoneFloor
// is spot-check item 1's own core regression proof: WITHOUT the
// identityCollision guard on the LoneFloor case, a colliding candidate that
// is nonetheless the SOLE commitIndex entry (searchTruncated=false, so it
// even reaches the confidence-threshold gates at all) would trivially
// clear LoneFloor on its manufactured confidence=1 -- an existence signal
// (is this claim unique) laundered through a strength gate. This is the
// bug: two candidates COLLIDE (both real, per HIGH-5), but only ONE of them
// (the eligible one) ever entered the pool via a path that reaches
// commitIndex at len==1 -- e.g. the team's own confidence (0.5) fell below
// commitIndex's own eligibility in some other unrelated gate, or (as here)
// we isolate the exact mechanism by putting ONLY the eligible candidate in
// candidatesBySubject while still recording the collision via the identity
// side channel, exactly as a real merge would if the team candidate were
// found (and counted, HIGH-5) via ordinary search but observation-blocked
// or otherwise excluded from commitIndex for an unrelated reason.
func TestResolveFromMergedCandidatesWithGate_IdentityCollisionBlocksLoneFloor(t *testing.T) {
	t.Parallel()
	repo := repoAliasCandidate("r1", "chaos")
	team := teamAliasCandidate("t1", "chaos")
	// Both recorded into the identity side channel (a real collision
	// happened), but only the repo enters candidatesBySubject/commitIndex --
	// isolating whether identityCollision alone (not identityIndex's own
	// len==1 fast-path gate, deliberately bypassed here since
	// aliasIdentityComplete is left false) is what blocks LoneFloor.
	identity, terms := identitySideChannels(repo, team)

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo), map[string]string{}, map[string]bool{}, 10, true, false /* searchTruncated=false: the confidence gates ARE reached */, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, false /* aliasIdentityComplete=false: fast path never even considered */, nil, "")

	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING -- identityCollision must block LoneFloor even though repo is alone in commitIndex with confidence=1", resolution.Committed)
	}
}

// TestResolveFromMergedCandidatesWithGate_IdentityCollisionBlocksTopFloor
// is the top-of-two counterpart: a colliding identity candidate at
// confidence=1 sitting atop an ordinary, unrelated candidate must not clear
// TopFloor/TopGap on the strength of its manufactured 1.0-vs-lower gap.
func TestResolveFromMergedCandidatesWithGate_IdentityCollisionBlocksTopFloor(t *testing.T) {
	t.Parallel()
	repo := repoAliasCandidate("r1", "chaos")
	team := teamAliasCandidate("t1", "chaos")
	other := noiseCandidate("ci1", 0.6)
	identity, terms := identitySideChannels(repo, team)

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo, other), map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, false, nil, "")

	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING -- identityCollision must block the top-of-two gate too", resolution.Committed)
	}
}

// TestResolveFromMergedCandidatesWithGate_IdentityCollisionNeverTouchesOrdinaryCandidates
// is the negative control every identityCollision call site needs: an
// ordinary top-of-two commit with NO identity mechanism anywhere in the
// pool must be completely unaffected by this ticket's new conjuncts.
func TestResolveFromMergedCandidatesWithGate_IdentityCollisionNeverTouchesOrdinaryCandidates(t *testing.T) {
	t.Parallel()
	top := contextfabric.SubjectCandidate{
		ReceiptID: "receipt_top", Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Ask Dev"},
		State: contextfabric.ResolutionProposed, Confidence: 0.95, MatchedTerms: []string{"Ask Dev"},
		MatchReasons: []string{"x"}, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchLexical, contextfabric.MatchVector},
	}
	second := contextfabric.SubjectCandidate{
		ReceiptID: "receipt_second", Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p2", Label: "Ask Dev 2"},
		State: contextfabric.ResolutionProposed, Confidence: 0.5, MatchedTerms: []string{"Ask Dev"},
		MatchReasons: []string{"x"}, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchLexical},
	}
	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(top, second), map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil, "")

	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "p1" {
		t.Fatalf("resolution.Committed = %#v, want p1 committed via the ordinary top-of-two gate, unaffected by nil identity side channels", resolution.Committed)
	}
}

// TestResolveSubjects_AliasLookupWiredEndToEnd exercises the FULL
// ResolveSubjects pipeline (not just ResolveFromMergedCandidatesWithGate
// directly) with a fake AliasLookup dependency, proving resolve.go's own
// wiring (the merge call site, aliasIdentityComplete computation, ordering
// before the question pass) produces the same identity-fast-path commit an
// isolated resolution.go-level test already proves the DECISION logic
// supports.
func TestResolveSubjects_AliasLookupWiredEndToEnd(t *testing.T) {
	t.Parallel()
	repoNode := aliasCandidateNode(contextfabric.SubjectRepository, "r1", "owner/dev-health-acr", -1, []string{"dev-health-acr"}, nil, true)
	backend := &fakeGraphBackend{
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{"dev-health-acr": {repoNode}},
		aliasLookupComplete:  true,
	}
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("dev-health-acr"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "r1" {
		t.Fatalf("resolution.Committed = %#v, want r1 committed end-to-end via AliasLookup", resolution.Committed)
	}
	if len(backend.aliasLookupCalls) != 1 {
		t.Fatalf("aliasLookupCalls = %v, want exactly one AliasLookup call", backend.aliasLookupCalls)
	}
}

// TestResolveSubjects_AliasLookupCommitsViaMatchAliasWhenOrdinarySearchMisses
// closes the direct-assertion gap TestResolveSubjects_AliasLookupWiredEndToEnd
// leaves structural (team-lead review, 2026-08-17, coverage question B): a
// present-axis (testInterpreted's TimeContext.Axis) resolution where
// backend.searchResults has NO entry for the term at all -- ordinary
// exact/lexical/vector retrieval structurally cannot produce a candidate,
// since Search(term) returns nothing -- and the committed candidate's OWN
// MatchMechanisms is asserted DIRECTLY (not inferred) to contain MatchAlias
// and NOT MatchExact. The term ("dev-health-acr") also does not equal the
// subject's own label ("owner/dev-health-acr"), so even if ordinary search
// had returned this node some other way, matched (exact) would still be
// false -- this is the alias-named, non-canonical-term shape the corpus
// coverage question (B) asks whether any real case exercises.
func TestResolveSubjects_AliasLookupCommitsViaMatchAliasWhenOrdinarySearchMisses(t *testing.T) {
	t.Parallel()
	repoNode := aliasCandidateNode(contextfabric.SubjectRepository, "r1", "owner/dev-health-acr", -1, []string{"dev-health-acr"}, nil, true)
	backend := &fakeGraphBackend{
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{"dev-health-acr": {repoNode}},
		aliasLookupComplete:  true,
	}
	interpreted := testInterpreted("dev-health-acr")
	if interpreted.TimeContext.Axis != contextfabric.TemporalCurrent {
		t.Fatalf("testInterpreted's own Axis = %v, want TemporalCurrent -- this test's whole claim depends on being present-axis", interpreted.TimeContext.Axis)
	}
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), interpreted, backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Candidates) != 1 {
		t.Fatalf("resolution.Candidates = %#v, want exactly one -- ordinary search returned nothing for this term, so AliasLookup must be the only source", resolution.Candidates)
	}
	mechanisms := resolution.Candidates[0].MatchMechanisms
	if !HasMechanism(mechanisms, contextfabric.MatchAlias) {
		t.Fatalf("mechanisms = %v, want MatchAlias present", mechanisms)
	}
	if HasMechanism(mechanisms, contextfabric.MatchExact) {
		t.Fatalf("mechanisms = %v, want MatchExact ABSENT -- term %q does not equal label %q", mechanisms, "dev-health-acr", "owner/dev-health-acr")
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "r1" {
		t.Fatalf("resolution.Committed = %#v, want r1 committed via the MatchAlias mechanism asserted above", resolution.Committed)
	}
}

// TestResolveSubjects_TracerObservesAliasLookupReachedThroughWiredComposition
// is the DURABLE reachability proof (team-lead ruling, 2026-08-17): unlike
// TestResolveSubjects_AliasLookupCommitsViaMatchAliasWhenOrdinarySearchMisses
// (cd482b3), which calls NodeCandidate/ResolveSubjects directly and proves
// the MATCHING logic is correct in isolation, this test proves the WIRED
// COMPOSITION itself reaches deps.AliasLookup -- reading that fact from the
// ResolutionTracer's own "alias_lookup" stage event, not from an assertion
// about the RESULT. A future composition change that accidentally stops
// wiring AliasLookup through (the exact "dead path behind a passing green
// unit test" failure mode this ticket was built to catch) would leave this
// event never firing, failing this test even if every OTHER resolution
// outcome still looked plausible.
func TestResolveSubjects_TracerObservesAliasLookupReachedThroughWiredComposition(t *testing.T) {
	t.Parallel()
	repoNode := aliasCandidateNode(contextfabric.SubjectRepository, "r1", "owner/dev-health-acr", -1, []string{"dev-health-acr"}, nil, true)
	backend := &fakeGraphBackend{
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{"dev-health-acr": {repoNode}},
		aliasLookupComplete:  true,
	}
	tracer := &captureResolutionTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	request := testRequest()
	request.RequestID = "request_reachability_diag"

	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("dev-health-acr"), deps)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "r1" {
		t.Fatalf("resolution.Committed = %#v, want r1 committed", resolution.Committed)
	}

	aliasLookupEvents := tracer.eventsForStage("alias_lookup")
	if len(aliasLookupEvents) != 1 {
		t.Fatalf("alias_lookup stage events = %d, want exactly 1 -- the wired composition never reached deps.AliasLookup (C1==0: dead-path wiring regression)", len(aliasLookupEvents))
	}
	event := aliasLookupEvents[0]
	if event.RequestID != request.RequestID {
		t.Errorf("alias_lookup event RequestID = %q, want %q", event.RequestID, request.RequestID)
	}
	if !event.AliasLookupComplete {
		t.Error("alias_lookup event AliasLookupComplete = false, want true")
	}
	if event.AliasLookupMatchedClaimants != 1 {
		t.Errorf("alias_lookup event AliasLookupMatchedClaimants = %d, want 1 (C2: the match count)", event.AliasLookupMatchedClaimants)
	}

	decisionEvents := tracer.eventsForStage("decision")
	if len(decisionEvents) != 1 || decisionEvents[0].Outcome != "committed" {
		t.Fatalf("decision events = %#v, want exactly one committed", decisionEvents)
	}

	identityGateEvents := tracer.eventsForStage("identity_gate")
	if len(identityGateEvents) != 1 {
		t.Fatalf("identity_gate events = %d, want exactly 1", len(identityGateEvents))
	}
	gate := identityGateEvents[0]
	if !gate.FromKeyedIdentityLookup || !gate.EligibleKind || !gate.GateFired || gate.FinalConfidence != 1 {
		t.Errorf("identity_gate event = %#v, want FromKeyedIdentityLookup/EligibleKind/GateFired all true and FinalConfidence 1 -- r1 committed via the identity-trust bump", gate)
	}
}

// TestResolveSubjects_TracerObservesIdentityTrustBoostDespiteStaleGraphAttribute
// is guardrail 3(b) (team-lead ruling, 2026-08-17): the WIRED-composition
// counterpart to TestNodeCandidate_IdentityTrustedAloneBoostsConfidenceDespiteAStaleGraphAttribute
// (candidate.go, direct-call unit level) -- proves end-to-end, through the
// real ResolveSubjects composition and read from the ResolutionTracer (not
// asserted against the result alone), that the identity-trust confidence
// boost fires when ordinary search finds NOTHING for the term (backend.searchResults
// has no entry) AND the claimant node's own graph attributes do not contain
// the alias (aliasCandidateNode's aliases param is nil, simulating a node
// projected before this ticket's alias-computation logic landed). This is
// the standing guard that would have caught the live-reproduced
// projection-lag bug at composition level, not just in isolation.
func TestResolveSubjects_TracerObservesIdentityTrustBoostDespiteStaleGraphAttribute(t *testing.T) {
	t.Parallel()
	// aliases: nil -- the graph's OWN stored attribute is stale/absent,
	// unlike the fresh-attribute repoNode the sibling reachability test
	// above uses.
	staleRepoNode := aliasCandidateNode(contextfabric.SubjectRepository, "r1", "owner/dev-health-acr", -1, nil, nil, true)
	staleRepoNode.Mechanism = contextfabric.MatchAlias
	backend := &fakeGraphBackend{
		// No entry for "dev-health-acr": ordinary search finds NOTHING for
		// this term, isolating the identity-trust path exactly as the
		// direct-call unit test does.
		searchResults:        map[string][]CandidateNode{},
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{"dev-health-acr": {staleRepoNode}},
		aliasLookupComplete:  true,
	}
	tracer := &captureResolutionTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer

	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("dev-health-acr"), deps)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "r1" {
		t.Fatalf("resolution.Committed = %#v, want r1 committed via the identity-trust bump despite the stale graph attribute", resolution.Committed)
	}

	corroborationEvents := tracer.eventsForStage("corroboration")
	if len(corroborationEvents) != 1 {
		t.Fatalf("corroboration events = %d, want exactly 1", len(corroborationEvents))
	}
	if corroborationEvents[0].BaseConfidence != 1 || corroborationEvents[0].FinalConfidence != 1 {
		t.Fatalf("corroboration event base/final confidence = %v/%v, want 1/1 -- identityTrusted alone, not a re-derivation against the stale graph attribute, must be what set the base", corroborationEvents[0].BaseConfidence, corroborationEvents[0].FinalConfidence)
	}

	decisionEvents := tracer.eventsForStage("decision")
	if len(decisionEvents) != 1 || decisionEvents[0].Outcome != "committed" {
		t.Fatalf("decision events = %#v, want exactly one committed", decisionEvents)
	}

	// THE before/after story guardrail 6 asked for, read from the REAL gate
	// inputs (not a proxy): FromKeyedIdentityLookup=true (the identity read
	// verified this claimant), AliasMatched=false (the graph's OWN
	// attribute is stale -- aliasCandidateNode's aliases param is nil),
	// GateFired=true anyway (the fix), FinalConfidence=1. Pre-fix this same
	// event would have read GateFired=false, FinalConfidence=0.5 -- the
	// exact bug, visible in the trace instead of hidden by a collapsed bool.
	identityGateEvents := tracer.eventsForStage("identity_gate")
	if len(identityGateEvents) != 1 {
		t.Fatalf("identity_gate events = %d, want exactly 1", len(identityGateEvents))
	}
	gate := identityGateEvents[0]
	if !gate.FromKeyedIdentityLookup {
		t.Error("identity_gate event FromKeyedIdentityLookup = false, want true")
	}
	if gate.AliasMatched {
		t.Error("identity_gate event AliasMatched = true, want false -- the graph's own attribute is deliberately stale in this test")
	}
	if !gate.GateFired {
		t.Error("identity_gate event GateFired = false, want true -- this is the fix: identityTrusted alone must fire the gate despite AliasMatched=false")
	}
	if gate.FinalConfidence != 1 {
		t.Errorf("identity_gate event FinalConfidence = %v, want 1", gate.FinalConfidence)
	}
}

// TestResolveSubjects_AliasLookupErrorAbortsResolution mirrors Search()'s
// own error handling: a genuine backend fault (as opposed to a
// completeness gap) must abort the whole resolution, not silently degrade.
func TestResolveSubjects_AliasLookupErrorAbortsResolution(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{enableAliasLookup: true, aliasLookupErr: context.DeadlineExceeded}
	_, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("dev-health-acr"), backend.deps())
	if err == nil {
		t.Fatal("ResolveSubjects() error = nil, want the AliasLookup error propagated")
	}
}

// TestResolveSubjects_GraphMissingSiblingNeverCommitsViaIdentityTrust is
// decision 1's own pin (team-lead amendment, 2026-08-17, settled):
// completeness is proven over the TABLE set, but identityCollision
// (resolution.go) counts the CANDIDATE set -- a claimant that fails
// falkorgraph's existence check silently vanishes from that count, so a
// surviving sibling's confidence=1 identity-trust bump (NodeCandidate's
// identityTrusted, gated on FromKeyedIdentityLookup alone) would otherwise
// be independent of aliasIdentityComplete and could clear LoneFloor on a
// claim never actually proven unique. reader.go's fix strips
// FromKeyedIdentityLookup from every survivor of a call that saw ANY
// graph-missing claimant -- this test constructs EXACTLY what that fixed
// closure now hands to ResolveSubjects (repo alone, fromKeyedLookup=false,
// aliasLookupComplete=false) and asserts it does not commit. Deliberately
// isolated from live full-text search (searchResults empty for the term)
// so nothing OTHER than the identity mechanism can explain the outcome --
// see chaos3884_identity_lookup_live_test.go's own doc comment for why a
// live version of this exact scenario is confounded and not attempted.
func TestResolveSubjects_GraphMissingSiblingNeverCommitsViaIdentityTrust(t *testing.T) {
	t.Parallel()
	repoNode := aliasCandidateNode(contextfabric.SubjectRepository, "r1", "owner/chaos-ops", -1, []string{"chaos-ops"}, nil, false /* fromKeyedLookup=false: reader.go's post-fix demotion */)
	backend := &fakeGraphBackend{
		searchResults:        map[string][]CandidateNode{"chaos-ops": {}},
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{"chaos-ops": {repoNode}},
		aliasLookupComplete:  false, // graphMissing > 0 somewhere in this call
	}
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("chaos-ops"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING -- a demoted (fromKeyedLookup=false) claimant must not commit on the strength of an identity-trust bump it no longer carries", resolution.Committed)
	}
}

// TestResolveSubjects_NilAliasLookupIsByteIdenticalToBeforeThisTicket is the
// backward-compatibility proof every optional ResolveDeps field carries: a
// backend that never sets AliasLookup gets exactly the pre-CHAOS-3884
// resolution.
func TestResolveSubjects_NilAliasLookupIsByteIdenticalToBeforeThisTicket(t *testing.T) {
	t.Parallel()
	node := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 0.9, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Ask Dev": {node}}}
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "project_ask_dev" {
		t.Fatalf("resolution.Committed = %#v, want the ordinary exact-match commit, unaffected by AliasLookup being unset", resolution.Committed)
	}
}
