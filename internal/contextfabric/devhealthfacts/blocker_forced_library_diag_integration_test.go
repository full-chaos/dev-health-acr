package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

var _ = clickhouse.Settings{}

// Diagnostic only: where the forced-library statements spend their memory.
func TestBlockerForcedLibraryDiagnostic(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_relations_read", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	seedProdShapedBlockerOrganization(t, ctx, direct)
	subjects := make([]contextfabric.SubjectRef, 0, 39)
	for team := 0; team < 39; team++ {
		subjects = append(subjects, teamSubject(fmt.Sprintf("team-%d", team)))
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	orgWide := storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: []string{"*"}}
	read := func() {}
	restricted := storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: []string{"acme/repo-0", "acme/repo-1", "acme/repo-2", "acme/repo-3", "acme/repo-4", "acme/repo-5"}}
	readAs := func(principal storage.Principal) func() {
		return func() {
			if _, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: subjects,
			}); err != nil {
				t.Fatalf("ReadFacts: %v", err)
			}
		}
	}
	_ = read
	measure := func(label string, run func()) {
		run()
		low, high := map[string]uint64{}, map[string]uint64{}
		for round := 0; round < 3; round++ {
			mark := serverNowMicros(t, ctx, direct)
			run()
			if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
				t.Fatal(err)
			}
			rows, err := direct.Query(ctx, `SELECT memory_usage, read_rows, read_bytes, length(query), substring(replaceRegexpAll(query, '\\s+', ' '), 1, 90)
FROM system.query_log WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_item_dependencies'))`, mark)
			if err != nil {
				t.Fatalf("query_log: %v", err)
			}
			for rows.Next() {
				var mem, readRows, readBytes, qlen uint64
				var head string
				if err := rows.Scan(&mem, &readRows, &readBytes, &qlen, &head); err != nil {
					t.Fatal(err)
				}
				if low[head] == 0 || mem < low[head] {
					low[head] = mem
				}
				if mem > high[head] {
					high[head] = mem
				}
				if round == 0 {
					t.Logf("DIAG[%s] read_rows %d read_bytes %d query_len %d | %s", label, readRows, readBytes, qlen, head)
				}
			}
			rows.Close()
		}
		for head := range low {
			t.Logf("DIAG[%s] min %.1f max %.1f MiB | %s", label, float64(low[head])/(1<<20), float64(high[head])/(1<<20), head)
		}
	}
	measure("normal org-wide", readAs(orgWide))
	measure("normal restricted", readAs(restricted))
	restoreBlock := devhealthfacts.SetBlockerMaxBlockSizeForTest(0)
	measure("default block size org-wide", readAs(orgWide))
	measure("default block size restricted", readAs(restricted))
	restoreBlock()
	restoreBound := devhealthfacts.SetBlockerAuthorizedIDsBoundForTest(0)
	measure("in-statement restricted", readAs(restricted))
	restoreBound()
	restoreLibrary := devhealthfacts.ForceLibraryAuthorizationForTest()
	measure("library forced org-wide", readAs(orgWide))
	restoreLibrary()
	t.Error("diagnostic: force the log lines to print")
}
