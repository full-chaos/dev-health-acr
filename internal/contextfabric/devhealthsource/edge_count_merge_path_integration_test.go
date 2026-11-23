package devhealthsource_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

// edgeMergePathMargin is the stated margin: in the transitions-on-the-merge-path
// fixture the one-thread peak must be at least this share below the four-thread peak.
const edgeMergePathMarginPercent = 10

func edgeMergePathOpen(t *testing.T, ctx context.Context) (contextpacket.ClickHouseQueryClient, clickhousedriver.Conn) {
	t.Helper()
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
	var unlimitedRows uint
	var unlimitedBytes uint64
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxResultRows: &unlimitedRows, MaxBytesToRead: &unlimitedBytes,
	})
	if err != nil {
		t.Fatalf("open the query client: %v", err)
	}
	t.Cleanup(func() { _ = query.Close() })
	return statementthreads.Unbounded(query), direct
}

func edgeMergePathScalar(t *testing.T, ctx context.Context, client contextpacket.ClickHouseQueryClient, statement, label string) uint64 {
	t.Helper()
	rows, err := client.Query(contextpacket.WithStatementReader(ctx, label), statement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: edgeThreadsOrg}})
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	defer rows.Close()
	var total uint64
	for rows.Next() {
		if err := rows.Scan(&total); err != nil {
			t.Fatalf("%s: scan: %v", label, err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: rows: %v", label, err)
	}
	return total
}

func edgeMergePathSeed(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, transitionsOnMergePath bool) {
	t.Helper()
	seed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	// devhealthschema:not-a-production-replica these are table NAMES for STOP MERGES and a seed label; the schema comes from productionSchemaDDL, no column or engine is declared here.
	for _, table := range []string{"work_items", "project_membership_transitions"} {
		if err := direct.Exec(ctx, "SYSTEM STOP MERGES "+table); err != nil {
			t.Fatalf("stop merges on %s: %v", table, err)
		}
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	pid := func(expr string) string {
		return fmt.Sprintf("concat('75000000-0000-4000-8000-', leftPad(toString(%s), 12, '0'))", expr)
	}
	seed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/threads', 'linear', ?)`, edgeThreadsRepo, edgeThreadsOrg, base)
	seed("projects", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at)
SELECT concat('75000000-0000-4000-8000-', leftPad(toString(number), 12, '0')), ?, concat('P', toString(number)), '', 'linear', 'backlog', '', 1, ? FROM numbers(12)`, edgeThreadsOrg, base)
	workItems := func(label, idExpr string, count, minutes int) {
		seed(label, fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, ingested_at, last_synced)
SELECT concat('linear:W-', leftPad(toString(%s), 8, '0')), toUUID(?), ?, 't', 'open', '', '', 'linear', %s, toDateTime64(?, 3, 'UTC') + toIntervalSecond(number), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number + 3), toDateTime64(?, 3, 'UTC') + toIntervalMinute(%d) FROM numbers(%d)`, idExpr, pid("number % 12"), minutes, count),
			edgeThreadsRepo, edgeThreadsOrg, base, base, base)
	}
	transitions := func(label string, from, to string, count, step int) {
		seed(label, fmt.Sprintf(`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at)
SELECT ?, NULL, toUUID(?), 'work_item', concat('linear:H-', toString(number)), 'linear', %s, %s, '', '', '', toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d), concat('ev-', toString(number), '-%d'), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d + 11) FROM numbers(%d)`,
			from, to, step, step, step, step, count), edgeThreadsOrg, edgeThreadsRepo, base, base, base)
	}
	if transitionsOnMergePath {
		workItems("one work_items part", "number", edgeThreadsSubjects, 0)
		transitions("transitions big part 1", "''", pid("number % 12"), edgeThreadsSubjects, 0)
		transitions("transitions big part 2", pid("number % 12"), pid("(number + 1) % 12"), edgeThreadsSubjects, 1)
		transitions("transitions small part", pid("(number + 1) % 12"), pid("number % 12"), 40, 2)
		return
	}
	workItems("big work_items part", "number", edgeThreadsSubjects, 0)
	for part := 1; part <= 3; part++ {
		workItems(fmt.Sprintf("overlapping newer work_items part %d", part), fmt.Sprintf("number * %d + %d", 997+part, part), 40, part)
	}
	transitions("one transitions part", "''", pid("number % 12"), 2*edgeThreadsSubjects, 0)
}

type edgeMergePathArm struct {
	peak, readRows, medianMillis uint64
}

// TestLiveMembershipEdgeCountTransitionsOnTheMergePath runs the edge count at
// four and one threads on two fixtures. T: work_items is one part and
// project_membership_transitions has two big intersecting parts and a small
// intersecting one, so FINAL merges transitions only. W: work_items has the big
// part and newer overlapping ones, transitions is one part, so FINAL merges
// work_items only. It logs the per-table reads and asserts which table is on the
// merge path and that one thread cuts the peak in T.
func TestLiveMembershipEdgeCountTransitionsOnTheMergePath(t *testing.T) {
	for _, fixture := range []struct {
		name                    string
		transitionsOnMergePath  bool
		workParts, transitionPs uint64
	}{
		{"T", true, 1, 3},
		{"W", false, 4, 1},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			client, direct := edgeMergePathOpen(t, ctx)
			edgeMergePathSeed(t, ctx, direct, fixture.transitionsOnMergePath)
			var workParts, transitionParts uint64
			if err := direct.QueryRow(ctx, `SELECT countIf(table = 'work_items'), countIf(table = 'project_membership_transitions') FROM system.parts WHERE database = currentDatabase() AND active`).Scan(&workParts, &transitionParts); err != nil {
				t.Fatal(err)
			}
			t.Logf("PROFILE %s parts work_items=%d transitions=%d", fixture.name, workParts, transitionParts)
			if workParts != fixture.workParts || transitionParts != fixture.transitionPs {
				t.Fatalf("%s: the fixture has %d work_items parts and %d transitions parts, want %d and %d", fixture.name, workParts, transitionParts, fixture.workParts, fixture.transitionPs)
			}
			shipped, _ := devhealthsource.MembershipEdgeCountSQLForTest(true)
			four := strings.TrimSuffix(shipped, "\nSETTINGS max_threads = 1")
			statements := map[string]string{
				"stmt-four":  four,
				"stmt-one":   four + "\nSETTINGS max_threads = 1",
				"trans-four": "SELECT count() FROM project_membership_transitions FINAL WHERE org_id = {org_id:String}",
				"trans-one":  "SELECT count() FROM project_membership_transitions FINAL WHERE org_id = {org_id:String} SETTINGS max_threads = 1",
				"work-four":  "SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String}",
				"work-one":   "SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} SETTINGS max_threads = 1",
			}
			kinds := []string{"stmt-four", "stmt-one", "trans-four", "trans-one", "work-four", "work-one"}
			labels := map[string][]string{}
			counts := map[string]map[string]uint64{}
			for i := 0; i < edgeThreadsRuns; i++ {
				for _, kind := range kinds {
					label := fmt.Sprintf("edge-merge-%s-%s-%d", fixture.name, kind, i)
					if strings.HasPrefix(kind, "stmt") {
						got := edgeThreadsMeasure(t, ctx, client, statements[kind], label)
						if counts[kind] == nil {
							counts[kind] = got
						}
					} else {
						edgeMergePathScalar(t, ctx, client, statements[kind], label)
					}
					labels[kind] = append(labels[kind], label)
				}
			}
			if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
				t.Fatal(err)
			}
			arms := map[string]edgeMergePathArm{}
			for _, kind := range kinds {
				var arm edgeMergePathArm
				var millis []uint64
				for _, label := range labels[kind] {
					run := edgeThreadsLogged(t, ctx, direct, label)
					if run.peak > arm.peak {
						arm.peak = run.peak
					}
					arm.readRows = run.readRows
					millis = append(millis, run.millis)
				}
				sort.Slice(millis, func(i, j int) bool { return millis[i] < millis[j] })
				arm.medianMillis = millis[len(millis)/2]
				arms[kind] = arm
				t.Logf("PROFILE %s %s: peak=%d read_rows=%d median_ms=%d", fixture.name, kind, arm.peak, arm.readRows, arm.medianMillis)
			}
			rows, err := direct.Query(ctx, `SELECT plan_step_name, plan_step_description, name, sum(input_rows), sum(output_rows) FROM system.processors_profile_log
WHERE query_id IN (SELECT query_id FROM system.query_log WHERE type = 'QueryFinish' AND log_comment = ? AND current_database = currentDatabase())
GROUP BY plan_step_name, plan_step_description, name HAVING sum(input_rows) > 0 ORDER BY sum(input_rows) DESC LIMIT 25`, labels["stmt-four"][0])
			if err != nil {
				t.Logf("PROFILE %s processors_profile_log unreadable: %v", fixture.name, err)
			} else {
				for rows.Next() {
					var step, description, name string
					var in, out uint64
					if err := rows.Scan(&step, &description, &name, &in, &out); err != nil {
						t.Logf("PROFILE %s processors scan: %v", fixture.name, err)
						break
					}
					t.Logf("PROFILE %s processors stmt-four: step=%q description=%q processor=%q input_rows=%d output_rows=%d", fixture.name, step, description, name, in, out)
				}
				_ = rows.Close()
			}
			mergePath := func(table string) bool {
				plan, err := direct.Query(ctx, "EXPLAIN PIPELINE SELECT count() FROM "+table+" FINAL WHERE org_id = '"+edgeThreadsOrg+"'")
				if err != nil {
					t.Fatalf("explain %s: %v", table, err)
				}
				defer plan.Close()
				var found bool
				var text strings.Builder
				for plan.Next() {
					var line string
					if err := plan.Scan(&line); err != nil {
						t.Fatal(err)
					}
					text.WriteString(line + "\n")
					found = found || strings.Contains(line, "ReplacingSorted")
				}
				t.Logf("PROFILE %s explain %s merge path=%v:\n%s", fixture.name, table, found, text.String())
				return found
			}
			// devhealthschema:not-a-production-replica these are table NAMES passed to EXPLAIN PIPELINE; the schema comes from productionSchemaDDL, no column or engine is declared here.
			transitionsMerge, workMerge := mergePath("project_membership_transitions"), mergePath("work_items")
			if transitionsMerge != fixture.transitionsOnMergePath || workMerge == fixture.transitionsOnMergePath {
				t.Errorf("%s: FINAL merges transitions=%v work_items=%v, want transitions=%v work_items=%v", fixture.name, transitionsMerge, workMerge, fixture.transitionsOnMergePath, !fixture.transitionsOnMergePath)
			}
			stmtFour, stmtOne := arms["stmt-four"], arms["stmt-one"]
			if fmt.Sprint(counts["stmt-four"]) != fmt.Sprint(counts["stmt-one"]) {
				t.Errorf("%s: the edge count differs between four and one thread: %v vs %v", fixture.name, counts["stmt-four"], counts["stmt-one"])
			}
			if len(counts["stmt-four"]) != 2 || counts["stmt-four"]["transition"] == 0 || counts["stmt-four"]["work_item_column"] == 0 {
				t.Errorf("%s: the seed counts no edge in each arm: %v", fixture.name, counts["stmt-four"])
			}
			if fixture.transitionsOnMergePath && stmtOne.peak*100 > stmtFour.peak*(100-edgeMergePathMarginPercent) {
				t.Errorf("T: one thread peaks at %d, four threads at %d: want the one-thread peak at least %d%% lower", stmtOne.peak, stmtFour.peak, edgeMergePathMarginPercent)
			}
		})
	}
}
