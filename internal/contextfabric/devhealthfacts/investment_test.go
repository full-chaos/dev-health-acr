package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func teamMixTables(teamID string) []fakeTable {
	return []fakeTable{watermarkTable(), ownsRepoTable(teamID), {match: "FROM work_unit_investments", rows: [][]any{
		themeMixRow(teamID, "", map[string]float64{"feature_delivery": 60, "operational": 20, "maintenance": 10, "quality": 6, "risk": 4}, 0),
	}}}
}

func TestInvestmentProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: teamMixTables("CHAOS")}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 || result.State != contextfabric.SourceAvailable || result.Reason != "" {
		t.Fatalf("result = %#v, want 1 fact, available, no reason", result)
	}
	fact := result.Facts[0]
	if got := fact.Fields["theme_feature_delivery"].Number; got == nil || *got < 0.6-1e-9 || *got > 0.6+1e-9 {
		t.Fatalf("fields = %#v, want theme_feature_delivery 0.6", fact.Fields)
	}
}

// TestInvestmentProviderTwoTeamsOneWithoutAMixServesOneAndDisclosesTheOther
// pins the loud-absence rule: no fact and no fabricated zero for the team
// without a mix, and the reason says how many are unavailable.
func TestInvestmentProviderTwoTeamsOneWithoutAMixServesOneAndDisclosesTheOther(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: teamMixTables("CHAOS")}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS"), teamSubject("ghost")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 || !strings.Contains(result.RequestNote, "investment mix unavailable for 1 of 2 requested teams") {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(result.Reason, "investment_team_no_owned_repository") || strings.Contains(result.Reason, "owns no repository") {
		t.Fatalf("kind reason %q carries the no-owned sentence every team row would inherit", result.Reason)
	}
	if got, has := result.SubjectReasons[contextfabric.FactSubjectKey(teamSubject("CHAOS"))]; has {
		t.Fatalf("the team with a served fact carries own reason %q", got)
	}
	if got := result.SubjectReasons[contextfabric.FactSubjectKey(teamSubject("ghost"))]; !strings.Contains(got, "owns no repository") {
		t.Fatalf("the team without a mix has own reason %q", got)
	}
}

func TestInvestmentProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := teamOwningClient(contextfabric.FactInvestment, "ghost")
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("ghost")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

func TestInvestmentProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_repo_ownership", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestInvestmentProviderScopedToOrgAndRequestedSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_repo_ownership", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(client.queries) < 2 {
		t.Fatalf("queries = %d, want the ownership read and the watermark read", len(client.queries))
	}
	ownership := client.queries[0]
	for _, binding := range ownership.bindings {
		if binding.Name == "org_id" && binding.Value != "org-8" {
			t.Fatalf("ownership org_id binding = %v", binding.Value)
		}
		if binding.Name == "ids" {
			if ids, _ := binding.Value.([]string); len(ids) != 1 || ids[0] != "CHAOS" {
				t.Fatalf("ownership ids binding = %#v, want exactly the requested subject", binding.Value)
			}
		}
	}
	assertQueryScopedToOrgAndSubjects(t, ownership.statement)
	watermark := client.queries[len(client.queries)-1]
	if !strings.Contains(watermark.statement, "FROM work_unit_investments WHERE org_id = {org_id:String}") {
		t.Fatalf("watermark statement = %q, want it scoped to the org", watermark.statement)
	}
}

// TestInvestmentProviderRowForUnrequestedTeamNeverAppears is the F5
// result-content guard.
func TestInvestmentProviderRowForUnrequestedTeamNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: teamMixTables("other-team")}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty -- the returned row belongs to an unrequested team", result.Facts)
	}
}

// TestInvestmentProviderProjectRollupNoOwningTeamsHasNoFactEntry mirrors
// metrics.go's identical guard for the investment project path.
func TestInvestmentProviderProjectRollupNoOwningTeamsHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_unit_investments", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
	// Missing is not healthy (CHAOS-9195): the absence is NAMED, never silent.
	if served := contextfabric.CombineRequestNote(result.Reason, result.RequestNote); !strings.Contains(served, "investment_project_no_canonical_mix") {
		t.Fatalf("reason = %q, want the named investment_project_no_canonical_mix", served)
	}
}
