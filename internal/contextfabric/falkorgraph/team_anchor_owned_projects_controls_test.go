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
	for _, m := range result.Cohort.Members {
		if len(m.InclusionReasons) != 1 || m.InclusionReasons[0] == teamAnchorInclusionReason {
			t.Fatalf("inclusion reasons = %v, want the pool's own reason for a non-team anchor", m.InclusionReasons)
		}
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
	if len(teamAnchoredCohort(contextfabric.GraphDiscoveryRequest{
		Frame: projectsOfAnchorFrame("platform"), ScopeAnchorKind: contextfabric.SubjectTeam,
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{team}},
	}, contextfabric.SubjectTeam)) > 0 {
		t.Fatal("guard must not bind a team member kind")
	}
}

// A second committed subject's own reach is not the team's: the project only
// the second subject reaches is not a member of the team's cohort.
func TestDiscoverContextTeamAnchorExcludesProjectsOnlyAnotherCommittedSubjectReaches(t *testing.T) {
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:other", Label: "full-chaos/other"}
	edgeRow := func(id, src, srcKind, dst string) row {
		return row{
			"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: id, propEvidenceRefs: []string{"evidence_" + id + "_1234"}}},
			"srcKind": srcKind, "srcId": src, "dstKind": "project", "dstId": dst,
		}
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			switch params["id"] {
			case "team:platform":
				return []row{edgeRow("rel_team", "team:platform", "team", "project:owned")}, nil
			case "repository:other":
				return []row{
					edgeRow("rel_repo", "repository:other", "repository", "project:other-only"),
					{
						"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "rel_rev", propEvidenceRefs: []string{"evidence_rel_rev_1234"}}},
						"srcKind": "project", "srcId": "project:other-only", "dstKind": "repository", "dstId": "repository:other",
					},
				}, nil
			}
			return nil, nil
		default:
			switch params["id"] {
			case "team:platform":
				return []row{fakeSubjectNodeRow("team", "team:platform", "Platform")}, nil
			case "repository:other":
				return []row{fakeSubjectNodeRow("repository", "repository:other", "full-chaos/other")}, nil
			case "project:owned":
				return []row{fakeSubjectNodeRow("project", "project:owned", "Billing")}, nil
			case "project:other-only":
				return []row{fakeSubjectNodeRow("project", "project:other-only", "Other")}, nil
			}
			return nil, nil
		}
	}}
	request := ownershipRoutingRequest(projectsOfAnchorFrame("Platform"), team)
	request.Resolution.Committed = []contextfabric.SubjectRef{team, repo}
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Question = "which projects does team Platform own?"
	result, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	ids := cohortIDs(result.Cohort)
	if !ids["project:owned"] || ids["project:other-only"] {
		t.Fatalf("members = %v, want only project:owned", ids)
	}
	if len(result.Paths) == 0 {
		t.Fatal("fixture admitted no path, so the path check proves nothing")
	}
	for _, path := range result.Paths {
		for _, e := range path.Edges {
			if e.From.CanonicalID == "project:other-only" || e.To.CanonicalID == "project:other-only" {
				t.Fatalf("path %+v carries the excluded project", path)
			}
		}
		for _, n := range path.Nodes {
			if n.CanonicalID == "project:other-only" {
				t.Fatalf("path %+v carries the excluded project", path)
			}
		}
	}
	for _, m := range result.Cohort.Members {
		if len(m.InclusionReasons) != 1 || m.InclusionReasons[0] != teamAnchorInclusionReason {
			t.Fatalf("inclusion reasons = %v, want the team-scoped reason", m.InclusionReasons)
		}
	}
	if result.Cohort.Rationale != teamAnchorCohortRationale {
		t.Fatalf("rationale = %q, want the team-scoped rationale", result.Cohort.Rationale)
	}
}

// The skipped lexical arm is a decision on the trace, not an absence.
func TestDiscoverContextTeamAnchorReportsTheSkippedKindFulltextArm(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, nil
	}}
	telemetry := &recordingTelemetry{}
	request := ownershipRoutingRequest(projectsOfAnchorFrame("Platform"), anchor)
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	if _, err := newFakeAdapterWithTelemetry(t, fake, telemetry).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.cohortKindFulltexts) != 1 || telemetry.cohortKindFulltexts[0].decision != CohortKindFulltextTeamAnchorReach {
		t.Fatalf("cohortKindFulltexts = %+v, want one team_anchor_reach decision", telemetry.cohortKindFulltexts)
	}
}
