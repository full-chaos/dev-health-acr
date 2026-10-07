package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A repository-restricted caller asking for the projects a team owns, where
// every owned project is outside the caller's reach: the answer must say the
// members were excluded by authorization, not that none were found.
func TestDiscoverContextTeamAnchorWhollyDeniedProjectsFilesTheDeniedCountRow(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:chaos", Label: "Fullchaos"}
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{"Fullchaos"}, MemberKind: contextfabric.SubjectProject,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	denied := []string{"other/private"}
	allowed := []string{"full-chaos/dev-health-acr"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "team:chaos" {
				return nil, nil
			}
			var rows []row
			for _, id := range []string{"p1", "p2", "p3"} {
				rows = append(rows, row{
					"r": &edge{Properties: map[string]interface{}{
						propRelationType: "OWNED_BY_TEAM", propRelationshipID: "rel_" + id,
						"authorization_repositories": allowed,
					}},
					"srcKind": "team", "srcId": "team:chaos", "dstKind": "project", "dstId": id,
				})
			}
			return rows, nil
		default:
			id, _ := params["id"].(string)
			if id == "team:chaos" {
				r := fakeSubjectNodeRow("team", id, "Fullchaos")
				r["n"].(*node).Properties["authorization_repositories"] = allowed
				return []row{r}, nil
			}
			if id == "p1" || id == "p2" || id == "p3" {
				r := fakeSubjectNodeRow("project", id, "Project "+id)
				r["n"].(*node).Properties["authorization_repositories"] = denied
				return []row{r}, nil
			}
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: allowed}
	request := ownershipRoutingRequest(frame, anchor)
	request.Request.Question = "which projects does team Fullchaos own?"

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort != nil {
		t.Fatalf("Cohort = %+v, want nil: every owned project is denied", result.Cohort)
	}
	found := false
	for _, reason := range result.Coverage.DegradedReasons {
		if strings.HasPrefix(reason, "cohort_denied_by_authorization") {
			found = true
		}
	}
	if !found || !result.Coverage.Partial {
		t.Fatalf("Partial=%v DegradedReasons=%v, want a cohort_denied_by_authorization reason", result.Coverage.Partial, result.Coverage.DegradedReasons)
	}
}
