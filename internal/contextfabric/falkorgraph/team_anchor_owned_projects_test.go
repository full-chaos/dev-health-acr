package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A team anchor with no ownership path to any project must not yield an
// organization-style cohort of whatever project the question text matched.
func TestDiscoverContextTeamAnchorWithoutOwnedProjectsDoesNotServeTextMatchedProject(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{"Platform"}, MemberKind: contextfabric.SubjectProject,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "fulltext") {
			return []row{{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "project:platform-docs", propLabel: "Platform Docs - User Guide Coverage"}}, "score": 1.0}}, nil
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	request := ownershipRoutingRequest(frame, anchor)
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Question = "which projects does team Platform own?"
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort != nil {
		t.Fatalf("Cohort = %+v, want nil: the team has no ownership path to the text-matched project", result.Cohort)
	}
}
