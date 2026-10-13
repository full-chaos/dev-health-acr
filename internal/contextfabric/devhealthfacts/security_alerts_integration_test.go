package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func seedSecurityAlert(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}, orgID, repoID, alertID, source, severity, state string, created, synced time.Time) {
	t.Helper()
	if err := direct.Exec(ctx, `INSERT INTO security_alerts (org_id, repo_id, alert_id, source, severity, state, created_at, last_synced) VALUES (?,?,?,?,?,?,?,?)`,
		orgID, repoID, alertID, source, severity, state, created, synced); err != nil {
		t.Fatalf("seed security_alerts(%s): %v", alertID, err)
	}
}

func seedSecurityRepo(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}, orgID, repoID, slug, provider string, at time.Time) {
	t.Helper()
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoID, orgID, slug, provider, at); err != nil {
		t.Fatalf("seed repos(%s): %v", slug, err)
	}
}

// The served read_facts output counts the newest stored version of each
// alert, scopes to the org, never zero-fills closed counts, and says
// not_collected for a provider with no security source.
func TestSecurityAlertsServedByReadFactsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	const orgID = "org-security-alerts"
	const otherOrg = "org-security-alerts-other"
	at := ts(2026, 10, 1, 6, 0, 0)
	later := at.Add(24 * time.Hour)
	ghRepo, glRepo, emptyRepo, jiraRepo := repoUUID("sec-gh"), repoUUID("sec-gl"), repoUUID("sec-empty"), repoUUID("sec-jira")
	seedSecurityRepo(t, ctx, direct, orgID, ghRepo, "acme/sec-gh", "github", at)
	seedSecurityRepo(t, ctx, direct, orgID, glRepo, "acme/sec-gl", "gitlab", at)
	seedSecurityRepo(t, ctx, direct, orgID, emptyRepo, "acme/sec-empty", "github", at)
	seedSecurityRepo(t, ctx, direct, orgID, jiraRepo, "acme/sec-jira", "jira", at)

	seedSecurityAlert(t, ctx, direct, orgID, ghRepo, "a1", "dependabot", "critical", "open", at, at)
	seedSecurityAlert(t, ctx, direct, orgID, ghRepo, "a2", "dependabot", "high", "open", at, at)
	seedSecurityAlert(t, ctx, direct, orgID, ghRepo, "a2", "dependabot", "high", "fixed", at, later)
	seedSecurityAlert(t, ctx, direct, orgID, ghRepo, "a3", "code_scanning", "medium", "open", at.Add(-48*time.Hour), at)
	seedSecurityAlert(t, ctx, direct, orgID, ghRepo, "a4", "advisory", "high", "published", at, at)
	seedSecurityAlert(t, ctx, direct, orgID, glRepo, "g1", "gitlab_vulnerability", "high", "detected", at, at)
	seedSecurityAlert(t, ctx, direct, orgID, jiraRepo, "j1", "dependabot", "high", "open", at, at)
	seedSecurityAlert(t, ctx, direct, otherOrg, ghRepo, "x1", "dependabot", "critical", "open", at, at)

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	seam := &seamCapture{registry: registry.WithoutScopeExpansion()}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitAllGraph{}, nil), directread.NewFactReader(seam), nil)
	subjects := []directread.RequestSubject{}
	for _, id := range []string{ghRepo, glRepo, emptyRepo, jiraRepo} {
		subjects = append(subjects, directread.RequestSubject{Kind: "repository", CanonicalID: "repository:" + id})
	}
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000009524"),
		storage.Principal{OrgID: orgID, Subject: "t9524", CredentialID: "t9524"}, directread.FactsRequest{
			Kinds: []string{"security_alerts"}, Subjects: subjects,
		})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	raw, _ := json.Marshal(response)
	byRepo := map[string]map[string]any{}
	for _, fact := range response.Facts {
		byRepo[fact.Subject.CanonicalID] = fact.Fields
	}
	if len(byRepo) != 4 {
		t.Fatalf("facts for %d repositories, want 4: %s", len(byRepo), raw)
	}
	num := func(fields map[string]any, name string) string {
		value, ok := fields[name]
		if !ok {
			return "<absent>"
		}
		out, _ := json.Marshal(value)
		return string(out)
	}
	gh := byRepo["repository:"+ghRepo]
	for name, want := range map[string]string{
		"open_alert_count": `"2"`, "open_dependabot_count": `"1"`, "open_code_scanning_count": `"1"`, "advisory_stored_count": `"1"`,
		"open_critical_count": `"1"`, "open_high_count": `"0"`, "open_medium_count": `"1"`,
	} {
		if got := num(gh, name); got != want {
			t.Errorf("github %s = %s, want %s (the fixed version of a2 must not count; the other org's alert must not count): %s", name, got, want, raw)
		}
	}
	for _, name := range []string{"closed_alert_basis", "secret_scanning_basis"} {
		if got := num(gh, name); got != `"not_collected"` {
			t.Errorf("github %s = %s, want not_collected", name, got)
		}
	}
	gl := byRepo["repository:"+glRepo]
	if got := num(gl, "open_gitlab_vulnerability_count"); got != `"1"` {
		t.Errorf("gitlab open_gitlab_vulnerability_count = %s, want 1 (detected counts as open)", got)
	}
	if got := num(byRepo["repository:"+emptyRepo], "collection_basis"); got != `"no_alerts_stored_collection_state_unknown"` {
		t.Errorf("empty repository collection_basis = %s", got)
	}
	if got := num(byRepo["repository:"+emptyRepo], "open_alert_count"); got != "<absent>" {
		t.Errorf("empty repository open_alert_count = %s, want absent, never a zero", got)
	}
	jira := byRepo["repository:"+jiraRepo]
	if got := num(jira, "collection_basis"); got != `"not_collected"` {
		t.Errorf("jira repository collection_basis = %s", got)
	}
	if got := num(jira, "open_alert_count"); got != "<absent>" {
		t.Errorf("jira repository open_alert_count = %s, want absent: its stored row must not be served", got)
	}
}
