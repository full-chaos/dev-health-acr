package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestZZPrRowsDiagLinkedPullRequestBreakdown(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "projects") {
		if err := direct.Exec(ctx, statement); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create table: %v", err)
		}
	}
	at := time.Now().UTC().Truncate(time.Second)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	repo := repoUUID("pr-rows-diag")
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo, orgID, "acme/diag", "github", at)
	const project = "prrows-diag-project"
	exec(`INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		project, orgID, "PR rows diag", project, "jira", "", "", uint8(1), at)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at, provider) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"WI-DIAG", repo, orgID, "linked", "open", "", "", project, at, "jira")
	seedIssueLink(t, ctx, direct, orgID, "WI-DIAG", repo, 900, "native", at)

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitAllGraph{}, nil), directread.NewFactReader(registry.WithoutScopeExpansion()), nil)
	var out []string
	for _, mode := range []string{directread.TablesInclude, directread.TablesOnly} {
		response, err := reader.Read(observability.WithRequestID(ctx, "req_0000000000000000000000000000d1a6"),
			storage.Principal{OrgID: orgID, Subject: "diag", CredentialID: "diag"},
			directread.FactsRequest{Kinds: []string{"pull_requests"}, Tables: mode,
				Subjects: []directread.RequestSubject{{Kind: "pull_request", CanonicalID: "pull_request:" + repo + ":900"}}})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		encoded, _ := json.Marshal(response)
		out = append(out, "mode="+mode+" "+string(encoded))
	}
	t.Fatalf("DIAG (requested PR subject on main code):\n%s", strings.Join(out, "\n"))
}
