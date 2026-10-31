package devhealthfacts_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func projectOnlyFactPrincipal() storage.Principal {
	return storage.Principal{OrgID: "org-1", ProjectScopes: []string{"project.v2:linear:proj-1"}}
}

// CHAOS-12299 F5: a project-only caller reading a project's health fact gets
// no team-scoped and no repository-scoped row. Every field of that fact rests
// on those rows, none is intrinsic to the project, so no fact is served and
// none of those rows is read. Every other class is served as before.
func TestAProjectOnlyCallerIsServedNoTeamOrRepositoryRowOfAProjectHealthFact(t *testing.T) {
	t.Parallel()
	tables := func() []fakeTable {
		return []fakeTable{
			{match: healthProjectRollupMatch, rows: [][]any{
				healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "low", 0.05),
				healthProjectRollupRow("linear", "proj-1", "repo", "repo-1", "acme/api", "elevated", 0.4),
			}},
			{match: healthSeverityMaxMatch, rows: [][]any{healthSeverityMaxRow("linear", "proj-1", 2, 2, "repo", "repo-1", "elevated")}},
		}
	}
	read := func(principal storage.Principal) (contextfabric.FactProviderResult, *fakeClient) {
		client := &fakeClient{tables: tables()}
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
		result, err := provider.ReadFacts(context.Background(), principal, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		return result, client
	}

	result, client := read(projectOnlyFactPrincipal())
	for _, fact := range result.Facts {
		if breakdown, ok := fact.Fields["risk_breakdown"]; ok && breakdown.Table != nil {
			for _, row := range breakdown.Table.Rows {
				t.Errorf("a project-only caller was served a %v row of a project health fact", row.Fields["scope"])
			}
		}
		t.Errorf("a project-only caller was served a project health fact resting on team and repository rows: %v", fact.Fields)
	}
	for _, query := range client.queries {
		if strings.Contains(query.statement, "team_project_ownership") {
			t.Errorf("a project-only caller's read joined team ownership: %.80s", query.statement)
		}
	}
	// Today the withheld subject carries no reason of its own (CHAOS-12330).
	if len(result.SubjectReasons) != 0 || len(result.NotApplicableSubjects) != 0 {
		t.Errorf("subject reasons %v, not applicable %v: a typed label arrived; update this pin", result.SubjectReasons, result.NotApplicableSubjects)
	}

	for _, principal := range []storage.Principal{
		{OrgID: "org-1"},
		{OrgID: "org-1", RepositoryScopes: []string{"*"}},
		{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}},
	} {
		result, _ := read(principal)
		if len(result.Facts) != 1 {
			t.Fatalf("%v: %d facts, want the project fact served as before", principal.RepositoryScopes, len(result.Facts))
		}
		if breakdown := result.Facts[0].Fields["risk_breakdown"]; breakdown.Table == nil || len(breakdown.Table.Rows) != 2 {
			t.Errorf("%v: risk_breakdown = %#v, want the team and repository rows as before", principal.RepositoryScopes, breakdown)
		}
	}
}

// Every guarded path (projectOnlyWithheld): a project-only caller's read of a
// project or organization subject issues no statement and serves no fact; an
// unrestricted caller's read of the same subject still issues one, so the pin
// keeps its control.
func TestEveryGuardedProjectAndOrganizationReadIsWithheldFromAProjectOnlyCaller(t *testing.T) {
	t.Parallel()
	project := projectSubject("linear", "proj-1")
	organization := organizationSubject("org-1")
	for _, tc := range []struct {
		kind    contextfabric.FactKind
		subject contextfabric.SubjectRef
		units   bool
	}{
		{kind: contextfabric.FactHealth, subject: project},
		{kind: contextfabric.FactFlow, subject: project},
		{kind: contextfabric.FactLandscape, subject: project},
		{kind: contextfabric.FactMetrics, subject: project},
		{kind: contextfabric.FactReadiness, subject: project},
		{kind: contextfabric.FactWorkload, subject: project},
		{kind: contextfabric.FactInvestment, subject: project},
		{kind: contextfabric.FactInvestment, subject: organization},
		{kind: contextfabric.FactInvestment, subject: organization, units: true},
		{kind: contextfabric.FactPullRequests, subject: project},
		{kind: contextfabric.FactBlockers, subject: project},
		{kind: contextfabric.FactCommitment, subject: project},
		{kind: contextfabric.FactActualCompletion, subject: project},
	} {
		name := string(tc.kind) + "/" + string(tc.subject.Kind)
		if tc.units {
			name += "/units"
		}
		read := func(principal storage.Principal) (facts, statements int) {
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
			ctx := context.Background()
			if tc.units {
				ctx = contextfabric.WithInvestmentUnits(ctx, contextfabric.InvestmentUnitsRequest{Max: 10})
			}
			query := contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: tc.kind, Subjects: []contextfabric.SubjectRef{tc.subject},
			}
			if loader, ok := provider.(interface {
				LoadPrerequisites(context.Context, storage.Principal, contextfabric.FactQuery) (context.Context, error)
			}); ok {
				loaded, err := loader.LoadPrerequisites(ctx, principal, query)
				if err != nil {
					t.Fatalf("%s: LoadPrerequisites() error = %v", name, err)
				}
				ctx = loaded
			}
			result, err := provider.ReadFacts(ctx, principal, query)
			if err != nil {
				t.Fatalf("%s: ReadFacts() error = %v", name, err)
			}
			return len(result.Facts), len(client.queries)
		}
		if facts, statements := read(projectOnlyFactPrincipal()); facts != 0 || statements != 0 {
			t.Errorf("%s: a project-only caller's read issued %d statements and served %d facts, want none", name, statements, facts)
		}
		if _, statements := read(storage.Principal{OrgID: "org-1"}); statements == 0 {
			t.Errorf("%s: an unrestricted caller's read issued no statement; this pin lost its control", name)
		}
	}
}

// source_health reads an operation, not a statement: a project-only caller's
// organization read makes no call and serves no fact; an unrestricted
// caller's makes one.
func TestSourceHealthIsWithheldFromAProjectOnlyCaller(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		principal storage.Principal
		calls     int
	}{{projectOnlyFactPrincipal(), 0}, {storage.Principal{OrgID: "org-1"}, 1}} {
		caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: sourceHealthData(
			`{"provider":"github","scope":"git","lastSyncAt":"2026-10-08T01:00:00Z","lastFailure":null}`)}}
		result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), tc.principal, organizationSubject("org-1"))
		if caller.calls != tc.calls || (tc.calls == 0 && len(result.Facts) != 0) {
			t.Errorf("%v: %d operation calls and %d facts, want %d calls", tc.principal.ProjectScopes, caller.calls, len(result.Facts), tc.calls)
		}
	}
}

// CHAOS-12299 r1 F2, the codex repro at provider level: a member work item's
// blocker and required-child reads, and every other work-item read, issue for
// a project-only caller exactly the statements and bindings a grant naming no
// valid repository issues, a grant that denies every row. With the empty
// list read as "all", a member's blocker to a non-member was served.
func TestAProjectOnlyWorkItemReadIsTheDenyRead(t *testing.T) {
	t.Parallel()
	subject := workItemSubject("repo-1", "WI-MEMBER")
	for _, kind := range []contextfabric.FactKind{
		contextfabric.FactBlockers, contextfabric.FactRequiredChildren, contextfabric.FactActualCompletion,
		contextfabric.FactStatus, contextfabric.FactWork,
	} {
		read := func(principal storage.Principal) []capturedQuery {
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
			if _, err := provider.ReadFacts(context.Background(), principal, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: kind, Subjects: []contextfabric.SubjectRef{subject},
			}); err != nil {
				t.Fatalf("%s: ReadFacts() error = %v", kind, err)
			}
			return client.queries
		}
		got := read(projectOnlyFactPrincipal())
		want := read(storage.Principal{OrgID: "org-1", RepositoryScopes: []string{" "}})
		if len(want) == 0 {
			t.Errorf("%s: the deny read issued no statement; this pin lost its control", kind)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: a project-only work-item read differs from the deny read (%d vs %d statements)", kind, len(got), len(want))
		}
		organizationWide := read(storage.Principal{OrgID: "org-1"})
		if reflect.DeepEqual(organizationWide, want) {
			t.Errorf("%s: the organization-wide read equals the deny read; this pin cannot tell them apart", kind)
		}
	}
}
