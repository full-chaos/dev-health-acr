package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5783: "how many teams own repository R" is an OWNERSHIP question,
// answered from authorization_repositories -- a declared, already
// multi-owner-safe PROPERTY (teams_projects.go's ownedRepositoriesJoinSQL) --
// never from hopWalk's bounded graph-proximity traversal, which answers a
// different question (which subjects happen to be within two hops of the
// repository's own activity) that can under- or over-count real owners.
//
// repositoryAnchorFrame is a children_of_scope frame anchored on a
// repository, declaring team as the member kind -- the one pairing
// repository-anchored team cohorts route through ownership.
func repositoryAnchorFrame() *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{"dev-health-acr"}, MemberKind: contextfabric.SubjectTeam,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
}

func ownershipRoutingRequest(frame *contextfabric.QuestionFrame, anchor contextfabric.SubjectRef) contextfabric.GraphDiscoveryRequest {
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "repository team ownership cohort fixture",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape:       contextfabric.ShapeDiscoveredCohort,
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}},
		Frame:      frame,
	}
}

// TestDiscoverContextRoutesRepositoryAnchoredTeamCohortThroughOwnership is the
// red-then-green pin: two teams whose OWN authorization_repositories names the
// anchor repository both count, a team hopWalk would have reached through
// incidental adjacency but that does not own the repository does not, and a
// team with no ownership signal at all (the fail-closed sentinel value
// queryTeams stamps for a team with no current ownership row) does not
// either. hopWalk itself is never called for the routed anchor --
// the fake fails the test outright if it is.
func TestDiscoverContextRoutesRepositoryAnchoredTeamCohortThroughOwnership(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: "full-chaos/dev-health-acr"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			inferredOwner := fakeSubjectNodeRow("team", "team:CHAOS", "Fullchaos")
			inferredOwner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/dev-health-acr", "full-chaos/dev-health-ops"}
			providerOwner := fakeSubjectNodeRow("team", "team:gh:ops-team", "Ops Team")
			providerOwner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/dev-health-acr"}
			proximateNonOwner := fakeSubjectNodeRow("team", "team:adjacent", "Adjacent")
			proximateNonOwner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/some-other-repo"}
			nullCarrying := fakeSubjectNodeRow("team", "team:null", "No Ownership Signal")
			nullCarrying["n"].(*node).Properties[propAuthzRepos] = []string{"acr-context-fabric:no-team-repository-ownership"}
			return []row{inferredOwner, providerOwner, proximateNonOwner, nullCarrying}, nil
		default:
			t.Fatalf("hopWalk must not run for a repository anchor whose member kind is team -- ownership routing serves it instead; got cypher: %s", cypher)
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := ownershipRoutingRequest(repositoryAnchorFrame(), anchor)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil, want the two owning teams")
	}
	if got := len(result.Cohort.Members); got != 2 {
		t.Fatalf("Cohort.Members = %d, want exactly 2 (both real owners): %+v", got, result.Cohort.Members)
	}
	gotIDs := map[string]bool{}
	for _, m := range result.Cohort.Members {
		gotIDs[m.Subject.CanonicalID] = true
	}
	if !gotIDs["team:CHAOS"] || !gotIDs["team:gh:ops-team"] {
		t.Fatalf("Cohort.Members = %+v, want team:CHAOS and team:gh:ops-team, no others", result.Cohort.Members)
	}
	if gotIDs["team:adjacent"] {
		t.Fatal("Cohort.Members includes team:adjacent, a proximate but non-owning team -- ownership routing must exclude it")
	}
	if gotIDs["team:null"] {
		t.Fatal("Cohort.Members includes team:null, a team with no ownership signal (R179: null-carrying teams are not owners)")
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceOwnership {
		t.Fatalf("CohortMemberSource = %q, want %q", result.CohortMemberSource, contextfabric.CohortMemberSourceOwnership)
	}
}

// TestDiscoverContextRepositoryAnchorWithNoSlugFallsBackToHopWalk is the
// boundary case the routing gate itself must refuse rather than guess: a
// committed repository anchor whose Label is empty carries no slug to narrow
// an ownership census by, so hopWalk still runs for it -- the same as a
// committed subject of any other kind.
func TestDiscoverContextRepositoryAnchorWithNoSlugFallsBackToHopWalk(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: ""}
	hopWalked := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("the ownership census must not run for a repository anchor with no slug to narrow by")
			return nil, nil
		default:
			hopWalked = true
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := ownershipRoutingRequest(repositoryAnchorFrame(), anchor)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if !hopWalked {
		t.Fatal("hopWalk's own query was never reached")
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceHopWalk {
		t.Fatalf("CohortMemberSource = %q, want %q", result.CohortMemberSource, contextfabric.CohortMemberSourceHopWalk)
	}
}

// TestDiscoverContextStillHopWalksForANonRepositoryAnchor is the control:
// team members under a PROJECT anchor are unaffected by the ownership
// routing above and still route through hopWalk, never the ownership census.
func TestDiscoverContextStillHopWalksForANonRepositoryAnchor(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:launchpad", Label: "Launchpad"}
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{"launchpad"}, MemberKind: contextfabric.SubjectTeam,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("the ownership census must not run for a non-repository anchor")
			return nil, nil
		default:
			// hopWalk's own edgesOfNode call -- no edges out of the origin,
			// which is a legitimate hopWalk outcome (an isolated node) and
			// proves the arm ran without needing an edge-shaped fake row.
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := ownershipRoutingRequest(frame, anchor)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceHopWalk {
		t.Fatalf("CohortMemberSource = %q, want %q", result.CohortMemberSource, contextfabric.CohortMemberSourceHopWalk)
	}
}
