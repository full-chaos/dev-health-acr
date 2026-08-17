package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestIdentityUniverseReadsAllFourKindsForOneOrganization is CHAOS-3884
// Option C's live end-to-end proof: a live-database enumeration returns a
// row for a seeded repository, project, team, and work item, each carrying
// its correct kind/label/alias data -- and reports complete=true when
// nothing exceeded the row budget.
func TestIdentityUniverseReadsAllFourKindsForOneOrganization(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	const orgID = "org-identity-universe"
	repoID := "33333333-3333-3333-3333-333333333333"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, "full-chaos/dev-health-acr", "github", at); err != nil {
		t.Fatalf("seed repos: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"project-1", orgID, "Ask Dev", "ASKDEV", "linear", "active", "", uint8(1), at); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO teams (id, org_id, name, description, provider, native_team_key, project_keys, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"team-1", orgID, "Chaos Team", "", "linear", "CHAOS", []string{}, uint8(1), at); err != nil {
		t.Fatalf("seed teams: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"linear:CHAOS-100", "00000000-0000-0000-0000-000000000000", orgID, "Harden session issuance", "in_progress", "", at, ""); err != nil {
		t.Fatalf("seed work_items: %v", err)
	}

	rows, observedAt, complete, err := devhealthsource.IdentityUniverse(ctx, query, orgID)
	if err != nil {
		t.Fatalf("IdentityUniverse() error = %v", err)
	}
	if !complete {
		t.Fatal("IdentityUniverse() complete = false, want true (nothing exceeded the row budget)")
	}
	if observedAt.IsZero() {
		t.Fatal("IdentityUniverse() observedAt is zero, want the seeded rows' last_synced/updated_at")
	}

	byKind := map[contractsv1.ContextFabricSubjectKind]graphrank.IdentityRow{}
	for _, row := range rows {
		byKind[row.Kind] = row
	}
	repo, ok := byKind[contractsv1.ContextFabricSubjectRepository]
	if !ok || repo.CanonicalID != "repository:"+repoID || len(repo.Aliases) != 1 || repo.Aliases[0] != "dev-health-acr" {
		t.Fatalf("repository row = %+v, ok=%v, want canonical_id=repository:%s and Aliases=[dev-health-acr]", repo, ok, repoID)
	}
	if len(repo.ProviderAliases) != 1 || repo.ProviderAliases[0] != "github:full-chaos/dev-health-acr" {
		t.Fatalf("repository row ProviderAliases = %v, want [github:full-chaos/dev-health-acr]", repo.ProviderAliases)
	}
	project, ok := byKind[contractsv1.ContextFabricSubjectProject]
	if !ok || project.Label != "Ask Dev" {
		t.Fatalf("project row = %+v, ok=%v, want label=Ask Dev", project, ok)
	}
	team, ok := byKind[contractsv1.ContextFabricSubjectTeam]
	if !ok || team.Label != "Chaos Team" {
		t.Fatalf("team row = %+v, ok=%v, want label=Chaos Team", team, ok)
	}
	workItem, ok := byKind[contractsv1.ContextFabricSubjectWorkItem]
	if !ok || len(workItem.Aliases) != 1 || workItem.Aliases[0] != "CHAOS-100" {
		t.Fatalf("work_item row = %+v, ok=%v, want Aliases=[CHAOS-100]", workItem, ok)
	}
}
