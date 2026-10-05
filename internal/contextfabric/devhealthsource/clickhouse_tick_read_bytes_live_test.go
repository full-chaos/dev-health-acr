package devhealthsource_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// The geometry of this test is prod's, scaled down. Prod reads under a 64 MiB
// max_bytes_to_read (the dev-health-go client default) from tables whose
// granules are cut at ClickHouse's default index_granularity_bytes of 10 MiB,
// a ratio of 6.4. Here the limit is 3 MiB and granules are cut at 512 KiB, a
// ratio of 6. As on prod (about 6800 pull requests in 7 parts and 9 marks)
// the wide tables are a few granules per part, every row has versions in
// several unmerged parts, and the rows of many repositories are interleaved
// in ingest order, so any page's keys spread over the whole key range. All
// versions of the wide text (a pull request's body, an incident's
// description) exceed the limit; one version of one page is far below it.
const (
	tickReadByteLimit              uint64 = 3 << 20
	tickGranuleBytes                      = 512 << 10
	tickRepositories                      = 20
	tickWideRows                          = 1500
	tickWideTextBytes                     = 1000
	tickNarrowRows                        = 1000
	tickOrganization                      = "86830000-0000-4000-8000-0000000003b7"
	tickPhaseCatchUp                      = "catch-up after a rebuild"
	tickPhaseSteady                       = "steady tick"
	tickSourcePagesPerPhaseCeiling        = 3000
)

// TestLiveWholeClickHouseSourceTickStaysUnderTheByteLimit runs the WHOLE
// dev_health_clickhouse source (every producer of the registry, in registry
// order, through the real ClickHouseProjectionSource and the real query
// client) on a populated store under a byte limit, in both states the prod
// fault showed: the catch-up after a rebuild (an empty cursor, every row past
// it) until the source is drained, and then a steady tick over a few newly
// synced rows. No read may be refused with ClickHouse 307. The read of every
// statement is measured from the server's own query log and reported per
// producer table against the limit.
//
// Needs Docker (ClickHouse container). Written to be run by CI or by the lane
// owner; not run in the authoring sandbox.
func TestLiveWholeClickHouseSourceTickStaysUnderTheByteLimit(t *testing.T) {
	ctx := context.Background()
	addr, admin := sharedDevHealthClickHouse(t)
	database := fmt.Sprintf("t%d", perTestDatabaseSeq.Add(1))
	if err := admin.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatalf("create per-test database %s: %v", database, err)
	}
	t.Cleanup(func() { _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" SYNC") })
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: database, Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open native ClickHouse connection: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	limit := tickReadByteLimit
	reader, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxBytesToRead: &limit,
	})
	if err != nil {
		t.Fatalf("open the limited query client: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	for _, statement := range productionSchemaDDL() {
		statement = strings.Replace(statement, "SETTINGS index_granularity = 8192", fmt.Sprintf("SETTINGS index_granularity = 8192, index_granularity_bytes = %d", tickGranuleBytes), 1)
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	// No merge runs during the test: the versions written below stay in
	// their own parts, and FINAL has them to resolve, as on prod.
	for _, stop := range []string{"SYSTEM STOP MERGES %s.git_pull_requests", "SYSTEM STOP MERGES %s.operational_incidents"} {
		if err := direct.Exec(ctx, fmt.Sprintf(stop, database)); err != nil {
			t.Fatalf("%s: %v", stop, err)
		}
	}

	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seedWholeTickStore(t, ctx, direct, base)
	writeLaterVersions(t, ctx, direct, base)

	// The limit bites at this size: reading every pull request body, or every
	// incident description, is refused. Without this, a limit the store does
	// not apply would make the test pass for the wrong reason.
	for _, wide := range []string{
		"SELECT sum(length(body)) FROM git_pull_requests WHERE org_id = {org_id:String}",
		"SELECT sum(length(description)) FROM operational_incidents WHERE org_id = {org_id:String}",
	} {
		if err := drainQuery(ctx, reader, wide, tickOrganization); !isTooManyBytes(err) {
			t.Fatalf("%q under the %d-byte limit returned %v, want ClickHouse 307: the limit does not bite at this size", wide, limit, err)
		}
	}

	// The seed reproduces the prod fault: main's pull request read (before
	// the keys-first read) is refused at the catch-up cursor.
	if err := devhealthsource.LegacyPullRequestReadForTest(ctx, reader, tickOrganization, 200); !isTooManyBytes(err) {
		t.Fatalf("main's pull request read at the catch-up cursor returned %v, want ClickHouse 307: this seed does not reproduce the prod fault", err)
	}

	source, err := devhealthsource.NewClickHouseProjectionSource(reader)
	if err != nil {
		t.Fatal(err)
	}
	phaseStart := map[string]time.Time{}
	phaseStart[tickPhaseCatchUp] = serverNow(t, ctx, admin)
	cursor, pages := runSourceUntilCaughtUp(t, ctx, source, "", tickPhaseCatchUp)
	if pages == 0 {
		t.Fatal("the catch-up read no page: the store was not read")
	}
	// A steady tick: a few rows of the wide tables synced again after the
	// drain, read from the drained cursor.
	resynced := base.Add(30 * 24 * time.Hour)
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, body, created_at, last_synced)
SELECT repo_id, org_id, number, title, 'merged', body, created_at, toDateTime64(?, 3, 'UTC') + toIntervalSecond(number)
FROM git_pull_requests FINAL WHERE org_id = ? AND number % 97 = 0`, resynced, tickOrganization); err != nil {
		t.Fatalf("resync pull requests: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, description, started_at)
SELECT org_id, toDateTime64(?, 6, 'UTC'), id, observed_at, toDateTime64(?, 6, 'UTC'), service_id, title, description, started_at
FROM operational_incidents FINAL WHERE org_id = ? AND cityHash64(id) % 97 = 0`, resynced, resynced, tickOrganization); err != nil {
		t.Fatalf("resync incidents: %v", err)
	}
	phaseStart[tickPhaseSteady] = serverNow(t, ctx, admin)
	if _, steadyPages := runSourceUntilCaughtUp(t, ctx, source, cursor, tickPhaseSteady); steadyPages == 0 {
		t.Fatal("the steady tick read no page: the resynced rows were not read")
	}

	// The server's own measure of every statement the source sent.
	if err := admin.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatalf("flush the query log: %v", err)
	}
	for _, phase := range []string{tickPhaseCatchUp, tickPhaseSteady} {
		until := time.Now().Add(time.Hour)
		if phase == tickPhaseCatchUp {
			until = phaseStart[tickPhaseSteady]
		}
		reportTickReads(t, ctx, admin, database, phase, phaseStart[phase], until)
	}
}

// seedWholeTickStore writes one organization with every table a producer of
// the source reads. Pull request bodies and incident descriptions are the
// wide columns; ingest times are spread in an order unrelated to the key.
func seedWholeTickStore(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, base time.Time) {
	t.Helper()
	repoIDs := make([]string, tickRepositories)
	for i := range repoIDs {
		repoIDs[i] = fmt.Sprintf("86830000-0000-4000-8000-%012d", 700+i)
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, created_at, last_synced) VALUES (?, ?, ?, 'github', ?, ?)`,
			repoIDs[i], tickOrganization, fmt.Sprintf("acme/r%02d", i), base, base); err != nil {
			t.Fatalf("seed repo %d: %v", i, err)
		}
	}
	repoArray := "[toUUID('" + strings.Join(repoIDs, "'), toUUID('") + "')]"
	// Row n belongs to repository n mod R and is the (n div R)+1-th of it;
	// its ingest second is n, so the repositories are interleaved in ingest
	// order and a page's keys spread over the whole key range.
	repoOf := func(n string, rows int) string {
		return fmt.Sprintf("arrayElement(%s, toUInt32(%s %% %d) + 1)", repoArray, n, tickRepositories)
	}
	numberOf := func(n string, rows int) string {
		return fmt.Sprintf("toUInt32(intDiv(%s, %d) + 1)", n, tickRepositories)
	}
	spread := func(n string, rows int) string {
		return fmt.Sprintf("toDateTime64('%s', 3, 'UTC') + toIntervalSecond(%s)", base.Format("2006-01-02 15:04:05"), n)
	}
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v\n%s", label, err, statement)
		}
	}
	wide := fmt.Sprintf("repeat('w', %d)", tickWideTextBytes)
	exec("pull requests", fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, body, created_at, last_synced, head_branch)
SELECT %s, ?, %s, concat('PR ', toString(number)), 'open', %s, toDateTime64(?, 3, 'UTC'), %s, concat('feat/b-', toString(number))
FROM numbers(%d)`, repoOf("number", tickWideRows), numberOf("number", tickWideRows), wide, spread("number", tickWideRows), tickWideRows), tickOrganization, base)
	exec("work items", fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, labels, parent_id, url, last_synced, org_id)
SELECT %s, concat('jira:W-', leftPad(toString(number), 6, '0')), 'jira', concat('Work item ', toString(number), ' ', repeat('t', 200)), 'issue', 'open', 'W', 'p-w', '', 'Project W', toDateTime64(?, 3), toDateTime64(?, 3),
       ['backend', 'api'], if(number %% 10 = 0, '', concat('jira:W-', leftPad(toString(number - number %% 10), 6, '0'))), '', %s, ?
FROM numbers(%d)`, repoOf("number", tickWideRows), spread("number", tickWideRows), tickWideRows), base, base, tickOrganization)
	for i := 0; i < tickRepositories; i++ {
		exec("service mapping", `INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, service_id, repo_id, is_active) VALUES (?, ?, ?, ?, ?, 1)`,
			tickOrganization, base, fmt.Sprintf("map-%d", i), fmt.Sprintf("svc-%d", i), repoIDs[i])
	}
	exec("incidents", fmt.Sprintf(`INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, description, started_at)
SELECT ?, toDateTime64(?, 6, 'UTC'), concat('inc-', leftPad(toString(number), 6, '0')), toDateTime64(?, 6, 'UTC'), toDateTime64(%s, 6, 'UTC'), concat('svc-', toString(number %% %d)), concat('Incident ', toString(number)), %s, toDateTime64(?, 6, 'UTC')
FROM numbers(%d)`, spread("number", tickWideRows), tickRepositories, wide, tickWideRows), tickOrganization, base, base, base)
	exec("links", fmt.Sprintf(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT %s, concat('jira:W-', leftPad(toString(number), 6, '0')), %s, 0.9, 'native', '', %s, ?
FROM numbers(%d)`, repoOf("number", tickWideRows), numberOf("number", tickWideRows), spread("number", tickWideRows), tickWideRows), tickOrganization)
	exec("dependencies", fmt.Sprintf(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id)
SELECT concat('jira:W-', leftPad(toString(number), 6, '0')), concat('jira:W-', leftPad(toString(number + 1), 6, '0')), 'blocks', 'blocks', %s, ?
FROM numbers(%d)`, spread("number", tickNarrowRows), tickNarrowRows), tickOrganization)
	exec("deployments", fmt.Sprintf(`INSERT INTO deployments (repo_id, deployment_id, status, environment, deployed_at, release_ref, release_ref_confidence, last_synced, org_id)
SELECT %s, concat('dep-', toString(number)), 'success', 'production', toDateTime64(?, 3, 'UTC'), 'v1', 1.0, %s, ?
FROM numbers(%d)`, repoOf("number", tickNarrowRows), spread("number", tickNarrowRows), tickNarrowRows), base, tickOrganization)
	exec("deployment incident edges", fmt.Sprintf(`INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at)
SELECT concat('edge-', toString(number)), toUUID(?), concat('dep-', toString(number)), concat('inc-', leftPad(toString(number), 6, '0')), %s, 0.8, 'temporal', '', toDateTime64(?, 3, 'UTC'), %s
FROM numbers(%d)`, repoOf("number", tickNarrowRows), spread("number", tickNarrowRows), tickNarrowRows), tickOrganization, base)
	exec("reviews", fmt.Sprintf(`INSERT INTO git_pull_request_reviews (repo_id, number, review_id, state, submitted_at, last_synced, org_id)
SELECT %s, %s, concat('rev-', toString(number)), 'APPROVED', toDateTime64(?, 3, 'UTC'), %s, ?
FROM numbers(%d)`, repoOf("number", tickNarrowRows), numberOf("number", tickNarrowRows), spread("number", tickNarrowRows), tickNarrowRows), base, tickOrganization)
	exec("ci runs", fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id, pipeline_name, branch)
SELECT %s, concat('run-', toString(number)), 'success', toDateTime64(?, 3, 'UTC'), %s, ?, 'ci', 'main'
FROM numbers(%d)`, repoOf("number", tickNarrowRows), spread("number", tickNarrowRows), tickNarrowRows), base, tickOrganization)
}

// writeLaterVersions writes two later versions of the wide tables' rows, each
// in its own part: every row again a day later, and half the rows again two
// days later. The ingest order of each version keeps the repositories
// interleaved.
func writeLaterVersions(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, base time.Time) {
	t.Helper()
	for _, v := range []struct {
		days              int
		pullRequestFilter string
		incidentFilter    string
	}{{1, "1 = 1", "1 = 1"}, {2, "number % 2 = 0", "cityHash64(id) % 2 = 0"}} {
		shift := fmt.Sprintf("toIntervalDay(%d)", v.days)
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, body, created_at, last_synced, head_branch)
SELECT repo_id, org_id, number, title, state, body, created_at, last_synced + `+shift+`, head_branch
FROM git_pull_requests FINAL WHERE org_id = ? AND `+v.pullRequestFilter, tickOrganization); err != nil {
			t.Fatalf("write pull request version +%dd: %v", v.days, err)
		}
		if err := direct.Exec(ctx, `INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, description, started_at)
SELECT org_id, source_version_at + `+shift+`, id, observed_at, last_synced + `+shift+`, service_id, title, description, started_at
FROM operational_incidents FINAL WHERE org_id = ? AND `+v.incidentFilter, tickOrganization); err != nil {
			t.Fatalf("write incident version +%dd: %v", v.days, err)
		}
	}
}

// runSourceUntilCaughtUp pulls batches from the source from cursor until it
// has nothing more, failing on any read error (the 307 the prod fault showed
// arrives here). It returns the last cursor and the number of batches.
func runSourceUntilCaughtUp(t *testing.T, ctx context.Context, source *devhealthsource.ClickHouseProjectionSource, cursor, phase string) (string, int) {
	t.Helper()
	pages := 0
	for ; pages < tickSourcePagesPerPhaseCeiling; pages++ {
		batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: tickOrganization, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("%s: batch %d failed (307 is the byte limit): %v", phase, pages, err)
		}
		if !available {
			return cursor, pages
		}
		if batch.NextCursor == cursor {
			t.Fatalf("%s: batch %d made no cursor progress", phase, pages)
		}
		cursor = batch.NextCursor
	}
	t.Fatalf("%s: the source did not catch up in %d batches", phase, tickSourcePagesPerPhaseCeiling)
	return cursor, pages
}

func serverNow(t *testing.T, ctx context.Context, admin clickhousedriver.Conn) time.Time {
	t.Helper()
	var now time.Time
	if err := admin.QueryRow(ctx, "SELECT now64(6)").Scan(&now); err != nil {
		t.Fatalf("read the server clock: %v", err)
	}
	return now
}

// reportTickReads lists, per producer table (the first table a statement
// reads), the statements the source sent in one phase, the largest read and
// any refusal, and fails when a read was refused or exceeded the limit.
func reportTickReads(t *testing.T, ctx context.Context, admin clickhousedriver.Conn, database, phase string, from, until time.Time) {
	t.Helper()
	rows, err := admin.Query(ctx, `SELECT extract(query, 'FROM ([a-z_]+)') AS producer_table, count() AS statements, max(read_bytes) AS max_read, countIf(exception_code = 307) AS refused
FROM system.query_log
WHERE current_database = ? AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing')
  AND query_start_time_microseconds >= ? AND query_start_time_microseconds < ?
  AND query NOT LIKE '%system.query_log%' AND query NOT LIKE 'INSERT%' AND query NOT LIKE 'SYSTEM%'
GROUP BY producer_table ORDER BY producer_table`, database, from, until)
	if err != nil {
		t.Fatalf("%s: read the query log: %v", phase, err)
	}
	defer rows.Close()
	type read struct {
		table               string
		statements, maxRead uint64
		refused             uint64
	}
	var reads []read
	for rows.Next() {
		var r read
		if err := rows.Scan(&r.table, &r.statements, &r.maxRead, &r.refused); err != nil {
			t.Fatalf("%s: scan the query log: %v", phase, err)
		}
		reads = append(reads, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: read the query log: %v", phase, err)
	}
	if len(reads) == 0 {
		t.Fatalf("%s: the query log holds no statement of the source: the measure did not happen", phase)
	}
	sort.Slice(reads, func(i, j int) bool { return reads[i].table < reads[j].table })
	var table strings.Builder
	fmt.Fprintf(&table, "%s, limit %d bytes:\n%-40s %10s %14s %8s\n", phase, tickReadByteLimit, "producer table", "statements", "max read", "refused")
	for _, r := range reads {
		fmt.Fprintf(&table, "%-40s %10d %14d %8d\n", r.table, r.statements, r.maxRead, r.refused)
		if r.refused > 0 || r.maxRead > tickReadByteLimit {
			t.Errorf("%s: %s read %d bytes (limit %d), %d refused", phase, r.table, r.maxRead, tickReadByteLimit, r.refused)
		}
	}
	t.Log(table.String())
}
