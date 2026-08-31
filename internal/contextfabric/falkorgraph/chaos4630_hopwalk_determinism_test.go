package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestHopWalkDiscoveredCohortMembershipIsDeterministicAcrossRuns is
// CHAOS-4630. hopWalk (queries.go) materializes its committed-origin
// traversal result by ranging a Go map (`visited`) with no sort -- Go
// randomizes map iteration order per range, so DiscoverContext's cohort
// node source (reader.go's resolvedNodes, which cohortNodes aliases
// whenever a discovered_cohort request already carries a committed
// subject -- see DiscoverContext's own comment on why the exact-name arm
// is skipped in that case) inherits that random order. DiscoveredCohort
// (graphrank/discover.go) then ranks members in INPUT order and stops at
// MaxCohortMembers: with more authorized, kind-matching neighbors than the
// cap, WHICH members survive truncation -- and every member's positional
// Rank -- varies run to run for an identical request.
//
// This proves it by walking a fixture neighbourhood of 12 same-kind team
// nodes, one hop from a single committed origin project, through a cap of
// 4, repeated many times: on origin/main this flaps (a different member
// set or a different Rank assignment) within a handful of iterations,
// because Go's map randomization is reliable, not flaky-in-reverse. After
// the fix (sorting hopWalk's materialized slice on a total key before it
// is returned), every iteration must produce byte-identical membership.
func TestHopWalkDiscoveredCohortMembershipIsDeterministicAcrossRuns(t *testing.T) {
	const teamCount = 12
	const maxCohortMembers = 4
	const iterations = 60

	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}
	teamIDs := make([]string, teamCount)
	for i := range teamIDs {
		teamIDs[i] = fmt.Sprintf("team_%02d", i)
	}

	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			// No lexical match for the question text -- membership comes
			// entirely from the committed origin's one-hop walk.
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			// edgesOfNode: only the origin has edges (one hop). Every team
			// node's own edgesOfNode call (hopWalk's second-hop probe)
			// returns nothing, terminating the walk there.
			if params["id"] != "p1" {
				return nil, nil
			}
			rows := make([]row, 0, teamCount)
			for i, id := range teamIDs {
				rows = append(rows, row{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: fmt.Sprintf("rel_%02d", i), propEvidenceRefs: []string{"evidence_" + id}}},
					"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": id,
				})
			}
			return rows, nil
		default: // nodeByKindID -- both resolveEdge's endpoint fetch and hopWalk's own neighbor-bookkeeping fetch
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
	adapter := newFakeAdapter(t, fake)
	// No RepositoryScopes and no RequestedScope: AuthorizedAttributes
	// admits unconditionally, so every team node is authorized -- this
	// test is about ORDER, not authorization.
	principal := storage.Principal{OrgID: "org-1"}
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "which teams are struggling",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: maxCohortMembers, MaxRelationshipPaths: 20,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			// discovered_cohort WITH a committed origin (an exact hint /
			// prior-turn carry-over) -- DiscoverContext's own comment
			// explains this is a real, expected combination, and it is the
			// one that skips the (already-sorted) exact-name census arm,
			// leaving hopWalk's order as the cohort's ONLY node source.
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{origin}},
	}

	var first []string
	for i := 0; i < iterations; i++ {
		result, err := adapter.DiscoverContext(context.Background(), principal, request)
		if err != nil {
			t.Fatalf("DiscoverContext() iteration %d error = %v", i, err)
		}
		if result.Cohort == nil {
			t.Fatalf("iteration %d: Cohort = nil, want %d members", i, maxCohortMembers)
		}
		if len(result.Cohort.Members) != maxCohortMembers {
			t.Fatalf("iteration %d: len(Cohort.Members) = %d, want %d (the cap)", i, len(result.Cohort.Members), maxCohortMembers)
		}
		got := make([]string, len(result.Cohort.Members))
		for m, member := range result.Cohort.Members {
			got[m] = fmt.Sprintf("%s#rank=%d", member.Subject.CanonicalID, member.Rank)
		}
		if i == 0 {
			first = got
			continue
		}
		if !equalStrings(first, got) {
			t.Fatalf("iteration %d: cohort membership/rank changed across identical requests.\n first: %v\n  this: %v", i, first, got)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
