package devhealthfacts_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func bareOrganizationSubject(id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: id, Label: id}
}

func readOrganizationInvestment(t *testing.T, client *fakeClient, principal storage.Principal, subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), principal, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return result
}

// organization row columns: theme effort, bugfix effort, work units,
// repositories, resolved effort, unresolved effort, earliest start.
func organizationMixTable() []fakeTable {
	return []fakeTable{{match: "FROM work_unit_investments", rows: [][]any{
		{map[string]float64{"feature_delivery": 30, "operational": 50, "maintenance": 10, "quality": 6, "risk": 4}, 5.0, uint64(9), uint64(3), 100.0, 25.0, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
	}}}
}

func TestInvestmentCapabilityServesTheOrganizationSubject(t *testing.T) {
	t.Parallel()
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{}), contextfabric.FactInvestment)
	found := false
	for _, kind := range provider.Capability().SupportedSubjectKinds {
		found = found || kind == contextfabric.SubjectOrganization
	}
	if !found {
		t.Fatalf("investment capability kinds = %v, want organization among them", provider.Capability().SupportedSubjectKinds)
	}
}

func TestInvestmentOrganizationFactIsOneFactNotATeamSum(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: organizationMixTable()}
	result := readOrganizationInvestment(t, client, storage.Principal{OrgID: "org-1"}, organizationSubject("org-1"))
	if len(result.Facts) != 1 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %#v, want one available fact", result)
	}
	fields := result.Facts[0].Fields
	if got := *fields["scope"].String; got != "organization" {
		t.Fatalf("scope = %q", got)
	}
	if got := *fields["repositories_in_scope"].Integer; got != 3 {
		t.Fatalf("repositories_in_scope = %d, want 3", got)
	}
	if got := *fields["work_unit_count"].Integer; got != 9 {
		t.Fatalf("work_unit_count = %d, want 9", got)
	}
	for theme, want := range map[string]float64{"theme_feature_delivery": 0.3, "theme_operational": 0.5, "theme_maintenance": 0.1, "theme_quality": 0.06, "theme_risk": 0.04} {
		if got := *fields[theme].Number; math.Abs(got-want) > 1e-9 {
			t.Fatalf("%s = %v, want %v", theme, got, want)
		}
	}
	if got, want := *fields["unresolved_effort_share"].Number, 25.0/125.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("unresolved_effort_share = %v, want %v", got, want)
	}
	for _, query := range client.queries {
		if strings.Contains(query.statement, "team_repo_ownership") {
			t.Fatalf("the organization mix went through team ownership: %s", query.statement)
		}
	}
	if err := fields["theme_breakdown"].Table.Validate(); err != nil {
		t.Fatalf("theme_breakdown invalid: %v", err)
	}
}

func TestInvestmentOrganizationAcceptsTheBareOrganizationId(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: organizationMixTable()}
	result := readOrganizationInvestment(t, client, storage.Principal{OrgID: "org-1"}, bareOrganizationSubject("org-1"))
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1 for the bare organization id", len(result.Facts))
	}
}

func TestInvestmentOrganizationOfAnotherOrganizationIsNeverRead(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: organizationMixTable()}
	result := readOrganizationInvestment(t, client, storage.Principal{OrgID: "org-1"}, organizationSubject("org-2"))
	if len(result.Facts) != 0 || len(client.queries) != 0 {
		t.Fatalf("facts=%d queries=%d, want none for a foreign organization", len(result.Facts), len(client.queries))
	}
}

func TestInvestmentOrganizationForARepositoryBoundCallerIsALimitationNotAPartialTotal(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: organizationMixTable()}
	result := readOrganizationInvestment(t, client, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/repo-a"}}, organizationSubject("org-1"))
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNotApplicable || !strings.Contains(result.Reason, "repository-bound") {
		t.Fatalf("result = %#v, want not_applicable with the repository-bound reason and no fact", result)
	}
	if len(client.queries) != 0 {
		t.Fatalf("a repository-bound caller must not trigger the org-wide read: %d queries", len(client.queries))
	}
}

func TestInvestmentOrganizationWithNoEffortServesNoFact(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_unit_investments", rows: [][]any{
		{map[string]float64{}, 0.0, uint64(0), uint64(0), 0.0, 0.0, time.Unix(0, 0).UTC()},
	}}}}
	result := readOrganizationInvestment(t, client, storage.Principal{OrgID: "org-1"}, organizationSubject("org-1"))
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %d, want none (never a zero mix)", len(result.Facts))
	}
}

func TestInvestmentOrganizationReadFailureIsAFailureNotAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_unit_investments", err: errors.New("store down")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{organizationSubject("org-1")},
	})
	if err == nil {
		t.Fatal("a failed organization mix read was served as an empty answer")
	}
}

func TestInvestmentOrganizationNamesTheStoredSpanWhenTheWindowStartsBeforeIt(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: organizationMixTable()}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{organizationSubject("org-1")},
	})
	if err != nil || len(result.Facts) != 1 {
		t.Fatalf("err=%v facts=%d", err, len(result.Facts))
	}
	if !strings.Contains(result.Reason, "investment_window_beyond_stored_history") {
		t.Fatalf("reason = %q, want the stored-span disclosure", result.Reason)
	}
}
