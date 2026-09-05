package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// poolTruncationDiscovery is the SINGLE fixture both directions of these
// tests run against.
//
// One fixture, varied in ONE input, is the point: the defect this file pins
// is that completeness was derived from the retained member count alone, so
// two runs whose retained members are IDENTICAL and whose candidate pools
// differ must produce different completeness. A test that varied the members
// too would prove only that the member cap still works.
//
// Three teams and a cohort cap of ten, so the cap is nowhere near reached and
// cannot be what any assertion below reads.
func poolTruncationDiscovery() contextfabric.GraphDiscoveryRequest {
	discovery := frameDiscovery(contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
	}, "teams_under_pressure", []string{"teams"})
	discovery.Request.Options.MaxCohortMembers = 10
	return discovery
}

func poolTruncationNodes() []CandidateNode {
	return []CandidateNode{
		candidateNode(contextfabric.SubjectTeam, "team_a", "Team A", 0.9, "*"),
		candidateNode(contextfabric.SubjectTeam, "team_b", "Team B", 0.9, "*"),
		candidateNode(contextfabric.SubjectTeam, "team_c", "Team C", 0.9, "*"),
	}
}

// TestDiscoveredCohortOverATruncatedPoolIsNeverComplete is the producer-side
// half of CHAOS-5168, and it carries the harm.
//
// The harm is not "a flag is wrong". It is that a cohort assembled from a
// pool retrieval already clipped reports Complete=true / Truncated=false --
// the exact document shape a complete census produces -- so the count served
// over it is read as a population. Three retained members below a cap of ten
// is precisely the case the old derivation could not see: no cap was hit, so
// by its own reasoning nothing was lost.
func TestDiscoveredCohortOverATruncatedPoolIsNeverComplete(t *testing.T) {
	t.Parallel()
	cohort, _, _, _, basis := DiscoveredCohort(
		storage.Principal{OrgID: "org_1"}, poolTruncationDiscovery(), poolTruncationNodes(), true, noInternal)

	if basis != CohortKindFromFrameMemberKind {
		t.Fatalf("basis = %q, want %q -- the fixture never reached cohort assembly, so it proves nothing", basis, CohortKindFromFrameMemberKind)
	}
	if cohort == nil {
		t.Fatal("cohort = nil, want a three-member team cohort")
	}
	if len(cohort.Members) != 3 {
		t.Fatalf("members = %d, want 3 -- the assertions below are about a cohort BELOW its cap", len(cohort.Members))
	}
	if len(cohort.Members) >= poolTruncationDiscovery().Request.Options.MaxCohortMembers {
		t.Fatalf("members (%d) reached MaxCohortMembers (%d): this fixture would pass on the member cap alone and would prove nothing about the pool",
			len(cohort.Members), poolTruncationDiscovery().Request.Options.MaxCohortMembers)
	}
	if cohort.Complete {
		t.Error("Cohort.Complete = true over a pool retrieval clipped before assembly -- a count served over this cohort reads as a census of the population")
	}
	if !cohort.Truncated {
		t.Error("Cohort.Truncated = false over a clipped pool -- CHAOS-4733 option (b) puts a discovery-level cap on the cohort's OWN Truncated, and every consumer that gates on truncation reads that field")
	}
}

// TestDiscoveredCohortOverAWholePoolStaysComplete is the complement, on the
// SAME fixture, and it is what stops the fix above from being "always report
// truncated".
//
// A guard that fires unconditionally is not a guard, and this direction is
// the one that would catch it: identical members, identical cap, pool NOT
// clipped, completeness intact.
func TestDiscoveredCohortOverAWholePoolStaysComplete(t *testing.T) {
	t.Parallel()
	cohort, _, _, _, basis := DiscoveredCohort(
		storage.Principal{OrgID: "org_1"}, poolTruncationDiscovery(), poolTruncationNodes(), false, noInternal)

	if basis != CohortKindFromFrameMemberKind {
		t.Fatalf("basis = %q, want %q", basis, CohortKindFromFrameMemberKind)
	}
	if cohort == nil {
		t.Fatal("cohort = nil, want a three-member team cohort")
	}
	if len(cohort.Members) != 3 {
		t.Fatalf("members = %d, want 3 -- the same three the truncated direction retains, or the two directions are not comparable", len(cohort.Members))
	}
	if !cohort.Complete {
		t.Error("Cohort.Complete = false over a whole pool below the member cap -- this cohort IS everything the search found")
	}
	if cohort.Truncated {
		t.Error("Cohort.Truncated = true over a whole pool: a truncation flag that is always set discloses nothing")
	}
}

// TestDiscoveredCohortAtTheMemberCapStaysTruncatedWithAWholePool pins the
// PRE-EXISTING loss the new input must not displace.
//
// The cap and the pool are two independent losses and this function now reads
// both. A fix that replaced the cap derivation with the pool one rather than
// taking their disjunction would pass every test above and lose the older
// disclosure entirely.
func TestDiscoveredCohortAtTheMemberCapStaysTruncatedWithAWholePool(t *testing.T) {
	t.Parallel()
	discovery := poolTruncationDiscovery()
	discovery.Request.Options.MaxCohortMembers = 2

	cohort, _, _, _, _ := DiscoveredCohort(
		storage.Principal{OrgID: "org_1"}, discovery, poolTruncationNodes(), false, noInternal)

	if cohort == nil {
		t.Fatal("cohort = nil, want the capped cohort")
	}
	if len(cohort.Members) != 2 {
		t.Fatalf("members = %d, want 2 (the cap) -- the fixture did not reach the cap it is testing", len(cohort.Members))
	}
	if cohort.Complete || !cohort.Truncated {
		t.Errorf("Complete=%v Truncated=%v at the member cap with a whole pool, want false/true -- the cap's own disclosure predates CHAOS-5168 and must survive it",
			cohort.Complete, cohort.Truncated)
	}
}

// TestDiscoveredCohortNeverClaimsBothCompleteAndTruncated is the contract
// bound, asserted at the producer rather than trusted.
//
// contracts/v1's cohort validator refuses a cohort that is both (see
// validate_context_fabric_result.go's `c.Complete && c.Truncated` clause), so
// a producer that could emit that pair would build a document the served
// answer's own validation rejects with an HTTP 500 -- the failure shape
// CHAOS-4926 already paid for once. Quantified over every combination of the
// two independent losses, so no combination can slip.
func TestDiscoveredCohortNeverClaimsBothCompleteAndTruncated(t *testing.T) {
	t.Parallel()
	for _, poolTruncated := range []bool{false, true} {
		for _, memberCap := range []int{2, 10} {
			discovery := poolTruncationDiscovery()
			discovery.Request.Options.MaxCohortMembers = memberCap
			cohort, _, _, _, _ := DiscoveredCohort(
				storage.Principal{OrgID: "org_1"}, discovery, poolTruncationNodes(), poolTruncated, noInternal)
			if cohort == nil {
				t.Fatalf("poolTruncated=%v cap=%d: cohort = nil", poolTruncated, memberCap)
			}
			if cohort.Complete == cohort.Truncated {
				t.Errorf("poolTruncated=%v cap=%d: Complete=%v Truncated=%v -- the two are mutually exclusive and jointly exhaustive on this producer; the v1 validator refuses the both-true pair outright",
					poolTruncated, memberCap, cohort.Complete, cohort.Truncated)
			}
		}
	}
}
