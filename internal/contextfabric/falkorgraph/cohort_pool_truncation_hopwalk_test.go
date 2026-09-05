package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	// STRICTLY BELOW the budget, not exactly at it. The original fixture sat
	// exactly ON the budget, and the r2 boundary fix correctly reclassified
	// that as truncation: spending the budget with an unvisited frontier IS a
	// clipped pool, whether or not visiting it would have found anything. The
	// fixture's expectation was what was wrong, not the code -- so the fixture
	// moved rather than the assertion being softened.
	const belowBudget = poolTruncationFulltextCollectLimit - 1
	adapter := hopWalkTruncationAdapter(t, belowBudget, telemetry)
	request := hopWalkTruncationRequest(poolTruncationFulltextCollectLimit + 100)

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil -- the fixture never reached cohort assembly")
	}
	if len(result.Cohort.Members) != belowBudget {
		t.Fatalf("members = %d, want %d", len(result.Cohort.Members), belowBudget)
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

// hopWalkTwoHopAdapter builds a walk whose FIRST hop admits exactly
// `firstHopEdges` edges and whose neighbours each carry one further edge, so
// the second hop is reachable and non-empty.
//
// The two-hop shape is what makes the exact-budget boundary reachable at all,
// and it is also the fixture the package was missing entirely: every prior
// hop-walk fixture returned edges only for the origin, so second-hop traversal
// was unpinned (r2 finding 1). One fixture, two properties.
func hopWalkTwoHopAdapter(t *testing.T, firstHopEdges int, telemetry GraphTelemetry) *Adapter {
	t.Helper()
	first := make([]string, firstHopEdges)
	for i := range first {
		first[i] = fmt.Sprintf("team_%03d", i)
	}
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"), strings.Contains(cypher, "$kinds"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			id, _ := params["id"].(string)
			if id == "p1" {
				rows := make([]row, 0, firstHopEdges)
				for i, tid := range first {
					rows = append(rows, row{
						"r": &edge{Properties: map[string]interface{}{
							propRelationType: "BLOCKS", propRelationshipID: fmt.Sprintf("rel_a_%03d", i),
						}},
						"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": tid,
					})
				}
				return rows, nil
			}
			// Every first-hop team has ONE second-hop edge to a further team.
			// Reached only if the walk actually visits the next frontier.
			if strings.HasPrefix(id, "team_") && !strings.HasPrefix(id, "team_deep_") {
				return []row{{
					"r": &edge{Properties: map[string]interface{}{
						propRelationType: "BLOCKS", propRelationshipID: "rel_b_" + id,
					}},
					"srcKind": "team", "srcId": id, "dstKind": "team", "dstId": "team_deep_" + id,
				}}, nil
			}
			return nil, nil
		default:
			if params["kind"] == "project" {
				return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
			}
			if params["kind"] == "team" {
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

// TestDiscoverContextHopWalkAtExactlyItsBudgetDisclosesTheUnvisitedFrontier is
// r2 finding 1 (P1), and it is a boundary INSIDE the fix that closed r1's
// finding 1.
//
// The truncation flag was set at the walk's INNER break, which fires only when
// a hop still has candidates after the budget is spent. The OUTER loop has its
// own budget exit, and a hop that admits EXACTLY collectLimit edges and
// exhausts its candidate list reaches that exit with the inner break never
// taken: an unvisited frontier, and a cohort that claimed completeness over
// subjects the walk stopped short of.
//
// Exactly at the budget is the whole point of the fixture; one more or one
// fewer takes a different path through the loop and proves nothing about this
// boundary.
func TestDiscoverContextHopWalkAtExactlyItsBudgetDisclosesTheUnvisitedFrontier(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	adapter := hopWalkTwoHopAdapter(t, poolTruncationFulltextCollectLimit, telemetry)
	// The member cap is far above the budget, so the cohort's own cap can
	// never be what discloses this.
	request := hopWalkTruncationRequest(poolTruncationFulltextCollectLimit + 100)

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil -- the fixture never reached cohort assembly")
	}
	if len(result.Cohort.Members) >= request.Request.Options.MaxCohortMembers {
		t.Fatalf("members (%d) reached MaxCohortMembers (%d): the cap would carry this test",
			len(result.Cohort.Members), request.Request.Options.MaxCohortMembers)
	}
	if !result.Cohort.Truncated {
		t.Error("Cohort.Truncated = false after the walk spent its edge budget with a frontier it never visited -- " +
			"the inner break never fired because the hop's candidates ran out at exactly the budget, and the outer " +
			"loop exited on the same budget without recording anything")
	}
	if result.Cohort.Complete {
		t.Error("Cohort.Complete = true over a pool the walk stopped short of")
	}
	if len(telemetry.cohortKindBases) != 1 {
		t.Fatalf("cohort kind basis lines = %d, want 1", len(telemetry.cohortKindBases))
	}
	if got := formatCohortPoolTruncationArms(telemetry.cohortKindBases[0].poolTruncationArms); got != string(CohortPoolTruncationArmHopWalk) {
		t.Errorf("cut arms = %q, want %q", got, CohortPoolTruncationArmHopWalk)
	}
}

// TestDiscoverContextHopWalkVisitsTheSecondHop is r2 finding 2 (P2), pinning a
// PRE-EXISTING line this branch did not write: `next = append(next, neighbor)`.
//
// Deleting it kills second-hop traversal outright and every hop-walk fixture in
// the package stayed green, because they all returned edges for the origin
// alone. Pinned here rather than forwarded, because the two-hop fixture the
// boundary test above needs is exactly the one this was missing.
func TestDiscoverContextHopWalkVisitsTheSecondHop(t *testing.T) {
	t.Parallel()
	// A budget far above the two hops' combined edge count, so nothing is
	// clipped and the only question is whether the second hop is walked.
	adapter := hopWalkTwoHopAdapter(t, 3, nil)
	request := hopWalkTruncationRequest(100)
	request.Request.Options.MaxRelationshipPaths = 100

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil")
	}
	var deep int
	for _, m := range result.Cohort.Members {
		if strings.HasPrefix(m.Subject.CanonicalID, "team_deep_") {
			deep++
		}
	}
	if deep == 0 {
		t.Error("no second-hop member reached the cohort -- the walk never advanced past its first frontier, " +
			"and every other hop-walk fixture in this package would still pass")
	}
	if !result.Cohort.Complete || result.Cohort.Truncated {
		t.Errorf("Complete=%v Truncated=%v: nothing was clipped on this fixture, so a two-hop walk that completes must claim completeness -- "+
			"this is the complement that stops the boundary fix above from simply always disclosing",
			result.Cohort.Complete, result.Cohort.Truncated)
	}
}

// TestDiscoverContextCensusKeepsBoundedDiscoveryMembers is r2 finding 2 (P2),
// pinning a PRE-EXISTING line this branch did not write:
// `cohortNodes = append(cohortNodes, resolvedNodes...)`.
//
// Deleting it drops every member found by the bounded arms whenever the census
// arm runs, and the package stayed green because the existing census fixtures
// either have no bounded match or get the expected member from the census
// itself. The reviewer is careful about the severity and so is this test: the
// answer stays INCOMPLETE either way, so this is retrieval loss, not false
// completeness — the assertion is about the member surviving the union, not
// about a flag.
func TestDiscoverContextCensusKeepsBoundedDiscoveryMembers(t *testing.T) {
	t.Parallel()
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			// A lexical-only member: it exists in NO census row below, so it
			// can only reach the cohort through the bounded-arm union.
			r := fulltextRow("team", "team_lexical_only", "Lexical Only", "teams struggling", nil)
			r["node"].(*node).Properties["authorization_repositories"] = "*"
			return []row{r}, nil
		case strings.Contains(cypher, "$kinds"):
			r := fakeSubjectNodeRow("team", "team_from_census", "From Census")
			r["n"].(*node).Properties["authorization_repositories"] = "*"
			return []row{r}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	// discovered_kind with NO committed subject: the census IS admitted here,
	// which is the condition under which the union is the only thing carrying
	// the lexical member.
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	request.Request.Options.MaxCohortMembers = 10

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil")
	}
	var lexical, census bool
	for _, m := range result.Cohort.Members {
		switch m.Subject.CanonicalID {
		case "team_lexical_only":
			lexical = true
		case "team_from_census":
			census = true
		}
	}
	if !census {
		t.Fatal("the census member is missing -- the fixture is not exercising the census arm and proves nothing about the union")
	}
	if !lexical {
		t.Error("the bounded-arm member did not survive the census merge -- a subject the lexical arm found is absent from the cohort " +
			"while the census-sourced one is present, which is exactly what deleting the union produces")
	}
}

// smallBudgetAdapter is a fake adapter with a deliberately tiny collect
// budget, so a wasted edge slot is observable in the cohort itself.
//
// The shared newFakeAdapter pins MaxResults at 25, which is larger than any
// fixture here can fill with distinct edges -- so with it, a duplicate costs
// nothing observable and the test would pass whether or not deduplication
// happened.
func smallBudgetAdapter(t *testing.T, maxResults int) *Adapter {
	t.Helper()
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"), strings.Contains(cypher, "$kinds"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			id, _ := params["id"].(string)
			switch id {
			case "p1":
				return []row{
					{"r": &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_a"}},
						"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": "team_a"},
					{"r": &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_b"}},
						"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": "team_b"},
				}, nil
			case "team_a":
				return []row{{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_shared"}},
					"srcKind": "team", "srcId": "team_a", "dstKind": "team", "dstId": "team_shared",
				}}, nil
			case "team_b":
				// The SAME edge id as team_a's, plus one unique edge. Ranking
				// ties break on UUID ascending, and "rel_shared" sorts before
				// "rel_unique", so the duplicate is examined first -- which is
				// what makes the wasted slot decisive rather than incidental.
				return []row{
					{"r": &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_shared"}},
						"srcKind": "team", "srcId": "team_a", "dstKind": "team", "dstId": "team_shared"},
					{"r": &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_unique"}},
						"srcKind": "team", "srcId": "team_b", "dstKind": "team", "dstId": "team_unique"},
				}, nil
			}
			return nil, nil
		default:
			switch params["kind"] {
			case "project":
				return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
			case "team":
				id, _ := params["id"].(string)
				return []row{fakeSubjectNodeRow("team", id, id)}, nil
			}
			return nil, nil
		}
	}}
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: maxResults, PoolSize: 1, AllowInsecure: true,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}
	return adapter
}

// TestDiscoverContextHopWalkDeduplicatesEdgesAcrossFrontierNodes is r2 finding
// 3 (P3), pinning a PRE-EXISTING line this branch did not write:
// `seenEdge[ce.UUID] = true` inside the walk.
//
// Deleting it let the SAME edge, returned from two different frontier nodes,
// be admitted twice -- consuming the collect budget for one real edge. Every
// existing fixture returned unique edges from a single origin, so nothing
// noticed.
//
// ASSERTED ON THE COHORT, not on served paths. The first version of this test
// counted duplicate relationship ids in result.Paths and its own guard caught
// it reporting on an EMPTY set: this fixture's team-to-team edges never reach
// the served paths at all, so "no duplicates" there was true and meaningless.
// A wasted edge slot is observable where it actually costs something -- a
// member that does not fit.
//
// With a budget of 4: the origin's two edges fill two slots; deduplicated, the
// second hop offers {shared, unique} and both fit, so team_unique is a member.
// Without deduplication it offers {shared, shared, unique}, the duplicate takes
// the last slot, and team_unique is lost.
func TestDiscoverContextHopWalkDeduplicatesEdgesAcrossFrontierNodes(t *testing.T) {
	t.Parallel()
	adapter := smallBudgetAdapter(t, 4)
	request := hopWalkTruncationRequest(50)
	request.Request.Options.MaxRelationshipPaths = 50

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil -- the fixture never reached cohort assembly and proves nothing")
	}
	members := map[string]bool{}
	for _, m := range result.Cohort.Members {
		members[m.Subject.CanonicalID] = true
	}
	// Reach controls first: without these, an absent team_unique could mean
	// the walk never got to the second hop at all.
	if !members["team_a"] || !members["team_b"] {
		t.Fatalf("the first-hop members are missing (%v) -- this fixture never exercised the walk", members)
	}
	if !members["team_shared"] {
		t.Fatalf("the second-hop shared member is missing (%v) -- the walk never reached the frontier where the duplicate lives", members)
	}
	if !members["team_unique"] {
		t.Errorf("team_unique is absent (%v) -- the edge returned from BOTH frontier nodes consumed two of the four slots, "+
			"and the member behind the unique edge did not fit", members)
	}
}

// TestDiscoverContextHopWalkBudgetSpentMidHopWithNoNewFrontier isolates the
// walk's INNER truncation assignment from its post-loop one.
//
// WHY IT EXISTS. The battery reported the inner `truncated = true` as a
// SURVIVING mutant: on every fixture written so far, a walk that hits the
// inner break also ends with a non-empty frontier, so the post-loop check
// fires too and deleting the inner assignment changed nothing observable. A
// surviving mutant is not a pass — either the code is subsumed and should go,
// or the property needs a fixture that can decide it. This is that fixture,
// and it shows the inner assignment is NOT subsumed.
//
// The isolating case is a budget spent MID-HOP that discovers no new
// neighbour: the second hop's edges all point back at a node already visited,
// so `next` comes out empty and the post-loop check — which requires an
// unvisited frontier — cannot fire. The inner break is then the only thing
// that knows candidates were left unexamined.
func TestDiscoverContextHopWalkBudgetSpentMidHopWithNoNewFrontier(t *testing.T) {
	t.Parallel()
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"), strings.Contains(cypher, "$kinds"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			id, _ := params["id"].(string)
			switch id {
			case "p1":
				return []row{{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_1"}},
					"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": "team_a",
				}}, nil
			case "team_a":
				// Three edges, every endpoint ALREADY VISITED (team_a itself).
				// They are real candidates that consume budget, but they add
				// nothing to the next frontier — which is exactly what makes
				// the post-loop check blind here.
				rows := make([]row, 0, 3)
				for i := 0; i < 3; i++ {
					rows = append(rows, row{
						"r": &edge{Properties: map[string]interface{}{
							propRelationType: "BLOCKS", propRelationshipID: fmt.Sprintf("rel_self_%d", i),
						}},
						"srcKind": "team", "srcId": "team_a", "dstKind": "team", "dstId": "team_a",
					})
				}
				return rows, nil
			}
			return nil, nil
		default:
			switch params["kind"] {
			case "project":
				return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
			case "team":
				id, _ := params["id"].(string)
				return []row{fakeSubjectNodeRow("team", id, id)}, nil
			}
			return nil, nil
		}
	}}
	// Budget 2: the origin's single edge, then ONE of team_a's three, then the
	// break — with candidates still unexamined and no new frontier.
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: 2, PoolSize: 1, AllowInsecure: true,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, hopWalkTruncationRequest(50))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil -- the fixture never reached cohort assembly")
	}
	if len(result.Cohort.Members) >= 50 {
		t.Fatalf("members (%d) reached MaxCohortMembers: the cap would carry this test", len(result.Cohort.Members))
	}
	if !result.Cohort.Truncated {
		t.Error("Cohort.Truncated = false: the walk broke out of a hop with candidate edges still unexamined, " +
			"and because that hop discovered no NEW neighbour the post-loop check cannot see it -- the inner " +
			"assignment is the only thing that knows, which is what makes it not subsumed")
	}
	if result.Cohort.Complete {
		t.Error("Cohort.Complete = true over a pool the walk stopped short of mid-hop")
	}
}

// TestDiscoverContextHopWalkDisclosesWhenDepthAndBudgetBindTogether IS THE
// RULING'S ENFORCEMENT, and it exists because reverting a mistake is not the
// same as pinning the correct behaviour.
//
// The ruling: a depth bound (maxHops) DEFINES the pool and is not truncation;
// a spent collect budget with an unvisited frontier CLIPS it and is; WHEN BOTH
// BIND AT ONCE, DISCLOSE.
//
// I once added `&& hop < maxHops` to the post-loop disclosure, which suppressed
// exactly the both-bind case, and reverted it on the ruling. But nothing in the
// suite would have caught someone re-adding it: every existing fixture has
// depth still available when the budget runs out, so the conjunct is invisible
// to them. r3 found that gap independently. This is the fixture that closes it.
//
// Shape: budget 3, one first-hop edge, two second-hop edges. Hop 0 admits 1;
// hop 1 admits 2, reaching exactly the budget with its candidates exhausted, so
// the INNER break never fires; the frontier is non-empty; and `hop` has reached
// maxHops, so the loop exits with BOTH bounds binding at once.
func TestDiscoverContextHopWalkDisclosesWhenDepthAndBudgetBindTogether(t *testing.T) {
	t.Parallel()
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"), strings.Contains(cypher, "$kinds"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			id, _ := params["id"].(string)
			switch id {
			case "p1":
				return []row{{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_1"}},
					"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": "team_a",
				}}, nil
			case "team_a":
				return []row{
					{"r": &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_2"}},
						"srcKind": "team", "srcId": "team_a", "dstKind": "team", "dstId": "team_b"},
					{"r": &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_3"}},
						"srcKind": "team", "srcId": "team_a", "dstKind": "team", "dstId": "team_c"},
				}, nil
			}
			// team_b and team_c are never queried: the walk stops before hop 2.
			return nil, nil
		default:
			switch params["kind"] {
			case "project":
				return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
			case "team":
				id, _ := params["id"].(string)
				return []row{fakeSubjectNodeRow("team", id, id)}, nil
			}
			return nil, nil
		}
	}}
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: 3, PoolSize: 1, AllowInsecure: true,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, hopWalkTruncationRequest(50))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil -- the fixture never reached cohort assembly")
	}
	// Reach control: team_b and team_c must be IN the cohort (their edges were
	// admitted) while the walk never visited them as a frontier. Without this,
	// a walk that stopped after hop 0 would look the same to the assertion.
	members := map[string]bool{}
	for _, m := range result.Cohort.Members {
		members[m.Subject.CanonicalID] = true
	}
	if !members["team_b"] || !members["team_c"] {
		t.Fatalf("the second hop's members are missing (%v) -- the walk did not reach the state this test is about", members)
	}
	if !result.Cohort.Truncated {
		t.Error("Cohort.Truncated = false with the budget spent AND an unvisited frontier AND the depth bound reached -- " +
			"this is the both-bind case the ruling says must disclose, and a `hop < maxHops` conjunct on the post-loop " +
			"check would produce exactly this result")
	}
	if result.Cohort.Complete {
		t.Error("Cohort.Complete = true in the both-bind case")
	}
}

// TestDiscoverContextFailedNeighborLookupIsNotACompleteCohort is r3 finding 2
// (P2): a THIRD way the walk stops short of an in-scope member.
//
// A neighbour reached through an admitted edge can fail its bookkeeping
// read-back. It is then never added to `visited`, never becomes a cohort
// member, and before this no arm reported it -- so `Coverage.Partial=true` sat
// beside `Cohort.Complete=true`, which is a contradiction a reader cannot
// resolve. Partial says "something was lost"; Complete says "nothing is
// missing".
//
// The failure is its OWN arm, not folded into hop_walk: a clipped budget and an
// unconfirmable member are different causes with different remedies.
func TestDiscoverContextFailedNeighborLookupIsNotACompleteCohort(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"), strings.Contains(cypher, "$kinds"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			return []row{
				{"r": &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_ok"}},
					"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": "team_ok"},
				{"r": &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_bad"}},
					"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": "team_unreadable"},
			}, nil
		default:
			// The bookkeeping read-back FAILS for one neighbour only. Both
			// edges resolved; only the node read is broken, which is exactly
			// the shape the count-only reporting hid.
			if params["kind"] == "team" && params["id"] == "team_unreadable" {
				return nil, errors.New("injected backend failure reading team_unreadable")
			}
			switch params["kind"] {
			case "project":
				return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
			case "team":
				id, _ := params["id"].(string)
				return []row{fakeSubjectNodeRow("team", id, id)}, nil
			}
			return nil, nil
		}
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, hopWalkTruncationRequest(50))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil -- the readable member should still form a cohort")
	}
	members := map[string]bool{}
	for _, m := range result.Cohort.Members {
		members[m.Subject.CanonicalID] = true
	}
	if !members["team_ok"] {
		t.Fatalf("the readable member is missing (%v) -- the fixture never reached cohort assembly", members)
	}
	if members["team_unreadable"] {
		t.Fatalf("the unreadable member is PRESENT (%v) -- the injected failure did not take effect, so this test is measuring nothing", members)
	}
	if !result.Coverage.Partial {
		t.Error("Coverage.Partial = false: the pre-existing failed-lookup disclosure regressed")
	}
	// THE HARM. Partial and Complete cannot both stand.
	if !result.Cohort.Truncated {
		t.Error("Cohort.Truncated = false while a neighbour the walk reached could not be read back -- " +
			"a kind-matching subject is missing from this cohort and the answer claims otherwise")
	}
	if result.Cohort.Complete {
		t.Error("Cohort.Complete = true beside Coverage.Partial = true -- one says nothing is missing, the other says something was lost")
	}
	if got := formatCohortPoolTruncationArms(telemetry.cohortKindBases[0].poolTruncationArms); got != string(CohortPoolTruncationArmEndpointLookupFailed) {
		t.Errorf("cut arms = %q, want %q -- the cause must be distinguishable from a clipped budget", got, CohortPoolTruncationArmEndpointLookupFailed)
	}
}

// TestNeighborLookupFailureNamesTheMemberItLost is r3 finding 3 (P2).
//
// The failure used to survive only as a count (`endpoint_lookup_failed:<n>`),
// so two different missing members produced identical diagnostics and the
// subject that went missing could not be named from the run's own artifacts.
// Chris's standing rule: a swallowed error on a touched failure path is a
// review finding, fixed in the same change.
func TestNeighborLookupFailureNamesTheMemberItLost(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"), strings.Contains(cypher, "$kinds"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			return []row{
				{"r": &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_1"}},
					"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": "team_first"},
				{"r": &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_2"}},
					"srcKind": "project", "srcId": "p1", "dstKind": "team", "dstId": "team_second"},
			}, nil
		default:
			// TWO distinct failures. A count cannot tell them apart; that is
			// the whole point of the fixture.
			if params["kind"] == "team" {
				return nil, errors.New("injected backend failure")
			}
			return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
		}
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, hopWalkTruncationRequest(50)); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.neighborLookupFailures) != 2 {
		t.Fatalf("neighbour lookup failure lines = %d, want 2 (one per lost member) -- a count reports 2 and names neither",
			len(telemetry.neighborLookupFailures))
	}
	named := map[string]bool{}
	for _, f := range telemetry.neighborLookupFailures {
		if f.neighborUUID == "" {
			t.Error("a failure line carries no neighbour identifier -- it is a count with extra steps")
		}
		if f.originCanonicalID != "p1" {
			t.Errorf("origin = %q, want p1 -- without it a failure cannot be tied to the walk that hit it", f.originCanonicalID)
		}
		if f.err == nil {
			t.Error("a failure line carries no error -- the message is the part a count throws away")
		}
		named[f.neighborUUID] = true
	}
	if len(named) != 2 {
		t.Errorf("the two failures reported %d distinct neighbour identifiers, want 2 -- two different missing members must not be indistinguishable", len(named))
	}
}
