package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A caller names an inactive team by id (answered team_inactive) and another
// hint resolves to a project; the question is a team-cohort question. Cohort
// discovery must not admit the active team of the same name: an inactive-hint
// team never enters any discovery path (CHAOS-9092 r2 P1).
func TestDiscoverContextInactiveTeamHintTwinNeverEntersTheCohort(t *testing.T) {
	for _, tc := range []struct{ name, twinLabel, hintTwinID string }{
		{"twin pointed by id and same name", "Platform", "team:jira:platform"},
		{"twin by name only (answer omitted the id)", "Platform", ""},
		{"twin pointed by id under another label", "Platform Team", "team:jira:platform"},
	} {
		t.Run(tc.name, func(t *testing.T) { inactiveHintTwinNeverEntersTheCohort(t, tc.twinLabel, tc.hintTwinID) })
	}
}

func inactiveHintTwinNeverEntersTheCohort(t *testing.T, twinLabel, hintTwinID string) {
	twinRow := func() row {
		r := fakeSubjectNodeRow("team", "team:jira:platform", twinLabel)
		r["n"].(*node).Properties["authorization_repositories"] = []string{"acme/api"}
		r["n"].(*node).Properties["authorization_teams"] = []string{"team:jira:platform"}
		return r
	}
	inactiveRow := func() row {
		return lookupRow("org-1", "team", "team:platform", "Platform", map[string]interface{}{propPropertyPrefix + "is_active": false, propAuthzRepos: []string{"acme/api"}})
	}
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		if id, ok := params["id"]; ok && id == "team:platform" {
			return []row{inactiveRow()}, nil
		}
		if targets, ok := params["targets"].([]interface{}); ok {
			var out []row
			for _, raw := range targets {
				if m, ok := raw.(map[string]interface{}); ok && m["id"] == "team:platform" {
					out = append(out, inactiveRow())
				}
			}
			return out, nil
		}
		if id, ok := params["id"]; ok && id == "p1" {
			return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
		}
		if strings.Contains(cypher, "fulltext") {
			return []row{{"node": twinRow()["n"], "score": 2.0}}, nil
		}
		if strings.Contains(cypher, "$kinds") {
			return []row{twinRow()}, nil
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}}
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	request.Request.Question = "which teams like Platform are struggling"
	request.Frame = discoveredTeamCohortFrame()
	request.Resolution = contextfabric.SubjectResolution{
		Candidates: []contextfabric.SubjectCandidate{},
		Committed:  []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}},
		InactiveHints: []contractsv1.ContextFabricInactiveHint{{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform"}, Answer: contractsv1.TeamInactiveReason, ActiveCanonicalID: hintTwinID,
		}},
	}
	// Control: with no inactive hint the same call DOES admit the twin, so the
	// absence below is the exclusion and not an unreachable path.
	control := request
	control.Resolution.InactiveHints = nil
	controlResult, err := adapter.DiscoverContext(context.Background(), principal, control)
	if err != nil {
		t.Fatal(err)
	}
	if controlResult.Cohort == nil || len(controlResult.Cohort.Members) != 1 || controlResult.Cohort.Members[0].Subject.CanonicalID != "team:jira:platform" {
		t.Fatalf("control cohort = %#v, want the twin as the one member", controlResult.Cohort)
	}
	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cohort != nil {
		for _, m := range result.Cohort.Members {
			if m.Subject.CanonicalID == "team:jira:platform" {
				t.Fatalf("cohort = %#v: the active twin of an inactive-hint team entered the cohort", result.Cohort.Members)
			}
		}
	}
}
