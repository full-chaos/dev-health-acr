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
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_dependency_first_seen", "work_graph_issue_pr",
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
	read := func() {
		if _, err := provider.ReadFacts(ctx, orgWide, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: subjects,
		}); err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
	}
	restore := devhealthfacts.ForceLibraryAuthorizationForTest()
	defer restore()
	read()
	mark := serverNowMicros(t, ctx, direct)
	read()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatal(err)
	}
	rows, err := direct.Query(ctx, `SELECT normalized_query_hash, type, memory_usage, read_rows, read_bytes, length(query),
  countSubstrings(query, 'work_item_dependencies'), countSubstrings(query, 'work_items'),
  arrayStringConcat(arrayMap(k -> concat(k, '=', toString(ProfileEvents[k])), arrayFilter(k -> positionCaseInsensitive(k, 'join') > 0 OR positionCaseInsensitive(k, 'set') > 0 OR positionCaseInsensitive(k, 'aggregat') > 0 OR k IN ('SelectedRows', 'SelectedBytes', 'SelectedMarks', 'MergeTreeDataSelectExecutorRowsRead'), mapKeys(ProfileEvents))), ' '),
  substring(replaceRegexpAll(query, '\\s+', ' '), 1, 90)
FROM system.query_log WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_item_dependencies')) ORDER BY memory_usage DESC`, mark)
	if err != nil {
		t.Fatalf("query_log: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash, mem, readRows, readBytes uint64
		var typ, events, head string
		var qlen, depCount, itemCount uint64
		if err := rows.Scan(&hash, &typ, &mem, &readRows, &readBytes, &qlen, &depCount, &itemCount, &events, &head); err != nil {
			t.Fatal(err)
		}
		t.Logf("DIAG statement %d %s peak %.1f MiB read_rows %d read_bytes %d query_len %d dep_refs %d item_refs %d | %s | %s", hash, typ, float64(mem)/(1<<20), readRows, readBytes, qlen, depCount, itemCount, head, events)
	}
	t.Error("diagnostic: force the log lines to print")
}
