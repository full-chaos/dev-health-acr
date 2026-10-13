package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Every scope expander statement that reads work items, link rows or
// membership transitions filters them to the requested origins first: each
// carries a candidate filter that the organization-wide rewrite removes whole.
func TestScopeExpanderStatementsFilterToTheRequestedOriginsFirst(t *testing.T) {
	client := &fakeClient{}
	expander := devhealthfacts.NewScopeExpander(client)
	now := time.Now().UTC()
	project, team := projectSubject("linear", "P"), teamSubject("A")
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	validTime := contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &now}
	for _, request := range []struct {
		policy contextfabric.FactScopePolicy
		kind   contextfabric.FactKind
		target contextfabric.SubjectKind
		axis   contextfabric.TimeContext
		origin contextfabric.SubjectRef
	}{
		{contextfabric.FactScopePolicyProjectWorkItemRepository, contextfabric.FactMetrics, contextfabric.SubjectRepository, current, project},
		{contextfabric.FactScopePolicyProjectWorkItemRepository, contextfabric.FactMetrics, contextfabric.SubjectRepository, validTime, project},
		{contextfabric.FactScopePolicyTeamPrimaryAttributionRepository, contextfabric.FactMetrics, contextfabric.SubjectRepository, current, team},
		{contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemStatus, contextfabric.FactStatus, contextfabric.SubjectWorkItem, current, team},
		{contextfabric.FactScopePolicyProjectWorkItemStatus, contextfabric.FactStatus, contextfabric.SubjectWorkItem, current, project},
		{contextfabric.FactScopePolicyProjectIssuePullRequestDeployments, contextfabric.FactDeployments, contextfabric.SubjectRepository, current, project},
	} {
		if _, err := expander.ExpandFactScope(context.Background(), contextfabric.FactScopeExpansionRequest{
			Principal: storage.Principal{OrgID: "org-filter-first"}, RequirementKind: request.kind, Origins: []contextfabric.SubjectRef{request.origin},
			Policy: request.policy, TargetKind: request.target, TimeContext: request.axis, Limit: 200,
		}); err != nil {
			t.Fatalf("%s: %v", request.policy, err)
		}
	}
	checked := 0
	for _, query := range client.queries {
		if !strings.Contains(query.statement, "work_items") && !strings.Contains(query.statement, "work_graph_issue_pr") && !strings.Contains(query.statement, "project_membership_transitions") {
			continue
		}
		checked++
		if _, filtered, clean := devhealthfacts.OrgWideScopeStatementForTest(query.statement); !filtered || !clean {
			t.Errorf("statement reads the organization's work items, links or transitions without the requested-origin filter (filtered %v, clean %v):\n%s", filtered, clean, query.statement)
		}
	}
	// The exact count of expander statements that read those tables: a reader
	// that disappears or a new one that arrives must change this on purpose.
	const wantReaders = 11
	if checked != wantReaders {
		t.Fatalf("%d statements read work items, links or transitions, want %d", checked, wantReaders)
	}
}
