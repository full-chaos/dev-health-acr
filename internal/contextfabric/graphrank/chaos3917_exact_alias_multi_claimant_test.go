package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3917 reproduces a live measurement defect (gen-trial-
// chaos3896-sliceC-run1-20260819T124758Z.json, corpus case index 45,
// resolved_active_epoch=1): under epoch-1 identity healing, a single
// caller-typed term simultaneously matched one subject's own label EXACTLY
// (a "project", mechanism=exact, confidence=1) and, independently, TWO
// OTHER subjects' aliases (two "repository" claimants, mechanism=alias,
// confidence=1, both FromKeyedIdentityLookup-trusted). resolution.go's
// exactIndex fast path (the CHAOS-3810 override) commits whenever exactly
// one RETAINED candidate has Confidence==1 && MatchExact -- it never
// consults the identity/identityTerms side channel at all, so it cannot
// see that the SAME literal term is ALSO claimed by two different
// canonical subjects via the alias mechanism. The live measurement graded
// this WRONG (a project committed when the caller's own corpus annotation
// names a different subject), and shadow.ran=false confirms the census
// safety net never even ran (the pool was not ambiguous, because the
// exact-index path had already committed).
//
// exactLabelCandidate/repoAliasCandidate/identityBySubject/
// identitySideChannels mirror chaos3884_identity_resolution_test.go's own
// helpers -- this is the SAME direct resolution.go-level harness that
// ticket's own regression tests already use, extended with an exact-label
// candidate so the two identity classes (label vs alias) can collide in
// one pool.
func exactLabelCandidate(kind contextfabric.SubjectKind, id, label, term string) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: "receipt_" + id,
		Subject:   contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: label},
		State:     contextfabric.ResolutionProposed, Confidence: 1,
		MatchedTerms: []string{term}, MatchReasons: []string{"Exact label match."},
		MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchExact},
	}
}

// TestResolveFromMergedCandidatesWithGate_ExactLabelNeverCommitsOverACollidingAliasClaimant
// is the direct, resolution.go-level reproduction of case 45's commit
// shape: an exact-label candidate (a "project") is the sole exactIndex
// member, but the SAME term is also claimed by two alias-eligible
// candidates (two "repositories") -- a genuine multi-claimant term the
// caller's own literal text cannot disambiguate. Pre-fix this wrongly
// commits the project (exact_index fires unconditionally on
// len(exactIndex)==1, never checking the identity side channel at all).
// Post-fix, nothing may commit: this is exactly the "no fast-path commit
// without a complete, unique claimant enumeration" proof CHAOS-3917
// ratifies, applied to the exact-label path for the first time.
func TestResolveFromMergedCandidatesWithGate_ExactLabelNeverCommitsOverACollidingAliasClaimant(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	project := exactLabelCandidate(contextfabric.SubjectProject, "proj1", term, term)
	repoA := repoAliasCandidate("repoA", term)
	repoB := repoAliasCandidate("repoB", term)
	identity, terms := identitySideChannels(project, repoA, repoB)

	resolution := ResolveFromMergedCandidatesWithGate(
		identityBySubject(project, repoA, repoB),
		map[string]string{}, map[string]bool{}, 10, true,
		true, /* searchTruncated: case 45's own receipt shape (wired_search_truncated=true) */
		nil, 0, false, 10, 20, true,
		DefaultCommitGatePolicy(), identity, terms,
		true, /* aliasIdentityComplete: the identity-universe read itself was complete -- the defect is NOT a completeness gap, it is exactIndex never consulting completeness OR collision at all */
		nil, "", "",
	)

	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING committed -- an exact-label match must not bypass a colliding alias claimant on the SAME term (CHAOS-3917 case 45)", resolution.Committed)
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("resolution.ClarificationPrompt is empty for a genuinely multi-claimant term")
	}
}

// TestResolveFromMergedCandidatesWithGate_ExactLabelAloneStillCommits is the
// non-regression control this fix must not break: an exact-label match with
// NO colliding claimant of any class commits exactly as before (CHAOS-3810
// unchanged for the ordinary, non-colliding case).
func TestResolveFromMergedCandidatesWithGate_ExactLabelAloneStillCommits(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	project := exactLabelCandidate(contextfabric.SubjectProject, "proj1", term, term)
	identity, terms := identitySideChannels(project)

	resolution := ResolveFromMergedCandidatesWithGate(
		identityBySubject(project),
		map[string]string{}, map[string]bool{}, 10, true, true, /* searchTruncated: CHAOS-3810's own truncation-immunity case */
		nil, 0, false, 10, 20, true,
		DefaultCommitGatePolicy(), identity, terms, true, nil, "", "",
	)

	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "proj1" {
		t.Fatalf("resolution.Committed = %#v, want proj1 committed via the exact-label override, unaffected by CHAOS-3917's new rival check when no rival exists", resolution.Committed)
	}
}

// TestResolveSubjects_ExactAliasMultiClaimantEndToEnd is the FULL
// ResolveSubjects pipeline reproduction of case 45 -- proving resolve.go's
// own wiring (the AliasLookup merge call site, recordIdentityClaim, the
// commit-gate call) produces the same fixed outcome an isolated
// resolution.go-level test already proves the decision logic supports.
// Mirrors the live receipt's own shape: ONE AliasLookup call returns THREE
// claimants for a single term -- a project whose OWN label equals the term
// (matched=true, NodeCandidate's exact-label check, mechanism=exact) and
// two repositories whose alias attribute equals the term
// (FromKeyedIdentityLookup=true + eligible kind, mechanism=alias,
// confidence=1 via the identity-trust bump) -- byte-shape-identical to
// gen-trial-chaos3896-sliceC-run1-20260819T124758Z.json case index 45
// (identity_universe_calls=1, identity_matched_rows=3).
func TestResolveSubjects_ExactAliasMultiClaimantEndToEnd(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	projectNode := aliasCandidateNode(contextfabric.SubjectProject, "proj1", term, -1, nil, nil, false)
	repoNodeA := aliasCandidateNode(contextfabric.SubjectRepository, "repoA", "owner/widget-service-a", -1, []string{term}, nil, true)
	repoNodeB := aliasCandidateNode(contextfabric.SubjectRepository, "repoB", "owner/widget-service-b", -1, []string{term}, nil, true)
	backend := &fakeGraphBackend{
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{term: {projectNode, repoNodeA, repoNodeB}},
		aliasLookupComplete:  true,
	}

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING committed end-to-end -- case 45's own wrong-commit shape (a project committed via exact-label match despite two colliding repository alias claimants on the identical term)", resolution.Committed)
	}
}

// TestResolveFromMergedCandidatesWithGate_EvidenceCensusRefusesOnACrossClassRivalClaimant
// is codex xhigh review's own confirmed P1 (task-mt05idj8-bxvfac,
// 2026-08-19): resolution.go's evidence_census path (CHAOS-3896 Slice C,
// design brief v6 §1.4) is a SIXTH commit path resolve.go re-enters
// ResolveFromMergedCandidatesWithGate through (resolve.go's second call
// site, passing evidenceCensusAttestedKey), reusing the SAME
// identity/identityTerms maps the first pass already populated -- before
// this fix it checked identityCollision only, not
// identityCrossClassRivalClaimant, so a candidate the first pass correctly
// left ambiguous BECAUSE of a visible cross-class rival (exactly case 45's
// own shape) could still be census-attested and wrongly committed on the
// second pass, since nothing in that block re-consulted the rival at all.
//
// Isolated at the resolution.go level (not a full ResolveSubjects
// end-to-end call): correctly wiring runShadowEvidenceRoundForResolution's
// own shadow-round preconditions (handle-grammar binding, per-kind census
// enumeration, NonCensusedSurvivor) for an exact-label-shaped candidate is
// its own undertaking, orthogonal to what this fix touches -- calling
// ResolveFromMergedCandidatesWithGate directly with
// evidenceCensusAttestedKey already set (exactly what resolve.go's own
// second call site does) exercises the EXACT switch branch codex flagged,
// with full control over the candidate pool, mirroring
// chaos3884_identity_resolution_test.go's own direct-call testing
// discipline for every other commit-gate branch in this file.
func TestResolveFromMergedCandidatesWithGate_EvidenceCensusRefusesOnACrossClassRivalClaimant(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	project := exactLabelCandidate(contextfabric.SubjectProject, "proj1", term, term)
	repoA := repoAliasCandidate("repoA", term)
	repoB := repoAliasCandidate("repoB", term)
	identity, terms := identitySideChannels(project, repoA, repoB)

	resolution := ResolveFromMergedCandidatesWithGate(
		identityBySubject(project, repoA, repoB),
		map[string]string{}, map[string]bool{}, 10, true,
		true, /* searchTruncated: required for the evidence_census block to even be reachable in production (resolve.go only re-enters when the first pass left it ambiguous under truncation) */
		nil, 0, false, 10, 20, true,
		DefaultCommitGatePolicy(), identity, terms, true, nil, "",
		SubjectKey(project.Subject), /* evidenceCensusAttestedKey: a census witness names the SAME exact-label candidate the rival check must still block */
	)

	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING committed -- the census attestation must not override a known cross-class rival claimant on the SAME term (CHAOS-3917, codex xhigh finding P1)", resolution.Committed)
	}
}
