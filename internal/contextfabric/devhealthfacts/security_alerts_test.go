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

func readSecurityAlerts(t *testing.T, provider string, alertRows [][]any) (contextfabric.CanonicalFact, *fakeClient, contextfabric.FactProviderResult) {
	t.Helper()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repos", rows: [][]any{{"repo-1", "example-org/widget", provider}}},
		{match: "FROM security_alerts", rows: alertRows},
	}}
	p := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactSecurityAlerts)
	result, err := p.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactSecurityAlerts, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	return result.Facts[0], client, result
}

func securityInt(t *testing.T, fact contextfabric.CanonicalFact, name string) int64 {
	t.Helper()
	value, ok := fact.Fields[name]
	if !ok || value.Integer == nil {
		t.Fatalf("field %s = %#v, want an integer", name, value)
	}
	return *value.Integer
}

func securityString(t *testing.T, fact contextfabric.CanonicalFact, name string) string {
	t.Helper()
	value, ok := fact.Fields[name]
	if !ok || value.String == nil {
		t.Fatalf("field %s = %#v, want a string", name, value)
	}
	return *value.String
}

func TestSecurityAlertsGitHubServesOpenCountsBySourceAndSeverity(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	fact, client, _ := readSecurityAlerts(t, "github", [][]any{
		{"repo-1", "dependabot", "critical", int64(2), int64(3), at, at.Add(24 * time.Hour), at.Add(48 * time.Hour)},
		{"repo-1", "code_scanning", "", int64(1), int64(1), at.Add(-24 * time.Hour), at.Add(-24 * time.Hour), at.Add(72 * time.Hour)},
		{"repo-1", "advisory", "low", int64(0), int64(1), time.Time{}, time.Time{}, at},
	})
	want := map[string]int64{
		"open_alert_count": 3, "open_dependabot_count": 2, "open_code_scanning_count": 1, "advisory_stored_count": 1,
		"open_critical_count": 2, "open_high_count": 0, "open_medium_count": 0, "open_low_count": 0, "open_other_severity_count": 1,
	}
	for name, count := range want {
		if got := securityInt(t, fact, name); got != count {
			t.Errorf("%s = %d, want %d", name, got, count)
		}
	}
	if got := securityString(t, fact, "collection_basis"); got != "open_alerts_only" {
		t.Errorf("collection_basis = %q", got)
	}
	if got := securityString(t, fact, "oldest_open_created_at"); got != "2026-09-30T00:00:00Z" {
		t.Errorf("oldest_open_created_at = %q", got)
	}
	if got := securityString(t, fact, "newest_open_created_at"); got != "2026-10-02T00:00:00Z" {
		t.Errorf("newest_open_created_at = %q", got)
	}
	if got := securityString(t, fact, "latest_synced_at"); got != "2026-10-04T00:00:00Z" {
		t.Errorf("latest_synced_at = %q", got)
	}
	for _, name := range []string{"open_gitlab_vulnerability_count", "open_gitlab_dependency_count"} {
		if _, present := fact.Fields[name]; present {
			t.Errorf("%s is served for a github repository", name)
		}
	}
	statement := client.queries[len(client.queries)-1].statement
	if !strings.Contains(statement, "argMax(ifNull(state, ''), last_synced)") || !strings.Contains(statement, "org_id = {org_id:String}") {
		t.Fatalf("statement = %q, want the newest stored version of each alert, scoped to the org", statement)
	}
}

func TestSecurityAlertsNeverServeClosedCountsOrAlertText(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	fact, _, _ := readSecurityAlerts(t, "gitlab", [][]any{{"repo-1", "gitlab_vulnerability", "high", int64(1), int64(5), at, at, at}})
	if got := securityString(t, fact, "closed_alert_basis"); got != "not_collected" {
		t.Errorf("closed_alert_basis = %q, want not_collected", got)
	}
	if got := securityString(t, fact, "secret_scanning_basis"); got != "not_collected" {
		t.Errorf("secret_scanning_basis = %q, want not_collected", got)
	}
	for name := range fact.Fields {
		for _, banned := range []string{"closed", "fixed", "resolved", "dismissed", "title", "url", "package", "description", "cve"} {
			if strings.Contains(name, banned) && name != "closed_alert_basis" {
				t.Errorf("field %s serves %s data", name, banned)
			}
		}
	}
	if got := securityInt(t, fact, "open_gitlab_vulnerability_count"); got != 1 {
		t.Errorf("open_gitlab_vulnerability_count = %d, want 1", got)
	}
	if got := securityInt(t, fact, "open_gitlab_dependency_count"); got != 0 {
		t.Errorf("open_gitlab_dependency_count = %d, want 0 (the source is collected, none open)", got)
	}
}

func TestSecurityAlertsWithNoStoredRowsDoNotClaimZero(t *testing.T) {
	t.Parallel()
	fact, _, _ := readSecurityAlerts(t, "github", nil)
	if got := securityString(t, fact, "collection_basis"); got != "no_alerts_stored_collection_state_unknown" {
		t.Errorf("collection_basis = %q", got)
	}
	if _, present := fact.Fields["open_alert_count"]; present {
		t.Errorf("open_alert_count is served for a repository with no stored alert rows")
	}
}

func TestSecurityAlertsProviderWithoutSecuritySourceIsNotCollected(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"jira", "linear", ""} {
		fact, client, _ := readSecurityAlerts(t, provider, nil)
		if got := securityString(t, fact, "collection_basis"); got != "not_collected" {
			t.Errorf("provider %q: collection_basis = %q, want not_collected", provider, got)
		}
		if _, present := fact.Fields["open_alert_count"]; present {
			t.Errorf("provider %q: open_alert_count is served", provider)
		}
		for _, query := range client.queries {
			if strings.Contains(query.statement, "FROM security_alerts") {
				t.Errorf("provider %q: security_alerts was read", provider)
			}
		}
	}
}

func TestSecurityAlertsRefusesAPastTime(t *testing.T) {
	t.Parallel()
	p := findProvider(t, devhealthfacts.NewProviders(&fakeClient{}), contextfabric.FactSecurityAlerts)
	asOf := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	result, err := p.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		Kind: contextfabric.FactSecurityAlerts, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if result.State != contextfabric.SourceNotApplicable || len(result.Facts) != 0 {
		t.Fatalf("result = %#v, want not_applicable with no facts", result)
	}
}

func securityGroupRows(n int) [][]any {
	at := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	rows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, []any{"repo-1", "dependabot", "high", int64(1), int64(1), at, at, at})
	}
	return rows
}

func TestSecurityAlertsExactlyTheRowLimitIsNotTruncated(t *testing.T) {
	t.Parallel()
	_, _, atLimit := readSecurityAlerts(t, "github", securityGroupRows(1000))
	if atLimit.Truncated {
		t.Errorf("a read of exactly 1000 groups is marked truncated")
	}
	_, _, overLimit := readSecurityAlerts(t, "github", securityGroupRows(1001))
	if !overLimit.Truncated {
		t.Errorf("a read of 1001 groups is not marked truncated")
	}
}

func TestSecurityAlertsStatementKeepsANullStateOfTheNewestVersion(t *testing.T) {
	t.Parallel()
	_, client, _ := readSecurityAlerts(t, "github", securityGroupRows(1))
	var statement string
	for _, recorded := range client.queries {
		if strings.Contains(recorded.statement, "FROM security_alerts") {
			statement = recorded.statement
		}
	}
	for _, want := range []string{"argMax(ifNull(state, ''), last_synced)", "argMax(ifNull(severity, ''), last_synced)", "LIMIT 1001"} {
		if !strings.Contains(statement, want) {
			t.Errorf("statement lacks %q: %s", want, statement)
		}
	}
}
