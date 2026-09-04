package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Admitting group subjects opened a hole that label binding does not close and
// that the existing claim-scoping rule cannot see.
//
// requireGroundedClaims binds a cited claim to the citing driver's OWN
// AffectedSubjects. That is a per-subject rule, and the allow-sets are global,
// so a driver may legally name Team A AND a project that belongs to Team B,
// then cite Team B's project claim: every subject is in scope, the claim is
// grounded in a real canonical fact, and the claim's subject IS one of the
// driver's own affected subjects. Each part is valid and the combination
// asserts something false -- that Team B's project is Team A's business.
//
// Nothing before this rule looked at the cohort's own group membership, which
// is the only place the truth lives.
func crossGroupFixture() (SynthesisInput, SynthesisDraft, SubjectRef, SubjectRef) {
	input, draft, _ := groupedCohortFixture()
	teamA := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "Team A"}
	teamB := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_b", Label: "Team B"}
	projectA := input.Graph.Resolution.Committed[0] // already a cohort member
	projectB := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ops", Label: "Ops"}

	cohort := *input.Graph.Cohort
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{Subject: teamA, MemberCanonicalIDs: []string{projectA.CanonicalID}, Complete: true, Total: 1},
		{Subject: teamB, MemberCanonicalIDs: []string{projectB.CanonicalID}, Complete: true, Total: 1},
	}
	input.Graph.Cohort = &cohort

	// A real, grounded canonical fact about project B.
	input.Facts.Facts = append(input.Facts.Facts, CanonicalFact{
		Kind: FactReadiness, Subject: projectB,
		Fields:         map[string]FactValue{"release_ready": BooleanFactValue(false)},
		EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable,
		Source: "ops", SourceVersion: "v1",
	})
	draft.ClaimedFacts = []ClaimedFact{{
		ClaimID: "claim_readiness_1", Kind: FactReadiness, Subject: projectB,
		Field: "release_ready", Value: boolScalar(false),
	}}
	draft.Drivers[0].Category = "readiness"
	draft.Drivers[0].ClaimedFactIDs = []string{"claim_readiness_1"}
	return input, draft, teamA, projectB
}

// TestDriverNamingAGroupMayNotCiteAnotherGroupsMember is the pinning test for
// the hole. Every individual check passes; the assertion is false.
func TestDriverNamingAGroupMayNotCiteAnotherGroupsMember(t *testing.T) {
	t.Parallel()
	input, draft, teamA, projectB := crossGroupFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{teamA, projectB}

	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatal("ValidateAgainst() = nil -- a driver about Team A citing Team B's project presents one team's data as another's")
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverGroupMemberForeign {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverGroupMemberForeign)
	}
}

// TestDriverNamingAGroupMayCiteItsOwnMember is the other half: the rule must
// be a MEMBERSHIP check, not a ban on group-scoped drivers, which are the
// whole point of a grouped answer.
func TestDriverNamingAGroupMayCiteItsOwnMember(t *testing.T) {
	t.Parallel()
	input, draft, teamA, _ := crossGroupFixture()
	projectA := input.Graph.Resolution.Committed[0]
	// Re-point the claim at team A's OWN member.
	draft.ClaimedFacts[0].Subject = projectA
	draft.Drivers[0].AffectedSubjects = []SubjectRef{teamA, projectA}
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a driver about a group citing its OWN member to be admitted", err)
	}
}

// TestDriverNotNamingAGroupIsUnaffected is the scope control: the rule fires
// only when a group subject is actually named, so every single-subject and
// flat-cohort answer behaves exactly as before.
func TestDriverNotNamingAGroupIsUnaffected(t *testing.T) {
	t.Parallel()
	input, draft, _, projectB := crossGroupFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{projectB}
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a driver naming no group to be unaffected by the group rule", err)
	}
}

// TestDriverNamingAGroupMayAlsoNameANonMemberSubject guards the rule against
// being OVER-strict, which is the failure mode that costs served answers
// rather than correctness.
//
// The rule is about MEMBER ATTRIBUTION: it refuses "this other group's member
// is my group's business". A subject that is not a cohort member at all -- a
// relationship path node, a committed resolution subject -- carries no group
// membership to violate, and a driver may legitimately name one alongside a
// group ("Team A is blocked by this release work item").
//
// This exists because deleting the non-member guard SURVIVED the battery:
// every other fixture named only members, so nothing observed the difference
// between the correct rule and one that rejects any subject outside the named
// group's membership. That mutant now dies here.
func TestDriverNamingAGroupMayAlsoNameANonMemberSubject(t *testing.T) {
	t.Parallel()
	input, draft, teamA, _ := crossGroupFixture()
	projectA := input.Graph.Resolution.Committed[0]
	// A path node, present in the payload and citable, but not a cohort member.
	nonMember := input.Graph.Paths[0].Nodes[1]
	for _, member := range input.Graph.Cohort.Members {
		if member.Subject.CanonicalID == nonMember.CanonicalID {
			t.Fatalf("fixture drift: %q must NOT be a cohort member for this test to mean anything", nonMember.CanonicalID)
		}
	}
	draft.ClaimedFacts[0].Subject = projectA
	draft.Drivers[0].AffectedSubjects = []SubjectRef{teamA, projectA, nonMember}

	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a group-scoped driver to be free to name a non-member subject it is genuinely about", err)
	}
}
