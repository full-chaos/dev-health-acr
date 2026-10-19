package devhealthfacts_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

const (
	blockerStatusSlopeFactor = 1.15
	blockerStatusSlopeFloor  = uint64(8 << 20)
	blockerStatusCeiling     = uint64(32 << 20)
	blockerStatusFillerItems = 150000
)

func blockerStatusPeaks(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, statement contextpacket.SourceQuery, plan contextpacket.ReadPlan, runs int) []uint64 {
	t.Helper()
	executor := contextpacket.NewClickHouseSourceExecutor(query)
	if _, err := executor.QueryEvidence(ctx, statement, plan.Bindings()); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	peaks := make([]uint64, 0, runs)
	for run := 0; run < runs; run++ {
		mark := serverNowMicros(t, ctx, direct)
		rows, err := executor.QueryEvidence(ctx, statement, plan.Bindings())
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if len(rows) == 0 {
			t.Fatalf("run %d served no dependency rows: the measurement is vacuous", run)
		}
		if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
			t.Fatalf("flush logs: %v", err)
		}
		var peak, threadless uint64
		if err := direct.QueryRow(ctx, `SELECT max(memory_usage), `+statementthreads.ThreadlessCount+` FROM system.query_log WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_item_dependencies')) AND position(query, 'blocker_status') > 0`, mark).Scan(&peak, &threadless); err != nil || peak == 0 {
			t.Fatalf("run %d: statement not found in query_log (measurement did not happen): %v", run, err)
		}
		statementthreads.RequireThreadBound(t, "blocker status", threadless)
		peaks = append(peaks, peak)
	}
	slices.Sort(peaks)
	return peaks
}

func blockerStatusSlopeViolated(small, large uint64) bool {
	return float64(large) > blockerStatusSlopeFactor*float64(small)+float64(blockerStatusSlopeFloor)
}

// The blocker status of work_item_dependencies.v1 is read for the blocking ids
// of one repository's dependency rows only. Its peak memory must follow those
// rows, not the organization's work items: unrelated organization rows may not
// grow it past 1.15x + 8 MiB (lowest of three warm reads per size; one read swings between 16.9 and 23.9 MB with no size effect, while the organization-wide form grows 20 -> 167 MB). The same guard is
// applied to the organization-wide form and must flag it.
func TestDependencyBlockerStatusMemoryFollowsTheRepositoryNotTheOrganizationOnRealClickHouse(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("the bound is the ClickHouse server peak, which the race detector does not change; the unit leg runs this row")
	}
	ctx := context.Background()
	rawQuery, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	query := statementthreads.API(rawQuery)
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_relations_read", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedProdShapedBlockerOrganization(t, ctx, direct)

	var catalog contextpacket.SourceQuery
	for _, candidate := range contextpacket.SourceQueryCatalogV1 {
		if candidate.ID == "work_item_dependencies.v1" {
			catalog = candidate
		}
	}
	// The organization-wide form drops the blocker id set, so the status join
	// groups every work item of the organization.
	orgWideStatement := catalog.Statement
	for {
		start := strings.Index(orgWideStatement, " AND work_item_id IN (")
		if start < 0 {
			break
		}
		end := strings.Index(orgWideStatement[start:], ") GROUP BY work_item_id")
		if end < 0 {
			t.Fatalf("the blocker status id set was not closed in the statement")
		}
		orgWideStatement = orgWideStatement[:start] + orgWideStatement[start+end+1:]
	}
	if orgWideStatement == catalog.Statement {
		t.Fatalf("the blocker status id set was not found in the statement")
	}
	orgWide := catalog
	orgWide.Statement = orgWideStatement
	plan := contextpacket.ReadPlan{OrgID: blockerProdShapeOrg, RepoID: "00000000-0000-4000-8000-000000000000", RepoSlug: "acme/repo-0"}

	smallScopedReads := blockerStatusPeaks(t, ctx, query, direct, catalog, plan, 3)
	smallOrgWideReads := blockerStatusPeaks(t, ctx, query, direct, orgWide, plan, 3)
	smallScoped, smallOrgWide := smallScopedReads[0], smallOrgWideReads[0]

	for part := 0; part < 3; part++ {
		if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat('unrelated-%d-', lpad(toString(number), 12, '0')), toUUID('00000000-0000-4000-8000-000000000007'), ?, 'title', 'issue', 'open', '', now64(3), now64(3), '', 'linear', '', NULL, now64(3) FROM numbers(%d)`, part, blockerStatusFillerItems/3), blockerProdShapeOrg); err != nil {
			t.Fatalf("seed unrelated rows: %v", err)
		}
	}
	largeScopedReads := blockerStatusPeaks(t, ctx, query, direct, catalog, plan, 3)
	largeOrgWideReads := blockerStatusPeaks(t, ctx, query, direct, orgWide, plan, 3)
	largeScoped, largeOrgWide := largeScopedReads[0], largeOrgWideReads[0]
	t.Logf("BLOCKERSTATUS reads (bytes, sorted; lowest first, highest last) scoped small %v large %v; organization-wide small %v large %v; slope bound on the lowest scoped small read %.0f bytes; ceiling %d bytes on the highest", smallScopedReads, largeScopedReads, smallOrgWideReads, largeOrgWideReads, blockerStatusSlopeFactor*float64(smallScoped)+float64(blockerStatusSlopeFloor), blockerStatusCeiling)

	t.Logf("BLOCKERSTATUS scoped: %d bytes (%.1f MiB) at 17k items, %d bytes (%.1f MiB) at +%d unrelated; organization-wide form: %d (%.1f MiB) and %d (%.1f MiB)",
		smallScoped, float64(smallScoped)/(1<<20), largeScoped, float64(largeScoped)/(1<<20), blockerStatusFillerItems,
		smallOrgWide, float64(smallOrgWide)/(1<<20), largeOrgWide, float64(largeOrgWide)/(1<<20))
	if blockerStatusSlopeViolated(smallScoped, largeScoped) {
		t.Errorf("the scoped statement grew with unrelated organization rows: %d -> %d bytes, over 1.15x + 8 MiB (lowest reads; all reads small %v large %v)", smallScoped, largeScoped, smallScopedReads, largeScopedReads)
	}
	if highest := slices.Max([]uint64{smallScopedReads[len(smallScopedReads)-1], largeScopedReads[len(largeScopedReads)-1]}); highest >= blockerStatusCeiling {
		t.Errorf("the scoped statement peaked at %d bytes (highest read), want under %d", highest, blockerStatusCeiling)
	}
	if !blockerStatusSlopeViolated(smallOrgWide, largeOrgWide) {
		t.Errorf("the guard did not flag the organization-wide form (%d -> %d bytes): the guard cannot fail", smallOrgWide, largeOrgWide)
	}

	mark := serverNowMicros(t, ctx, direct)
	var counterpartRows uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM (SELECT work_item_id, toString(any(repo_id)) AS counterpart_repo_id, uniqExact(repo_id) AS counterpart_repo_count FROM work_items FINAL WHERE org_id = ? GROUP BY work_item_id) /* frag-counterpart */`, blockerProdShapeOrg).Scan(&counterpartRows); err != nil {
		t.Fatalf("counterpart-repository join shape: %v", err)
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	var counterpartPeak uint64
	if err := direct.QueryRow(ctx, `SELECT max(memory_usage) FROM system.query_log WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND position(query, 'frag-counterpart') > 0`, mark).Scan(&counterpartPeak); err != nil {
		t.Fatalf("counterpart peak: %v", err)
	}
	t.Logf("BLOCKERSTATUS counterpart-repository join shape (org-wide GROUP BY work_item_id) over %d groups: peak %d bytes (%.1f MiB)", counterpartRows, counterpartPeak, float64(counterpartPeak)/(1<<20))
}

func TestDependencyBlockerStatusIsCarriedOnlyForTheRepositoryAndTheCurrentAxisOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, nil)
	for _, statement := range devhealthschema.DDL("repos", "work_items", "work_item_dependencies", "work_item_relations_read") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const org = "5b1c6e0a-7d94-4a23-9f58-3e7a1c2d4b60"
	const repoA, repoB = "00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000b2"
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 10, 1, 0, 0, 0)
	for _, repo := range []struct{ id, name string }{{repoA, "acme/a"}, {repoB, "acme/b"}} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repo.id, org, repo.name, "github", at)
	}
	for _, item := range []struct{ id, repo, status string }{
		{"a1", repoA, "in_progress"}, {"a2", repoA, "open"}, {"a3", repoA, "open"}, {"a4", repoA, "done"},
		{"a6", repoA, "open"}, {"b1", repoB, "open"}, {"a7", repoA, "in_progress"}, {"a7", repoB, "done"}, {"a8", repoA, "unknown"},
	} {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced)
VALUES (?, ?, ?, 'title', 'issue', ?, '', ?, ?, '', 'linear', '', NULL, ?)`, item.id, item.repo, org, item.status, at, at, at)
	}
	for _, dep := range []struct{ source, target, kind string }{
		{"a1", "a2", "blocks"}, {"a3", "a1", "blocked_by"}, {"a4", "a2", "blocks"}, {"a6", "b1", "blocked_by"}, {"a3", "a2", "relates_to"},
		{"a7", "a2", "blocks"}, {"a8", "a2", "blocks"},
	} {
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at) VALUES (?,?,?,?,?,?,NULL)`,
			org, dep.source, dep.target, dep.kind, dep.kind, at)
	}
	var catalog contextpacket.SourceQuery
	for _, candidate := range contextpacket.SourceQueryCatalogV1 {
		if candidate.ID == "work_item_dependencies.v1" {
			catalog = candidate
		}
	}
	read := func(plan contextpacket.ReadPlan) map[string][3]string {
		t.Helper()
		rows, err := contextpacket.NewClickHouseSourceExecutor(query).QueryEvidence(ctx, catalog, plan.Bindings())
		if err != nil {
			t.Fatalf("query evidence: %v", err)
		}
		out := map[string][3]string{}
		for _, row := range rows {
			key, _ := row.Metadata["relation_key"].(string)
			blocker, _ := row.Metadata["blocker_work_item_id"].(string)
			status, _ := row.Metadata["blocker_status"].(string)
			out[row.Source.DisplayLabel] = [3]string{key, blocker, status}
		}
		return out
	}
	plan := contextpacket.ReadPlan{OrgID: org, RepoID: repoA, RepoSlug: "acme/a"}
	got := read(plan)
	want := map[string][3]string{
		"a1 blocks a2":     {"blocks:fwd", "a1", "in_progress"},
		"a3 blocked_by a1": {"blocks:inv", "a1", "in_progress"},
		"a4 blocks a2":     {"blocks:fwd", "a4", "done"},
		"a6 blocked_by b1": {"blocks:inv", "b1", ""},
		"a3 relates_to a2": {"relates_to:fwd", "", ""},
	}
	for label, expected := range want {
		if got[label] != expected {
			t.Errorf("%s: (relation, blocker, status) = %v, want %v (all: %v)", label, got[label], expected, got)
		}
	}
	asOf := ts(2026, 10, 2, 0, 0, 0)
	plan.AsOf = &asOf
	for label, row := range read(plan) {
		if row[2] != "" {
			t.Errorf("%s: a historical request carried the current blocker status %q", label, row[2])
		}
	}
}
