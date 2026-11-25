package devhealthsource_test

// DO NOT MERGE. Throwaway exec proof of a prod read form (FORM-338-MEMQL-3: system.query_log, the read_rows and memory of the
// membership edge count statement per statement text). The three statements are the form's run-block cuts, byte for byte, with their
// sha256 pinned. Synthetic rows only (two fake orgs). The ClickHouse server is shared with the package's other tests, so a server-wide
// count is a lower bound here; the exact checks run a test copy with one more conjunct, query_id IN (the ids this test sent).
// Leg 1 (acr #2208, job 139001226621): its "Guard unit shard package budget" step failed only on this test's package FAIL line
// (devhealthsource 347.2 s = 57.9 % of the 600 s budget; the guard fails at 85 % or on any FAIL line). No budget row is needed.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/google/uuid"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

const (
	memqlCutS0SHA256    = "6a12477c4201c1e6caf2232ba69a672b94f4469bacf224037aeee00b40c3ba57"
	memqlCutStmtRSHA256 = "428761509478f35928b8994c06684d99c4675e3f09efe66091d6c5f040efd91f"
	memqlCutStmt0SHA256 = "efc407e149e38208fad4be4e270f9a704417ec0211658331792c163e524ea737"
)

const memqlCutS0 = `SELECT partition, sum(rows) AS rows, sum(bytes_on_disk) AS bytes, count() AS active_parts, toString(min(min_date)) AS min_day
FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= yesterday()
GROUP BY partition ORDER BY partition;
`

const memqlCutStmtR = `SELECT count() AS replicas, toString(version()) AS server_version FROM clusterAllReplicas('default', system.one);
`

const memqlCutStmt0 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= yesterday()) > 200000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= yesterday()) > 100000000000
  OR (SELECT count() FROM system.query_log WHERE event_date >= yesterday() AND event_time >= now() - INTERVAL 24 HOUR) > 9000000,
  'STOP-COST-GATE')) AS cost_gate,
  position(query, 'ARRAY JOIN [(to_project_id, toUInt8(1)), (from_project_id, toUInt8(0))] AS touch') > 0 AS m_new,
  position(query, 'WITH touches AS (') > 0 AS m_old,
  position(query, 'ingested_at') > 0 AS m_ing
SELECT toString(today()) AS day,
  toString(now(), 'UTC') AS run_at,
  countIf(type = 'QueryFinish' AND m_new AND NOT m_old AND m_ing) AS new_finished,
  maxIf(read_rows, type = 'QueryFinish' AND m_new AND NOT m_old AND m_ing) AS new_max_read_rows,
  toUInt64(ifNotFinite(avgIf(read_rows, type = 'QueryFinish' AND m_new AND NOT m_old AND m_ing), 0)) AS new_avg_read_rows,
  maxIf(memory_usage, type = 'QueryFinish' AND m_new AND NOT m_old AND m_ing) AS new_max_memory,
  toUInt64(ifNotFinite(avgIf(memory_usage, type = 'QueryFinish' AND m_new AND NOT m_old AND m_ing), 0)) AS new_avg_memory,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND m_new AND NOT m_old AND m_ing) AS new_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND m_new AND NOT m_old AND m_ing) AS new_failed_241,
  toString(minIf(event_time, m_new AND NOT m_old AND m_ing), 'UTC') AS new_first,
  countIf(type = 'QueryFinish' AND m_old AND NOT m_new AND m_ing) AS old_finished,
  maxIf(read_rows, type = 'QueryFinish' AND m_old AND NOT m_new AND m_ing) AS old_max_read_rows,
  toUInt64(ifNotFinite(avgIf(read_rows, type = 'QueryFinish' AND m_old AND NOT m_new AND m_ing), 0)) AS old_avg_read_rows,
  maxIf(memory_usage, type = 'QueryFinish' AND m_old AND NOT m_new AND m_ing) AS old_max_memory,
  toUInt64(ifNotFinite(avgIf(memory_usage, type = 'QueryFinish' AND m_old AND NOT m_new AND m_ing), 0)) AS old_avg_memory,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND m_old AND NOT m_new AND m_ing) AS old_failed,
  toString(maxIf(event_time, m_old AND NOT m_new AND m_ing), 'UTC') AS old_last,
  countIf(type = 'QueryFinish' AND (m_new OR m_old) AND NOT m_ing) AS ingest_off_finished,
  countIf(type = 'QueryFinish' AND m_new = m_old) AS other_finished
FROM system.query_log
WHERE event_date >= yesterday() AND event_time >= now() - INTERVAL 24 HOUR
  AND type != 'QueryStart' AND query_kind = 'Select'
  AND startsWith(query, 'SELECT source, uniqExact(subject_kind, repo_id_str, subject_id, provider, resolved_project_id, event_id) AS edge_count')
  AND cost_gate = 0;
`

const (
	memqlOrgA = "33800000-0000-4000-8000-00000000000a"
	memqlOrgB = "33800000-0000-4000-8000-00000000000b"
	memqlRepo = "33800000-0000-4000-8000-0000000000c1"
	memqlHead = "SELECT source, uniqExact(subject_kind, repo_id_str, subject_id, provider, resolved_project_id, event_id) AS edge_count"
	memqlNew  = "f0b969e82275"
	memqlOld  = "e9a488c626c2"
	memqlNewF = "ff310d988be6"
	memqlOldF = "7cf7c5fc685f"
	// The writer markers of FORM -3 (teams_projects_edges.go membershipIntervalsSQL at a47904e8 / at e67c4128; the ingest column).
	memqlMarkNew = "ARRAY JOIN [(to_project_id, toUInt8(1)), (from_project_id, toUInt8(0))] AS touch"
	memqlMarkOld = "WITH touches AS ("
	memqlMarkIng = "ingested_at"
	// The projector's statement cap (cmd/acr-projector/runtime.go projectorStatementMaxMemoryUsage).
	memqlProjectorCap = 192 << 20
)

func memqlSHA12(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

func memqlRows(ctx context.Context, conn clickhousedriver.Conn, text string) ([]map[string]string, []string, error) {
	rows, err := conn.Query(ctx, text)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	types := rows.ColumnTypes()
	var names []string
	for _, ct := range types {
		names = append(names, ct.DatabaseTypeName())
	}
	var got []map[string]string
	for rows.Next() {
		cells := make([]any, len(types))
		for i, ct := range types {
			cells[i] = reflect.New(ct.ScanType()).Interface()
		}
		if err := rows.Scan(cells...); err != nil {
			return nil, nil, err
		}
		row := map[string]string{}
		for i, name := range rows.Columns() {
			row[name] = fmt.Sprint(reflect.ValueOf(cells[i]).Elem().Interface())
		}
		got = append(got, row)
	}
	return got, names, rows.Err()
}

func memqlFirstLine(err error) string {
	var ex *clickhousedriver.Exception
	if errors.As(err, &ex) {
		return fmt.Sprintf("code %d %s", ex.Code, strings.SplitN(ex.Message, "\n", 2)[0])
	}
	return strings.SplitN(err.Error(), "\n", 2)[0]
}

// memqlWant is the Go recompute of the stmt0 columns from the query_log rows of a set of query ids.
type memqlWant struct {
	newFinished, newFailed, newFailed241, oldFinished, oldFailed, offFinished, otherFinished int
	newMaxRead, newMaxMem, oldMaxRead, oldMaxMem                                             uint64
	newSumRead, newSumMem, oldSumRead, oldSumMem                                             float64
}

func (w memqlWant) cols() map[string]string {
	avg := func(sum float64, n int) string {
		if n == 0 {
			return "0"
		}
		return fmt.Sprint(uint64(math.Trunc(sum / float64(n))))
	}
	m := map[string]string{
		"new_finished": fmt.Sprint(w.newFinished), "new_failed": fmt.Sprint(w.newFailed), "new_failed_241": fmt.Sprint(w.newFailed241),
		"old_finished": fmt.Sprint(w.oldFinished), "old_failed": fmt.Sprint(w.oldFailed), "ingest_off_finished": fmt.Sprint(w.offFinished),
		"other_finished": fmt.Sprint(w.otherFinished), "new_max_read_rows": fmt.Sprint(w.newMaxRead), "new_max_memory": fmt.Sprint(w.newMaxMem),
		"old_max_read_rows": fmt.Sprint(w.oldMaxRead), "old_max_memory": fmt.Sprint(w.oldMaxMem),
		"new_avg_read_rows": avg(w.newSumRead, w.newFinished), "new_avg_memory": avg(w.newSumMem, w.newFinished),
		"old_avg_read_rows": avg(w.oldSumRead, w.oldFinished), "old_avg_memory": avg(w.oldSumMem, w.oldFinished),
	}
	return m
}

func TestForm338MemQLExecutesOnTheEdgeCountStatements(t *testing.T) {
	// 1. The cuts are the run-block texts (sha256 pins) and hold the form's rules once each.
	for _, c := range []struct{ label, text, want string }{{"S0", memqlCutS0, memqlCutS0SHA256}, {"stmtR", memqlCutStmtR, memqlCutStmtRSHA256}, {"stmt0", memqlCutStmt0, memqlCutStmt0SHA256}} {
		sum := sha256.Sum256([]byte(c.text))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Fatalf("cut %s sha256 = %s, want %s", c.label, got, c.want)
		}
		t.Logf("CUT %s sha12 %s lines %d", c.label, c.want[:12], strings.Count(c.text, "\n"))
	}
	caps := []string{"> 200000000\n", "> 100000000000\n", "> 9000000,"}
	headLit := "startsWith(query, '" + memqlHead + "')"
	for lit, n := range map[string]int{caps[0]: 1, caps[1]: 1, caps[2]: 1, "'STOP-COST-GATE'": 1, "cost_gate = 0": 1, headLit: 1,
		"FROM system.query_log": 2, "now() - INTERVAL 24 HOUR": 2, "position(query, '" + memqlMarkNew + "') > 0 AS m_new": 1, "position(query, '" + memqlMarkOld + "') > 0 AS m_old": 1,
		"position(query, '" + memqlMarkIng + "') > 0 AS m_ing": 1, "m_new AND NOT m_old AND m_ing": 8, "m_old AND NOT m_new AND m_ing": 7,
		"(m_new OR m_old) AND NOT m_ing": 1, "m_new = m_old": 1, "SHA256": 0, "toUInt64(ifNotFinite(avgIf(": 4, "), 0)) AS ": 4} {
		if got := strings.Count(memqlCutStmt0, lit); got != n {
			t.Fatalf("stmt0: %q found %d times, want %d", lit, got, n)
		}
	}
	if strings.Contains(memqlCutS0, "STOP-COST-GATE") || strings.Contains(memqlCutS0, "FROM system.query_log") || !strings.Contains(memqlCutS0, "toString(min(min_date)) AS min_day") {
		t.Fatal("S0 reads system.parts only and names min_day")
	}
	if strings.Count(memqlCutStmtR, "\n") != 1 || !strings.Contains(memqlCutStmtR, "FROM clusterAllReplicas('default', system.one);") {
		t.Fatalf("stmtR: %q", memqlCutStmtR)
	}

	// 2. The statement texts (Go side): the shipped text (acr a47904e8) and acr main e67c4128's text, byte for byte
	// (MembershipEdgeCountMainSQLForTest), ingest on and off. Each starts with the form's head literal and holds the markers of its build.
	// INFO: the Go text shas (the near-cap statement_sha ids; query_log holds the bound org value, so they are not the logged shas).
	newOn, oldOn := devhealthsource.MembershipEdgeCountMainSQLForTest(true)
	newOff, oldOff := devhealthsource.MembershipEdgeCountMainSQLForTest(false)
	_, legacyOn := devhealthsource.MembershipEdgeCountSQLForTest(true)
	t.Logf("SHA12 legacy-oracle (MembershipEdgeCountSQLForTest ingest=true) = %s (INFO: not a prod text)", memqlSHA12(legacyOn))
	for _, c := range []struct{ label, text, want string }{{"new ingest=true", newOn, memqlNew}, {"old ingest=true (e67c4128)", oldOn, memqlOld},
		{"new ingest=false", newOff, memqlNewF}, {"old ingest=false (e67c4128)", oldOff, memqlOldF}} {
		got := memqlSHA12(c.text)
		t.Logf("INFO SHA12 %s = %s (near-cap id %s: equal %v) bytes %d", c.label, got, c.want, got == c.want, len(c.text))
		if !strings.HasPrefix(c.text, memqlHead) {
			t.Errorf("%s does not start with the form's head literal", c.label)
		}
		isNew, ing := strings.HasPrefix(c.label, "new"), strings.Contains(c.label, "ingest=true")
		mn, mo, mi := strings.Count(c.text, memqlMarkNew), strings.Count(c.text, memqlMarkOld), strings.Count(c.text, memqlMarkIng)
		t.Logf("MARKERS %s: new %d old %d ingested_at %d", c.label, mn, mo, mi)
		if (mn > 0) != isNew || (mo > 0) == isNew || (mi > 0) != ing {
			t.Errorf("MARKERS %s: new %d old %d ingested_at %d, want new>0=%v old>0=%v ingested_at>0=%v", c.label, mn, mo, mi, isNew, !isNew, ing)
		}
	}
	// The producer page statements read the same transition arm: they hold a marker but do not start with the head (marker uniqueness).
	pageNew, pageOld, pageBindings := devhealthsource.MembershipStatementMainForTest(memqlOrgA, time.Time{}, "", 1000, true, false)
	legacyPage, legacyBindings := devhealthsource.LegacyMembershipStatementForTest(memqlOrgA, time.Time{}, "", 1000, true, false)
	for _, c := range []struct {
		label, text string
		mark        string
	}{{"page new", pageNew, memqlMarkNew}, {"page e67c4128", pageOld, memqlMarkOld}, {"legacy oracle page", legacyPage, memqlMarkIng}} {
		if strings.HasPrefix(c.text, memqlHead) || !strings.Contains(c.text, c.mark) || !strings.Contains(c.text, memqlMarkIng) {
			t.Errorf("%s: want a text that holds %q and %q and does not start with the head", c.label, c.mark, memqlMarkIng)
		}
		t.Logf("MARKERS %s: new %d old %d ingested_at %d starts-with-head %v", c.label, strings.Count(c.text, memqlMarkNew), strings.Count(c.text, memqlMarkOld), strings.Count(c.text, memqlMarkIng), strings.HasPrefix(c.text, memqlHead))
	}
	if t.Failed() {
		t.FailNow()
	}

	// 3. ClickHouse: a per-test database with the production schema and two synthetic orgs.
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
	var unlimitedRows uint
	var unlimitedBytes uint64
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxResultRows: &unlimitedRows, MaxBytesToRead: &unlimitedBytes,
	})
	if err != nil {
		t.Fatalf("open the query client: %v", err)
	}
	t.Cleanup(func() { _ = query.Close() })
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	var version string
	if err := admin.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("clickhouse %s", version)
	seed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	pid := func(expr string) string {
		return fmt.Sprintf("concat('33900000-0000-4000-8000-', leftPad(toString(%s), 12, '0'))", expr)
	}
	for _, org := range []string{memqlOrgA, memqlOrgB} {
		seed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/memql', 'linear', ?)`, memqlRepo, org, base)
		for i := 0; i < 6; i++ {
			seed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, '', 'linear', 'backlog', '', 1, ?)`,
				fmt.Sprintf("33900000-0000-4000-8000-%012d", i), org, fmt.Sprintf("P%d", i), base)
		}
		seed("work items", fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, ingested_at, last_synced)
SELECT concat('linear:W-', leftPad(toString(number), 8, '0')), toUUID(?), ?, 't', 'open', '', '', 'linear', %s, toDateTime64(?, 3, 'UTC') + toIntervalSecond(number), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number + 3), toDateTime64(?, 3, 'UTC') FROM numbers(400)`, pid("number % 6")),
			memqlRepo, org, base, base, base)
		for step, pair := range [][2]string{{"''", pid("number % 6")}, {pid("number % 6"), pid("(number + 1) % 6")}} {
			seed("transitions", fmt.Sprintf(`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at)
SELECT ?, NULL, toUUID(?), 'work_item', concat('linear:H-', toString(number)), 'linear', %s, %s, '', '', '', toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d), concat('ev-', toString(number), '-%d'), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d + 11) FROM numbers(200)`,
				pair[0], pair[1], step, step, step, step), org, memqlRepo, base, base, base)
		}
	}

	// 4. The statements, each under its own query id: the shipped and the e67c4128 text through the projector's client path (the
	// memory-capped client at the projector cap and the statement thread count), for both orgs; the ingest-off texts; a third text
	// (same head, other settings); a text that holds the head later; a new-text run refused by its own cap (code 241).
	capped := contextpacket.NewMemoryCappedQueryClient(query, memqlProjectorCap, contextpacket.WithMaxThreads(contextpacket.StatementMaxThreads))
	tiny := contextpacket.NewMemoryCappedQueryClient(query, 1, contextpacket.WithMaxThreads(contextpacket.StatementMaxThreads))
	ids := map[string]string{}
	run := func(name string, client contextpacket.ClickHouseQueryClient, statement, org string) error {
		t.Helper()
		ids[name] = "memql-" + name + "-" + uuid.NewString()[:8]
		qctx := clickhousedriver.Context(ctx, clickhousedriver.WithQueryID(ids[name]))
		rows, err := client.Query(qctx, statement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: org}})
		if err != nil {
			return err
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		t.Logf("RUN %s rows %d", name, n)
		return nil
	}
	for _, c := range []struct{ name, text, org string }{{"newA", newOn, memqlOrgA}, {"newB", newOn, memqlOrgB}, {"oldA", oldOn, memqlOrgA}, {"oldB", oldOn, memqlOrgB},
		{"newOff", newOff, memqlOrgA}, {"oldOff", oldOff, memqlOrgA}} {
		if err := run(c.name, capped, c.text, c.org); err != nil {
			t.Fatalf("RUN %s: %v", c.name, err)
		}
	}
	// The third text: the head and BOTH build markers (the old marker in a log_comment literal) = other_finished.
	third := strings.Replace(newOn, "SETTINGS max_threads = 1", "SETTINGS max_threads = 1, log_comment = '"+memqlMarkOld+"'", 1)
	if third == newOn || !strings.HasPrefix(third, memqlHead) {
		t.Fatal("no third text")
	}
	if err := run("third", capped, third, memqlOrgA); err != nil {
		t.Fatalf("RUN third: %v", err)
	}
	ids["later"] = "memql-later-" + uuid.NewString()[:8]
	if err := direct.QueryRow(clickhousedriver.Context(ctx, clickhousedriver.WithQueryID(ids["later"])), "SELECT 1 AS x, '"+memqlHead+"' AS y").Scan(new(uint8), new(string)); err != nil {
		t.Fatalf("RUN later: %v", err)
	}
	runWith := func(name, statement string, bindings []contextpacket.ClickHouseBinding) {
		t.Helper()
		ids[name] = "memql-" + name + "-" + uuid.NewString()[:8]
		rows, err := capped.Query(clickhousedriver.Context(ctx, clickhousedriver.WithQueryID(ids[name])), statement, bindings)
		if err != nil {
			t.Fatalf("RUN %s: %v", name, err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("RUN %s: %v", name, err)
		}
		t.Logf("RUN %s rows %d", name, n)
	}
	runWith("pageNew", pageNew, pageBindings)
	runWith("pageOld", pageOld, pageBindings)
	runWith("legacyPage", legacyPage, legacyBindings)
	refusedErr := run("refused", tiny, newOn, memqlOrgA)
	if refusedErr == nil {
		t.Fatalf("RUN refused: no error, want a memory-limit refusal at a 1-byte cap")
	}
	t.Logf("RUN refused: %s (the query_log code is checked below)", memqlFirstLine(refusedErr))
	if err := admin.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}

	// 5. The Go recompute per query id: every query_log row of the id, the form's rules applied in Go.
	recompute := func(names ...string) memqlWant {
		t.Helper()
		var w memqlWant
		for _, name := range names {
			rows, err := admin.Query(ctx, `SELECT toString(type), query, read_rows, memory_usage, exception_code, toString(query_kind), event_time >= now() - INTERVAL 24 HOUR FROM system.query_log WHERE query_id = ? AND type != 'QueryStart'`, ids[name])
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var typ, text, kind string
				var read, mem uint64
				var code int32
				var inWindow bool
				if err := rows.Scan(&typ, &text, &read, &mem, &code, &kind, &inWindow); err != nil {
					t.Fatal(err)
				}
				if !inWindow || kind != "Select" || !strings.HasPrefix(text, memqlHead) {
					continue
				}
				mn, mo, mi := strings.Contains(text, memqlMarkNew), strings.Contains(text, memqlMarkOld), strings.Contains(text, memqlMarkIng)
				finish := typ == "QueryFinish"
				failed := typ == "ExceptionBeforeStart" || typ == "ExceptionWhileProcessing"
				isNew, isOld := mn && !mo && mi, mo && !mn && mi
				if finish && isNew {
					w.newFinished++
					w.newSumRead += float64(read)
					w.newSumMem += float64(mem)
					w.newMaxRead, w.newMaxMem = max(w.newMaxRead, read), max(w.newMaxMem, mem)
				}
				if failed && isNew {
					w.newFailed++
					if code == 241 {
						w.newFailed241++
					}
				}
				if finish && isOld {
					w.oldFinished++
					w.oldSumRead += float64(read)
					w.oldSumMem += float64(mem)
					w.oldMaxRead, w.oldMaxMem = max(w.oldMaxRead, read), max(w.oldMaxMem, mem)
				}
				if failed && isOld {
					w.oldFailed++
				}
				if finish && (mn || mo) && !mi {
					w.offFinished++
				}
				if finish && mn == mo {
					w.otherFinished++
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
		}
		return w
	}
	only := func(text string, names ...string) string {
		t.Helper()
		var quoted []string
		for _, n := range names {
			quoted = append(quoted, "'"+ids[n]+"'")
		}
		out := strings.Replace(text, "AND cost_gate = 0", "AND cost_gate = 0 AND query_id IN ("+strings.Join(quoted, ", ")+")", 1)
		if out == text {
			t.Fatal("no cost_gate conjunct to extend")
		}
		return out
	}
	sent := func(text string) string { return strings.TrimSuffix(strings.TrimRight(text, "\n"), ";") }
	one := func(label, text string, comment ...string) map[string]string {
		t.Helper()
		qctx := ctx
		if len(comment) > 0 {
			qctx = clickhousedriver.Context(ctx, clickhousedriver.WithSettings(clickhousedriver.Settings{"log_comment": comment[0]}))
		}
		got, types, err := memqlRows(qctx, admin, sent(text))
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: err %v rows %d", label, err, len(got))
		}
		if len(types) != 19 {
			t.Errorf("%s: %d columns %v, want 19", label, len(types), types)
		}
		// Output content: no column holds the bound org value or any query text.
		for k, v := range got[0] {
			for _, bad := range []string{memqlOrgA, memqlOrgB, "uniqExact(subject_kind", "SELECT", memqlMarkOld, "to_project_id", memqlMarkIng} {
				if strings.Contains(v, bad) {
					t.Errorf("%s: column %s holds %q", label, k, bad)
				}
			}
		}
		t.Logf("%s ROW %v TYPES %v", label, got[0], types)
		return got[0]
	}
	check := func(label string, got map[string]string, w memqlWant) {
		t.Helper()
		for k, v := range w.cols() {
			if got[k] != v {
				t.Errorf("%s: %s = %s, want %s (Go recompute)", label, k, got[k], v)
			}
		}
	}

	// 6. S0, stmtR, then the clean stmt0 (server-wide) and the exact copies.
	s0, _, err := memqlRows(ctx, admin, sent(memqlCutS0))
	if err != nil || len(s0) < 1 {
		t.Fatalf("S0: err %v rows %d, want >= 1", err, len(s0))
	}
	var minDay string
	if err := admin.QueryRow(ctx, `SELECT toString(min(min_date)) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= yesterday()`).Scan(&minDay); err != nil {
		t.Fatal(err)
	}
	lowDay := ""
	for _, r := range s0 {
		if r["rows"] == "0" {
			t.Errorf("S0 row %v: rows 0", r)
		}
		if lowDay == "" || r["min_day"] < lowDay {
			lowDay = r["min_day"]
		}
	}
	if lowDay != minDay {
		t.Errorf("S0 min(min_day) %q, want %q (a direct min(min_date) read)", lowDay, minDay)
	}
	t.Logf("PASS S0 ROWS %v (min_day %s = a direct read)", s0, lowDay)
	hosts := map[string]uint64{}
	var clusterNames []string
	if rows, err := admin.Query(ctx, `SELECT cluster, count() FROM system.clusters GROUP BY cluster ORDER BY cluster`); err != nil {
		t.Fatal(err)
	} else {
		for rows.Next() {
			var name string
			var n uint64
			if err := rows.Scan(&name, &n); err != nil {
				t.Fatal(err)
			}
			hosts[name] = n
			clusterNames = append(clusterNames, name)
		}
		rows.Close()
	}
	t.Logf("CLUSTERS on the CI server (hosts per cluster): %v", hosts)
	replicasOf := func(label, text string) (string, error) {
		t.Helper()
		got, types, err := memqlRows(ctx, admin, sent(text))
		if err != nil {
			return "", err
		}
		if len(got) != 1 || strings.Join(types, ",") != "UInt64,String" {
			t.Errorf("%s: rows %v types %v, want 1 row of UInt64,String", label, got, types)
			return "", nil
		}
		t.Logf("%s ROW %v", label, got[0])
		return got[0]["replicas"], nil
	}
	proved := 0
	if n, ok := hosts["default"]; ok {
		if got, err := replicasOf("stmtR", memqlCutStmtR); err != nil || got != fmt.Sprint(n) {
			t.Errorf("ARM stmtR clean ('default' has %d hosts): replicas %q err %v", n, got, err)
		} else {
			proved++
			t.Logf("ARM stmtR clean: replicas %s = the 'default' hosts %d (WANT 1: %v)", got, n, got == "1")
		}
	} else if _, err := replicasOf("stmtR", memqlCutStmtR); err == nil {
		t.Errorf("ARM stmtR clean (no 'default' cluster on the CI server): no error, want a server exception (= STOP)")
	} else {
		t.Logf("ARM stmtR clean: no 'default' cluster on the CI server: %s (= STOP)", memqlFirstLine(err))
	}
	for _, name := range clusterNames {
		if name == "default" {
			continue
		}
		got, err := replicasOf("stmtR cluster "+name, strings.Replace(memqlCutStmtR, "'default'", "'"+name+"'", 1))
		if err != nil {
			t.Logf("ARM stmtR cluster %s (%d hosts): NOT PROVEN, %s", name, hosts[name], memqlFirstLine(err))
			continue
		}
		if got != fmt.Sprint(hosts[name]) {
			t.Errorf("ARM stmtR cluster %s: replicas %s, want %d", name, got, hosts[name])
			continue
		}
		proved++
		t.Logf("ARM stmtR cluster %s: replicas %s = its hosts %d (WANT 1: %v)", name, got, hosts[name], got == "1")
	}
	if len(hosts) > 0 && proved == 0 {
		t.Errorf("stmtR: no CI cluster gave replicas = its system.clusters hosts")
	}
	if _, err := replicasOf("MUTANT stmtR unknown cluster", strings.Replace(memqlCutStmtR, "'default'", "'memql_no_such_cluster'", 1)); err == nil {
		t.Errorf("MUTANT stmtR unknown cluster: no error, want a server exception (= STOP)")
	} else {
		t.Logf("MUTANT stmtR unknown cluster: %s (= STOP)", memqlFirstLine(err))
	}

	if _, _, err := memqlRows(ctx, admin, memqlCutStmt0); err != nil {
		t.Logf("INFO the stmt0 cut AS SENT (with its ';' line end) on the native protocol: %s", memqlFirstLine(err))
	} else {
		t.Logf("INFO the stmt0 cut AS SENT (with its ';' line end) on the native protocol: ran")
	}
	passComment := "memql-pass-" + uuid.NewString()[:8]
	clean := one("PASS stmt0 (server-wide)", memqlCutStmt0, passComment)
	all := recompute("newA", "newB", "oldA", "oldB", "newOff", "oldOff", "third", "later", "refused", "pageNew", "pageOld", "legacyPage")
	for _, name := range []string{"newA", "newB", "oldA", "oldB", "newOff", "oldOff", "third", "later", "refused", "pageNew", "pageOld", "legacyPage"} {
		rows, err := admin.Query(ctx, `SELECT toString(type), substring(lower(hex(SHA256(query))), 1, 12), exception_code, toString(query_kind) FROM system.query_log WHERE query_id = ? AND type != 'QueryStart'`, ids[name])
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var typ, qsha, kind string
			var code int32
			if err := rows.Scan(&typ, &qsha, &code, &kind); err != nil {
				t.Fatal(err)
			}
			t.Logf("QLOG %s type %s server-sha12 %s code %d kind %s", name, typ, qsha, code, kind)
		}
		rows.Close()
	}
	if all.newFinished != 2 || all.oldFinished != 2 || all.offFinished != 2 || all.otherFinished != 1 || all.newFailed241 != 1 {
		t.Fatalf("the CI statements did not give the cases: %+v", all)
	}
	for k, lo := range map[string]int{"new_finished": 2, "old_finished": 2, "ingest_off_finished": 2, "other_finished": 1, "new_failed_241": 1} {
		var n int
		if _, err := fmt.Sscan(clean[k], &n); err != nil || n < lo {
			t.Errorf("PASS stmt0 server-wide %s = %s, want >= %d (this test's own statements)", k, clean[k], lo)
		}
	}
	if clean["run_at"] < clean["new_first"] || clean["old_last"] > clean["run_at"] {
		t.Errorf("INFO times out of order: new_first %s old_last %s run_at %s", clean["new_first"], clean["old_last"], clean["run_at"])
	}
	every := []string{"newA", "newB", "oldA", "oldB", "newOff", "oldOff", "third", "later", "refused", "pageNew", "pageOld", "legacyPage"}
	check("EXACT all", one("EXACT all", only(memqlCutStmt0, every...)), all)
	check("EXACT org A + org B new (the form is org-blind)", one("EXACT orgs", only(memqlCutStmt0, "newA", "newB")), recompute("newA", "newB"))
	check("EXACT old only", one("EXACT old only", only(memqlCutStmt0, "oldA", "oldB")), recompute("oldA", "oldB"))
	check("EXACT ingest off", one("EXACT ingest off", only(memqlCutStmt0, "newOff", "oldOff")), recompute("newOff", "oldOff"))
	check("EXACT refused", one("EXACT refused", only(memqlCutStmt0, "refused")), recompute("refused"))
	if got := one("EXACT third", only(memqlCutStmt0, "third")); got["other_finished"] != "1" || got["new_finished"] != "0" || got["old_finished"] != "0" {
		t.Errorf("EXACT third (head + both markers): other_finished %s new %s old %s, want 1 / 0 / 0", got["other_finished"], got["new_finished"], got["old_finished"])
	}
	zero := map[string]string{"new_finished": "0", "old_finished": "0", "ingest_off_finished": "0", "other_finished": "0", "new_failed": "0", "old_failed": "0"}
	isZero := func(label string, got map[string]string) {
		t.Helper()
		for k, v := range zero {
			if got[k] != v {
				t.Errorf("%s: %s = %s, want %s", label, k, got[k], v)
			}
		}
	}
	isZero("EXACT later (head not at the start)", one("EXACT later", only(memqlCutStmt0, "later")))
	if w := recompute("later"); w.otherFinished != 0 {
		t.Errorf("the later recompute counts it: %+v", w)
	}
	// Marker uniqueness: the real producer page statements (a marker + ingested_at, not the head) count 0 in every column; the head
	// anchor is the guard: a copy without the startsWith conjunct counts them.
	pages := []string{"pageNew", "pageOld", "legacyPage"}
	isZero("EXACT producer pages (marker, no head)", one("EXACT pages", only(memqlCutStmt0, pages...)))
	if w := recompute(pages...); w != (memqlWant{}) {
		t.Errorf("the page recompute counts them: %+v", w)
	}
	noAnchor := strings.Replace(memqlCutStmt0, "\n  AND "+headLit+"\n", "\n", 1)
	if noAnchor == memqlCutStmt0 {
		t.Fatal("no head anchor line to drop")
	}
	if got := one("MUTANT no head anchor", only(noAnchor, pages...)); got["new_finished"] == "0" || got["old_finished"] == "0" {
		t.Errorf("MUTANT no head anchor: new_finished %s old_finished %s, want both > 0 (the page statements are counted)", got["new_finished"], got["old_finished"])
	}

	// 7. Mutants: each test copy changes ONE rule; the count it gives must differ from the clean form's.
	markNewCut := strings.Replace(memqlCutStmt0, "AS touch') > 0 AS m_new", "AS toucx') > 0 AS m_new", 1)
	markOldCut := strings.Replace(memqlCutStmt0, "'WITH touches AS (')", "'WITH touchez AS (')", 1)
	markIngCut := strings.Replace(memqlCutStmt0, "'ingested_at') > 0 AS m_ing", "'ingested_ax') > 0 AS m_ing", 1)
	if markNewCut == memqlCutStmt0 || markOldCut == memqlCutStmt0 || markIngCut == memqlCutStmt0 {
		t.Fatal("a marker literal to mutate is missing")
	}
	if got := one("MUTANT new marker literal", only(markNewCut, "newA", "newB")); got["new_finished"] != "0" || got["other_finished"] != "2" {
		t.Errorf("MUTANT new marker: new_finished %s other_finished %s, want 0 / 2", got["new_finished"], got["other_finished"])
	}
	if got := one("MUTANT old marker literal", only(markOldCut, "oldA", "oldB")); got["old_finished"] != "0" || got["other_finished"] != "2" {
		t.Errorf("MUTANT old marker: old_finished %s other_finished %s, want 0 / 2", got["old_finished"], got["other_finished"])
	}
	if got := one("MUTANT ingest marker literal", only(markIngCut, "newA")); got["new_finished"] != "0" || got["ingest_off_finished"] != "1" {
		t.Errorf("MUTANT ingest marker: new_finished %s ingest_off_finished %s, want 0 / 1", got["new_finished"], got["ingest_off_finished"])
	}
	// 0-row avg cell: a marker mutant leaves both avg columns of that build over 0 matched rows: WANT 0, not nan.
	for _, c := range []struct{ text, ids, prefix string }{{markNewCut, "newA newB", "new"}, {markOldCut, "oldA oldB", "old"}} {
		got := one("ZERO-ROW avg "+c.prefix, only(c.text, strings.Fields(c.ids)...))
		for _, k := range []string{c.prefix + "_avg_read_rows", c.prefix + "_avg_memory", c.prefix + "_finished", c.prefix + "_max_read_rows", c.prefix + "_max_memory"} {
			if got[k] != "0" {
				t.Errorf("ZERO-ROW avg %s: %s = %s, want 0", c.prefix, k, got[k])
			}
		}
	}
	headCut := strings.Replace(memqlCutStmt0, "AS edge_count')", "AS edge_counx')", 1)
	if headCut == memqlCutStmt0 {
		t.Fatal("no head literal to mutate")
	}
	isZero("MUTANT head literal last char", one("MUTANT head literal", only(headCut, every...)))
	noStart := strings.Replace(memqlCutStmt0, headLit, "position(query, '"+memqlHead+"') > 0", 1)
	if got := one("MUTANT startsWith -> position", only(noStart, "later")); got["other_finished"] != "1" {
		t.Errorf("MUTANT position: the later statement other_finished %s, want 1 (startsWith is the rule that keeps it out)", got["other_finished"])
	}
	late := strings.Replace(memqlCutStmt0, "AND event_time >= now() - INTERVAL 24 HOUR\n", "AND event_time >= now() + INTERVAL 1 HOUR\n", 1)
	if late == memqlCutStmt0 {
		t.Fatal("no scan window to mutate")
	}
	isZero("MUTANT window starts after the CI rows", one("MUTANT window", only(late, every...)))

	// 8. Gate fires: each cap lowered to 0 in a test copy. WANT code 395 STOP-COST-GATE, then the query_log type and read_rows.
	var fireComments []string
	for _, c := range caps {
		comment := "memql-gate-" + uuid.NewString()[:8]
		fctx := clickhousedriver.Context(ctx, clickhousedriver.WithSettings(clickhousedriver.Settings{"log_comment": comment}))
		_, _, err := memqlRows(fctx, admin, sent(strings.Replace(memqlCutStmt0, c, "> 0"+c[len(c)-1:], 1)))
		var gx *clickhousedriver.Exception
		if err == nil || !errors.As(err, &gx) || gx.Code != 395 || !strings.Contains(gx.Message, "STOP-COST-GATE") {
			t.Errorf("GATE %q lowered to 0: err = %v, want code 395 STOP-COST-GATE", c, err)
			continue
		}
		t.Logf("GATE %q lowered to 0: fired code=%d", c, gx.Code)
		fireComments = append(fireComments, comment)
	}
	if len(fireComments) != 3 {
		t.Errorf("gates fired %d times, want 3", len(fireComments))
	}
	// 9. PASS path and fire read_rows: W = the 24 h window rows, P = the query_log parts of the parts gate. A pass reads the window twice
	// + the parts gate twice + system.one; a fire reads the window once (the scalar subqueries run before throwIf). Whole granules: slack.
	if err := admin.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	var w, p uint64
	if err := admin.QueryRow(ctx, `SELECT count() FROM system.query_log WHERE event_date >= yesterday() AND event_time >= now() - INTERVAL 24 HOUR`).Scan(&w); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT count() FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= yesterday()`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	slack := 2 * 8192 * p
	logOf := func(comment string) ([]string, uint64) {
		t.Helper()
		rows, err := admin.Query(ctx, `SELECT toString(type), read_rows FROM system.query_log WHERE log_comment = ? AND type != 'QueryStart' ORDER BY event_time_microseconds`, comment)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var types []string
		var total uint64
		for rows.Next() {
			var typ string
			var read uint64
			if err := rows.Scan(&typ, &read); err != nil {
				t.Fatal(err)
			}
			types = append(types, typ)
			total += read
		}
		return types, total
	}
	if types, read := logOf(passComment); len(types) != 1 || types[0] != "QueryFinish" || read < 1 || read > 2*w+2*p+1+slack {
		t.Errorf("PASS stmt0 query_log types %v read_rows %d, want [QueryFinish] and 1..%d (2W + 2P + 1 + slack, W %d P %d)", types, read, 2*w+2*p+1+slack, w, p)
	} else {
		t.Logf("PASS stmt0 query_log types %v read_rows %d (2W + 2P + 1 = %d, + slack %d; W %d P %d)", types, read, 2*w+2*p+1, slack, w, p)
	}
	for _, c := range fireComments {
		if types, read := logOf(c); len(types) != 1 || types[0] != "ExceptionBeforeStart" || read > w+2*p+1+slack/2 {
			t.Errorf("GATE %s query_log types %v read_rows %d, want [ExceptionBeforeStart] and <= W + 2P + 1 + slack/2 = %d", c, types, read, w+2*p+1+slack/2)
		} else {
			t.Logf("GATE %s query_log types %v read_rows %d (W + 2P + 1 = %d)", c, types, read, w+2*p+1)
		}
	}
}
