package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func aliasIdentityCandidate(kind contextfabric.SubjectKind, id, term string, mechanism contextfabric.MatchMechanism) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		Subject:         contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id},
		MatchedTerms:    []string{term},
		MatchMechanisms: []contextfabric.MatchMechanism{mechanism},
		Confidence:      1,
	}
}

func TestRecordIdentityClaim_NilMapsNoOp(t *testing.T) {
	t.Parallel()
	// Must not panic when both maps are nil (the SearchQuestion call site's
	// convention).
	recordIdentityClaim(aliasIdentityCandidate(contextfabric.SubjectRepository, "r1", "foo", contextfabric.MatchAlias), nil, nil)
}

func TestRecordIdentityClaim_IgnoresNonIdentityMechanisms(t *testing.T) {
	t.Parallel()
	claimants := identityClaimants{}
	terms := identityMatchTerms{}
	recordIdentityClaim(aliasIdentityCandidate(contextfabric.SubjectRepository, "r1", "foo", contextfabric.MatchLexical), claimants, terms)
	if len(claimants) != 0 || len(terms) != 0 {
		t.Fatalf("recordIdentityClaim recorded a non-identity mechanism: claimants=%v terms=%v", claimants, terms)
	}
}

func TestRecordIdentityClaim_NormalizesTermAndRecordsClass(t *testing.T) {
	t.Parallel()
	claimants := identityClaimants{}
	terms := identityMatchTerms{}
	c := aliasIdentityCandidate(contextfabric.SubjectRepository, "r1", "  Dev-Health-Ops  ", contextfabric.MatchAlias)
	recordIdentityClaim(c, claimants, terms)

	key := SubjectKey(c.Subject)
	if !claimants[identityKeyClassAlias]["dev-health-ops"][key] {
		t.Fatalf("claimants = %+v, want normalized term %q recorded under the alias class for %q", claimants, "dev-health-ops", key)
	}
	if len(terms[key]) != 1 || terms[key][0] != (identityMatchTermEntry{class: identityKeyClassAlias, term: "dev-health-ops"}) {
		t.Fatalf("terms[%q] = %+v, want one alias entry for the normalized term", key, terms[key])
	}
}

// TestIdentityCollision_UniqueSingleClaimantIsSafe is the base case: one
// candidate, one claimant for its term -- no collision.
func TestIdentityCollision_UniqueSingleClaimantIsSafe(t *testing.T) {
	t.Parallel()
	claimants := identityClaimants{}
	terms := identityMatchTerms{}
	repo := aliasIdentityCandidate(contextfabric.SubjectRepository, "r1", "chaos-ops", contextfabric.MatchAlias)
	recordIdentityClaim(repo, claimants, terms)

	if identityCollision(SubjectKey(repo.Subject), claimants, terms) {
		t.Fatal("identityCollision = true for a genuinely unique claimant")
	}
}

// TestIdentityCollision_TwoClaimantsSameClassSameTermCollide is HIGH-5's
// core scenario: a repository and a team both claim the same bare-name
// alias -- both must read as colliding, even though only the repository is
// commit-eligible.
func TestIdentityCollision_TwoClaimantsSameClassSameTermCollide(t *testing.T) {
	t.Parallel()
	claimants := identityClaimants{}
	terms := identityMatchTerms{}
	repo := aliasIdentityCandidate(contextfabric.SubjectRepository, "r1", "chaos", contextfabric.MatchAlias)
	team := aliasIdentityCandidate(contextfabric.SubjectTeam, "t1", "chaos", contextfabric.MatchAlias)
	recordIdentityClaim(repo, claimants, terms)
	recordIdentityClaim(team, claimants, terms)

	if !identityCollision(SubjectKey(repo.Subject), claimants, terms) {
		t.Fatal("identityCollision = false, want true -- a team claims the same alias")
	}
	if !identityCollision(SubjectKey(team.Subject), claimants, terms) {
		t.Fatal("identityCollision = false for the team's own side of the same collision")
	}
}

// TestIdentityCollision_DifferentKeyClassesDoNotCollide is MEDIUM-C: a
// same-STRING coincidence across classes (one candidate's alias vs a
// different candidate's provider-variant) must not be conflated with a
// same-class collision.
func TestIdentityCollision_DifferentKeyClassesDoNotCollide(t *testing.T) {
	t.Parallel()
	claimants := identityClaimants{}
	terms := identityMatchTerms{}
	repo := aliasIdentityCandidate(contextfabric.SubjectRepository, "r1", "chaos", contextfabric.MatchAlias)
	other := aliasIdentityCandidate(contextfabric.SubjectRepository, "r2", "chaos", contextfabric.MatchProviderKey)
	recordIdentityClaim(repo, claimants, terms)
	recordIdentityClaim(other, claimants, terms)

	if identityCollision(SubjectKey(repo.Subject), claimants, terms) {
		t.Fatal("identityCollision = true across DIFFERENT key classes on the same string, want false -- classes are counted separately")
	}
}

// TestIdentityCollision_BoundToProducingTermNotAnyTerm is MEDIUM-B: a
// candidate that matched an ADDITIONAL, unrelated, genuinely-unique term
// must still read as colliding on the strength of its OTHER, colliding
// term -- "collides on A, unique on B" must not fast-path.
func TestIdentityCollision_BoundToProducingTermNotAnyTerm(t *testing.T) {
	t.Parallel()
	claimants := identityClaimants{}
	terms := identityMatchTerms{}
	repo := aliasIdentityCandidate(contextfabric.SubjectRepository, "r1", "chaos-ops", contextfabric.MatchAlias)
	teamCollision := aliasIdentityCandidate(contextfabric.SubjectTeam, "t1", "chaos-ops", contextfabric.MatchAlias)
	recordIdentityClaim(repo, claimants, terms)
	recordIdentityClaim(teamCollision, claimants, terms)
	// The SAME repo subject also matched a second, unrelated, genuinely
	// unique term ("dev-health-acr") via a separate call -- MergeCandidates
	// would union this into the SAME subject's MatchedTerms in the real
	// pipeline; here we simulate that by recording a second claim under the
	// SAME subject key directly.
	repoUniqueTerm := aliasIdentityCandidate(contextfabric.SubjectRepository, "r1", "dev-health-acr", contextfabric.MatchAlias)
	recordIdentityClaim(repoUniqueTerm, claimants, terms)

	if !identityCollision(SubjectKey(repo.Subject), claimants, terms) {
		t.Fatal("identityCollision = false, want true -- candidate collides on term A even though it is also uniquely matched on unrelated term B")
	}
}

// TestIdentityCollision_NoIdentityTermsIsSafe pins spot-check item 1's own
// requirement: a candidate this mechanism never touched (empty terms[key])
// must never read as colliding -- nothing here may suppress a commit path
// this ticket did not touch (e.g. an ordinary lexical/vector CHAOS-3829
// rescue candidate).
func TestIdentityCollision_NoIdentityTermsIsSafe(t *testing.T) {
	t.Parallel()
	claimants := identityClaimants{}
	terms := identityMatchTerms{}
	if identityCollision("kind\x00unrelated_subject", claimants, terms) {
		t.Fatal("identityCollision = true for a subject with no recorded identity match terms at all")
	}
}
