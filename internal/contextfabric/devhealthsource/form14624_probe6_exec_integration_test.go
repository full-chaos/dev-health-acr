package devhealthsource_test

// DO NOT MERGE. Throwaway exec leg of a prod read form (FORM-14624-PROBE-6: the ci_pipeline_run total count statement (S7) of the
// marker-absent probe, replica-safe: the replica count is variable). The cuts are the form's run-block texts with the org prefix and the
// window swapped to synthetic values; each swapped cut's sha256 is pinned. Synthetic rows only (two fake orgs). Every function here that
// reads memory_usage from system.query_log calls statementthreads.RequireThreadBound (the statementthreads census).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/startterm"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const form14624p6CutstmtR = `SELECT count() AS replicas, toString(version()) AS server_version, toUInt64OrZero(extract((SELECT value FROM system.settings WHERE name = 'max_threads'), '[0-9]+')) AS server_max_threads FROM clusterAllReplicas('default', system.one);
`
const form14624p6CutstmtRSHA256 = "303a281ba7fde24dc8850cd1be6a3fe29f44e6acf878b95841a037d918c8cec2"

const form14624p6CutSSET = `WITH (SELECT throwIf((SELECT count() FROM clusterAllReplicas('default', system.one)) > 16, 'STOP-COST-GATE')) AS cost_gate
SELECT hostName() AS replica, toString(version()) AS server_version, name, value, changed, transform(name, ['max_memory_usage', 'max_threads', 'max_bytes_to_read', 'max_execution_time', 'max_result_rows'], [toUInt64(201326592), 2, 268435456, 10, 1000], toUInt64(0)) AS form_value,
  multiIf(name = 'readonly', toUInt8(value != '1'), name IN ('read_overflow_mode', 'timeout_overflow_mode', 'result_overflow_mode'), toUInt8(value = 'throw'), name IN ('enable_analyzer', 'max_parallel_replicas', 'enable_parallel_replicas'), toUInt8(1),
    toUInt8(toUInt64OrZero(extract(value, '[0-9]+')) = 0 OR toUInt64OrZero(extract(value, '[0-9]+')) >= form_value)) AS ok
FROM clusterAllReplicas('default', system.settings) WHERE name IN ('readonly', 'max_memory_usage', 'max_threads', 'max_bytes_to_read', 'max_execution_time', 'max_result_rows', 'read_overflow_mode', 'timeout_overflow_mode', 'result_overflow_mode', 'enable_analyzer', 'max_parallel_replicas', 'enable_parallel_replicas') AND cost_gate = 0
ORDER BY replica, name;
`
const form14624p6CutSSETSHA256 = "682721669aa8e88da74a347c32f85471af9fac6cd3bdb3eeb9471ea3ac611326"

const form14624p6CutSSRV = `WITH (SELECT throwIf((SELECT count() FROM clusterAllReplicas('default', system.one)) > 16, 'STOP-COST-GATE')) AS cost_gate
SELECT hostName() AS replica, name, value FROM clusterAllReplicas('default', system.server_settings)
WHERE name IN ('max_server_memory_usage', 'max_server_memory_usage_to_ram_ratio') AND cost_gate = 0 ORDER BY replica, name;
`
const form14624p6CutSSRVSHA256 = "a58b7759143614df5e943ca01636627446367c3e0373f8a9d25ee9922e9ea9ff"

const form14624p6CutS0 = `SELECT 'local' AS scope, '' AS replica, table, sum(rows) AS rows, sum(bytes_on_disk) AS bytes, count() AS active_parts
FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos') GROUP BY table
UNION ALL
SELECT 'query_log' AS scope, hostName() AS replica, table, sum(rows) AS rows, sum(bytes_on_disk) AS bytes, count() AS active_parts
FROM clusterAllReplicas('default', system.parts) WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01') GROUP BY replica, table
ORDER BY scope, replica, table;
`
const form14624p6CutS0SHA256 = "011bfe7e89f10a0c1a7993a1b49437d6a4a400b5009afeb0a6afe514720039c8"

const form14624p6CutORG1 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table = 'repos') > 5000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table = 'repos') > 5000000000,
  'STOP-COST-GATE')) AS cost_gate
SELECT uniqExact(org_id) AS orgs FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0;
`
const form14624p6CutORG1SHA256 = "47766c9e6de9ca0dcd89c893ba276ce8c31627d2dca61a6b2fabf8265bd30992"

const form14624p6CutC0 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT length(form_org_id) AS org_id_length
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 1, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe6_c0';
`
const form14624p6CutC0SHA256 = "85ebc8187a9b644677c3b8e3517a9d1c6373192bcc2f088f3498c24c8cbcc609"

const form14624p6CutS7T4 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(c.repo_id, c.run_id) FROM ci_pipeline_runs AS c FINAL
INNER JOIN repos AS repo FINAL ON repo.id = c.repo_id AND repo.org_id = c.org_id
WHERE c.org_id = form_org_id AND length(concat('ci_pipeline_run.v2:', replaceAll(replaceAll(toString(c.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(c.run_id, '%', '%25'), ':', '%3A'))) <= 256
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 4, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe6_s7';
`
const form14624p6CutS7T4SHA256 = "712d1d298a90905f7b093c9330d47e3e980280a02f0c7b2b10eb8d7108117edf"

const form14624p6CutS7T3 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(c.repo_id, c.run_id) FROM ci_pipeline_runs AS c FINAL
INNER JOIN repos AS repo FINAL ON repo.id = c.repo_id AND repo.org_id = c.org_id
WHERE c.org_id = form_org_id AND length(concat('ci_pipeline_run.v2:', replaceAll(replaceAll(toString(c.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(c.run_id, '%', '%25'), ':', '%3A'))) <= 256
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 3, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe6_s7';
`
const form14624p6CutS7T3SHA256 = "683f982874c1795dc32a69b9cf05b9dcc4cb5d047f9ddf32e3db250e1f81c25e"

const form14624p6CutS7T2 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(c.repo_id, c.run_id) FROM ci_pipeline_runs AS c FINAL
INNER JOIN repos AS repo FINAL ON repo.id = c.repo_id AND repo.org_id = c.org_id
WHERE c.org_id = form_org_id AND length(concat('ci_pipeline_run.v2:', replaceAll(replaceAll(toString(c.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(c.run_id, '%', '%25'), ':', '%3A'))) <= 256
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe6_s7';
`
const form14624p6CutS7T2SHA256 = "e0e50d7d5712f4d6a2f2cb8b9e8d5204cb294839fe8a10b463f452fd596f3852"

const form14624p6CutQL = `WITH (SELECT throwIf(
  (SELECT count() FROM clusterAllReplicas('default', system.one)) > 16
  OR (SELECT sum(rows) FROM clusterAllReplicas('default', system.parts) WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01')) > 200000000
  OR (SELECT sum(bytes_on_disk) FROM clusterAllReplicas('default', system.parts) WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01')) > 100000000000
  OR (SELECT count() FROM clusterAllReplicas('default', system.query_log) WHERE event_date >= toDate('2025-01-01') AND event_time >= toDateTime('2025-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2099-01-01 00:00:00', 'UTC')) > 9000000,
  'STOP-COST-GATE')) AS cost_gate
SELECT hostName() AS replica, toUInt8(is_initial_query) AS initial, log_comment AS stmt, toString(type) AS type, exception_code AS code, query_duration_ms AS elapsed_ms, read_rows, read_bytes, memory_usage AS peak_memory, result_rows,
  Settings['max_memory_usage'] AS set_max_memory_usage, Settings['max_threads'] AS set_max_threads, Settings['max_bytes_to_read'] AS set_max_bytes_to_read,
  Settings['max_execution_time'] AS set_max_execution_time, Settings['max_result_rows'] AS set_max_result_rows, Settings['read_overflow_mode'] AS set_read_overflow_mode,
  Settings['timeout_overflow_mode'] AS set_timeout_overflow_mode, Settings['result_overflow_mode'] AS set_result_overflow_mode, toString(event_time, 'UTC') AS event_time_utc
FROM clusterAllReplicas('default', system.query_log)
WHERE event_date >= toDate('2025-01-01') AND event_time >= toDateTime('2025-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2099-01-01 00:00:00', 'UTC') AND type != 'QueryStart' AND query_kind = 'Select'
  AND log_comment IN ('form14624_probe6_c0', 'form14624_probe6_s7')
  AND cost_gate = 0
ORDER BY event_time_utc, stmt, replica
LIMIT 200;
`
const form14624p6CutQLSHA256 = "fe4c7c947b428fe1bde47537ea3a9200111c9ddd24a51d3f645deab2329d8dd4"

const form14624p6CutQLR = `WITH (SELECT throwIf(
  (SELECT count() FROM clusterAllReplicas('default', system.one)) > 16
  OR (SELECT sum(rows) FROM clusterAllReplicas('default', system.parts) WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01')) > 200000000
  OR (SELECT sum(bytes_on_disk) FROM clusterAllReplicas('default', system.parts) WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01')) > 100000000000
  OR (SELECT count() FROM clusterAllReplicas('default', system.query_log) WHERE event_date >= toDate('2025-01-01') AND event_time >= toDateTime('2025-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2099-01-01 00:00:00', 'UTC')) > 9000000,
  'STOP-COST-GATE')) AS cost_gate
SELECT hostName() AS replica, count() AS window_rows
FROM clusterAllReplicas('default', system.query_log)
WHERE event_date >= toDate('2025-01-01') AND event_time >= toDateTime('2025-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2099-01-01 00:00:00', 'UTC') AND cost_gate = 0
GROUP BY replica ORDER BY replica;
`
const form14624p6CutQLRSHA256 = "b96deb70a82d85a7e095268023cbea8dcb4b61ba64d830969dcb1e658c7d20b6"

const form14624p6PastQL = `WITH (SELECT throwIf(
  (SELECT count() FROM clusterAllReplicas('default', system.one)) > 16
  OR (SELECT sum(rows) FROM clusterAllReplicas('default', system.parts) WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2024-01-01')) > 200000000
  OR (SELECT sum(bytes_on_disk) FROM clusterAllReplicas('default', system.parts) WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2024-01-01')) > 100000000000
  OR (SELECT count() FROM clusterAllReplicas('default', system.query_log) WHERE event_date >= toDate('2024-01-01') AND event_time >= toDateTime('2024-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2024-01-02 00:00:00', 'UTC')) > 9000000,
  'STOP-COST-GATE')) AS cost_gate
SELECT hostName() AS replica, toUInt8(is_initial_query) AS initial, log_comment AS stmt, toString(type) AS type, exception_code AS code, query_duration_ms AS elapsed_ms, read_rows, read_bytes, memory_usage AS peak_memory, result_rows,
  Settings['max_memory_usage'] AS set_max_memory_usage, Settings['max_threads'] AS set_max_threads, Settings['max_bytes_to_read'] AS set_max_bytes_to_read,
  Settings['max_execution_time'] AS set_max_execution_time, Settings['max_result_rows'] AS set_max_result_rows, Settings['read_overflow_mode'] AS set_read_overflow_mode,
  Settings['timeout_overflow_mode'] AS set_timeout_overflow_mode, Settings['result_overflow_mode'] AS set_result_overflow_mode, toString(event_time, 'UTC') AS event_time_utc
FROM clusterAllReplicas('default', system.query_log)
WHERE event_date >= toDate('2024-01-01') AND event_time >= toDateTime('2024-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2024-01-02 00:00:00', 'UTC') AND type != 'QueryStart' AND query_kind = 'Select'
  AND log_comment IN ('form14624_probe6_c0', 'form14624_probe6_s7')
  AND cost_gate = 0
ORDER BY event_time_utc, stmt, replica
LIMIT 200;
`
const form14624p6PastQLSHA256 = "3632b5f1597dd843a5641d52f182541ae8f79eda37822733b36e1f3ec39d47f8"

// form14624p6CodeS7SHA256 pins the S7 code text the form carries (acr 49c36439, source_counts.go:79-81).
const form14624p6CodeS7SHA256 = "70f782872fd4dfb7581b67a98b926217003f171199706e8a67320bc1c7d54e71"

const (
	form14624p6PrefixA = "f0f0f0f0"
	form14624p6OrgA    = "f0f0f0f0-0000-4000-8000-00000000000a"
	form14624p6PrefixB = "e1e1e1e1"
	form14624p6OrgB    = "e1e1e1e1-0000-4000-8000-00000000000b"
	// form14624p6ProjectorMemoryCap is cmd/acr-projector projectorStatementMaxMemoryUsage (runtime.go:407; package main, not importable).
	form14624p6ProjectorMemoryCap = 192 << 20
	form14624p6PeakLimit          = form14624p6ProjectorMemoryCap * 60 / 100
	form14624p6RowsCap            = "> 6000000\n"
	form14624p6BytesCap           = "> 8589934592,\n"
	form14624p6RunsA              = 250000
	form14624p6SeedTime           = "toDateTime64('2025-03-03 00:00:00', 3, 'UTC')"
)

// form14624p6Variants maps k to its S7 cut.
var form14624p6Variants = map[uint64]string{4: form14624p6CutS7T4, 3: form14624p6CutS7T3, 2: form14624p6CutS7T2}

func form14624p6Settings(threads int, comment string) string {
	return fmt.Sprintf("SETTINGS max_memory_usage = %d, max_threads = %d, max_bytes_to_read = %d, max_execution_time = 10, max_result_rows = 1000, log_comment = '%s';",
		form14624p6ProjectorMemoryCap, threads, config.DefaultClickHouseMaxBytesToRead, comment)
}

func TestForm14624Probe6CutsAreTheCodeTexts(t *testing.T) {
	for _, c := range []struct{ label, text, want string }{
		{"stmtR", form14624p6CutstmtR, form14624p6CutstmtRSHA256}, {"S-SET", form14624p6CutSSET, form14624p6CutSSETSHA256}, {"S-SRV", form14624p6CutSSRV, form14624p6CutSSRVSHA256},
		{"S0", form14624p6CutS0, form14624p6CutS0SHA256}, {"ORG1", form14624p6CutORG1, form14624p6CutORG1SHA256}, {"C0", form14624p6CutC0, form14624p6CutC0SHA256},
		{"S7T4", form14624p6CutS7T4, form14624p6CutS7T4SHA256}, {"S7T3", form14624p6CutS7T3, form14624p6CutS7T3SHA256}, {"S7T2", form14624p6CutS7T2, form14624p6CutS7T2SHA256},
		{"QL", form14624p6CutQL, form14624p6CutQLSHA256}, {"QLR", form14624p6CutQLR, form14624p6CutQLRSHA256}, {"past QL", form14624p6PastQL, form14624p6PastQLSHA256},
	} {
		sum := sha256.Sum256([]byte(c.text))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Fatalf("cut %s sha256 = %s, want %s", c.label, got, c.want)
		}
		t.Logf("CUT %s sha12 %s lines %d", c.label, c.want[:12], strings.Count(c.text, "\n"))
	}
	statements := devhealthsource.Form14624ProbeStatementsForTest()
	if len(statements) != 9 || statements[6].Kind != "ci_pipeline_run" {
		t.Fatalf("the probe statements moved: %d", len(statements))
	}
	sum := sha256.Sum256([]byte(statements[6].SQL))
	if got := hex.EncodeToString(sum[:]); got != form14624p6CodeS7SHA256 {
		t.Fatalf("S7 code text sha256 %s, the form carries %s: the code moved, the form is stale", got, form14624p6CodeS7SHA256)
	}
	prefixRule := "(SELECT any(org_id) FROM repos WHERE startsWith(org_id, '" + form14624p6PrefixA + "') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id\n"
	inner := strings.ReplaceAll(statements[6].SQL, "{org_id:String}", "form_org_id")
	for k, cut := range form14624p6Variants {
		body := "SELECT * FROM (\n" + inner + "\n) WHERE cost_gate = 0\n" + form14624p6Settings(int(k), "form14624_probe6_s7") + "\n"
		if !strings.HasSuffix(cut, body) {
			t.Fatalf("S7T%d: not the gate + the code text (token form_org_id) + SETTINGS at max_threads %d", k, k)
		}
		head := strings.TrimSuffix(cut, body)
		for lit, n := range map[string]int{form14624p6RowsCap: 1, form14624p6BytesCap: 1, "'STOP-COST-GATE'": 1, prefixRule: 1, "table IN ('ci_pipeline_runs', 'repos')": 2, "{org_id": 0} {
			if got := strings.Count(head, lit); got != n {
				t.Fatalf("S7T%d gate: %q found %d times, want %d", k, lit, got, n)
			}
		}
		if k != 4 && strings.Replace(cut, fmt.Sprintf("max_threads = %d,", k), "max_threads = 4,", 1) != form14624p6CutS7T4 {
			t.Fatalf("S7T%d differs from S7T4 by more than max_threads", k)
		}
	}
	c0 := "SELECT * FROM (\nSELECT length(form_org_id) AS org_id_length\n) WHERE cost_gate = 0\n" + form14624p6Settings(1, "form14624_probe6_c0") + "\n"
	if !strings.HasSuffix(form14624p6CutC0, c0) || !strings.Contains(form14624p6CutC0, prefixRule) {
		t.Fatalf("C0 is not the wrapper around length(form_org_id) at max_threads 1")
	}
	for label, text := range map[string]string{"S-SET": form14624p6CutSSET, "S-SRV": form14624p6CutSSRV, "QL": form14624p6CutQL, "QLR": form14624p6CutQLR} {
		if !strings.Contains(text, "FROM clusterAllReplicas('default', system.") || !strings.Contains(text, "(SELECT count() FROM clusterAllReplicas('default', system.one)) > 16") || !strings.Contains(text, "cost_gate = 0") {
			t.Fatalf("%s is not a gated clusterAllReplicas read", label)
		}
	}
	if strings.Contains(form14624p6CutQL, "FROM system.query_log") || strings.Contains(form14624p6CutQLR, "FROM system.query_log") {
		t.Fatalf("QL / QLR read a local query_log")
	}
	t.Logf("TEXT S7 code sha12 %s; variants k = 4, 3, 2; C0 at max_threads 1; S-SET / S-SRV / QL / QLR over clusterAllReplicas", form14624p6CodeS7SHA256[:12])
}

type form14624p6LogRow struct {
	replica, comment, typ                          string
	initial                                        uint8
	code                                           int32
	elapsed, readRows, readBytes, peak, resultRows uint64
	settings                                       [8]string
}

// form14624p6QL runs a QL-shaped cut and returns its rows (no memory_usage literal here: the cut holds it, and the census reads
// function literals; the query_log peak readers below check threads).
func form14624p6QL(t *testing.T, label string, run func(string, string) (chdriver.Rows, string, error), text string) []form14624p6LogRow {
	t.Helper()
	rows, _, err := run(label, text)
	if err != nil {
		t.Fatalf("FORM %s: %v", label, err)
	}
	defer rows.Close()
	var out []form14624p6LogRow
	for rows.Next() {
		var r form14624p6LogRow
		var eventTime string
		if err := rows.Scan(&r.replica, &r.initial, &r.comment, &r.typ, &r.code, &r.elapsed, &r.readRows, &r.readBytes, &r.peak, &r.resultRows,
			&r.settings[0], &r.settings[1], &r.settings[2], &r.settings[3], &r.settings[4], &r.settings[5], &r.settings[6], &r.settings[7], &eventTime); err != nil {
			t.Fatalf("FORM %s scan: %v", label, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("FORM %s rows: %v", label, err)
	}
	return out
}

// form14624p6Peak reads one statement's query_log row by query id on one server (FuncDecl: it reads memory_usage from system.query_log,
// so it checks the statement threads of the row, the statementthreads census rule).
func form14624p6Peak(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, label, id string) (peak, readRows uint64, threads string) {
	t.Helper()
	var unbound uint64
	if err := direct.QueryRow(ctx, "SELECT "+statementthreads.UnboundThreadsCount+" FROM system.query_log WHERE type = 'QueryFinish' AND query_id = ?", id).Scan(&unbound); err != nil {
		t.Fatalf("%s: thread check: %v", label, err)
	}
	statementthreads.RequireThreadBound(t, ctx, direct, label, unbound)
	if err := direct.QueryRow(ctx, "SELECT memory_usage, read_rows, Settings['max_threads'] FROM system.query_log WHERE type = 'QueryFinish' AND query_id = ?", id).Scan(&peak, &readRows, &threads); err != nil {
		t.Fatalf("%s: read query_log: %v", label, err)
	}
	return peak, readRows, threads
}

// form14624p6SSet runs an S-SET-shaped cut: per replica, the base rows and ok; it returns ok per replica/name and k.
func form14624p6SSet(t *testing.T, label string, run func(string, string) (chdriver.Rows, string, error), text string) (map[string]uint8, uint64, map[string]bool) {
	t.Helper()
	rows, _, err := run(label, text)
	if err != nil {
		t.Fatalf("FORM %s: %v", label, err)
	}
	defer rows.Close()
	ok := map[string]uint8{}
	replicas := map[string]bool{}
	k := uint64(4)
	for rows.Next() {
		var replica, version, name, value string
		var changed, okv uint8
		var formValue uint64
		if err := rows.Scan(&replica, &version, &name, &value, &changed, &formValue, &okv); err != nil {
			t.Fatalf("FORM %s scan: %v", label, err)
		}
		replicas[replica] = true
		ok[replica+"/"+name] = okv
		if name == "max_threads" {
			digits := strings.TrimSuffix(strings.TrimPrefix(value, "auto("), ")")
			if live, err := strconv.ParseUint(digits, 10, 64); err == nil && live > 0 && live < k {
				k = live
			}
		}
		t.Logf("%s %s %s %s = %q changed %d form_value %d ok %d", label, replica, version, name, value, changed, formValue, okv)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("FORM %s rows: %v", label, err)
	}
	return ok, k, replicas
}

func form14624p6Code(err error) (int32, string) {
	var exception *clickhousedriver.Exception
	if errors.As(err, &exception) {
		return exception.Code, exception.Message
	}
	return -1, ""
}

// form14624p6Seed creates the two read tables in the connection's database and the two synthetic orgs.
func form14624p6Seed(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) {
	t.Helper()
	exec := func(statement string) {
		t.Helper()
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	// devhealthschema:not-a-production-replica the table names here select which tables devhealthschema.DDL renders; no schema is defined in this file
	for _, statement := range devhealthschema.DDL("repos", "ci_pipeline_runs") {
		exec(statement)
	}
	seed := func(prefix, org string, repos, runs int) {
		repo := func(expr string) string {
			return fmt.Sprintf("toUUID(concat('%s', '-0000-4000-8000-', leftPad(toString(%s), 12, '0')))", prefix, expr)
		}
		// devhealthschema:not-a-production-replica the table names here fill tables that devhealthschema.DDL rendered above; no schema is defined in this file
		exec(fmt.Sprintf("INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT %s, '%s', concat('acme/r', toString(number)), 'github', %s FROM numbers(%d)", repo("number"), org, form14624p6SeedTime, repos))
		exec(fmt.Sprintf("INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) SELECT %s, if(number = 0, concat('R-', repeat('X', 300)), concat('R-', toString(number))), 'success', %s, %s, '%s' FROM numbers(%d)", repo(fmt.Sprintf("number %% %d", repos)), form14624p6SeedTime, form14624p6SeedTime, org, runs))
	}
	seed(form14624p6PrefixA, form14624p6OrgA, 3, form14624p6RunsA)
	seed(form14624p6PrefixB, form14624p6OrgB, 20000, 100000)
	exec("OPTIMIZE TABLE repos FINAL")
	exec("OPTIMIZE TABLE ci_pipeline_runs FINAL")
}

// form14624p6Runner returns a run function that sends each cut through a plain connection (pm's connector sends no settings of its own).
func form14624p6Runner(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, tag string) func(string, string) (chdriver.Rows, string, error) {
	n := 0
	runID := time.Now().UnixNano()
	return func(label, text string) (chdriver.Rows, string, error) {
		t.Helper()
		n++
		id := fmt.Sprintf("form14624p6-%s-%s-%d-%d", tag, label, runID, n)
		rows, err := direct.Query(clickhousedriver.Context(ctx, clickhousedriver.WithQueryID(id)), strings.TrimSuffix(strings.TrimRight(text, "\n"), ";"))
		return rows, id, err
	}
}

func form14624p6One(t *testing.T, label string, run func(string, string) (chdriver.Rows, string, error), text string) (uint64, string) {
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

func form14624p6Error(label string, run func(string, string) (chdriver.Rows, string, error), text string) (int32, string) {
	rows, _, err := run(label, text)
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
	}
	return form14624p6Code(err)
}

func form14624p6Swap(t *testing.T, text, from, to string) string {
	t.Helper()
	if strings.Count(text, from) == 0 {
		t.Fatalf("mutant literal %q not in the cut", from)
	}
	return strings.ReplaceAll(text, from, to)
}

// TestForm14624Probe6ExecutesOnOneReplica runs every statement of the form on the shared ClickHouse (cluster 'default' = 1 replica).
func TestForm14624Probe6ExecutesOnOneReplica(t *testing.T) {
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
	maxBytes := config.DefaultClickHouseMaxBytesToRead
	raw, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxBytesToRead: &maxBytes,
	})
	if err != nil {
		t.Fatalf("open the projector query client: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	projector := statementthreads.Capped(raw, form14624p6ProjectorMemoryCap)
	form14624p6Seed(t, ctx, direct)

	st := devhealthsource.Form14624ProbeStatementsForTest()[6]
	codeCount := func(org, reader string) uint64 {
		t.Helper()
		rows, err := projector.Query(contextpacket.WithStatementReader(ctx, reader), st.SQL, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: org}})
		if err != nil {
			t.Fatalf("CODE %s: %v", reader, err)
		}
		defer rows.Close()
		var total uint64
		for rows.Next() {
			if err := rows.Scan(&total); err != nil {
				t.Fatalf("CODE %s scan: %v", reader, err)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("CODE %s: %v", reader, err)
		}
		return total
	}
	codeA, codeB := codeCount(form14624p6OrgA, "form14624p6_code_s7"), codeCount(form14624p6OrgB, "form14624p6_code_b_s7")
	if codeA != form14624p6RunsA-1 || codeB == 0 || codeB == codeA {
		t.Fatalf("CODE S7: org A %d (want %d), org B %d", codeA, form14624p6RunsA-1, codeB)
	}

	run := form14624p6Runner(t, ctx, direct, "n1")
	var replicas, serverThreads uint64
	var version string
	if err := direct.QueryRow(ctx, strings.TrimSuffix(strings.TrimRight(form14624p6CutstmtR, "\n"), ";")).Scan(&replicas, &version, &serverThreads); err != nil || replicas != 1 {
		t.Fatalf("FORM stmtR: replicas %d err %v, want 1 (one server)", replicas, err)
	}
	ok, k, replicaSet := form14624p6SSet(t, "S-SET", run, form14624p6CutSSET)
	if len(replicaSet) != 1 {
		t.Fatalf("FORM S-SET: %d replicas, want 1", len(replicaSet))
	}
	base := 0
	for key, v := range ok {
		name := key[strings.Index(key, "/")+1:]
		if name == "max_parallel_replicas" || name == "enable_parallel_replicas" {
			continue
		}
		base++
		if v != 1 {
			t.Errorf("FORM S-SET %s ok 0 on the test server", key)
		}
	}
	if base != 10 {
		t.Fatalf("FORM S-SET: %d base rows, want 10", base)
	}
	t.Logf("FORM S-SET k = min(4, live max_threads) = %d (server %s)", k, version)
	rows, _, err := run("S-SRV", form14624p6CutSSRV)
	if err != nil {
		t.Logf("FORM S-SRV: %v (INFO: the card says an S-SRV error is INFO)", err)
	} else {
		for rows.Next() {
			var replica, name, value string
			if err := rows.Scan(&replica, &name, &value); err != nil {
				t.Fatalf("FORM S-SRV scan: %v", err)
			}
			t.Logf("FORM S-SRV %s %s = %s", replica, name, value)
		}
		rows.Close()
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	rows, _, err = run("S0", form14624p6CutS0)
	if err != nil {
		t.Fatalf("FORM S0: %v", err)
	}
	s0 := map[string]uint64{}
	for rows.Next() {
		var scope, replica, table string
		var r, b, p uint64
		if err := rows.Scan(&scope, &replica, &table, &r, &b, &p); err != nil {
			t.Fatalf("FORM S0 scan: %v", err)
		}
		s0[scope+"/"+table] += r
		t.Logf("FORM S0 %s %s %s rows %d bytes %d parts %d", scope, replica, table, r, b, p)
	}
	rows.Close()
	if len(s0) != 3 || s0["local/ci_pipeline_runs"] == 0 || s0["query_log/query_log"] == 0 {
		t.Fatalf("FORM S0: %v, want local ci_pipeline_runs, local repos and query_log", s0)
	}
	if v, _ := form14624p6One(t, "ORG1", run, form14624p6CutORG1); v != 1 {
		t.Fatalf("FORM ORG1 orgs %d, want 1", v)
	}
	if v, _ := form14624p6One(t, "C0", run, form14624p6CutC0); v != uint64(len(form14624p6OrgA)) {
		t.Fatalf("FORM C0 org_id_length %d", v)
	}
	cut, has := form14624p6Variants[k]
	if !has {
		t.Fatalf("k = %d has no S7 variant (live max_threads < 2 = STOP)", k)
	}
	got, s7ID := form14624p6One(t, fmt.Sprintf("S7T%d", k), run, cut)
	if got != codeA {
		t.Fatalf("FORM S7T%d = %d, the code text on org A = %d", k, got, codeA)
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	ql := form14624p6QL(t, "QL", run, form14624p6CutQL)
	per := map[string]int{}
	var s7 form14624p6LogRow
	for _, r := range ql {
		per[r.comment]++
		if r.comment == "form14624_probe6_s7" {
			s7 = r
		}
	}
	if len(ql) != 2 || per["form14624_probe6_c0"] != 1 || per["form14624_probe6_s7"] != 1 || s7.typ != "QueryFinish" || s7.initial != 1 {
		t.Fatalf("FORM QL: %v (rows %d), want exactly 1 C0 and 1 S7 row, QueryFinish, initial", per, len(ql))
	}
	peak, readRows, threads := form14624p6Peak(t, ctx, direct, "S7", s7ID)
	if s7.peak != peak || s7.readRows != readRows || s7.settings[1] != threads || threads != strconv.FormatUint(k, 10) {
		t.Errorf("QL S7 peak %d read_rows %d threads %q, the row by query id %d / %d / %q, want threads %d", s7.peak, s7.readRows, s7.settings[1], peak, readRows, threads, k)
	}
	if s7.peak >= form14624p6PeakLimit {
		t.Errorf("PEAK S7 %d >= %d (60 %% of the cap): STOP, the fix is code first", s7.peak, form14624p6PeakLimit)
	}
	t.Logf("QL S7 on replica %s: k %d, read_rows %d, bytes %d, peak %d, ms %d, settings %v", s7.replica, k, s7.readRows, s7.readBytes, s7.peak, s7.elapsed, s7.settings)
	rows, _, err = run("QLR", form14624p6CutQLR)
	if err != nil {
		t.Fatalf("FORM QLR: %v", err)
	}
	qlr := map[string]uint64{}
	for rows.Next() {
		var replica string
		var n uint64
		if err := rows.Scan(&replica, &n); err != nil {
			t.Fatalf("FORM QLR scan: %v", err)
		}
		qlr[replica] = n
	}
	rows.Close()
	if len(qlr) != 1 || qlr[s7.replica] == 0 {
		t.Fatalf("FORM QLR: %v, want 1 replica (= stmtR) with rows >= 1", qlr)
	}
	if past := form14624p6QL(t, "QL-past", run, form14624p6PastQL); len(past) != 0 {
		t.Errorf("MUTANT QL past window: %d rows, want 0", len(past))
	}

	// MUTANTS. The other S7 variants (INFO: peak per thread count, same data), org, S-SET arms, rule codes.
	for _, kk := range []uint64{4, 3, 2} {
		if kk == k {
			continue
		}
		v, id := form14624p6One(t, fmt.Sprintf("S7T%d-info", kk), run, form14624p6Variants[kk])
		if v != codeA {
			t.Errorf("S7T%d = %d, want %d", kk, v, codeA)
		}
		if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
			t.Fatal(err)
		}
		p, rr, th := form14624p6Peak(t, ctx, direct, fmt.Sprintf("S7T%d", kk), id)
		t.Logf("THREADS S7T%d peak %d read_rows %d query_log max_threads %q (INFO, synthetic org of %d runs)", kk, p, rr, th, form14624p6RunsA)
	}
	prefixA := "'" + form14624p6PrefixA + "'"
	if v, _ := form14624p6One(t, "S7-B", run, form14624p6Swap(t, cut, prefixA, "'"+form14624p6PrefixB+"'")); v != codeB {
		t.Errorf("MUTANT S7 org B = %d, the code text on org B = %d", v, codeB)
	}
	for label, text := range map[string]string{"C0": form14624p6CutC0, "S7": cut} {
		for _, to := range []string{"'ffffffff'", "''"} {
			if code, msg := form14624p6Error(label+"-org", run, form14624p6Swap(t, text, prefixA, to)); code != 395 || !strings.Contains(msg, "STOP-ORG1") {
				t.Fatalf("MUTANT %s prefix %s: code %d %q, want 395 STOP-ORG1", label, to, code, msg)
			}
		}
	}
	for _, m := range []struct{ label, from, to, name string }{
		{"S-SET-readonly", "toUInt8(value != '1')", "toUInt8(value != '0')", "readonly"},
		{"S-SET-overflow", "toUInt8(value = 'throw')", "toUInt8(value = 'break')", "read_overflow_mode"},
		{"S-SET-threads", "[toUInt64(201326592), 2,", "[toUInt64(201326592), 1000000,", "max_threads"},
	} {
		got, _, _ := form14624p6SSet(t, m.label, run, form14624p6Swap(t, form14624p6CutSSET, m.from, m.to))
		for key, v := range got {
			if strings.HasSuffix(key, "/"+m.name) && v != 0 {
				t.Errorf("MUTANT %s: %s ok = %d, want 0 (STOP)", m.label, key, v)
			}
		}
	}
	if code, _ := form14624p6Error("S7-bytes", run, form14624p6Swap(t, cut, "max_bytes_to_read = 268435456", "max_bytes_to_read = 1")); code != 307 {
		t.Errorf("MUTANT S7 max_bytes_to_read 1: code %d, want 307", code)
	}
	if code, _ := form14624p6Error("S7-memory", run, form14624p6Swap(t, cut, "max_memory_usage = 201326592", "max_memory_usage = 1")); code != 241 {
		t.Errorf("MUTANT S7 max_memory_usage 1: code %d, want 241", code)
	}
	if code, _ := form14624p6Error("S7-raw", run, form14624p6Swap(t, cut, " = form_org_id", " = {org_id:String}")); code != 456 {
		t.Errorf("MUTANT S7 raw text: code %d, want 456", code)
	}

	// GATES: each cap literal at 0 fires STOP-COST-GATE (395) before any data row.
	fires := []struct{ label, text, lit, to string }{
		{"S-SET-replicas", form14624p6CutSSET, "system.one)) > 16,", "system.one)) > 0,"},
		{"S-SRV-replicas", form14624p6CutSSRV, "system.one)) > 16,", "system.one)) > 0,"},
		{"ORG1-rows", form14624p6CutORG1, "> 5000000\n", "> 0\n"}, {"ORG1-bytes", form14624p6CutORG1, "> 5000000000,\n", "> 0,\n"},
		{"C0-rows", form14624p6CutC0, form14624p6RowsCap, "> 0\n"}, {"C0-bytes", form14624p6CutC0, form14624p6BytesCap, "> 0,\n"},
		{"QL-replicas", form14624p6CutQL, "system.one)) > 16\n", "system.one)) > 0\n"}, {"QL-rows", form14624p6CutQL, "> 200000000\n", "> 0\n"},
		{"QL-bytes", form14624p6CutQL, "> 100000000000\n", "> 0\n"}, {"QL-count", form14624p6CutQL, "> 9000000,", "> 0,"},
		{"QLR-replicas", form14624p6CutQLR, "system.one)) > 16\n", "system.one)) > 0\n"}, {"QLR-count", form14624p6CutQLR, "> 9000000,", "> 0,"},
	}
	for kk, text := range form14624p6Variants {
		fires = append(fires, struct{ label, text, lit, to string }{fmt.Sprintf("S7T%d-rows", kk), text, form14624p6RowsCap, "> 0\n"},
			struct{ label, text, lit, to string }{fmt.Sprintf("S7T%d-bytes", kk), text, form14624p6BytesCap, "> 0,\n"})
	}
	for _, f := range fires {
		if strings.Count(f.text, f.lit) != 1 {
			t.Fatalf("GATE %s: %q not once in the cut", f.label, f.lit)
		}
		if code, msg := form14624p6Error("gate-"+f.label, run, strings.Replace(f.text, f.lit, f.to, 1)); code != 395 || !strings.Contains(msg, "STOP-COST-GATE") {
			t.Fatalf("GATE %s lowered to 0: code %d %q, want 395 STOP-COST-GATE", f.label, code, msg)
		}
	}
	t.Logf("GATE %d caps fired at 0: code 395 STOP-COST-GATE", len(fires))
}

// form14624p6ClusterConfig lists both servers as the 2 replicas of cluster 'default'.
const form14624p6ClusterConfig = `<clickhouse>
    <remote_servers>
        <default replace="replace">
            <shard>
                <replica><host>chr1</host><port>9000</port><user>acr</user><password>acr</password></replica>
                <replica><host>chr2</host><port>9000</port><user>acr</user><password>acr</password></replica>
            </shard>
        </default>
    </remote_servers>
</clickhouse>
`

// form14624p6StartReplicas starts 2 ClickHouse servers on one docker network, each with the 2-replica cluster 'default', and returns a
// connection to each (database default).
func form14624p6StartReplicas(t *testing.T, ctx context.Context) (clickhousedriver.Conn, clickhousedriver.Conn) {
	t.Helper()
	chfixture.RequireContainers(t)
	dockerNetwork, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create docker network: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = dockerNetwork.Remove(cctx)
	})
	configPath := filepath.Join(t.TempDir(), "zz-form14624-cluster.xml")
	if err := os.WriteFile(configPath, []byte(form14624p6ClusterConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	var conns []clickhousedriver.Conn
	for _, alias := range []string{"chr1", "chr2"} {
		container, err := startterm.Generic(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image: chfixture.Image, ExposedPorts: []string{"9000/tcp"},
				Env:            map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
				Networks:       []string{dockerNetwork.Name},
				NetworkAliases: map[string][]string{dockerNetwork.Name: {alias}},
				Files:          []testcontainers.ContainerFile{{HostFilePath: configPath, ContainerFilePath: "/etc/clickhouse-server/config.d/zz-form14624-cluster.xml", FileMode: 0o644}},
				WaitingFor:     wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
			},
			Started: true,
		})
		if err != nil {
			t.Fatalf("start ClickHouse %s: %v", alias, err)
		}
		t.Cleanup(func() {
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = container.Terminate(cctx)
		})
		host, err := container.Host(ctx)
		if err != nil {
			t.Fatal(err)
		}
		port, err := container.MappedPort(ctx, "9000/tcp")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := clickhousedriver.Open(&clickhousedriver.Options{
			Addr: []string{net.JoinHostPort(host, port.Port())}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
		})
		if err != nil {
			t.Fatalf("open %s: %v", alias, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		deadline := time.Now().Add(60 * time.Second)
		for conn.Ping(ctx) != nil {
			if time.Now().After(deadline) {
				t.Fatalf("%s not ready", alias)
			}
			time.Sleep(500 * time.Millisecond)
		}
		conns = append(conns, conn)
	}
	return conns[0], conns[1]
}

// TestForm14624Probe6FindsS7OnTheOtherReplica is the 2-replica stand-in: pm's connector can send S7 to one replica and QL to another.
// S7 runs on replica 2; stmtR, S-SET, QL and QLR run on replica 1. The -5 local QL misses the S7 row; the -6 QL finds it with its host.
func TestForm14624Probe6FindsS7OnTheOtherReplica(t *testing.T) {
	ctx := context.Background()
	one, two := form14624p6StartReplicas(t, ctx)
	for _, conn := range []clickhousedriver.Conn{one, two} {
		form14624p6Seed(t, ctx, conn)
	}
	var hostOne, hostTwo string
	if err := one.QueryRow(ctx, "SELECT hostName()").Scan(&hostOne); err != nil {
		t.Fatal(err)
	}
	if err := two.QueryRow(ctx, "SELECT hostName()").Scan(&hostTwo); err != nil {
		t.Fatal(err)
	}
	if hostOne == hostTwo {
		t.Fatalf("the two servers report one host name %q", hostOne)
	}
	runOne, runTwo := form14624p6Runner(t, ctx, one, "r1"), form14624p6Runner(t, ctx, two, "r2")
	var replicas, serverThreads uint64
	var version string
	if err := one.QueryRow(ctx, strings.TrimSuffix(strings.TrimRight(form14624p6CutstmtR, "\n"), ";")).Scan(&replicas, &version, &serverThreads); err != nil || replicas != 2 {
		t.Fatalf("FORM stmtR: replicas %d err %v, want 2 (the stand-in)", replicas, err)
	}
	ok, k, replicaSet := form14624p6SSet(t, "S-SET", runOne, form14624p6CutSSET)
	if len(replicaSet) != 2 || !replicaSet[hostOne] || !replicaSet[hostTwo] {
		t.Fatalf("FORM S-SET replicas %v, want %s and %s", replicaSet, hostOne, hostTwo)
	}
	for _, host := range []string{hostOne, hostTwo} {
		if ok[host+"/max_threads"] != 1 || ok[host+"/readonly"] != 1 {
			t.Fatalf("FORM S-SET %s: ok %v", host, ok)
		}
	}
	rows, _, err := runOne("S-SRV", form14624p6CutSSRV)
	if err == nil {
		hosts := map[string]bool{}
		for rows.Next() {
			var replica, name, value string
			if err := rows.Scan(&replica, &name, &value); err != nil {
				t.Fatalf("FORM S-SRV scan: %v", err)
			}
			hosts[replica] = true
			t.Logf("FORM S-SRV %s %s = %s", replica, name, value)
		}
		rows.Close()
		if len(hosts) != 2 {
			t.Errorf("FORM S-SRV: %d replicas, want 2", len(hosts))
		}
	} else {
		t.Logf("FORM S-SRV: %v (INFO)", err)
	}
	if v, _ := form14624p6One(t, "ORG1", runOne, form14624p6CutORG1); v != 1 {
		t.Fatalf("FORM ORG1 orgs %d, want 1", v)
	}
	if v, _ := form14624p6One(t, "C0", runOne, form14624p6CutC0); v != uint64(len(form14624p6OrgA)) {
		t.Fatalf("FORM C0 org_id_length %d", v)
	}
	cut, has := form14624p6Variants[k]
	if !has {
		t.Fatalf("k = %d has no S7 variant", k)
	}
	if v, _ := form14624p6One(t, fmt.Sprintf("S7T%d on replica 2", k), runTwo, cut); v != form14624p6RunsA-1 {
		t.Fatalf("FORM S7T%d on replica 2 = %d, want %d", k, v, form14624p6RunsA-1)
	}
	for _, conn := range []clickhousedriver.Conn{one, two} {
		if err := conn.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, err = runOne("S0", form14624p6CutS0)
	if err != nil {
		t.Fatalf("FORM S0: %v", err)
	}
	qlParts := map[string]bool{}
	for rows.Next() {
		var scope, replica, table string
		var r, b, p uint64
		if err := rows.Scan(&scope, &replica, &table, &r, &b, &p); err != nil {
			t.Fatalf("FORM S0 scan: %v", err)
		}
		if scope == "query_log" {
			qlParts[replica] = true
		}
	}
	rows.Close()
	if len(qlParts) != 2 {
		t.Errorf("FORM S0: query_log parts of %d replicas, want 2", len(qlParts))
	}
	ql := form14624p6QL(t, "QL", runOne, form14624p6CutQL)
	byComment := map[string][]form14624p6LogRow{}
	for _, r := range ql {
		byComment[r.comment] = append(byComment[r.comment], r)
	}
	s7, c0 := byComment["form14624_probe6_s7"], byComment["form14624_probe6_c0"]
	if len(s7) != 1 || len(c0) != 1 || s7[0].replica != hostTwo || c0[0].replica != hostOne || s7[0].typ != "QueryFinish" {
		t.Fatalf("FORM QL over 2 replicas: S7 %+v, C0 %+v; want 1 S7 row on %s and 1 C0 row on %s", s7, c0, hostTwo, hostOne)
	}
	if s7[0].peak >= form14624p6PeakLimit {
		t.Errorf("PEAK S7 %d >= %d (60 %% of the cap): STOP", s7[0].peak, form14624p6PeakLimit)
	}
	t.Logf("QL found S7 on the other replica %s (QL ran on %s): k %d, read_rows %d, peak %d, threads %q", s7[0].replica, hostOne, k, s7[0].readRows, s7[0].peak, s7[0].settings[1])
	rows, _, err = runOne("QLR", form14624p6CutQLR)
	if err != nil {
		t.Fatalf("FORM QLR: %v", err)
	}
	qlr := map[string]uint64{}
	for rows.Next() {
		var replica string
		var n uint64
		if err := rows.Scan(&replica, &n); err != nil {
			t.Fatalf("FORM QLR scan: %v", err)
		}
		qlr[replica] = n
	}
	rows.Close()
	if len(qlr) != 2 || qlr[hostOne] == 0 || qlr[hostTwo] == 0 {
		t.Fatalf("FORM QLR: %v, want 2 replicas (= stmtR) with rows >= 1", qlr)
	}
	// The -5 shape (a LOCAL query_log) on replica 1 misses the S7 row that replica 2 logged: the gap -6 closes.
	local := form14624p6Swap(t, form14624p6CutQL, "FROM clusterAllReplicas('default', system.query_log)\nWHERE", "FROM system.query_log\nWHERE")
	missing := 0
	for _, r := range form14624p6QL(t, "QL-local", runOne, local) {
		if r.comment == "form14624_probe6_s7" {
			missing++
		}
	}
	if missing != 0 {
		t.Errorf("the local QL on replica 1 found %d S7 rows, want 0 (S7 ran on replica 2)", missing)
	}
	// The QL gate sums query_log over both replicas: its count cap at 0 fires.
	if code, msg := form14624p6Error("gate-QL-count", runOne, strings.Replace(form14624p6CutQL, "> 9000000,", "> 0,", 1)); code != 395 || !strings.Contains(msg, "STOP-COST-GATE") {
		t.Fatalf("GATE QL count at 0 on the stand-in: code %d %q, want 395", code, msg)
	}
	t.Logf("STAND-IN 2 replicas: stmtR 2, S-SET 2 hosts, QLR %v, the local QL misses S7 (0 rows), the cluster QL finds it", qlr)
}
