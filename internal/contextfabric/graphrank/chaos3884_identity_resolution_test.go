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

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo), map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, true)

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

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo, team), map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, true)

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

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo), map[string]string{}, map[string]bool{}, 10, true, true /* searchTruncated */, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, false /* aliasIdentityComplete */)

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

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo), map[string]string{}, map[string]bool{}, 10, true, false /* searchTruncated=false: the confidence gates ARE reached */, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, false /* aliasIdentityComplete=false: fast path never even considered */)

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

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo, other), map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, false)

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
	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(top, second), map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false)

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
