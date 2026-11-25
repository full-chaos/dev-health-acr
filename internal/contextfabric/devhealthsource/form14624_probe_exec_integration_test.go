package devhealthsource_test

// DO NOT MERGE. Throwaway exec leg of a prod read form (FORM-14624-PROBE-2: the 9 TOTAL kind count statements of the CHAOS-14624
// marker-absent probe, measured once through system.query_log). The cuts are the form's run-block texts with the org prefix swapped
// to a synthetic one; each swapped cut's sha256 is pinned. Synthetic rows only (two fake orgs). Every function here that reads
// memory_usage from system.query_log calls statementthreads.RequireThreadBound (the statementthreads census).

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
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const form14624CutSSET = `SELECT name, value, changed, transform(name, ['max_memory_usage', 'max_threads', 'max_bytes_to_read', 'max_execution_time', 'max_result_rows'], [toUInt64(201326592), 2, 268435456, 10, 1000], toUInt64(0)) AS form_value,
  multiIf(name = 'readonly', toUInt8(value != '1'), name IN ('read_overflow_mode', 'timeout_overflow_mode', 'result_overflow_mode'), toUInt8(value = 'throw'), name = 'enable_analyzer', toUInt8(1),
    toUInt8(toUInt64OrZero(extract(value, '[0-9]+')) = 0 OR toUInt64OrZero(extract(value, '[0-9]+')) >= form_value)) AS ok
FROM system.settings WHERE name IN ('readonly', 'max_memory_usage', 'max_threads', 'max_bytes_to_read', 'max_execution_time', 'max_result_rows', 'read_overflow_mode', 'timeout_overflow_mode', 'result_overflow_mode', 'enable_analyzer')
ORDER BY name;
`
const form14624CutSSETSHA256 = "bce61680528d10eec8a5b59ba6e20f4d21ed9f765489308e6850bdee2588ccb7"

const form14624CutstmtR = `SELECT count() AS replicas, toString(version()) AS server_version, toUInt64OrZero(extract((SELECT value FROM system.settings WHERE name = 'max_threads'), '[0-9]+')) AS server_max_threads FROM clusterAllReplicas('default', system.one);
`
const form14624CutstmtRSHA256 = "303a281ba7fde24dc8850cd1be6a3fe29f44e6acf878b95841a037d918c8cec2"

const form14624CutS0 = `SELECT database = 'system' AS is_system, table, sum(rows) AS rows, sum(bytes_on_disk) AS bytes, count() AS active_parts
FROM system.parts WHERE active AND ((database = currentDatabase() AND table IN ('ci_pipeline_runs', 'deployments', 'git_pull_request_reviews', 'git_pull_requests', 'operational_incidents', 'operational_service_repository_mappings', 'projects', 'repos', 'teams', 'work_items'))
  OR (database = 'system' AND table = 'query_log' AND max_date >= toDate('2026-01-01')))
GROUP BY is_system, table ORDER BY is_system, table;
`
const form14624CutS0SHA256 = "084ef6c29ecd556d29ea507562b121ffcd97186281d2323d5b3147ccfe0df4a7"

const form14624CutORG1 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table = 'repos') > 5000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table = 'repos') > 5000000000,
  'STOP-COST-GATE')) AS cost_gate
SELECT uniqExact(org_id) AS orgs FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0;
`
const form14624CutORG1SHA256 = "47766c9e6de9ca0dcd89c893ba276ce8c31627d2dca61a6b2fabf8265bd30992"

const form14624CutC0 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT length(form_org_id) AS org_id_length
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe_c0';
`
const form14624CutC0SHA256 = "b64cb7af8d88e3b136c06ba1aee198c7a78079b75819c2a0af724ed7b335035b"

const form14624CutS1 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(id) FROM repos FINAL WHERE org_id = form_org_id
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe_s1';
`
const form14624CutS1SHA256 = "969bd1d65a22f170efd062767199ff64281f0af74fe047a913a3403ac30f3ec1"

const form14624CutS2 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('work_items', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('work_items', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(w.repo_id, w.work_item_id) FROM work_items AS w FINAL WHERE w.org_id = form_org_id AND length(concat('work_item.v2:', replaceAll(replaceAll(toString(w.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(w.work_item_id, '%', '%25'), ':', '%3A'))) <= 256
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe_s2';
`
const form14624CutS2SHA256 = "9de5f5576872b70c9a2edbd6a6a55feba1074d355a09748132dae41d732f42d9"

const form14624CutS3 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('git_pull_requests', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('git_pull_requests', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(p.repo_id, p.number) FROM git_pull_requests AS p FINAL
INNER JOIN repos AS r FINAL ON r.id = p.repo_id AND r.org_id = p.org_id
WHERE p.org_id = form_org_id
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe_s3';
`
const form14624CutS3SHA256 = "e38d65321dab019bbcad11492ae1ff4fe8216ca0246b2b98d5ea4ab76e257b35"

const form14624CutS4 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('deployments', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('deployments', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(d.repo_id, d.deployment_id) FROM deployments AS d FINAL
INNER JOIN repos AS r FINAL ON r.id = d.repo_id AND r.org_id = d.org_id
WHERE d.org_id = form_org_id AND length(concat('deployment.v2:', replaceAll(replaceAll(toString(d.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(d.deployment_id, '%', '%25'), ':', '%3A'))) <= 256
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe_s4';
`
const form14624CutS4SHA256 = "5c815e4ba8839955b0a6f7f6130f2a3e80c36d0ea96bbda3127d5453e9a6e343"

const form14624CutS5 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('operational_incidents', 'operational_service_repository_mappings', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('operational_incidents', 'operational_service_repository_mappings', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(i.id) FROM (SELECT * FROM operational_incidents WHERE org_id = form_org_id ORDER BY org_id, id, source_revision DESC, source_conflict_key DESC, ingest_revision DESC LIMIT 1 BY org_id, id) AS i
INNER JOIN (SELECT * FROM operational_service_repository_mappings WHERE org_id = form_org_id ORDER BY org_id, id, source_revision DESC, source_conflict_key DESC, ingest_revision DESC LIMIT 1 BY org_id, id) AS m ON i.org_id = m.org_id AND i.service_id = m.service_id AND m.is_active = 1
INNER JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
WHERE i.org_id = form_org_id AND i.is_deleted = 0
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe_s5';
`
const form14624CutS5SHA256 = "bdad6f702907a1c38a87f503609e6d2ce65d9e98156354f82dd0c5638cbb3e1b"

const form14624CutS6 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('git_pull_request_reviews', 'git_pull_requests', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('git_pull_request_reviews', 'git_pull_requests', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(r.repo_id, r.number, r.review_id) FROM git_pull_request_reviews AS r FINAL
INNER JOIN git_pull_requests AS p FINAL ON r.repo_id = p.repo_id AND r.number = p.number AND r.org_id = p.org_id
INNER JOIN repos AS repo FINAL ON repo.id = r.repo_id AND repo.org_id = r.org_id
WHERE r.org_id = form_org_id AND length(concat('pull_request_review.v2:', replaceAll(replaceAll(toString(r.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(toString(r.number), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(r.review_id, '%', '%25'), ':', '%3A'))) <= 256
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe_s6';
`
const form14624CutS6SHA256 = "daa9a1285e181de2e6041a6fd8835007b573e32ef1487d34b11c7ebf54b12603"

const form14624CutS7 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('ci_pipeline_runs', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(c.repo_id, c.run_id) FROM ci_pipeline_runs AS c FINAL
INNER JOIN repos AS repo FINAL ON repo.id = c.repo_id AND repo.org_id = c.org_id
WHERE c.org_id = form_org_id AND length(concat('ci_pipeline_run.v2:', replaceAll(replaceAll(toString(c.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(c.run_id, '%', '%25'), ':', '%3A'))) <= 256
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe_s7';
`
const form14624CutS7SHA256 = "f34f6032fa63a3d595a84bbf90c843e2c0fa94c4017072f000b16769fc7125b4"

const form14624CutS8 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('teams', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('teams', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(id) FROM teams FINAL WHERE org_id = form_org_id AND is_active = 1
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe_s8';
`
const form14624CutS8SHA256 = "1548a24d2f069a2b237e876f1b2427b8f3f0735ef2cdffb108dc9dd907985d1d"

const form14624CutS9 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('projects', 'repos')) > 6000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('projects', 'repos')) > 8589934592,
  'STOP-COST-GATE')) AS cost_gate,
  (SELECT any(org_id) FROM repos WHERE startsWith(org_id, 'f0f0f0f0') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id
SELECT * FROM (
SELECT uniqExact(provider, id) FROM projects FINAL WHERE org_id = form_org_id AND length(concat('project.v2:', replaceAll(replaceAll(provider, '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(id, '%', '%25'), ':', '%3A'))) <= 256
) WHERE cost_gate = 0
SETTINGS max_memory_usage = 201326592, max_threads = 2, max_bytes_to_read = 268435456, max_execution_time = 10, max_result_rows = 1000, log_comment = 'form14624_probe_s9';
`
const form14624CutS9SHA256 = "95bf004c974c172085ed5b1d52a29252c1822a8483f9913470062f2b740e7e4e"

const form14624CutQL = `WITH (SELECT throwIf(
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
  AND log_comment IN ('form14624_probe_c0', 'form14624_probe_s1', 'form14624_probe_s2', 'form14624_probe_s3', 'form14624_probe_s4', 'form14624_probe_s5', 'form14624_probe_s6', 'form14624_probe_s7', 'form14624_probe_s8', 'form14624_probe_s9')
  AND cost_gate = 0
ORDER BY event_time_utc, stmt
LIMIT 200;
`
const form14624CutQLSHA256 = "b18d7e0db096f443ee205da3d2841d2f33c6f6e44d185bbbe725431aee03bbfb"

const form14624PastQL = `WITH (SELECT throwIf(
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
  AND log_comment IN ('form14624_probe_c0', 'form14624_probe_s1', 'form14624_probe_s2', 'form14624_probe_s3', 'form14624_probe_s4', 'form14624_probe_s5', 'form14624_probe_s6', 'form14624_probe_s7', 'form14624_probe_s8', 'form14624_probe_s9')
  AND cost_gate = 0
ORDER BY event_time_utc, stmt
LIMIT 200;
`
const form14624PastQLSHA256 = "652e004aabd1db9db7e7cc092a800ad67b74e64dbe32b9c0ac5568e928371d13"

// form14624CodeSHA256 pins the 9 code texts the FORM's S1..S9 carry (acr 49c36439, source_counts.go), in probe order.
var form14624CodeSHA256 = []string{
	"3789b8c1560a1571fac7112cff2d5ed74108b41e51b39bbdfb587f53ff5f1ea5",
	"5e2b172b5d7f8e24df6dad9f8cab0860e7e75823f8934e94588f12b00afa3194",
	"45c12a4919d178ebb4a7e3a96ad82ed80f80ffc360335edf95b5615f410855fb",
	"8ddec26c940e23a7f456892c94389dfa86178b568927708135aeb9928eb66a17",
	"cc4ba35613ed8c512ca005d0a554ad8351381011fbe7fb6758bedac1034eec57",
	"e200ec8a74a7dd81bb5b5f93e6965fd617ee6a4961febea641e501e4cfa9d621",
	"70f782872fd4dfb7581b67a98b926217003f171199706e8a67320bc1c7d54e71",
	"94b6d9394dd6f3851b00b8ae28935ac8cf247accfe1e9070e42a49a6f5f433eb",
	"29784c6be1680614fc7527d5da1b3b54c823d398603d81d74ddd259d81a3f0e0",
}

var form14624Kinds = []string{"repository", "work_item", "pull_request", "deployment", "incident", "pull_request_review", "ci_pipeline_run", "team", "project"}

var form14624Cuts = []string{form14624CutS1, form14624CutS2, form14624CutS3, form14624CutS4, form14624CutS5, form14624CutS6, form14624CutS7, form14624CutS8, form14624CutS9}

const (
	form14624PrefixA = "f0f0f0f0"
	form14624OrgA    = "f0f0f0f0-0000-4000-8000-00000000000a"
	form14624PrefixB = "e1e1e1e1"
	form14624OrgB    = "e1e1e1e1-0000-4000-8000-00000000000b"
	// form14624ProjectorMemoryCap is cmd/acr-projector projectorStatementMaxMemoryUsage (runtime.go:407; package main, not importable).
	form14624ProjectorMemoryCap = 192 << 20
	// dev-health-go v0.49.0 clickhouse/options.go: max_execution_time 10 (:11, :39), max_result_rows 1000 when unset (:40).
	// form14624FormThreads is the form's max_threads: pm's connector default (pm ruling B on acp COND-1 C1); the projector sends
	// contextpacket.StatementMaxThreads (4), which would raise the connector's limit.
	form14624FormThreads      = 2
	form14624MaxExecutionTime = 10
	form14624MaxResultRows    = 1000
	form14624RowsCap          = "> 6000000\n"
	form14624BytesCap         = "> 8589934592,\n"
)

// form14624Settings is the SETTINGS line the form sends: the projector client's settings (memory cap, statement threads, read budget,
// the client defaults) and the statement's own log_comment.
func form14624Settings(comment string) string {
	return fmt.Sprintf("SETTINGS max_memory_usage = %d, max_threads = %d, max_bytes_to_read = %d, max_execution_time = %d, max_result_rows = %d, log_comment = '%s';",
		form14624ProjectorMemoryCap, form14624FormThreads, config.DefaultClickHouseMaxBytesToRead, form14624MaxExecutionTime, form14624MaxResultRows, comment)
}

func form14624Comment(label string) string { return "form14624_probe_" + strings.ToLower(label) }

var form14624ReadTable = regexp.MustCompile(`(?:FROM|JOIN) ([a-z_]+)`)
var form14624GateTables = regexp.MustCompile(`table IN \(([^)]*)\)`)

// form14624GateTableSet is the table list of a cut's parts gate (both IN lists must be the same).
func form14624GateTableSet(t *testing.T, label, text string) []string {
	t.Helper()
	lists := form14624GateTables.FindAllStringSubmatch(text, -1)
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

func TestForm14624ProbeCutsAreTheCodeTexts(t *testing.T) {
	all := []struct{ label, text, want string }{
		{"S-SET", form14624CutSSET, form14624CutSSETSHA256}, {"past QL", form14624PastQL, form14624PastQLSHA256},
		{"S0", form14624CutS0, form14624CutS0SHA256}, {"stmtR", form14624CutstmtR, form14624CutstmtRSHA256}, {"ORG1", form14624CutORG1, form14624CutORG1SHA256},
		{"C0", form14624CutC0, form14624CutC0SHA256}, {"S1", form14624CutS1, form14624CutS1SHA256}, {"S2", form14624CutS2, form14624CutS2SHA256},
		{"S3", form14624CutS3, form14624CutS3SHA256}, {"S4", form14624CutS4, form14624CutS4SHA256}, {"S5", form14624CutS5, form14624CutS5SHA256},
		{"S6", form14624CutS6, form14624CutS6SHA256}, {"S7", form14624CutS7, form14624CutS7SHA256}, {"S8", form14624CutS8, form14624CutS8SHA256},
		{"S9", form14624CutS9, form14624CutS9SHA256}, {"QL", form14624CutQL, form14624CutQLSHA256},
	}
	for _, c := range all {
		sum := sha256.Sum256([]byte(c.text))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Fatalf("cut %s sha256 = %s, want %s", c.label, got, c.want)
		}
		t.Logf("CUT %s sha12 %s lines %d", c.label, c.want[:12], strings.Count(c.text, "\n"))
	}
	statements := devhealthsource.Form14624ProbeStatementsForTest()
	if len(statements) != 9 {
		t.Fatalf("the probe runs %d total count statements, the form measures 9", len(statements))
	}
	prefixRule := "(SELECT any(org_id) FROM repos WHERE startsWith(org_id, '" + form14624PrefixA + "') AND cost_gate = 0 HAVING throwIf(uniqExact(org_id) != 1, 'STOP-ORG1') = 0) AS form_org_id\n"
	for i, st := range statements {
		label := fmt.Sprintf("S%d", i+1)
		sum := sha256.Sum256([]byte(st.SQL))
		if got := hex.EncodeToString(sum[:]); got != form14624CodeSHA256[i] || st.Kind != form14624Kinds[i] {
			t.Fatalf("%s: code text %s sha256 %s, the form carries %s %s: the code moved, the form is stale", label, st.Kind, got, form14624Kinds[i], form14624CodeSHA256[i])
		}
		inner := strings.ReplaceAll(st.SQL, "{org_id:String}", "form_org_id")
		body := "SELECT * FROM (\n" + inner + "\n) WHERE cost_gate = 0\n" + form14624Settings(form14624Comment(label)) + "\n"
		cut := form14624Cuts[i]
		if !strings.HasSuffix(cut, body) {
			t.Fatalf("%s: the cut is not the gate + the code text (token form_org_id) + the projector SETTINGS:\n%s\nwant suffix:\n%s", label, cut, body)
		}
		head := strings.TrimSuffix(cut, body)
		for lit, n := range map[string]int{form14624RowsCap: 1, form14624BytesCap: 1, "'STOP-COST-GATE'": 1, "cost_gate = 0": 1, prefixRule: 1, "{org_id": 0, "startsWith(": 1} {
			if got := strings.Count(head, lit); got != n {
				t.Fatalf("%s gate: %q found %d times, want %d", label, lit, got, n)
			}
		}
		read := map[string]bool{"repos": true}
		for _, m := range form14624ReadTable.FindAllStringSubmatch(inner, -1) {
			read[m[1]] = true
		}
		var want []string
		for name := range read {
			want = append(want, name)
		}
		sort.Strings(want)
		if got := form14624GateTableSet(t, label, head); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: the gate caps tables %v, the statement reads %v (+ repos for form_org_id)", label, got, want)
		}
		t.Logf("TEXT %s %s code sha12 %s, %d x {org_id:String} -> form_org_id, gate tables %v", label, st.Kind, form14624CodeSHA256[i][:12], strings.Count(st.SQL, "{org_id:String}"), want)
	}
	c0 := "SELECT * FROM (\nSELECT length(form_org_id) AS org_id_length\n) WHERE cost_gate = 0\n" + form14624Settings(form14624Comment("C0")) + "\n"
	if !strings.HasSuffix(form14624CutC0, c0) || !strings.Contains(form14624CutC0, prefixRule) {
		t.Fatalf("C0 is not the S<n> wrapper around length(form_org_id)")
	}
	for _, label := range []string{"C0", "S1", "S2", "S3", "S4", "S5", "S6", "S7", "S8", "S9"} {
		if !strings.Contains(form14624CutQL, "'"+form14624Comment(label)+"'") {
			t.Fatalf("QL does not read %s", form14624Comment(label))
		}
	}
}

type form14624LogRow struct {
	comment, typ                                   string
	code                                           int32
	elapsed, readRows, readBytes, peak, resultRows uint64
	settings                                       [8]string
}

// form14624Logged reads the query_log rows (not QueryStart) of the query ids, by id (FuncDecl: it reads memory_usage from
// system.query_log, so it checks the statement threads of the finished rows, the statementthreads census rule).
func form14624Logged(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, label string, ids, threadIDs []string) map[string]form14624LogRow {
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
	out := map[string]form14624LogRow{}
	for rows.Next() {
		var id string
		var r form14624LogRow
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

func form14624Code(err error) int32 {
	var exception *clickhousedriver.Exception
	if errors.As(err, &exception) {
		return exception.Code
	}
	return -1
}

func TestForm14624ProbeExecutesOnTheProbeStatements(t *testing.T) {
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
	// The projector's client stack: the dev-health-go client with the hosted read budget (runtime.go:428-429, :440; MaxResultRows and
	// MaxExecutionTime unset), wrapped with the projector memory cap and the statement threads (runtime.go:434).
	maxBytes := config.DefaultClickHouseMaxBytesToRead
	raw, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxBytesToRead: &maxBytes,
	})
	if err != nil {
		t.Fatalf("open the projector query client: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	projector := statementthreads.Capped(raw, form14624ProjectorMemoryCap)
	// devhealthschema:not-a-production-replica the table names here select which tables devhealthschema.DDL renders; no schema is defined in this file
	for _, statement := range devhealthschema.DDL("repos", "work_items", "git_pull_requests", "deployments", "operational_incidents",
		"operational_service_repository_mappings", "git_pull_request_reviews", "ci_pipeline_runs", "teams", "projects") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	var version string
	if err := direct.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("clickhouse %s", version)

	// 1. Two synthetic orgs: A small (one id per kind the producer omits for its length, one deleted incident, one inactive team,
	// one review of no pull request), B large, so a read that loses the org key reads B's rows.
	exec := func(statement string) {
		t.Helper()
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	const at3 = "toDateTime64('2026-09-01 00:00:00', 3, 'UTC')"
	const at6 = "toDateTime64('2026-09-01 00:00:00', 6, 'UTC')"
	long := func(prefix string) string {
		return fmt.Sprintf("if(number = 0, concat('%s-', repeat('X', 300)), concat('%s-', toString(number)))", prefix, prefix)
	}
	seed := func(prefix, org string, repos, big, services, small int) {
		repo := func(expr string) string {
			return fmt.Sprintf("toUUID(concat('%s', '-0000-4000-8000-', leftPad(toString(%s), 12, '0')))", prefix, expr)
		}
		mod := fmt.Sprintf("number %% %d", repos)
		// devhealthschema:not-a-production-replica the table names here fill tables that devhealthschema.DDL rendered above; no schema is defined in this file
		exec(fmt.Sprintf("INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT %s, '%s', concat('acme/r', toString(number)), 'github', %s FROM numbers(%d)", repo("number"), org, at3, repos))
		exec(fmt.Sprintf("INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at, last_synced) SELECT %s, %s, '%s', 'github', 't', 'open', '', '', '', %s, %s FROM numbers(%d)", long("W"), repo(mod), org, at3, at3, big))
		exec(fmt.Sprintf("INSERT INTO git_pull_requests (repo_id, number, title, state, created_at, last_synced, org_id) SELECT %s, toUInt32(number + 1), 'pr', 'open', %s, %s, '%s' FROM numbers(%d)", repo(mod), at3, at3, org, big))
		exec(fmt.Sprintf("INSERT INTO git_pull_request_reviews (repo_id, number, review_id, state, submitted_at, last_synced, org_id) SELECT %s, toUInt32(number + 1 + if(number = 0, 1000000, 0)), concat('rev-', toString(number)), 'APPROVED', %s, %s, '%s' FROM numbers(%d)", repo(mod), at3, at3, org, big))
		exec(fmt.Sprintf("INSERT INTO deployments (repo_id, deployment_id, status, environment, deployed_at, started_at, last_synced, org_id) SELECT %s, %s, 'success', 'prod', %s, %s, %s, '%s' FROM numbers(%d)", repo(mod), long("D"), at3, at3, at3, org, big))
		exec(fmt.Sprintf("INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) SELECT %s, %s, 'success', %s, %s, '%s' FROM numbers(%d)", repo(mod), long("R"), at3, at3, org, big))
		exec(fmt.Sprintf("INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, service_id, repo_id, is_active) SELECT '%s', %s, concat('map-', toString(number)), concat('svc-', toString(number)), %s, 1 FROM numbers(%d)", org, at6, repo(mod), services))
		exec(fmt.Sprintf("INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, is_deleted) SELECT '%s', %s, concat('inc-', toString(number)), %s, %s, concat('svc-', toString(number %% %d)), 'i', if(number = 0, 1, 0) FROM numbers(%d)", org, at6, at6, at6, services, big))
		exec(fmt.Sprintf("INSERT INTO teams (id, name, updated_at, org_id, provider, project_keys, is_active) SELECT concat('team-', toString(number)), 'n', %s, '%s', 'github', [], if(number = 0, 0, 1) FROM numbers(%d)", at6, org, small))
		exec(fmt.Sprintf("INSERT INTO projects (id, org_id, provider, name, is_active, state, url, updated_at) SELECT %s, '%s', 'github', 'p', 1, 'backlog', '', %s FROM numbers(%d)", long("P"), org, at3, small))
	}
	seed(form14624PrefixA, form14624OrgA, 3, 6, 2, 4)
	seed(form14624PrefixB, form14624OrgB, 20000, 100000, 1000, 5000)
	// One part per table: no background merge between the code run and the form run moves a FINAL read.
	var tables []string
	if err := func() error {
		rows, err := direct.Query(ctx, "SELECT name FROM system.tables WHERE database = currentDatabase()")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			tables = append(tables, name)
		}
		return rows.Err()
	}(); err != nil || len(tables) != 10 {
		t.Fatalf("list tables: %d, err %v", len(tables), err)
	}
	for _, name := range tables {
		exec("OPTIMIZE TABLE " + name + " FINAL")
	}

	// 2. CODE: each code text through the projector client stack, bound org, per org; and the real source counts of org A.
	statements := devhealthsource.Form14624ProbeStatementsForTest()
	codeCount := func(st devhealthsource.Form14624Statement, org, reader string) uint64 {
		t.Helper()
		rows, err := projector.Query(contextpacket.WithStatementReader(ctx, reader), st.SQL, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: org}})
		if err != nil {
			t.Fatalf("CODE %s %s: %v", reader, st.Kind, err)
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
	codeA, codeB := make([]uint64, 9), make([]uint64, 9)
	for i, st := range statements {
		codeA[i] = codeCount(st, form14624OrgA, fmt.Sprintf("form14624_code_s%d", i+1))
		codeB[i] = codeCount(st, form14624OrgB, fmt.Sprintf("form14624_code_b_s%d", i+1))
		if codeA[i] == 0 || codeB[i] <= codeA[i] {
			t.Fatalf("CODE S%d %s: org A %d, org B %d; want A > 0 and B > A (the fixture must tell the orgs apart)", i+1, st.Kind, codeA[i], codeB[i])
		}
		t.Logf("CODE S%d %s org A %d org B %d", i+1, st.Kind, codeA[i], codeB[i])
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(projector)
	if err != nil {
		t.Fatal(err)
	}
	teamsSource, err := devhealthsource.NewTeamsProjectsSource(projector, true)
	if err != nil {
		t.Fatal(err)
	}
	sourceCounts := map[string]int64{}
	for _, read := range []func(context.Context, string) (map[contextfabric.SubjectKind]int64, error){source.ProjectionSourceCounts, teamsSource.ProjectionSourceCounts} {
		counts, err := read(ctx, form14624OrgA)
		if err != nil {
			t.Fatalf("ProjectionSourceCounts: %v", err)
		}
		for kind, n := range counts {
			sourceCounts[string(kind)] = n
		}
	}
	for i, st := range statements {
		if sourceCounts[st.Kind] != int64(codeA[i]) {
			t.Fatalf("REAL %s: ProjectionSourceCounts %d, the code text %d", st.Kind, sourceCounts[st.Kind], codeA[i])
		}
	}

	// 3. FORM: each cut through a plain connection (pm's connector sends no settings of its own), one query id each.
	runID := time.Now().UnixNano()
	var formIDs []string
	idOf := map[string]string{}
	run := func(label, text string) (chdriver.Rows, string, error) {
		t.Helper()
		id := fmt.Sprintf("form14624-%s-%d-%d", label, runID, len(formIDs))
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
	// S-SET: the session's limits and overflow modes; ok per row.
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
	if got := sset("S-SET", form14624CutSSET); len(got) != 10 {
		t.Fatalf("FORM S-SET: %d rows, want 10: %v", len(got), got)
	} else {
		for name, ok := range got {
			if ok != 1 {
				t.Fatalf("FORM S-SET %s ok = 0 on the test server: the WANT would STOP", name)
			}
		}
	}
	rows, _, err := run("stmtR", form14624CutstmtR)
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
	rows, _, err = run("S0", form14624CutS0)
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
	if _, ok := sizes["query_log"]; len(sizes) != 11 || !ok {
		t.Fatalf("FORM S0: %d table rows, want 11 (10 tables + query_log): %v", len(sizes), sizes)
	}
	t.Logf("FORM S0 sizes %v", sizes)
	t.Logf("FORM stmtR replicas 1 server %s max_threads default %d", serverVersion, serverThreads)
	if orgs, _ := one("ORG1", form14624CutORG1); orgs != 1 {
		t.Fatalf("FORM ORG1 orgs %d, want 1", orgs)
	}
	length, c0ID := one("C0", form14624CutC0)
	if length != uint64(len(form14624OrgA)) {
		t.Fatalf("FORM C0 org_id_length %d, want %d", length, len(form14624OrgA))
	}
	idOf["C0"] = c0ID
	for i := range statements {
		label := fmt.Sprintf("S%d", i+1)
		got, id := one(label, form14624Cuts[i])
		if got != codeA[i] {
			t.Fatalf("FORM %s = %d, the code text on org A = %d", label, got, codeA[i])
		}
		if got == 0 {
			t.Fatalf("FORM %s = 0: the control (S1 > 0) and every fixture kind want a count", label)
		}
		idOf[label] = id
		t.Logf("FORM %s = %d = CODE org A", label, got)
	}

	// 4. QL: the form's one query_log read, against a recompute by query id; the settings against the code run's query_log row.
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	rows, _, err = run("QL", form14624CutQL)
	if err != nil {
		t.Fatalf("FORM QL: %v", err)
	}
	ql := map[string]form14624LogRow{}
	for rows.Next() {
		var r form14624LogRow
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
	if len(ql) != 10 {
		t.Fatalf("FORM QL: %d rows, want 10 (C0, S1..S9): %v", len(ql), ql)
	}
	var ids []string
	for _, label := range []string{"C0", "S1", "S2", "S3", "S4", "S5", "S6", "S7", "S8", "S9"} {
		ids = append(ids, idOf[label])
	}
	// The form runs at max_threads 2 (not the statement threads): its rows are checked against 2 below, not by RequireThreadBound.
	logged := form14624Logged(t, ctx, direct, "FORM", ids, nil)
	var codeIDs []string
	if err := func() error {
		rows, err := direct.Query(ctx, "SELECT query_id FROM system.query_log WHERE type = 'QueryFinish' AND current_database = currentDatabase() AND startsWith(log_comment, 'form14624_code_s')")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			codeIDs = append(codeIDs, id)
		}
		return rows.Err()
	}(); err != nil || len(codeIDs) != 9 {
		t.Fatalf("CODE query ids: %d, err %v, want 9", len(codeIDs), err)
	}
	codeLogged := map[string]form14624LogRow{}
	for _, r := range form14624Logged(t, ctx, direct, "CODE", codeIDs, codeIDs) {
		codeLogged[r.comment] = r
	}
	var partsAll uint64
	if err := direct.QueryRow(ctx, "SELECT count() FROM system.parts WHERE active").Scan(&partsAll); err != nil {
		t.Fatal(err)
	}
	c0 := ql[form14624Comment("C0")]
	wantSettings := [8]string{fmt.Sprint(form14624ProjectorMemoryCap), fmt.Sprint(form14624FormThreads), fmt.Sprint(config.DefaultClickHouseMaxBytesToRead), fmt.Sprint(form14624MaxExecutionTime), fmt.Sprint(form14624MaxResultRows)}
	for i, st := range statements {
		label := fmt.Sprintf("S%d", i+1)
		q, rec, code := ql[form14624Comment(label)], logged[idOf[label]], codeLogged[fmt.Sprintf("form14624_code_s%d", i+1)]
		if q.typ != "QueryFinish" || q.code != 0 || q != rec {
			t.Errorf("QL %s = %+v, the recompute by query id = %+v", label, q, rec)
		}
		for k := range wantSettings {
			// max_threads: the form sends 2, the code run the statement threads (4); a value equal to the server default logs ''.
			// max_execution_time: the form sends 10; the code run sends int(remaining client deadline + 5) per query (clickhouse-go
			// v2.47.0 context.go:239-240 over dev-health-go's 10 s query deadline, client.go:157-158), so 10..15, logged.
			// The overflow modes: neither sends one; both log the server default ('').
			threadsDefault := k == 1 && q.settings[k] == "" && serverThreads == form14624FormThreads
			sameAsCode := q.settings[k] == code.settings[k]
			if k == 3 {
				var codeSeconds int
				_, scanErr := fmt.Sscanf(code.settings[k], "%d", &codeSeconds)
				sameAsCode = scanErr == nil && codeSeconds >= 10 && codeSeconds <= 15
				t.Logf("SETTINGS %s max_execution_time: form %q, code run %q (client deadline + 5)", label, q.settings[k], code.settings[k])
			}
			if (q.settings[k] != wantSettings[k] && !threadsDefault) || (k != 1 && !sameAsCode) {
				t.Errorf("SETTINGS %s key %d: form %q, code run %q, want %q", label, k, q.settings[k], code.settings[k], wantSettings[k])
			}
		}
		var sumRows uint64
		for _, name := range form14624GateTableSet(t, label, form14624Cuts[i]) {
			sumRows += sizes[name][0]
		}
		bound := 3*sumRows + 2*partsAll + 1
		// The pruning parity reads the form's own read without C0: form_org_id reads repos once more, so S1 (T = repos only) reads
		// more than the table while both of its reads prune.
		var formOwn uint64
		if q.readRows > c0.readRows {
			formOwn = q.readRows - c0.readRows
		}
		t.Logf("PRUNE %s %s: form read_rows %d, C0 %d, form minus C0 %d, code read_rows %d, table rows %d", label, st.Kind, q.readRows, c0.readRows, formOwn, code.readRows, sumRows)
		if q.readRows < code.readRows || q.readRows > code.readRows+c0.readRows+4*partsAll+2 || q.readRows > bound || (formOwn < sumRows) != (code.readRows < sumRows) {
			t.Errorf("PASS %s %s: form read_rows %d, code read_rows %d, C0 %d, parts %d, table rows %d, work bound %d", label, st.Kind, q.readRows, code.readRows, c0.readRows, partsAll, sumRows, bound)
		}
		t.Logf("PASS %s %s form read_rows %d bytes %d peak %d ms %d | code read_rows %d bytes %d peak %d ms %d | C0 %d table rows %d bound %d settings %v",
			label, st.Kind, q.readRows, q.readBytes, q.peak, q.elapsed, code.readRows, code.readBytes, code.peak, code.elapsed, c0.readRows, sumRows, bound, q.settings)
	}
	if c0 != logged[idOf["C0"]] || c0.typ != "QueryFinish" {
		t.Errorf("QL C0 = %+v, recompute %+v", c0, logged[idOf["C0"]])
	}

	// The QL window holds: the same text with a past window (synthetic) reads none of these rows.
	rows, _, err = run("QL-past", form14624PastQL)
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

	// 5. MUTANTS (after QL: they log under the same comments). Org: prefix B, no org, every org. Rule: the read budget and the memory
	// cap reach the statement (code 307 / 241, logged under its comment); the raw code text cannot run without a bound parameter.
	expectCode := func(label, text string, want int32) string {
		t.Helper()
		rows, id, err := run(label, text)
		if err == nil {
			for rows.Next() {
			}
			err = rows.Err()
			rows.Close()
		}
		if got := form14624Code(err); got != want {
			t.Fatalf("MUTANT %s: err %v (code %d), want code %d", label, err, got, want)
		}
		return id
	}
	swap := func(text, from, to string) string {
		t.Helper()
		if strings.Count(text, from) == 0 {
			t.Fatalf("mutant literal %q not in the cut", from)
		}
		return strings.ReplaceAll(text, from, to)
	}
	if v, _ := one("ORG1-all", swap(form14624CutORG1, "'"+form14624PrefixA+"'", "''")); v != 2 {
		t.Errorf("MUTANT ORG1 every org: orgs %d, want 2 (STOP)", v)
	}
	if v, _ := one("ORG1-none", swap(form14624CutORG1, "'"+form14624PrefixA+"'", "'ffffffff'")); v != 0 {
		t.Errorf("MUTANT ORG1 no org: orgs %d, want 0 (STOP)", v)
	}
	if v, _ := one("C0-B", swap(form14624CutC0, "'"+form14624PrefixA+"'", "'"+form14624PrefixB+"'")); v != uint64(len(form14624OrgB)) {
		t.Errorf("MUTANT C0 org B: %d", v)
	}
	// form_org_id's own throwIf: 0 orgs or 2 orgs under the prefix stop the statement (code 395 STOP-ORG1) before its read.
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
	expectStop("C0-none", swap(form14624CutC0, "'"+form14624PrefixA+"'", "'ffffffff'"), "STOP-ORG1")
	expectStop("C0-all", swap(form14624CutC0, "'"+form14624PrefixA+"'", "''"), "STOP-ORG1")
	// S-SET: each WANT arm reads the value (readonly, the overflow modes, a limit below the form value).
	for _, m := range []struct{ label, from, to, row string }{
		{"S-SET-readonly", "toUInt8(value != '1')", "toUInt8(value != '0')", "readonly"},
		{"S-SET-overflow", "toUInt8(value = 'throw')", "toUInt8(value = 'break')", "read_overflow_mode"},
		{"S-SET-threads", "[toUInt64(201326592), 2,", "[toUInt64(201326592), 1000000,", "max_threads"},
	} {
		if got := sset(m.label, swap(form14624CutSSET, m.from, m.to)); got[m.row] != 0 {
			t.Errorf("MUTANT %s: row %s ok = %d, want 0 (STOP)", m.label, m.row, got[m.row])
		}
	}
	var refusedIDs []string
	refused := map[string]int32{}
	for i := range statements {
		label := fmt.Sprintf("S%d", i+1)
		if v, _ := one(label+"-B", swap(form14624Cuts[i], "'"+form14624PrefixA+"'", "'"+form14624PrefixB+"'")); v != codeB[i] {
			t.Errorf("MUTANT %s org B = %d, the code text on org B = %d", label, v, codeB[i])
		}
		expectStop(label+"-none", swap(form14624Cuts[i], "'"+form14624PrefixA+"'", "'ffffffff'"), "STOP-ORG1")
		expectStop(label+"-all", swap(form14624Cuts[i], "'"+form14624PrefixA+"'", "''"), "STOP-ORG1")
		id := expectCode(label+"-bytes", swap(form14624Cuts[i], "max_bytes_to_read = 268435456", "max_bytes_to_read = 1"), 307)
		refusedIDs, refused[id] = append(refusedIDs, id), 307
		id = expectCode(label+"-memory", swap(form14624Cuts[i], "max_memory_usage = 201326592", "max_memory_usage = 1"), 241)
		refusedIDs, refused[id] = append(refusedIDs, id), 241
		expectCode(label+"-raw", swap(form14624Cuts[i], " = form_org_id", " = {org_id:String}"), 456)
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	for id, r := range form14624Logged(t, ctx, direct, "REFUSED", refusedIDs, nil) {
		if r.code != refused[id] || !strings.HasPrefix(r.comment, "form14624_probe_s") || !strings.HasPrefix(r.typ, "Exception") {
			t.Errorf("REFUSED %s: query_log %+v, want an exception row with code %d under its probe comment (QL reports it)", id, r, refused[id])
		}
	}

	// 6. GATES: each cap literal lowered to 0 fires STOP-COST-GATE (code 395) before any data row: ExceptionBeforeStart, read_rows
	// <= 2 x parts + 1 (+ the query_log window count W for QL's count cap).
	var window uint64
	if err := direct.QueryRow(ctx, "SELECT count() FROM system.query_log WHERE event_date >= toDate('2026-01-01')").Scan(&window); err != nil {
		t.Fatal(err)
	}
	type fire struct{ label, text, lit, to string }
	fires := []fire{
		{"ORG1-rows", form14624CutORG1, "> 5000000\n", "> 0\n"}, {"ORG1-bytes", form14624CutORG1, "> 5000000000,\n", "> 0,\n"},
		{"C0-rows", form14624CutC0, form14624RowsCap, "> 0\n"}, {"C0-bytes", form14624CutC0, form14624BytesCap, "> 0,\n"},
		{"QL-rows", form14624CutQL, "> 200000000\n", "> 0\n"}, {"QL-bytes", form14624CutQL, "> 100000000000\n", "> 0\n"}, {"QL-count", form14624CutQL, "> 9000000,", "> 0,"},
	}
	for i := range statements {
		label := fmt.Sprintf("S%d", i+1)
		fires = append(fires, fire{label + "-rows", form14624Cuts[i], form14624RowsCap, "> 0\n"}, fire{label + "-bytes", form14624Cuts[i], form14624BytesCap, "> 0,\n"})
	}
	// serverSecond is the server clock (UTC, seconds) after a 1.1 s pause, so a window edge at it splits the rows before and after.
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
	fireStart := serverSecond()
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

	// 7. QL over the gate-fired statements (the card's "STOP-COST-GATE = not measured, go on" + "QL row missing = STOP"): each fired
	// C0 / S<n> logs an ExceptionBeforeStart row with query_kind Select and its probe comment, so QL reports it. A window from after
	// the fires reads none of them (the T_R edge).
	qlFrom := func(label, from string) []form14624LogRow {
		t.Helper()
		text := swap(swap(form14624CutQL, "toDateTime('2026-01-01 00:00:00', 'UTC')", "toDateTime('"+from+"', 'UTC')"), "toDate('2026-01-01')", "toDate('"+from[:10]+"')")
		rows, _, err := run(label, text)
		if err != nil {
			t.Fatalf("FORM %s: %v", label, err)
		}
		defer rows.Close()
		var out []form14624LogRow
		for rows.Next() {
			var r form14624LogRow
			var eventTime string
			if err := rows.Scan(&r.comment, &r.typ, &r.code, &r.elapsed, &r.readRows, &r.readBytes, &r.peak, &r.resultRows,
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
	fireEnd := serverSecond()
	perComment := map[string]int{}
	for _, r := range qlFrom("QL-fired", fireStart) {
		if r.typ != "ExceptionBeforeStart" || r.code != 395 {
			t.Fatalf("QL-fired %s: type %s code %d, want ExceptionBeforeStart 395 (a gate-fired statement)", r.comment, r.typ, r.code)
		}
		perComment[r.comment]++
	}
	for _, label := range []string{"C0", "S1", "S2", "S3", "S4", "S5", "S6", "S7", "S8", "S9"} {
		if n := perComment[form14624Comment(label)]; n != 2 {
			t.Fatalf("QL-fired %s: %d rows, want 2 (its rows cap and bytes cap fired): %v", label, n, perComment)
		}
	}
	if len(perComment) != 10 {
		t.Fatalf("QL-fired: rows for %d comments, want 10: %v", len(perComment), perComment)
	}
	if after := qlFrom("QL-after", fireEnd); len(after) != 0 {
		t.Fatalf("QL with T_R after the fires: %d rows, want 0", len(after))
	}
	t.Logf("QL-fired 20 rows (2 per C0 / S1..S9), ExceptionBeforeStart 395; QL from %s (after the fires) 0 rows", fireEnd)
}
