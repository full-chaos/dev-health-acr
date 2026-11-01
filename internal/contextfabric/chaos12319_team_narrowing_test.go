package contextfabric

import (
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12319 (lead ruling, anchor B): when no requested team owns the anchor,
// the anchor stays the subject and its own totals are not the team's. A walk
// with no member is then never explained by the anchor's totals (no pull
// request, unlinked): it is no_match.
func TestAnAnchorOutsideTheTeamNarrowingIsNotExplainedByItsTotals(t *testing.T) {
	for name, tc := range map[string]struct {
		outside                   bool
		pullRequests, linkedIssue int
		want                      RepositoryWorkItemWalkOutcome
	}{
		"outside, no pull request":       {true, 0, 0, RepositoryWorkItemWalkNoMatch},
		"outside, unlinked":              {true, 3, 0, RepositoryWorkItemWalkNoMatch},
		"owned, no pull request":         {false, 0, 0, RepositoryWorkItemWalkNoPullRequests},
		"owned, unlinked":                {false, 3, 0, RepositoryWorkItemWalkUnlinked},
		"no narrowing, linked, no match": {false, 3, 2, RepositoryWorkItemWalkNoMatch},
	} {
		t.Run(name, func(t *testing.T) {
			gate, _ := NewWorkItemMembershipGate(1, 0)
			ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
			defer owner.Complete()
			basis := TeamNarrowingAnchorOwned
			if tc.outside {
				basis = TeamNarrowingIssueProject
			}
			graph := &treeGraphFake{walk: TreeWorkItemWalk{
				Members: []TreeWorkItemMember{}, PullRequests: tc.pullRequests, LinkedIssues: tc.linkedIssue,
				AnchorOutsideTeamNarrowing: tc.outside,
				TeamNarrowing:              basis, TeamExcluded: tc.linkedIssue, TeamFloor: TeamNarrowingFloorProjectOwnerAbsent,
			}}
			telemetry := &recordingTelemetry{}
			engine := &Engine{treeWorkItemMembership: NewTreeWorkItemMembership(graph, &treeFilterFake{}, gate), telemetry: telemetry}
			request := InvestigationRequest{RequestedScope: RequestedScope{TeamIDs: []string{"team-C"}}}
			_, _, err := engine.discoverWorkItemTuple(ctx, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}, request, ResolvedGraphBinding{}, repositoryAnchoredResolution(repositoryWorkItemAnchor), &AnswerPlan{}, workItemMemberFilter{})
			if err != nil {
				t.Fatalf("discoverWorkItemTuple() error = %v", err)
			}
			if graph.calls != 1 {
				t.Fatalf("premise: walk calls %d", graph.calls)
			}
			if len(telemetry.repositoryWorkItemWalks) != 1 || telemetry.repositoryWorkItemWalks[0].Outcome != tc.want {
				t.Fatalf("repository walk lines = %+v, want one with outcome %s", telemetry.repositoryWorkItemWalks, tc.want)
			}
			// The decision basis reaches the walk line.
			line := telemetry.repositoryWorkItemWalks[0]
			if line.TeamNarrowing != basis || line.TeamExcluded != tc.linkedIssue || line.TeamFloor != TeamNarrowingFloorProjectOwnerAbsent {
				t.Fatalf("walk line basis = %q %d %q, want %q %d project_owner_absent", line.TeamNarrowing, line.TeamExcluded, line.TeamFloor, basis, tc.linkedIssue)
			}
		})
	}
}

// The walk's Info line carries the team narrowing's decision basis only when
// the request narrows by team: the rule, the issues left out of scope, and
// the floor reason (closed values and counts only).
func TestTheRepositoryWalkLineCarriesTheTeamNarrowingBasis(t *testing.T) {
	for name, tc := range map[string]struct {
		event   RepositoryWorkItemWalkEvent
		present bool
	}{
		"a team narrowing": {RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkMembers, Measured: true, Members: 2,
			TeamNarrowing: TeamNarrowingIssueProject, TeamExcluded: 6, TeamFloor: TeamNarrowingFloorProjectOwnerAbsent}, true},
		"no team narrowing": {RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkMembers, Measured: true, Members: 2}, false},
	} {
		t.Run(name, func(t *testing.T) {
			records := captureSlogJSON(t, func(logger *slog.Logger) {
				NewSlogEngineTelemetry(logger).RecordRepositoryWorkItemWalk(context.Background(), storage.Principal{OrgID: "org_sink_test"}, tc.event)
			})
			if len(records) != 1 {
				t.Fatalf("records = %v", records)
			}
			for _, key := range []string{"team_narrowing", "team_excluded", "team_floor_reason"} {
				if _, ok := records[0][key]; ok != tc.present {
					t.Fatalf("key %q present = %v, want %v (%v)", key, ok, tc.present, records[0])
				}
			}
			if tc.present && (records[0]["team_narrowing"] != "issue_project" || records[0]["team_excluded"] != float64(6) || records[0]["team_floor_reason"] != "project_owner_absent") {
				t.Fatalf("team fields = %v %v %v", records[0]["team_narrowing"], records[0]["team_excluded"], records[0]["team_floor_reason"])
			}
		})
	}
}
