package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12160 through ReadFacts: the universal fake serves a project the
// owning teams' roll-up (no native row reaches the project), so the read must
// put the project's own unit count on the fact and the disclosure in its
// coverage reason.
func TestAProjectRollupReadDisclosesTheProjectsOwnUnits(t *testing.T) {
	t.Parallel()
	canonical, row := clockTestSubjectID(contextfabric.SubjectProject)
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonical, Label: "subject-1"}
	provider := findProvider(t, devhealthfacts.NewProviders(&universalClient{subjectID: row, rows: 1, rules: clockTestRules()}), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-x", RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{subject}, Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	fact := result.Facts[0]
	source := fact.Fields[contextfabric.FactFieldInvestmentMixSource]
	if source.String == nil || *source.String != contextfabric.InvestmentMixSourceOwningTeamRollup {
		t.Fatalf("fixture defect: investment_mix_source = %v, want owning_team_rollup", source.String)
	}
	own := fact.Fields["project_own_work_unit_count"]
	if own.Integer == nil || *own.Integer != 0 {
		t.Fatalf("project_own_work_unit_count = %v, want 0", own.Integer)
	}
	reason := result.SubjectReasons[contextfabric.FactSubjectKey(subject)]
	if !strings.Contains(reason, "investment_project_owning_team_rollup: "+canonical+": the theme mix is the owning teams' roll-up over ") || !strings.Contains(reason, "the project has 0 work units of its own in the window") {
		t.Fatalf("subject reason = %q, want the owning-team roll-up disclosure", reason)
	}
}
