package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func projectsOfAnchorFrame(term string) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{term}, MemberKind: contextfabric.SubjectProject,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
}

func fulltextProjectRow(id, label string) row {
	return row{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: id, propLabel: label}}, "score": 1.0}
}

func cohortIDs(c *contextfabric.Cohort) map[string]bool {
	ids := map[string]bool{}
	if c == nil {
		return ids
	}
	for _, m := range c.Members {
		ids[m.Subject.CanonicalID] = true
	}
	return ids
}

// The team's own reach is the member set: an owned project is a member and a
// project only the question text matched is not.
func TestDiscoverContextTeamAnchorServesOnlyOwnedProjects(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return []row{fulltextProjectRow("project:text-only", "Platform Docs")}, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "team:platform" {
				return nil, nil
			}
			return []row{{
				"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNS", propRelationshipID: "rel_owned"}},
				"srcKind": "team", "srcId": "team:platform", "dstKind": "project", "dstId": "project:owned",
			}}, nil
		default:
			switch params["id"] {
			case "team:platform":
				return []row{fakeSubjectNodeRow("team", "team:platform", "Platform")}, nil
			case "project:owned":
				return []row{fakeSubjectNodeRow("project", "project:owned", "Billing")}, nil
			}
			return nil, nil
		}
	}}
	request := ownershipRoutingRequest(projectsOfAnchorFrame("Platform"), anchor)
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Question = "which projects does team Platform own?"
	result, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	ids := cohortIDs(result.Cohort)
	if !ids["project:owned"] || ids["project:text-only"] || len(ids) != 1 {
		t.Fatalf("members = %v, want exactly project:owned", ids)
	}
}

// A repository anchor keeps the lexical arm: only a team anchor is bound to
// its own reach.
func TestDiscoverContextNonTeamAnchorKeepsTextMatchedProjects(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:platform", Label: "full-chaos/platform"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "fulltext") {
			return []row{fulltextProjectRow("project:text-only", "Platform Docs")}, nil
		}
		return nil, nil
	}}
	request := ownershipRoutingRequest(projectsOfAnchorFrame("platform"), anchor)
	request.ScopeAnchorKind = contextfabric.SubjectRepository
	request.Request.Question = "which projects does repository platform have?"
	result, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if !cohortIDs(result.Cohort)["project:text-only"] {
		t.Fatalf("members = %v, want the text-matched project kept for a non-team anchor", cohortIDs(result.Cohort))
	}
}

// The guard binds the project members of a team anchor only: a team anchor
// asked for repositories, and a repository anchor beside a committed team,
// keep the lexical arm.
func TestDiscoverContextTeamGuardDoesNotBindOtherMemberKindsOrAnchorKinds(t *testing.T) {
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "fulltext") {
			return []row{fulltextProjectRow("project:text-only", "Platform Docs")}, nil
		}
		return nil, nil
	}}
	// A committed team while the frame's anchor is a repository.
	request := ownershipRoutingRequest(projectsOfAnchorFrame("platform"), team)
	request.ScopeAnchorKind = contextfabric.SubjectRepository
	request.Request.Question = "which projects does repository platform have?"
	result, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if !cohortIDs(result.Cohort)["project:text-only"] {
		t.Fatalf("members = %v, want the text match kept when the scope anchor is a repository", cohortIDs(result.Cohort))
	}
	// A team anchor asked for teams (kind fulltext arm stays on).
	if teamAnchoredProjectCohort(contextfabric.GraphDiscoveryRequest{
		Frame: projectsOfAnchorFrame("platform"), ScopeAnchorKind: contextfabric.SubjectTeam,
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{team}},
	}, contextfabric.SubjectRepository) {
		t.Fatal("guard must not bind a repository member kind")
	}
}
