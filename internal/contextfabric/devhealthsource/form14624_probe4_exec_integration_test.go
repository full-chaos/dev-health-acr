package devhealthsource_test

// DO NOT MERGE. Throwaway exec leg of a prod read form (FORM-14624-PROBE-4: the ci_pipeline_run total count statement (S7) of the
// marker-absent probe at the projector's max_threads 4, measured once through system.query_log). The cuts are the form's run-block texts
// with the org prefix and the window swapped to synthetic values; each swapped cut's sha256 is pinned. Synthetic rows only (two fake orgs).
// Every function here that reads memory_usage from system.query_log calls statementthreads.RequireThreadBound (the statementthreads census).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const form14624p4CutSSET = `SELECT name, value, changed, transform(name, ['max_memory_usage', 'max_threads', 'max_bytes_to_read', 'max_execution_time', 'max_result_rows'], [toUInt64(201326592), 4, 268435456, 10, 1000], toUInt64(0)) AS form_value,
  multiIf(name = 'readonly', toUInt8(value != '1'), name IN ('read_overflow_mode', 'timeout_overflow_mode', 'result_overflow_mode'), toUInt8(value = 'throw'), name = 'enable_analyzer', toUInt8(1),
    toUInt8(toUInt64OrZero(extract(value, '[0-9]+')) = 0 OR toUInt64OrZero(extract(value, '[0-9]+')) >= form_value)) AS ok
FROM system.settings WHERE name IN ('readonly', 'max_memory_usage', 'max_threads', 'max_bytes_to_read', 'max_execution_time', 'max_result_rows', 'read_overflow_mode', 'timeout_overflow_mode', 'result_overflow_mode', 'enable_analyzer')
ORDER BY name;
`
const form14624p4CutSSETSHA256 = "c3bedf30cec195bb14bc136e88067e349b025b1ccbeb6dde43ccd8548b6ffdf7"

const form14624p4CutstmtR = `SELECT count() AS replicas, toString(version()) AS server_version, toUInt64OrZero(extract((SELECT value FROM system.settings WHERE name = 'max_threads'), '[0-9]+')) AS server_max_threads FROM clusterAllReplicas('default', system.one);
`
const form14624p4CutstmtRSHA256 = "303a281ba7fde24dc8850cd1be6a3fe29f44e6acf878b95841a037d918c8cec2"

const form14624p4CutS0 = `SELECT database = 'system' AS is_system, table, sum(rows) AS rows, sum(bytes_on_disk) AS bytes, count() AS active_parts
FROM system.parts WHERE active AND ((database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos'))
  OR (database = 'system' AND table = 'query_log' AND max_date >= toDate('2026-01-01')))
GROUP BY is_system, table ORDER BY is_system, table;
`
const form14624p4CutS0SHA256 = "957b3a36c5b66635f22befec95b379b7bb8d343b355269d21cb1a9110195ac8d"

const form14624p4CutORG1 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table = 'repos') > 5000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table = 'repos') > 5000000000,
  'STOP-COST-GATE')) AS cost_gate
SELECT uniqExact(org_id) AS orgs FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0;
`
const form14624p4CutORG1SHA256 = "47766c9e6de9ca0dcd89c893ba276ce8c31627d2dca61a6b2fabf8265bd30992"

const form14624p4CutC0 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT length(form_org_id) AS org_id_length
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 4, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe4_c0';
`
const form14624p4CutC0SHA256 = "28da113420d29eba3b9860da401454c8f2cd3c3fc356c79bd5283ea30159f4c8"

const form14624p4CutS7 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(c.repo_id, c.run_id) FROM ci_pipeline_runs AS c FINAL
INNER JOIN repos AS repo FINAL ON repo.id = c.repo_id AND repo.org_id = c.org_id
WHERE c.org_id = form_org_id AND length(concat('ci_pipeline_run.v2:', replaceAll(replaceAll(toString(c.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(c.run_id, '%', '%25'), ':', '%3A'))) <= 256
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 4, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe4_s7';
`
const form14624p4CutS7SHA256 = "8d6d019cf79a46c378c22d18dcf526910cba865aa821c73988447b2dffb046ce"

const form14624p4CutQL = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2026-01-01')) > 200000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2026-01-01')) > 100000000000
  OR (SELECT count() FROM system.query_log WHERE event_date >= toDate('2026-01-01') AND event_time >= toDateTime('2026-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2099-01-01 00:00:00', 'UTC')) > 9000000,
  'STOP-COST-GATE')) AS cost_gate
SELECT log_comment AS stmt, toString(type) AS type, exception_code AS code, query_duration_ms AS elapsed_ms, read_rows, read_bytes, memory_usage AS peak_memory, result_rows,
  Settings['max_memory_usage'] AS set_max_memory_usage, Settings['max_threads'] AS set_max_threads, Settings['max_bytes_to_read'] AS set_max_bytes_to_read,
  Settings['max_execution_time'] AS set_max_execution_time, Settings['max_result_rows'] AS set_max_result_rows, Settings['read_overflow_mode'] AS set_read_overflow_mode,
  Settings['timeout_overflow_mode'] AS set_timeout_overflow_mode, Settings['result_overflow_mode'] AS set_result_overflow_mode, toString(event_time, 'UTC') AS event_time_utc
FROM system.query_log
WHERE event_date >= toDate('2026-01-01') AND event_time >= toDateTime('2026-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2099-01-01 00:00:00', 'UTC') AND type != 'QueryStart' AND query_kind = 'Select'
  AND log_comment IN ('form14624_probe4_c0', 'form14624_probe4_s7')
  AND cost_gate = 0
ORDER BY event_time_utc, stmt
LIMIT 200;
`
const form14624p4CutQLSHA256 = "eceb2fb95e0b20e973697279db384031f3fbddf720335329b6f4de7c2e221d02"

const form14624p4PastQL = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01')) > 200000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01')) > 100000000000
  OR (SELECT count() FROM system.query_log WHERE event_date >= toDate('2025-01-01') AND event_time >= toDateTime('2025-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2025-01-02 00:00:00', 'UTC')) > 9000000,
  'STOP-COST-GATE')) AS cost_gate
SELECT log_comment AS stmt, toString(type) AS type, exception_code AS code, query_duration_ms AS elapsed_ms, read_rows, read_bytes, memory_usage AS peak_memory, result_rows,
  Settings['max_memory_usage'] AS set_max_memory_usage, Settings['max_threads'] AS set_max_threads, Settings['max_bytes_to_read'] AS set_max_bytes_to_read,
  Settings['max_execution_time'] AS set_max_execution_time, Settings['max_result_rows'] AS set_max_result_rows, Settings['read_overflow_mode'] AS set_read_overflow_mode,
  Settings['timeout_overflow_mode'] AS set_timeout_overflow_mode, Settings['result_overflow_mode'] AS set_result_overflow_mode, toString(event_time, 'UTC') AS event_time_utc
FROM system.query_log
WHERE event_date >= toDate('2025-01-01') AND event_time >= toDateTime('2025-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2025-01-02 00:00:00', 'UTC') AND type != 'QueryStart' AND query_kind = 'Select'
  AND log_comment IN ('form14624_probe4_c0', 'form14624_probe4_s7')
  AND cost_gate = 0
ORDER BY event_time_utc, stmt
LIMIT 200;
`
const form14624p4PastQLSHA256 = "aa2f958a63478ba5f11605c8f22c02414426e02af6c5bd7b1e3b5d470415a8d0"

// form14624p4CodeS7SHA256 pins the S7 code text the form carries (acr 49c36439, source_counts.go:79-81).
const form14624p4CodeS7SHA256 = "70f782872fd4dfb7581b67a98b926217003f171199706e8a67320bc1c7d54e71"

const (
	form14624p4PrefixA = "f0f0f0f0"
	form14624p4OrgA    = "f0f0f0f0-0000-4000-8000-00000000000a"
	form14624p4PrefixB = "e1e1e1e1"
	form14624p4OrgB    = "e1e1e1e1-0000-4000-8000-00000000000b"
	// form14624p4ProjectorMemoryCap is cmd/acr-projector projectorStatementMaxMemoryUsage (runtime.go:407; package main, not importable).
	form14624p4ProjectorMemoryCap = 192 << 20
	// form14624p4PeakLimit is the card's 60 % rule.
	form14624p4PeakLimit        = form14624p4ProjectorMemoryCap * 60 / 100
	form14624p4MaxExecutionTime = 10
	form14624p4MaxResultRows    = 1000
	form14624p4RowsCap          = "> 6000000\n"
	form14624p4BytesCap         = "> 8589934592,\n"
	// form14624p4RunsA is org A's synthetic ci run count: large enough that the uniqExact state, not the base cost, sets the peak.
	form14624p4RunsA = 250000
)

// form14624p4Settings is the form's SETTINGS line: the projector client's per-statement settings, max_threads = the statement threads.
func form14624p4Settings(comment string) string {
	return fmt.Sprintf("SETTINGS max_memory_usage = %d, max_threads = %d, max_bytes_to_read = %d, max_execution_time = %d, max_result_rows = %d, log_comment = '%s';",
		form14624p4ProjectorMemoryCap, contextpacket.StatementMaxThreads, config.DefaultClickHouseMaxBytesToRead, form14624p4MaxExecutionTime, form14624p4MaxResultRows, comment)
}

var form14624p4GateTables = regexp.MustCompile(`table IN \(([^)]*)\)`)

func form14624p4GateTableSet(t *testing.T, label, text string) []string {
	t.Helper()
	lists := form14624p4GateTables.FindAllStringSubmatch(text, -1)
	if len(lists) != 2 || lists[0][1] != lists[1][1] {
		t.Fatalf("%s: want 2 equal gate table lists, got %q", label, lists)
	}
	var out []string
	for _, name := range strings.Split(lists[0][1], ", ") {
		out = append(out, strings.Trim(name, "'"))
	}
	sort.Strings(out)
	return out
}

func TestForm14624Probe4CutsAreTheCodeTexts(t *testing.T) {
	for _, c := range []struct{ label, text, want string }{
		{"S-SET", form14624p4CutSSET, form14624p4CutSSETSHA256}, {"stmtR", form14624p4CutstmtR, form14624p4CutstmtRSHA256}, {"S0", form14624p4CutS0, form14624p4CutS0SHA256},
		{"ORG1", form14624p4CutORG1, form14624p4CutORG1SHA256}, {"C0", form14624p4CutC0, form14624p4CutC0SHA256}, {"S7", form14624p4CutS7, form14624p4CutS7SHA256},
		{"QL", form14624p4CutQL, form14624p4CutQLSHA256}, {"past QL", form14624p4PastQL, form14624p4PastQLSHA256},
	} {
		sum := sha256.Sum256([]byte(c.text))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Fatalf("cut %s sha256 = %s, want %s", c.label, got, c.want)
		}
		t.Logf("CUT %s sha12 %s lines %d", c.label, c.want[:12], strings.Count(c.text, "\n"))
	}
	statements := devhealthsource.Form14624ProbeStatementsForTest()
	if len(statements) != 9 || statements[6].Kind != "ci_pipeline_run" {
		t.Fatalf("the probe statements moved: %d, the 7th %v", len(statements), statements)
	}
	st := statements[6]
	sum := sha256.Sum256([]byte(st.SQL))
	if got := hex.EncodeToString(sum[:]); got != form14624p4CodeS7SHA256 {
		t.Fatalf("S7 code text sha256 %s, the form carries %s: the code moved, the form is stale", got, form14624p4CodeS7SHA256)
	}
	prefixRule := "(SELECT any(org_id) FROM repos WHERE startsWith(org_id, '" + form14624p4PrefixA + "') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id\n"
	inner := strings.ReplaceAll(st.SQL, "{org_id:String}", "form_org_id")
	body := "SELECT * FROM (\n" + inner + "\n) WHERE cost_gate = 0\n" + form14624p4Settings("form14624_probe4_s7") + "\n"
	if !strings.HasSuffix(form14624p4CutS7, body) {
		t.Fatalf("S7: the cut is not the gate + the code text (token form_org_id) + the projector SETTINGS:\n%s\nwant suffix:\n%s", form14624p4CutS7, body)
	}
	head := strings.TrimSuffix(form14624p4CutS7, body)
	for lit, n := range map[string]int{form14624p4RowsCap: 1, form14624p4BytesCap: 1, "'STOP-COST-GATE'": 1, "cost_gate = 0": 1, prefixRule: 1, "{org_id": 0, "startsWith(": 1} {
		if got := strings.Count(head, lit); got != n {
			t.Fatalf("S7 gate: %q found %d times, want %d", lit, got, n)
		}
	}
	if got := form14624p4GateTableSet(t, "S7", head); strings.Join(got, ",") != "ci_pipeline_runs,repos" {
		t.Fatalf("S7 gate tables %v, want ci_pipeline_runs and repos", got)
	}
	c0 := "SELECT * FROM (\nSELECT length(form_org_id) AS org_id_length\n) WHERE cost_gate = 0\n" + form14624p4Settings("form14624_probe4_c0") + "\n"
	if !strings.HasSuffix(form14624p4CutC0, c0) || !strings.Contains(form14624p4CutC0, prefixRule) {
		t.Fatalf("C0 is not the S7 wrapper around length(form_org_id)")
	}
	for _, c := range []string{"'form14624_probe4_c0'", "'form14624_probe4_s7'"} {
		if !strings.Contains(form14624p4CutQL, c) {
			t.Fatalf("QL does not read %s", c)
		}
	}
	if !strings.Contains(form14624p4CutSSET, "[toUInt64(201326592), 4, 268435456, 10, 1000]") {
		t.Fatalf("S-SET form values are not the SETTINGS values")
	}
	t.Logf("TEXT S7 code sha12 %s, gate tables ci_pipeline_runs,repos, max_threads %d", form14624p4CodeS7SHA256[:12], contextpacket.StatementMaxThreads)
}

type form14624p4LogRow struct {
	comment, typ                                   string
	code                                           int32
	elapsed, readRows, readBytes, peak, resultRows uint64
	settings                                       [8]string
}

// form14624p4Logged reads the query_log rows (not QueryStart) of the query ids (FuncDecl: it reads memory_usage from system.query_log, so it
// checks the statement threads of the finished rows, the statementthreads census rule).
func form14624p4Logged(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, label string, ids, threadIDs []string) map[string]form14624p4LogRow {
	t.Helper()
	var unbound uint64
	if err := direct.QueryRow(ctx, "SELECT "+statementthreads.UnboundThreadsCount+" FROM system.query_log WHERE type = 'QueryFinish' AND has(?, query_id)", threadIDs).Scan(&unbound); err != nil {
		t.Fatalf("%s: thread check: %v", label, err)
	}
	statementthreads.RequireThreadBound(t, ctx, direct, label, unbound)
	rows, err := direct.Query(ctx, `SELECT query_id, log_comment, toString(type), exception_code, query_duration_ms, read_rows, read_bytes, memory_usage, result_rows,
  Settings['max_memory_usage'], Settings['max_threads'], Settings['max_bytes_to_read'], Settings['max_execution_time'], Settings['max_result_rows'],
  Settings['read_overflow_mode'], Settings['timeout_overflow_mode'], Settings['result_overflow_mode']
FROM system.query_log WHERE type != 'QueryStart' AND has(?, query_id)`, ids)
	if err != nil {
		t.Fatalf("%s: read query_log: %v", label, err)
	}
	defer rows.Close()
	out := map[string]form14624p4LogRow{}
	for rows.Next() {
		var id string
		var r form14624p4LogRow
		if err := rows.Scan(&id, &r.comment, &r.typ, &r.code, &r.elapsed, &r.readRows, &r.readBytes, &r.peak, &r.resultRows,
			&r.settings[0], &r.settings[1], &r.settings[2], &r.settings[3], &r.settings[4], &r.settings[5], &r.settings[6], &r.settings[7]); err != nil {
			t.Fatalf("%s: scan query_log: %v", label, err)
		}
		if _, dup := out[id]; dup {
			t.Fatalf("%s: two query_log rows for one query id %s", label, id)
		}
		out[id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: rows: %v", label, err)
	}
	return out
}

func form14624p4Code(err error) int32 {
	var exception *clickhousedriver.Exception
	if errors.As(err, &exception) {
		return exception.Code
	}
	return -1
}

func TestForm14624Probe4ExecutesS7AtFourThreads(t *testing.T) {
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
	// The projector's client stack: the dev-health-go client with the hosted read budget, the projector memory cap, the statement threads.
	maxBytes := config.DefaultClickHouseMaxBytesToRead
	raw, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxBytesToRead: &maxBytes,
	})
	if err != nil {
		t.Fatalf("open the projector query client: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	projector := statementthreads.Capped(raw, form14624p4ProjectorMemoryCap)
	// devhealthschema:not-a-production-replica the table names here select which tables devhealthschema.DDL renders; no schema is defined in this file
	for _, statement := range devhealthschema.DDL("repos", "ci_pipeline_runs") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	var version string
	if err := direct.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("clickhouse %s", version)

	// 1. Two synthetic orgs: A with many runs (one run id too long for the producer), B with fewer runs on many repos.
	exec := func(statement string) {
		t.Helper()
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	const at3 = "toDateTime64('2025-03-03 00:00:00', 3, 'UTC')"
	seed := func(prefix, org string, repos, runs int) {
		repo := func(expr string) string {
			return fmt.Sprintf("toUUID(concat('%s', '-0000-4000-8000-', leftPad(toString(%s), 12, '0')))", prefix, expr)
		}
		// devhealthschema:not-a-production-replica the table names here fill tables that devhealthschema.DDL rendered above; no schema is defined in this file
		exec(fmt.Sprintf("INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT %s, '%s', concat('acme/r', toString(number)), 'github', %s FROM numbers(%d)", repo("number"), org, at3, repos))
		exec(fmt.Sprintf("INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) SELECT %s, if(number = 0, concat('R-', repeat('X', 300)), concat('R-', toString(number))), 'success', %s, %s, '%s' FROM numbers(%d)", repo(fmt.Sprintf("number %% %d", repos)), at3, at3, org, runs))
	}
	seed(form14624p4PrefixA, form14624p4OrgA, 3, form14624p4RunsA)
	seed(form14624p4PrefixB, form14624p4OrgB, 20000, 100000)
	for _, name := range []string{"repos", "ci_pipeline_runs"} {
		exec("OPTIMIZE TABLE " + name + " FINAL")
	}

	// 2. CODE: the S7 code text through the projector client stack (4 threads), bound org, per org.
	st := devhealthsource.Form14624ProbeStatementsForTest()[6]
	codeCount := func(org, reader string) uint64 {
		t.Helper()
		rows, err := projector.Query(contextpacket.WithStatementReader(ctx, reader), st.SQL, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: org}})
		if err != nil {
			t.Fatalf("CODE %s: %v", reader, err)
		}
		defer rows.Close()
		var total uint64
		n := 0
		for rows.Next() {
			if err := rows.Scan(&total); err != nil {
				t.Fatalf("CODE %s scan: %v", reader, err)
			}
			n++
		}
		if err := rows.Err(); err != nil || n != 1 {
			t.Fatalf("CODE %s: rows %d err %v", reader, n, err)
		}
		return total
	}
	codeA := codeCount(form14624p4OrgA, "form14624p4_code_s7")
	codeB := codeCount(form14624p4OrgB, "form14624p4_code_b_s7")
	if codeA != form14624p4RunsA-1 || codeB == 0 || codeB == codeA {
		t.Fatalf("CODE S7: org A %d (want %d: one run id is too long), org B %d", codeA, form14624p4RunsA-1, codeB)
	}
	t.Logf("CODE S7 org A %d org B %d", codeA, codeB)

	// 3. FORM: each cut through a plain connection (pm's connector sends no settings of its own), one query id each.
	runID := time.Now().UnixNano()
	var formIDs []string
	run := func(label, text string) (chdriver.Rows, string, error) {
		t.Helper()
		id := fmt.Sprintf("form14624p4-%s-%d-%d", label, runID, len(formIDs))
		formIDs = append(formIDs, id)
		rows, err := direct.Query(clickhousedriver.Context(ctx, clickhousedriver.WithQueryID(id)), strings.TrimSuffix(strings.TrimRight(text, "\n"), ";"))
		return rows, id, err
	}
	one := func(label, text string) (uint64, string) {
		t.Helper()
		rows, id, err := run(label, text)
		if err != nil {
			t.Fatalf("FORM %s: %v", label, err)
		}
		defer rows.Close()
		var v uint64
		n := 0
		for rows.Next() {
			if err := rows.Scan(&v); err != nil {
				t.Fatalf("FORM %s scan: %v", label, err)
			}
			n++
		}
		if err := rows.Err(); err != nil || n != 1 {
			t.Fatalf("FORM %s: rows %d err %v, want 1 row", label, n, err)
		}
		return v, id
	}
	sset := func(label, text string) map[string]uint8 {
		t.Helper()
		rows, _, err := run(label, text)
		if err != nil {
			t.Fatalf("FORM %s: %v", label, err)
		}
		defer rows.Close()
		out := map[string]uint8{}
		for rows.Next() {
			var name, value string
			var changed uint8
			var formValue uint64
			var ok uint8
			if err := rows.Scan(&name, &value, &changed, &formValue, &ok); err != nil {
				t.Fatalf("FORM %s scan: %v", label, err)
			}
			out[name] = ok
			t.Logf("%s %s = %q changed %d form_value %d ok %d", label, name, value, changed, formValue, ok)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("FORM %s rows: %v", label, err)
		}
		return out
	}
	if got := sset("S-SET", form14624p4CutSSET); len(got) != 10 {
		t.Fatalf("FORM S-SET: %d rows, want 10: %v", len(got), got)
	} else {
		for name, ok := range got {
			if ok != 1 {
				t.Fatalf("FORM S-SET %s ok = 0 on the test server: the WANT would STOP", name)
			}
		}
	}
	rows, _, err := run("stmtR", form14624p4CutstmtR)
	if err != nil {
		t.Fatalf("FORM stmtR: %v", err)
	}
	var replicas, serverThreads uint64
	var serverVersion string
	for rows.Next() {
		if err := rows.Scan(&replicas, &serverVersion, &serverThreads); err != nil {
			t.Fatalf("FORM stmtR scan: %v", err)
		}
	}
	rows.Close()
	if replicas != 1 {
		t.Fatalf("FORM stmtR replicas %d, want 1", replicas)
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	rows, _, err = run("S0", form14624p4CutS0)
	if err != nil {
		t.Fatalf("FORM S0: %v", err)
	}
	sizes := map[string][3]uint64{}
	for rows.Next() {
		var isSystem uint8
		var table string
		var r, b, p uint64
		if err := rows.Scan(&isSystem, &table, &r, &b, &p); err != nil {
			t.Fatalf("FORM S0 scan: %v", err)
		}
		sizes[table] = [3]uint64{r, b, p}
	}
	rows.Close()
	if _, ok := sizes["query_log"]; len(sizes) != 3 || !ok {
		t.Fatalf("FORM S0: %d table rows, want 3 (2 tables + query_log): %v", len(sizes), sizes)
	}
	t.Logf("FORM S0 sizes %v; stmtR replicas 1 server %s max_threads default %d", sizes, serverVersion, serverThreads)
	if orgs, _ := one("ORG1", form14624p4CutORG1); orgs != 1 {
		t.Fatalf("FORM ORG1 orgs %d, want 1", orgs)
	}
	length, c0ID := one("C0", form14624p4CutC0)
	if length != uint64(len(form14624p4OrgA)) {
		t.Fatalf("FORM C0 org_id_length %d, want %d", length, len(form14624p4OrgA))
	}
	got, s7ID := one("S7", form14624p4CutS7)
	if got != codeA {
		t.Fatalf("FORM S7 = %d, the code text on org A = %d", got, codeA)
	}
	t.Logf("FORM S7 = %d = CODE org A", got)

	// 4. QL + PASS: the form's query_log read vs a recompute by query id; settings; the read bound; the peak at 4 threads vs the 60 % rule.
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	rows, _, err = run("QL", form14624p4CutQL)
	if err != nil {
		t.Fatalf("FORM QL: %v", err)
	}
	ql := map[string]form14624p4LogRow{}
	for rows.Next() {
		var r form14624p4LogRow
		var eventTime string
		if err := rows.Scan(&r.comment, &r.typ, &r.code, &r.elapsed, &r.readRows, &r.readBytes, &r.peak, &r.resultRows,
			&r.settings[0], &r.settings[1], &r.settings[2], &r.settings[3], &r.settings[4], &r.settings[5], &r.settings[6], &r.settings[7], &eventTime); err != nil {
			t.Fatalf("FORM QL scan: %v", err)
		}
		if _, dup := ql[r.comment]; dup {
			t.Fatalf("FORM QL: two rows for %s", r.comment)
		}
		ql[r.comment] = r
	}
	rows.Close()
	if len(ql) != 2 {
		t.Fatalf("FORM QL: %d rows, want 2 (C0, S7): %v", len(ql), ql)
	}
	logged := form14624p4Logged(t, ctx, direct, "FORM", []string{c0ID, s7ID}, []string{c0ID, s7ID})
	var codeID string
	if err := direct.QueryRow(ctx, "SELECT query_id FROM system.query_log WHERE type = 'QueryFinish' AND current_database = currentDatabase() AND log_comment = 'form14624p4_code_s7'").Scan(&codeID); err != nil {
		t.Fatalf("CODE query id: %v", err)
	}
	code := form14624p4Logged(t, ctx, direct, "CODE", []string{codeID}, []string{codeID})[codeID]
	var partsAll uint64
	if err := direct.QueryRow(ctx, "SELECT count() FROM system.parts WHERE active").Scan(&partsAll); err != nil {
		t.Fatal(err)
	}
	c0, q := ql["form14624_probe4_c0"], ql["form14624_probe4_s7"]
	if c0 != logged[c0ID] || q != logged[s7ID] || c0.typ != "QueryFinish" || q.typ != "QueryFinish" {
		t.Errorf("QL = %+v / %+v, the recompute by query id = %+v / %+v", c0, q, logged[c0ID], logged[s7ID])
	}
	wantSettings := [5]string{fmt.Sprint(form14624p4ProjectorMemoryCap), fmt.Sprint(contextpacket.StatementMaxThreads), fmt.Sprint(config.DefaultClickHouseMaxBytesToRead), fmt.Sprint(form14624p4MaxExecutionTime), fmt.Sprint(form14624p4MaxResultRows)}
	for k := range wantSettings {
		// max_threads 4 equal to the server default logs ''. max_execution_time: the code run sends its client deadline + 5 (10..15).
		threadsDefault := k == 1 && q.settings[k] == "" && serverThreads == contextpacket.StatementMaxThreads
		sameAsCode := q.settings[k] == code.settings[k]
		if k == 3 {
			var codeSeconds int
			_, scanErr := fmt.Sscanf(code.settings[k], "%d", &codeSeconds)
			sameAsCode = scanErr == nil && codeSeconds >= 10 && codeSeconds <= 15
		}
		if (q.settings[k] != wantSettings[k] && !threadsDefault) || !sameAsCode {
			t.Errorf("SETTINGS S7 key %d: form %q, code run %q, want %q", k, q.settings[k], code.settings[k], wantSettings[k])
		}
	}
	for k := 5; k < 8; k++ {
		if q.settings[k] != code.settings[k] {
			t.Errorf("SETTINGS S7 overflow key %d: form %q, code run %q", k, q.settings[k], code.settings[k])
		}
	}
	sumRows := sizes["ci_pipeline_runs"][0] + sizes["repos"][0]
	bound := 3*sumRows + 2*partsAll + 1
	var formOwn uint64
	if q.readRows > c0.readRows {
		formOwn = q.readRows - c0.readRows
	}
	if q.readRows < code.readRows || q.readRows > code.readRows+c0.readRows+4*partsAll+2 || q.readRows > bound || (formOwn < sumRows) != (code.readRows < sumRows) {
		t.Errorf("PASS S7: form read_rows %d, C0 %d, form minus C0 %d, code read_rows %d, parts %d, table rows %d, work bound %d", q.readRows, c0.readRows, formOwn, code.readRows, partsAll, sumRows, bound)
	}
	t.Logf("PASS S7 form read_rows %d bytes %d peak %d ms %d | code read_rows %d bytes %d peak %d ms %d | C0 read_rows %d peak %d | table rows %d bound %d settings %v",
		q.readRows, q.readBytes, q.peak, q.elapsed, code.readRows, code.readBytes, code.peak, code.elapsed, c0.readRows, c0.peak, sumRows, bound, q.settings)
	if q.peak >= form14624p4PeakLimit || c0.peak >= form14624p4PeakLimit {
		t.Errorf("PEAK S7 %d / C0 %d >= %d (60 %% of the cap): STOP, the fix is code first", q.peak, c0.peak, form14624p4PeakLimit)
	}

	// 5. THREADS (INFO, the card's scaling question): the same S7 cut at max_threads 2; its peak vs the 4-thread peak on the same data.
	swap := func(text, from, to string) string {
		t.Helper()
		if strings.Count(text, from) == 0 {
			t.Fatalf("mutant literal %q not in the cut", from)
		}
		return strings.ReplaceAll(text, from, to)
	}
	got2, s7at2 := one("S7-threads2", swap(form14624p4CutS7, "max_threads = 4,", "max_threads = 2,"))
	if got2 != codeA {
		t.Errorf("S7 at 2 threads = %d, want %d", got2, codeA)
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	at2 := form14624p4Logged(t, ctx, direct, "S7-threads2", []string{s7at2}, []string{s7at2})[s7at2]
	if at2.settings[1] != "2" {
		t.Errorf("S7-threads2: query_log max_threads %q, want 2", at2.settings[1])
	}
	t.Logf("THREADS S7 peak at 4 = %d, at 2 = %d, ratio %.2f; read_rows at 4 = %d, at 2 = %d (INFO, synthetic org of %d runs)",
		q.peak, at2.peak, float64(q.peak)/float64(at2.peak), q.readRows, at2.readRows, form14624p4RunsA)

	// The QL window holds: a past window (synthetic) reads none of these rows.
	rows, _, err = run("QL-past", form14624p4PastQL)
	if err != nil {
		t.Fatalf("FORM QL past window: %v", err)
	}
	pastRows := 0
	for rows.Next() {
		pastRows++
	}
	rows.Close()
	if pastRows != 0 {
		t.Errorf("MUTANT QL past window: %d rows, want 0", pastRows)
	}

	// 6. MUTANTS. Org: prefix B, no org, every org. S-SET arms. Rule: the read budget and the memory cap reach S7; the raw text needs a
	// bound parameter.
	expectCode := func(label, text string, want int32) string {
		t.Helper()
		rows, id, err := run(label, text)
		if err == nil {
			for rows.Next() {
			}
			err = rows.Err()
			rows.Close()
		}
		if got := form14624p4Code(err); got != want {
			t.Fatalf("MUTANT %s: err %v (code %d), want code %d", label, err, got, want)
		}
		return id
	}
	expectStop := func(label, text, stop string) {
		t.Helper()
		rows, _, err := run(label, text)
		if err == nil {
			for rows.Next() {
			}
			err = rows.Err()
			rows.Close()
		}
		var exception *clickhousedriver.Exception
		if !errors.As(err, &exception) || exception.Code != 395 || !strings.Contains(exception.Message, stop) {
			t.Fatalf("MUTANT %s: err %v, want code 395 %s", label, err, stop)
		}
	}
	prefixA := "'" + form14624p4PrefixA + "'"
	if v, _ := one("ORG1-all", swap(form14624p4CutORG1, prefixA, "''")); v != 2 {
		t.Errorf("MUTANT ORG1 every org: orgs %d, want 2 (STOP)", v)
	}
	if v, _ := one("ORG1-none", swap(form14624p4CutORG1, prefixA, "'ffffffff'")); v != 0 {
		t.Errorf("MUTANT ORG1 no org: orgs %d, want 0 (STOP)", v)
	}
	if v, _ := one("S7-B", swap(form14624p4CutS7, prefixA, "'"+form14624p4PrefixB+"'")); v != codeB {
		t.Errorf("MUTANT S7 org B = %d, the code text on org B = %d", v, codeB)
	}
	for _, text := range []struct{ label, cut string }{{"C0", form14624p4CutC0}, {"S7", form14624p4CutS7}} {
		expectStop(text.label+"-none", swap(text.cut, prefixA, "'ffffffff'"), "STOP-ORG1")
		expectStop(text.label+"-all", swap(text.cut, prefixA, "''"), "STOP-ORG1")
	}
	for _, m := range []struct{ label, from, to, row string }{
		{"S-SET-readonly", "toUInt8(value != '1')", "toUInt8(value != '0')", "readonly"},
		{"S-SET-overflow", "toUInt8(value = 'throw')", "toUInt8(value = 'break')", "read_overflow_mode"},
		{"S-SET-threads", "[toUInt64(201326592), 4,", "[toUInt64(201326592), 1000000,", "max_threads"},
	} {
		if got := sset(m.label, swap(form14624p4CutSSET, m.from, m.to)); got[m.row] != 0 {
			t.Errorf("MUTANT %s: row %s ok = %d, want 0 (STOP)", m.label, m.row, got[m.row])
		}
	}
	refused := map[string]int32{}
	var refusedIDs []string
	id := expectCode("S7-bytes", swap(form14624p4CutS7, "max_bytes_to_read = 268435456", "max_bytes_to_read = 1"), 307)
	refusedIDs, refused[id] = append(refusedIDs, id), 307
	id = expectCode("S7-memory", swap(form14624p4CutS7, "max_memory_usage = 201326592", "max_memory_usage = 1"), 241)
	refusedIDs, refused[id] = append(refusedIDs, id), 241
	expectCode("S7-raw", swap(form14624p4CutS7, " = form_org_id", " = {org_id:String}"), 456)
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	for id, r := range form14624p4Logged(t, ctx, direct, "REFUSED", refusedIDs, nil) {
		if r.code != refused[id] || r.comment != "form14624_probe4_s7" || !strings.HasPrefix(r.typ, "Exception") {
			t.Errorf("REFUSED %s: query_log %+v, want an exception row with code %d under form14624_probe4_s7 (QL reports it)", id, r, refused[id])
		}
	}

	// 7. GATES: each cap literal lowered to 0 fires STOP-COST-GATE (code 395) before any data row; then QL over the fired statements.
	serverSecond := func() string {
		t.Helper()
		time.Sleep(1100 * time.Millisecond)
		var at string
		if err := direct.QueryRow(ctx, "SELECT toString(now(), 'UTC')").Scan(&at); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond)
		return at
	}
	var window uint64
	if err := direct.QueryRow(ctx, "SELECT count() FROM system.query_log WHERE event_date >= toDate('2026-01-01')").Scan(&window); err != nil {
		t.Fatal(err)
	}
	fireStart := serverSecond()
	fires := []struct{ label, text, lit, to string }{
		{"ORG1-rows", form14624p4CutORG1, "> 5000000\n", "> 0\n"}, {"ORG1-bytes", form14624p4CutORG1, "> 5000000000,\n", "> 0,\n"},
		{"C0-rows", form14624p4CutC0, form14624p4RowsCap, "> 0\n"}, {"C0-bytes", form14624p4CutC0, form14624p4BytesCap, "> 0,\n"},
		{"S7-rows", form14624p4CutS7, form14624p4RowsCap, "> 0\n"}, {"S7-bytes", form14624p4CutS7, form14624p4BytesCap, "> 0,\n"},
		{"QL-rows", form14624p4CutQL, "> 200000000\n", "> 0\n"}, {"QL-bytes", form14624p4CutQL, "> 100000000000\n", "> 0\n"}, {"QL-count", form14624p4CutQL, "> 9000000,", "> 0,"},
	}
	fired := map[string]string{}
	var firedIDs []string
	for _, f := range fires {
		if strings.Count(f.text, f.lit) != 1 {
			t.Fatalf("GATE %s: %q not once in the cut", f.label, f.lit)
		}
		rows, id, err := run("gate-"+f.label, strings.Replace(f.text, f.lit, f.to, 1))
		if err == nil {
			for rows.Next() {
			}
			err = rows.Err()
			rows.Close()
		}
		var exception *clickhousedriver.Exception
		if !errors.As(err, &exception) || exception.Code != 395 || !strings.Contains(exception.Message, "STOP-COST-GATE") {
			t.Fatalf("GATE %s lowered to 0: err %v, want code 395 STOP-COST-GATE", f.label, err)
		}
		fired[id] = f.label
		firedIDs = append(firedIDs, id)
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	if err := func() error {
		rows, err := direct.Query(ctx, "SELECT query_id, toString(type), read_rows FROM system.query_log WHERE type != 'QueryStart' AND has(?, query_id)", firedIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := 0
		for rows.Next() {
			var id, typ string
			var read uint64
			if err := rows.Scan(&id, &typ, &read); err != nil {
				return err
			}
			bound := 2*partsAll + 1
			if strings.HasPrefix(fired[id], "QL-count") {
				bound += window
			}
			if typ != "ExceptionBeforeStart" || read > bound {
				return fmt.Errorf("GATE %s: query_log %s read_rows %d, want ExceptionBeforeStart and <= %d", fired[id], typ, read, bound)
			}
			seen++
		}
		if seen != len(firedIDs) {
			return fmt.Errorf("GATE: %d query_log rows for %d fires", seen, len(firedIDs))
		}
		return rows.Err()
	}(); err != nil {
		t.Fatal(err)
	}
	t.Logf("GATE %d caps fired at 0: code 395, ExceptionBeforeStart, read_rows <= 2 x %d parts + 1", len(firedIDs), partsAll)
	qlFrom := func(label, from string) map[string]int {
		t.Helper()
		text := swap(swap(form14624p4CutQL, "toDateTime('2026-01-01 00:00:00', 'UTC')", "toDateTime('"+from+"', 'UTC')"), "toDate('2026-01-01')", "toDate('"+from[:10]+"')")
		rows, _, err := run(label, text)
		if err != nil {
			t.Fatalf("FORM %s: %v", label, err)
		}
		defer rows.Close()
		per := map[string]int{}
		for rows.Next() {
			var r form14624p4LogRow
			var eventTime string
			if err := rows.Scan(&r.comment, &r.typ, &r.code, &r.elapsed, &r.readRows, &r.readBytes, &r.peak, &r.resultRows,
				&r.settings[0], &r.settings[1], &r.settings[2], &r.settings[3], &r.settings[4], &r.settings[5], &r.settings[6], &r.settings[7], &eventTime); err != nil {
				t.Fatalf("FORM %s scan: %v", label, err)
			}
			if r.typ != "ExceptionBeforeStart" || r.code != 395 {
				t.Errorf("%s %s: type %s code %d, want ExceptionBeforeStart 395", label, r.comment, r.typ, r.code)
			}
			per[r.comment]++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("FORM %s rows: %v", label, err)
		}
		return per
	}
	fireEnd := serverSecond()
	if per := qlFrom("QL-fired", fireStart); len(per) != 2 || per["form14624_probe4_c0"] != 2 || per["form14624_probe4_s7"] != 2 {
		t.Errorf("QL-fired: %v, want 2 rows each for form14624_probe4_c0 and form14624_probe4_s7", per)
	}
	if after := qlFrom("QL-after", fireEnd); len(after) != 0 {
		t.Errorf("QL with T_R after the fires: %v, want 0 rows", after)
	}
	t.Logf("QL-fired 4 rows (2 per C0 / S7), ExceptionBeforeStart 395; QL from %s (after the fires) 0 rows", fireEnd)
}
