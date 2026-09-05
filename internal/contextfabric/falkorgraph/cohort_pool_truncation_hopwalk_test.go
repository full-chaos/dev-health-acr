package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5168 r1 finding 1 (P1), as a red-first test.
//
// The full-text arm was not the only bounded arm feeding the cohort. hopWalk
// stops admitting edges at collectLimit, and a neighbour is discovered ONLY
// through an admitted edge, so an edge dropped at the cap takes its endpoint
// with it: a kind-matching, authorized team that never reaches `visited`,
// never reaches cohortNodes, and never becomes a member. hopWalk reported no
// truncation, so with the retained count still under MaxCohortMembers the
// cohort claimed Complete=true over a pool the walk itself had clipped --
// the SAME defect this ticket exists to close, on the arm it did not cover.
//
// This is the population the first fix missed: "every retrieval arm that can
// clip the cohort's candidate pool" is three arms, and it covered two.

// hopWalkTruncationAdapter builds a committed-origin walk over `teamCount`
// authorized team neighbours, with NO lexical matches, so the cohort's members
// come from the hop walk alone and the full-text arm cannot be what any
// assertion below reads.
func hopWalkTruncationAdapter(t *testing.T, teamCount int, telemetry GraphTelemetry) *Adapter {
	t.Helper()
	teamIDs := make([]string, teamCount)
	for i := range teamIDs {
		teamIDs[i] = fmt.Sprintf("team_%03d", i)
	}
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			// The census must not run on this fixture (a committed subject
			// already prevents it); returning nothing keeps that unambiguous.
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			// edgesOfNode. Only the origin has edges, so the walk terminates
			// after one hop and the cap is the only thing that can bound it.
			if params["id"] != "p1" {
				return nil, nil
			}
			rows := make([]row, 0, teamCount)
			for i, id := range teamIDs {
				rows = append(rows, row{
					"r": &edge{Properties: map[string]interface{}{
						propRelationType: "BLOCKS", propRelationshipID: fmt.Sprintf("rel_%03d", i),
						propEvidenceRefs: []string{"evidence_" + id},
					}},
					"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": id,
				})
			}
			return rows, nil
		default: // nodeByKindID: resolveEdge's endpoint fetch and the walk's neighbour bookkeeping
			switch params["kind"] {
			case "project":
				if params["id"] == "p1" {
					return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
				}
			case "team":
				id, _ := params["id"].(string)
				return []row{fakeSubjectNodeRow("team", id, id)}, nil
			}
			return nil, nil
		}
	}}
	if telemetry == nil {
		return newFakeAdapter(t, fake)
	}
	return newFakeAdapterWithTelemetry(t, fake, telemetry)
}

// hopWalkTruncationRequest is a discovered_cohort request carrying a COMMITTED
// origin -- a real, expected combination (an exact hint, a prior-turn
// carry-over), and the one that skips the exact-name census, leaving the hop
// walk as the cohort's only node source.
//
// MaxCohortMembers is deliberately far ABOVE the collect budget so the member
// cap can never be what discloses the loss.
func hopWalkTruncationRequest(maxCohortMembers int) contextfabric.GraphDiscoveryRequest {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "which teams are struggling",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: maxCohortMembers, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{origin}},
		Frame:      discoveredTeamCohortFrame(),
	}
}

// TestDiscoverContextClippedHopWalkMakesTheCohortTruncated carries the harm.
//
// One more neighbour than the walk's edge budget, and a member cap far above
// both: the walk drops exactly one authorized team, and before the fix the
// cohort reported itself complete over the remainder.
func TestDiscoverContextClippedHopWalkMakesTheCohortTruncated(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	adapter := hopWalkTruncationAdapter(t, poolTruncationFulltextCollectLimit+1, telemetry)
	request := hopWalkTruncationRequest(poolTruncationFulltextCollectLimit + 100)

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil -- the fixture never reached cohort assembly and proves nothing")
	}
	if len(result.Cohort.Members) != poolTruncationFulltextCollectLimit {
		t.Fatalf("members = %d, want %d (the walk's edge budget) -- the fixture's own shape moved",
			len(result.Cohort.Members), poolTruncationFulltextCollectLimit)
	}
	if len(result.Cohort.Members) >= request.Request.Options.MaxCohortMembers {
		t.Fatalf("members (%d) reached MaxCohortMembers (%d): the pre-existing cap disclosure would carry this test and the walk's own loss would go unmeasured",
			len(result.Cohort.Members), request.Request.Options.MaxCohortMembers)
	}
	if !result.Cohort.Truncated {
		t.Error("Cohort.Truncated = false after the hop walk spent its edge budget with a neighbour still unexamined -- that neighbour is a team this cohort does not carry, and the count step reads this field")
	}
	if result.Cohort.Complete {
		t.Error("Cohort.Complete = true over a pool the hop walk clipped")
	}

	if len(telemetry.cohortKindBases) != 1 {
		t.Fatalf("cohort kind basis lines = %d, want 1", len(telemetry.cohortKindBases))
	}
	line := telemetry.cohortKindBases[0]
	if line.poolTruncation != CohortPoolTruncationTruncated {
		t.Errorf("pool truncation basis = %q, want %q", line.poolTruncation, CohortPoolTruncationTruncated)
	}
	if got := formatCohortPoolTruncationArms(line.poolTruncationArms); got != string(CohortPoolTruncationArmHopWalk) {
		t.Errorf("cut arms = %q, want %q -- an operator must be able to see WHICH arm cut the pool without re-reading source", got, CohortPoolTruncationArmHopWalk)
	}
}

// TestDiscoverContextWholeHopWalkLeavesTheCohortComplete is the complement, on
// the SAME fixture with one neighbour fewer: exactly at the budget, which is
// the boundary the walk's own `len(edges) >= collectLimit` draws. Without this
// direction, "always truncated" would pass the test above and would destroy
// the completeness claim of every committed-origin cohort answer.
func TestDiscoverContextWholeHopWalkLeavesTheCohortComplete(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	adapter := hopWalkTruncationAdapter(t, poolTruncationFulltextCollectLimit, telemetry)
	request := hopWalkTruncationRequest(poolTruncationFulltextCollectLimit + 100)

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil -- the fixture never reached cohort assembly")
	}
	if len(result.Cohort.Members) != poolTruncationFulltextCollectLimit {
		t.Fatalf("members = %d, want %d -- the two directions must retain the same members or they are not comparable",
			len(result.Cohort.Members), poolTruncationFulltextCollectLimit)
	}
	if result.Cohort.Truncated {
		t.Error("Cohort.Truncated = true for a walk that admitted every edge it found -- a flag that is always set discloses nothing")
	}
	if !result.Cohort.Complete {
		t.Error("Cohort.Complete = false over a whole pool below the member cap")
	}
	if len(telemetry.cohortKindBases) != 1 {
		t.Fatalf("cohort kind basis lines = %d, want 1", len(telemetry.cohortKindBases))
	}
	if got := telemetry.cohortKindBases[0].poolTruncation; got != CohortPoolTruncationNone {
		t.Errorf("pool truncation basis = %q, want %q", got, CohortPoolTruncationNone)
	}
	if got := formatCohortPoolTruncationArms(telemetry.cohortKindBases[0].poolTruncationArms); got != "" {
		t.Errorf("cut arms = %q, want empty", got)
	}
}

// TestDiscoverContextEmitsCohortKindBasisWhenNoCohortIsBuilt is r1 finding 3.
//
// The reviewer's mutant: guard the emit with `if cohort != nil` and the whole
// package stays green. That mutant deletes the disclosure exactly where it is
// the ONLY one available: a clipped pool that yields no authorized member of
// the requested kind returns no cohort at all, so there is no wire field left
// to carry the truncation, and this line is all an operator has.
//
// reader.go's own comment at the emit site promises this. A comment naming a
// guarantee nothing enforces is worse than no comment -- this is the test that
// makes the promise real.
func TestDiscoverContextEmitsCohortKindBasisWhenNoCohortIsBuilt(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	// Every neighbour is a PROJECT, so the team cohort finds no member of its
	// requested kind and DiscoveredCohort returns nil -- while the walk still
	// spends its edge budget, so the pool is genuinely truncated.
	teamCount := poolTruncationFulltextCollectLimit + 1
	ids := make([]string, teamCount)
	for i := range ids {
		ids[i] = fmt.Sprintf("proj_%03d", i)
	}
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"), strings.Contains(cypher, "$kinds"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			rows := make([]row, 0, teamCount)
			for i, id := range ids {
				rows = append(rows, row{
					"r": &edge{Properties: map[string]interface{}{
						propRelationType: "BLOCKS", propRelationshipID: fmt.Sprintf("rel_%03d", i),
					}},
					"srcKind": "project", "srcId": "p1", "dstKind": "project", "dstId": id,
				})
			}
			return rows, nil
		default:
			if params["kind"] == "project" {
				id, _ := params["id"].(string)
				return []row{fakeSubjectNodeRow("project", id, id)}, nil
			}
			return nil, nil
		}
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"},
		hopWalkTruncationRequest(poolTruncationFulltextCollectLimit+100))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort != nil {
		t.Fatalf("Cohort = %#v, want nil -- this fixture is about the case where the answer carries NO cohort to disclose on", result.Cohort)
	}
	if len(telemetry.cohortKindBases) != 1 {
		t.Fatalf("cohort kind basis lines = %d, want exactly 1 -- the emit is documented as firing on EVERY call, and with no cohort this line is the only disclosure that exists",
			len(telemetry.cohortKindBases))
	}
	line := telemetry.cohortKindBases[0]
	if line.discovered {
		t.Errorf("discovered = true on a call that built no cohort")
	}
	if line.poolTruncation != CohortPoolTruncationTruncated {
		t.Errorf("pool truncation basis = %q, want %q -- the pool was clipped and nothing else on this answer says so",
			line.poolTruncation, CohortPoolTruncationTruncated)
	}
}
