package devhealthfacts_test

// DO NOT MERGE. Throwaway exec proof of a prod read form (FORM-339-LINKQL-2: system.query_log, the acr-api team link readers of the
// CHAOS-10430 roll, NEW (acr 7521ec7a) vs OLD (acr a47904e8) text, by reader name + head anchor + writer marker). The five statements are
// the form's run-block cuts with the window tokens and the org prefix swapped to synthetic values; each swapped copy's sha256 is pinned.
// Synthetic rows only (two fake orgs). A dedicated ClickHouse container: query_log holds this test's statements only. Every function here
// that reads memory_usage from system.query_log calls statementthreads.RequireThreadBound (the statementthreads census).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const linkqlCutS0 = `SELECT partition, sum(rows) AS rows, sum(bytes_on_disk) AS bytes, count() AS active_parts, toString(min(min_date)) AS min_day
FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2026-01-01')
GROUP BY partition ORDER BY partition;
`
const linkqlCutS0SHA256 = "01910358bc2a35d70049cba687457a90f3ba364b3492cb26f144829ea9ebe18f"
const linkqlCutstmtR = `SELECT count() AS replicas, toString(version()) AS server_version FROM clusterAllReplicas('default', system.one);
`
const linkqlCutstmtRSHA256 = "428761509478f35928b8994c06684d99c4675e3f09efe66091d6c5f040efd91f"
const linkqlCutORG1 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = currentDatabase() AND table = 'repos') > 5000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() AND table = 'repos') > 5000000000,
  'STOP-COST-GATE')) AS cost_gate
SELECT uniqExact(org_id) AS orgs FROM repos WHERE startsWith(org_id, '33900000') AND cost_gate = 0;
`
const linkqlCutORG1SHA256 = "4d33e6908dd3acbc44035d097707fbc6c5792af6cb57054d9715bec13a6a563b"
const linkqlCutstmt0 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2026-01-01') AND min_date <= toDate('2099-01-01')) > 200000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2026-01-01') AND min_date <= toDate('2099-01-01')) > 100000000000
  OR (SELECT count() FROM system.query_log WHERE event_date >= toDate('2026-01-01') AND event_date <= toDate('2099-01-01') AND event_time >= toDateTime('2026-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2099-01-01 00:00:00', 'UTC')) > 9000000,
  'STOP-COST-GATE')) AS cost_gate,
  position(query, '_team_project_linked_pull_requests') > 0 AS m_new,
  position(query, 'team_project_ownership') > 0 AS m_old,
  position(query, concat('''', '33900000')) > 0 AS m_org,
  (log_comment = 'ReadTeamProjectReachedRepositories' AND startsWith(query, 'SELECT lr.team_id, toString(lr.pr_repo_id), ifNull(any(r.repo), '''')')) AS r_reach,
  (log_comment = 'ReadTeamProjectLinkedPullRequests' AND startsWith(query, 'SELECT lr.team_id, toString(lr.pr_repo_id),')) AS r_counts,
  (log_comment = 'ReadTeamProjectLinkedPullRequestStates' AND startsWith(query, 'SELECT team_id, repo, arrayStringConcat(arrayMap(x -> concat(x.1, ''')) AS r_states,
  (log_comment = 'ReadTeamLinkCandidates' AND startsWith(query, 'SELECT groupArray(work_item_id) FROM (SELECT cp.work_item_id AS work_item_id FROM work_items AS cp WHERE')) AS r_cand,
  (log_comment = 'ReadTeamLinkedPullRequestSet' AND startsWith(query, 'SELECT groupArray(r), groupArray(n), groupArray(t), groupArray(i), groupArray(k) FROM (')) AS r_lset
SELECT toString(today()) AS day,
  toString(now(), 'UTC') AS run_at,
  countIf(type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_reach AND m_new AND NOT m_old) AS reach_new_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_reach AND m_new AND NOT m_old) AS reach_new_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_max_threads,
  countIf(type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_finished,
  countIf(type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_counts AND m_new AND NOT m_old) AS counts_new_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_counts AND m_new AND NOT m_old) AS counts_new_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_max_threads,
  countIf(type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_finished,
  countIf(type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_states AND m_new AND NOT m_old) AS states_new_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_states AND m_new AND NOT m_old) AS states_new_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_max_threads,
  countIf(type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_finished,
  countIf(type = 'QueryFinish' AND r_cand) AS cand_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_cand) AS cand_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_cand) AS cand_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_cand) AS cand_max_memory,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_cand) AS cand_max_threads,
  countIf(type = 'QueryFinish' AND r_lset) AS lset_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_lset) AS lset_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_lset) AS lset_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_lset) AS lset_max_memory,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_lset) AS lset_max_threads,
  countIf(type = 'QueryFinish' AND (r_reach OR r_counts OR r_states) AND m_new = m_old) AS moved_other_finished
FROM system.query_log
WHERE event_date >= toDate('2026-01-01') AND event_date <= toDate('2099-01-01') AND event_time >= toDateTime('2026-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2099-01-01 00:00:00', 'UTC')
  AND type != 'QueryStart' AND query_kind = 'Select' AND m_org
  AND (r_reach OR r_counts OR r_states OR r_cand OR r_lset)
  AND cost_gate = 0;
`
const linkqlCutstmt0SHA256 = "9454dc2b9149689582ca4d01460fac47925f672745bd1dbc70e4667fc25c8c76"
const linkqlCutstmt0h = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2026-01-01') AND min_date <= toDate('2099-01-01')) > 200000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2026-01-01') AND min_date <= toDate('2099-01-01')) > 100000000000
  OR (SELECT count() FROM system.query_log WHERE event_date >= toDate('2026-01-01') AND event_date <= toDate('2099-01-01') AND event_time >= toDateTime('2026-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2099-01-01 00:00:00', 'UTC')) > 9000000,
  'STOP-COST-GATE')) AS cost_gate,
  position(query, '_team_project_linked_pull_requests') > 0 AS m_new,
  position(query, 'team_project_ownership') > 0 AS m_old,
  position(query, concat('''', '33900000')) > 0 AS m_org,
  (log_comment = 'ReadTeamProjectReachedRepositories' AND startsWith(query, 'SELECT lr.team_id, toString(lr.pr_repo_id), ifNull(any(r.repo), '''')')) AS r_reach,
  (log_comment = 'ReadTeamProjectLinkedPullRequests' AND startsWith(query, 'SELECT lr.team_id, toString(lr.pr_repo_id),')) AS r_counts,
  (log_comment = 'ReadTeamProjectLinkedPullRequestStates' AND startsWith(query, 'SELECT team_id, repo, arrayStringConcat(arrayMap(x -> concat(x.1, ''')) AS r_states,
  (log_comment = 'ReadTeamLinkCandidates' AND startsWith(query, 'SELECT groupArray(work_item_id) FROM (SELECT cp.work_item_id AS work_item_id FROM work_items AS cp WHERE')) AS r_cand,
  (log_comment = 'ReadTeamLinkedPullRequestSet' AND startsWith(query, 'SELECT groupArray(r), groupArray(n), groupArray(t), groupArray(i), groupArray(k) FROM (')) AS r_lset
SELECT toString(today()) AS day,
  toString(now(), 'UTC') AS run_at,
  countIf(type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_reach AND m_old AND NOT m_new) AS reach_old_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_reach AND m_old AND NOT m_new) AS reach_old_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_max_threads,
  countIf(type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_finished,
  countIf(type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_counts AND m_old AND NOT m_new) AS counts_old_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_counts AND m_old AND NOT m_new) AS counts_old_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_max_threads,
  countIf(type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_finished,
  countIf(type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_states AND m_old AND NOT m_new) AS states_old_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_states AND m_old AND NOT m_new) AS states_old_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_max_threads,
  countIf(type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_finished,
  countIf(type = 'QueryFinish' AND r_cand) AS cand_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_cand) AS cand_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_cand) AS cand_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_cand) AS cand_max_memory,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_cand) AS cand_max_threads,
  countIf(type = 'QueryFinish' AND r_lset) AS lset_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_lset) AS lset_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_lset) AS lset_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_lset) AS lset_max_memory,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_lset) AS lset_max_threads,
  countIf(type = 'QueryFinish' AND (r_reach OR r_counts OR r_states) AND m_new = m_old) AS moved_other_finished
FROM system.query_log
WHERE event_date >= toDate('2026-01-01') AND event_date <= toDate('2099-01-01') AND event_time >= toDateTime('2026-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2099-01-01 00:00:00', 'UTC')
  AND type != 'QueryStart' AND query_kind = 'Select' AND m_org
  AND (r_reach OR r_counts OR r_states OR r_cand OR r_lset)
  AND cost_gate = 0;
`
const linkqlCutstmt0hSHA256 = "6da6896d9d35e52bd39e9ff7ae4e1ac9fa13036e815a8ad5d2ba5913d8997548"
const linkqlPaststmt0 = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01') AND min_date <= toDate('2025-01-02')) > 200000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01') AND min_date <= toDate('2025-01-02')) > 100000000000
  OR (SELECT count() FROM system.query_log WHERE event_date >= toDate('2025-01-01') AND event_date <= toDate('2025-01-02') AND event_time >= toDateTime('2025-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2025-01-02 00:00:00', 'UTC')) > 9000000,
  'STOP-COST-GATE')) AS cost_gate,
  position(query, '_team_project_linked_pull_requests') > 0 AS m_new,
  position(query, 'team_project_ownership') > 0 AS m_old,
  position(query, concat('''', '33900000')) > 0 AS m_org,
  (log_comment = 'ReadTeamProjectReachedRepositories' AND startsWith(query, 'SELECT lr.team_id, toString(lr.pr_repo_id), ifNull(any(r.repo), '''')')) AS r_reach,
  (log_comment = 'ReadTeamProjectLinkedPullRequests' AND startsWith(query, 'SELECT lr.team_id, toString(lr.pr_repo_id),')) AS r_counts,
  (log_comment = 'ReadTeamProjectLinkedPullRequestStates' AND startsWith(query, 'SELECT team_id, repo, arrayStringConcat(arrayMap(x -> concat(x.1, ''')) AS r_states,
  (log_comment = 'ReadTeamLinkCandidates' AND startsWith(query, 'SELECT groupArray(work_item_id) FROM (SELECT cp.work_item_id AS work_item_id FROM work_items AS cp WHERE')) AS r_cand,
  (log_comment = 'ReadTeamLinkedPullRequestSet' AND startsWith(query, 'SELECT groupArray(r), groupArray(n), groupArray(t), groupArray(i), groupArray(k) FROM (')) AS r_lset
SELECT toString(today()) AS day,
  toString(now(), 'UTC') AS run_at,
  countIf(type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_reach AND m_new AND NOT m_old) AS reach_new_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_reach AND m_new AND NOT m_old) AS reach_new_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_max_threads,
  countIf(type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_finished,
  countIf(type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_counts AND m_new AND NOT m_old) AS counts_new_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_counts AND m_new AND NOT m_old) AS counts_new_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_max_threads,
  countIf(type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_finished,
  countIf(type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_states AND m_new AND NOT m_old) AS states_new_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_states AND m_new AND NOT m_old) AS states_new_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_max_threads,
  countIf(type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_finished,
  countIf(type = 'QueryFinish' AND r_cand) AS cand_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_cand) AS cand_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_cand) AS cand_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_cand) AS cand_max_memory,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_cand) AS cand_max_threads,
  countIf(type = 'QueryFinish' AND r_lset) AS lset_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_lset) AS lset_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_lset) AS lset_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_lset) AS lset_max_memory,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_lset) AS lset_max_threads,
  countIf(type = 'QueryFinish' AND (r_reach OR r_counts OR r_states) AND m_new = m_old) AS moved_other_finished
FROM system.query_log
WHERE event_date >= toDate('2025-01-01') AND event_date <= toDate('2025-01-02') AND event_time >= toDateTime('2025-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2025-01-02 00:00:00', 'UTC')
  AND type != 'QueryStart' AND query_kind = 'Select' AND m_org
  AND (r_reach OR r_counts OR r_states OR r_cand OR r_lset)
  AND cost_gate = 0;
`
const linkqlPaststmt0SHA256 = "12236be48b4751915a91f8df5dfdb25c001633b792de6a1fd0ba7e85ba0f50b6"
const linkqlPaststmt0h = `WITH (SELECT throwIf(
  (SELECT sum(rows) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01') AND min_date <= toDate('2025-01-02')) > 200000000
  OR (SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = 'system' AND table = 'query_log' AND max_date >= toDate('2025-01-01') AND min_date <= toDate('2025-01-02')) > 100000000000
  OR (SELECT count() FROM system.query_log WHERE event_date >= toDate('2025-01-01') AND event_date <= toDate('2025-01-02') AND event_time >= toDateTime('2025-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2025-01-02 00:00:00', 'UTC')) > 9000000,
  'STOP-COST-GATE')) AS cost_gate,
  position(query, '_team_project_linked_pull_requests') > 0 AS m_new,
  position(query, 'team_project_ownership') > 0 AS m_old,
  position(query, concat('''', '33900000')) > 0 AS m_org,
  (log_comment = 'ReadTeamProjectReachedRepositories' AND startsWith(query, 'SELECT lr.team_id, toString(lr.pr_repo_id), ifNull(any(r.repo), '''')')) AS r_reach,
  (log_comment = 'ReadTeamProjectLinkedPullRequests' AND startsWith(query, 'SELECT lr.team_id, toString(lr.pr_repo_id),')) AS r_counts,
  (log_comment = 'ReadTeamProjectLinkedPullRequestStates' AND startsWith(query, 'SELECT team_id, repo, arrayStringConcat(arrayMap(x -> concat(x.1, ''')) AS r_states,
  (log_comment = 'ReadTeamLinkCandidates' AND startsWith(query, 'SELECT groupArray(work_item_id) FROM (SELECT cp.work_item_id AS work_item_id FROM work_items AS cp WHERE')) AS r_cand,
  (log_comment = 'ReadTeamLinkedPullRequestSet' AND startsWith(query, 'SELECT groupArray(r), groupArray(n), groupArray(t), groupArray(i), groupArray(k) FROM (')) AS r_lset
SELECT toString(today()) AS day,
  toString(now(), 'UTC') AS run_at,
  countIf(type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_reach AND m_old AND NOT m_new) AS reach_old_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_reach AND m_old AND NOT m_new) AS reach_old_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_reach AND m_old AND NOT m_new) AS reach_old_max_threads,
  countIf(type = 'QueryFinish' AND r_reach AND m_new AND NOT m_old) AS reach_new_finished,
  countIf(type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_counts AND m_old AND NOT m_new) AS counts_old_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_counts AND m_old AND NOT m_new) AS counts_old_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_counts AND m_old AND NOT m_new) AS counts_old_max_threads,
  countIf(type = 'QueryFinish' AND r_counts AND m_new AND NOT m_old) AS counts_new_finished,
  countIf(type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_states AND m_old AND NOT m_new) AS states_old_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_states AND m_old AND NOT m_new) AS states_old_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_max_memory,
  maxIf(read_rows, type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_max_read_rows,
  maxIf(read_bytes, type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_max_read_bytes,
  maxIf(ProfileEvents['NetworkReceiveBytes'], type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_max_recv_bytes,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_states AND m_old AND NOT m_new) AS states_old_max_threads,
  countIf(type = 'QueryFinish' AND r_states AND m_new AND NOT m_old) AS states_new_finished,
  countIf(type = 'QueryFinish' AND r_cand) AS cand_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_cand) AS cand_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_cand) AS cand_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_cand) AS cand_max_memory,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_cand) AS cand_max_threads,
  countIf(type = 'QueryFinish' AND r_lset) AS lset_finished,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND r_lset) AS lset_failed,
  countIf(type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND exception_code = 241 AND r_lset) AS lset_failed_241,
  maxIf(memory_usage, type = 'QueryFinish' AND r_lset) AS lset_max_memory,
  maxIf(toUInt64OrZero(Settings['max_threads']), type = 'QueryFinish' AND r_lset) AS lset_max_threads,
  countIf(type = 'QueryFinish' AND (r_reach OR r_counts OR r_states) AND m_new = m_old) AS moved_other_finished
FROM system.query_log
WHERE event_date >= toDate('2025-01-01') AND event_date <= toDate('2025-01-02') AND event_time >= toDateTime('2025-01-01 00:00:00', 'UTC') AND event_time < toDateTime('2025-01-02 00:00:00', 'UTC')
  AND type != 'QueryStart' AND query_kind = 'Select' AND m_org
  AND (r_reach OR r_counts OR r_states OR r_cand OR r_lset)
  AND cost_gate = 0;
`
const linkqlPaststmt0hSHA256 = "ca4a46e010c860dbcd4da41238b3fbdf443519fe3189e273d8ccb006e9530af6"

const (
	linkqlOrgAPrefix = "33900000"
	linkqlOrgA       = "33900000-0000-4000-8000-00000000000a"
	linkqlOrgB       = "44900000-0000-4000-8000-00000000000b"
	linkqlOrgBPrefix = "44900000"
	linkqlMarkNew    = "_team_project_linked_pull_requests"
	linkqlMarkOld    = "team_project_ownership"
	linkqlAPICap80   = 214748364
)

// The five readers of the form: name, head literal (its first line).
var linkqlReaders = []struct{ key, name, head string }{
	{"reach", "ReadTeamProjectReachedRepositories", "SELECT lr.team_id, toString(lr.pr_repo_id), ifNull(any(r.repo), '')"},
	{"counts", "ReadTeamProjectLinkedPullRequests", "SELECT lr.team_id, toString(lr.pr_repo_id),"},
	{"states", "ReadTeamProjectLinkedPullRequestStates", "SELECT team_id, repo, arrayStringConcat(arrayMap(x -> concat(x.1, '"},
	{"cand", "ReadTeamLinkCandidates", "SELECT groupArray(work_item_id) FROM (SELECT cp.work_item_id AS work_item_id FROM work_items AS cp WHERE"},
	{"lset", "ReadTeamLinkedPullRequestSet", "SELECT groupArray(r), groupArray(n), groupArray(t), groupArray(i), groupArray(k) FROM ("},
}

// linkqlRow is one query_log row of a test statement (a QueryStart row is never read).
type linkqlRow struct {
	typ, query, comment, kind                  string
	readRows, readBytes, memory, recv, threads uint64
	code                                       int32
}

// linkqlRecompute reads every query_log row of the query ids (FuncDecl: it reads memory_usage from system.query_log, so it checks the
// statement threads of the same rows, the statementthreads census rule).
func linkqlRecompute(t *testing.T, ctx context.Context, direct chdriver.Conn, ids []string) []linkqlRow {
	t.Helper()
	if len(ids) == 0 {
		return nil
	}
	var unbound uint64
	if err := direct.QueryRow(ctx, "SELECT "+statementthreads.UnboundThreadsCount+" FROM system.query_log WHERE type = 'QueryFinish' AND has(?, query_id)", ids).Scan(&unbound); err != nil {
		t.Fatalf("thread bound read: %v", err)
	}
	statementthreads.RequireThreadBound(t, ctx, direct, "FORM-339 test statements", unbound)
	rows, err := direct.Query(ctx, `SELECT toString(type), query, log_comment, toString(query_kind), read_rows, read_bytes, memory_usage, ProfileEvents['NetworkReceiveBytes'], toUInt64OrZero(Settings['max_threads']), exception_code FROM system.query_log WHERE has(?, query_id) AND type != 'QueryStart'`, ids)
	if err != nil {
		t.Fatalf("query_log read: %v", err)
	}
	defer rows.Close()
	var out []linkqlRow
	for rows.Next() {
		var r linkqlRow
		if err := rows.Scan(&r.typ, &r.query, &r.comment, &r.kind, &r.readRows, &r.readBytes, &r.memory, &r.recv, &r.threads, &r.code); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// linkqlWant is the Go recompute of the stmt0 (ver "new") or stmt0h (ver "old") columns for the org prefix, by the form's rules.
func linkqlWant(rows []linkqlRow, ver, prefix string) map[string]string {
	other := map[string]string{"new": "old", "old": "new"}[ver]
	w := map[string]uint64{}
	reader := func(r linkqlRow, key string) bool {
		for _, rd := range linkqlReaders {
			if rd.key == key {
				return r.comment == rd.name && strings.HasPrefix(r.query, rd.head)
			}
		}
		return false
	}
	for _, rd := range linkqlReaders {
		for _, c := range []string{"finished", "failed", "failed_241", "max_memory", "max_read_rows", "max_read_bytes", "max_recv_bytes", "max_threads"} {
			if rd.key == "cand" || rd.key == "lset" {
				w[rd.key+"_"+c] = 0
			} else {
				w[rd.key+"_"+ver+"_"+c] = 0
			}
		}
		if rd.key != "cand" && rd.key != "lset" {
			w[rd.key+"_"+other+"_finished"] = 0
		}
	}
	w["moved_other_finished"] = 0
	for _, r := range rows {
		if r.kind != "Select" || !strings.Contains(r.query, "'"+prefix) {
			continue
		}
		named := false
		for _, rd := range linkqlReaders {
			named = named || reader(r, rd.key)
		}
		if !named {
			continue
		}
		mn, mo := strings.Contains(r.query, linkqlMarkNew), strings.Contains(r.query, linkqlMarkOld)
		finish := r.typ == "QueryFinish"
		failed := r.typ == "ExceptionBeforeStart" || r.typ == "ExceptionWhileProcessing"
		isVer := map[string]bool{"new": mn && !mo, "old": mo && !mn}
		for _, key := range []string{"reach", "counts", "states"} {
			if !reader(r, key) {
				continue
			}
			p := key + "_" + ver + "_"
			if isVer[ver] && finish {
				w[p+"finished"]++
				w[p+"max_memory"] = max(w[p+"max_memory"], r.memory)
				w[p+"max_read_rows"] = max(w[p+"max_read_rows"], r.readRows)
				w[p+"max_read_bytes"] = max(w[p+"max_read_bytes"], r.readBytes)
				w[p+"max_recv_bytes"] = max(w[p+"max_recv_bytes"], r.recv)
				w[p+"max_threads"] = max(w[p+"max_threads"], r.threads)
			}
			if isVer[ver] && failed {
				w[p+"failed"]++
				if r.code == 241 {
					w[p+"failed_241"]++
				}
			}
			if isVer[other] && finish {
				w[key+"_"+other+"_finished"]++
			}
			if finish && mn == mo {
				w["moved_other_finished"]++
			}
		}
		for _, key := range []string{"cand", "lset"} {
			if !reader(r, key) {
				continue
			}
			if finish {
				w[key+"_finished"]++
				w[key+"_max_memory"] = max(w[key+"_max_memory"], r.memory)
				w[key+"_max_threads"] = max(w[key+"_max_threads"], r.threads)
			}
			if failed {
				w[key+"_failed"]++
				if r.code == 241 {
					w[key+"_failed_241"]++
				}
			}
		}
	}
	for _, key := range []string{"cand", "lset"} {
		delete(w, key+"_max_read_rows")
		delete(w, key+"_max_read_bytes")
		delete(w, key+"_max_recv_bytes")
	}
	out := map[string]string{}
	for k, v := range w {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func linkqlQuery(ctx context.Context, conn chdriver.Conn, text string) ([]map[string]string, []string, error) {
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

func linkqlFirstLine(err error) string {
	var ex *clickhousedriver.Exception
	if errors.As(err, &ex) {
		return fmt.Sprintf("code %d %s", ex.Code, strings.SplitN(ex.Message, "\n", 2)[0])
	}
	return strings.SplitN(err.Error(), "\n", 2)[0]
}

func linkqlSent(text string) string { return strings.TrimSuffix(strings.TrimRight(text, "\n"), ";") }

func TestForm339LinkQLExecutesOnTheTeamLinkReaders(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("query_log exec proof of a prod read form: the unit legs run it; the race detector changes no server count")
	}
	// 1. The cuts are the run-block texts (swapped; sha256 pins) and hold the form's rules.
	for _, c := range []struct{ label, text, want string }{{"S0", linkqlCutS0, linkqlCutS0SHA256}, {"stmtR", linkqlCutstmtR, linkqlCutstmtRSHA256},
		{"ORG1", linkqlCutORG1, linkqlCutORG1SHA256}, {"stmt0", linkqlCutstmt0, linkqlCutstmt0SHA256}, {"stmt0h", linkqlCutstmt0h, linkqlCutstmt0hSHA256},
		{"past stmt0", linkqlPaststmt0, linkqlPaststmt0SHA256}, {"past stmt0h", linkqlPaststmt0h, linkqlPaststmt0hSHA256}} {
		sum := sha256.Sum256([]byte(c.text))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Fatalf("cut %s sha256 = %s, want %s", c.label, got, c.want)
		}
		t.Logf("CUT %s sha12 %s lines %d", c.label, c.want[:12], strings.Count(c.text, "\n"))
	}
	for _, c := range []struct {
		label, text string
		lits        map[string]int
	}{
		{"stmt0", linkqlCutstmt0, map[string]int{"> 200000000\n": 1, "> 100000000000\n": 1, "> 9000000,": 1, "'STOP-COST-GATE'": 1, "cost_gate = 0": 1, "FROM system.query_log": 2,
			"position(query, '" + linkqlMarkNew + "') > 0 AS m_new": 1, "position(query, '" + linkqlMarkOld + "') > 0 AS m_old": 1, "position(query, concat(''" + "'', '" + linkqlOrgAPrefix + "')) > 0 AS m_org": 1, " AND m_org\n": 1}},
		{"stmt0h", linkqlCutstmt0h, map[string]int{"> 200000000\n": 1, "> 100000000000\n": 1, "> 9000000,": 1, "'STOP-COST-GATE'": 1, "cost_gate = 0": 1, "FROM system.query_log": 2, " AND m_org\n": 1}},
		{"ORG1", linkqlCutORG1, map[string]int{"> 5000000\n": 1, "> 5000000000,": 1, "startsWith(org_id, '" + linkqlOrgAPrefix + "')": 1, "cost_gate = 0": 1}},
	} {
		for lit, n := range c.lits {
			if got := strings.Count(c.text, lit); got != n {
				t.Fatalf("%s: %q found %d times, want %d", c.label, lit, got, n)
			}
		}
	}
	for _, rd := range linkqlReaders {
		lit := "(log_comment = '" + rd.name + "' AND startsWith(query, '" + strings.ReplaceAll(rd.head, "'", "''") + "')) AS r_" + rd.key
		if strings.Count(linkqlCutstmt0, lit) != 1 || strings.Count(linkqlCutstmt0h, lit) != 1 {
			t.Fatalf("reader %s: anchor %q not once in stmt0 / stmt0h", rd.key, lit)
		}
	}

	// 2. The OLD texts (acr a47904e8, the #2207 helpers): each starts with its reader's head and holds the OLD marker only.
	team, project, item := "team-owner", "proj-owner", "i-reached"
	reachedRepo := repoUUID("repo-reached")
	winStart, winEnd := ts(2026, 9, 1, 0, 0, 0), ts(2026, 10, 1, 0, 0, 0)
	olds := devhealthfacts.Form339MainTextsForTest([]string{team}, []string{reachedRepo}, winStart, winEnd)
	for i, o := range olds {
		if o.Reader != linkqlReaders[i].name || !strings.HasPrefix(o.Text, linkqlReaders[i].head) || strings.Contains(o.Text, linkqlMarkNew) || !strings.Contains(o.Text, linkqlMarkOld) {
			t.Fatalf("OLD text %s: reader %s head %v new %v old %v", linkqlReaders[i].key, o.Reader, strings.HasPrefix(o.Text, linkqlReaders[i].head), strings.Contains(o.Text, linkqlMarkNew), strings.Contains(o.Text, linkqlMarkOld))
		}
		sum := sha256.Sum256([]byte(o.Text))
		t.Logf("INFO OLD text %s sha12 %s (evidence window form) bytes %d", o.Reader, hex.EncodeToString(sum[:6]), len(o.Text))
	}

	// 3. A dedicated ClickHouse server, the production schema, two synthetic orgs with one team, one owned project, one linked PR.
	ctx := context.Background()
	rawQuery, direct := newDedicatedClickHouseIntegrationClient(t, ctx)
	query := statementthreads.API(rawQuery)
	// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
	for _, statement := range devhealthschema.DDL("repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	var version string
	if err := direct.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("clickhouse %s", version)
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	for _, org := range []string{linkqlOrgA, linkqlOrgB} {
		// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
		for _, label := range []string{"repo-reached", "repo-filler"} {
			exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), org, "acme/"+label, "github", at)
		}
		exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`, team, team, at, org, "linear", uint8(1))
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, project, org, "linear", nil, project, uint8(1), "active", "", at)
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			org, "linear", team, project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
			item, repoUUID("repo-filler"), org, "title", at, project)
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			reachedRepo, org, uint32(1), "pr", "open", at, at, "feature", "")
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			reachedRepo, item, uint32(1), float32(1), "native", "", at, org)
	}
	moreLinks := func(org string, n int) {
		t.Helper()
		// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('owned-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', ?, NULL FROM numbers(?)`, repoUUID("repo-filler"), org, at, project, uint64(n))
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + 1000), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(?)`, reachedRepo, org, at, at, uint64(n))
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('owned-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?)`, reachedRepo, at, org, uint64(n))
	}

	// 4. The statements. NEW = the real tip read (the pull request fact of the team) through the API client stack under the read-stats
	// instrumentation (log_comment = the reader name); OLD = the a47904e8 texts through the same client under each reader's name.
	provider := findProvider(t, devhealthfacts.NewInstrumentedProvidersWithOptions(query, devhealthfacts.NewReadStatsInstrumentation(nil, nil, nil), devhealthfacts.ProviderOptions{}), contextfabric.FactPullRequests)
	window := contractsv1TimeContext(winStart, ts(2026, 9, 30, 23, 59, 59))
	names := []string{}
	for _, rd := range linkqlReaders {
		names = append(names, rd.name)
	}
	groups := map[string][]string{}
	tipRead := func(label, org string) {
		t.Helper()
		mark := serverNowMicros(t, ctx, direct)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: window, Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{teamSubject(team)},
		})
		if err != nil {
			t.Fatalf("TIP %s ReadFacts: %v: %s", label, err, readFailureCause(err))
		}
		wantInt(t, factFor(t, result, teamSubject(team)).Fields, "project_reached_repository_count", 1)
		if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
			t.Fatal(err)
		}
		rows, err := direct.Query(ctx, `SELECT query_id, log_comment FROM system.query_log WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND has(?, log_comment)`, mark, names)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for rows.Next() {
			var id, comment string
			if err := rows.Scan(&id, &comment); err != nil {
				t.Fatal(err)
			}
			groups[label] = append(groups[label], id)
			seen[comment]++
		}
		rows.Close()
		t.Logf("TIP %s: %d reader statements by name %v", label, len(groups[label]), seen)
		for _, n := range names {
			if seen[n] == 0 {
				t.Fatalf("TIP %s: no QueryFinish row with log_comment %s (the read-stats instrumentation did not name the statement)", label, n)
			}
		}
	}
	send := func(base context.Context, label, reader, text, org string, bindings []contextpacket.ClickHouseBinding) error {
		t.Helper()
		id := "linkql-" + label + "-" + uuid.NewString()[:8]
		qctx := clickhousedriver.Context(contextpacket.WithStatementReader(base, reader), clickhousedriver.WithQueryID(id))
		rows, err := query.Query(qctx, text, append([]contextpacket.ClickHouseBinding{{Name: "org_id", Value: org}}, bindings...))
		groups[label] = append(groups[label], id)
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
		t.Logf("SENT %s (%s) rows %d", label, reader, n)
		return nil
	}
	tipRead("tipA", linkqlOrgA)
	tipRead("tipB", linkqlOrgB)
	// The a47904e8 texts read the candidate ids from the external table _team_link_candidates (withTeamLinkCandidates at a47904e8):
	// each org's old texts are sent with that table, built from the same candidate ids its tip read reads.
	candidates := map[string]context.Context{}
	for _, org := range []string{linkqlOrgA, linkqlOrgB} {
		cctx, n, err := devhealthfacts.Form339CandidatesContextForTest(ctx, rawQuery, org, []string{team})
		if err != nil {
			t.Fatalf("candidates of %s: %v", org[:8], err)
		}
		t.Logf("CANDIDATES org %s: %d id(s) in _team_link_candidates", org[:8], n)
		candidates[org] = cctx
	}
	for _, org := range []struct{ label, id string }{{"oldA", linkqlOrgA}, {"oldB", linkqlOrgB}} {
		for _, o := range olds {
			if err := send(candidates[org.id], org.label, o.Reader, o.Text, org.id, o.Bindings); err != nil {
				t.Fatalf("SENT %s %s: %v", org.label, o.Reader, err)
			}
		}
	}
	// The third text: the reach head + BOTH markers (the new marker as a HAVING literal) = moved_other_finished.
	third := strings.Replace(olds[0].Text, "GROUP BY lr.team_id, lr.pr_repo_id\n\nORDER BY", "GROUP BY lr.team_id, lr.pr_repo_id\nHAVING '"+linkqlMarkNew+"' != ''\nORDER BY", 1)
	if third == olds[0].Text || !strings.HasPrefix(third, linkqlReaders[0].head) || !strings.Contains(third, linkqlMarkNew) || !strings.Contains(third, linkqlMarkOld) {
		t.Fatal("no third text")
	}
	if err := send(candidates[linkqlOrgA], "third", olds[0].Reader, third, linkqlOrgA, olds[0].Bindings); err != nil {
		t.Fatalf("SENT third: %v", err)
	}
	// A statement under the counts reader's name that holds the counts head LATER and the OLD marker (marker, no head at the start).
	later := "SELECT 9 AS lead, '" + linkqlReaders[1].head + "' AS h, '" + linkqlMarkOld + "' AS m, {org_id:String} AS o"
	if err := send(ctx, "later", linkqlReaders[1].name, later, linkqlOrgA, nil); err != nil {
		t.Fatalf("SENT later: %v", err)
	}
	// A refusal at the statement's own cap (code 241) of the OLD reach text, under its name.
	tiny := statementthreads.Capped(rawQuery, 1)
	{
		id := "linkql-refused-" + uuid.NewString()[:8]
		qctx := clickhousedriver.Context(contextpacket.WithStatementReader(candidates[linkqlOrgA], olds[0].Reader), clickhousedriver.WithQueryID(id))
		groups["refused"] = append(groups["refused"], id)
		rows, err := tiny.Query(qctx, olds[0].Text, append([]contextpacket.ClickHouseBinding{{Name: "org_id", Value: linkqlOrgA}}, olds[0].Bindings...))
		if err == nil {
			for rows.Next() {
			}
			err = rows.Err()
			rows.Close()
		}
		if err == nil {
			t.Fatal("SENT refused: no error at a 1-byte cap")
		}
		t.Logf("SENT refused: %s", linkqlFirstLine(err))
	}
	// The larger sent set (cell e): 2,000 more owned links of org A, then the tip read again.
	moreLinks(linkqlOrgA, 2000)
	tipRead("tipA2", linkqlOrgA)
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	ids := func(labels ...string) []string {
		var out []string
		for _, l := range labels {
			out = append(out, groups[l]...)
		}
		return out
	}

	// 5. The org rendering in the logged text (shape only) and the logged texts' markers.
	for _, label := range []string{"tipA", "oldA"} {
		for _, r := range linkqlRecompute(t, ctx, direct, ids(label)) {
			i := strings.Index(r.query, linkqlOrgA)
			if i < 0 {
				t.Errorf("ORG RENDER %s %s: the logged text does not hold the bound org value", label, r.comment)
				continue
			}
			before, after := r.query[max(0, i-2):i], r.query[i+len(linkqlOrgA):min(len(r.query), i+len(linkqlOrgA)+2)]
			t.Logf("ORG RENDER %s %s: %q + <ORG> + %q (m_org = quote + prefix: %v); new marker %v old marker %v", label, r.comment, before, after,
				strings.Contains(r.query, "'"+linkqlOrgAPrefix), strings.Contains(r.query, linkqlMarkNew), strings.Contains(r.query, linkqlMarkOld))
		}
	}

	// 6. Helpers: one form copy (1 row; output content), the exact copy (query_id IN the ids), the column check.
	only := func(text string, labels ...string) string {
		t.Helper()
		var quoted []string
		for _, id := range ids(labels...) {
			quoted = append(quoted, "'"+id+"'")
		}
		out := strings.Replace(text, "AND cost_gate = 0", "AND cost_gate = 0 AND query_id IN ("+strings.Join(quoted, ", ")+")", 1)
		if out == text {
			t.Fatal("no cost_gate conjunct to extend")
		}
		return out
	}
	bad := []string{linkqlOrgA, linkqlOrgB, "SELECT", linkqlMarkNew, linkqlMarkOld, "groupArray", "toString(lr.pr_repo_id)"}
	one := func(label, text string, comment ...string) map[string]string {
		t.Helper()
		qctx := ctx
		if len(comment) > 0 {
			qctx = clickhousedriver.Context(ctx, clickhousedriver.WithSettings(clickhousedriver.Settings{"log_comment": comment[0]}))
		}
		got, types, err := linkqlQuery(qctx, direct, linkqlSent(text))
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: err %v rows %d", label, err, len(got))
		}
		for k, v := range got[0] {
			for _, b := range bad {
				if strings.Contains(v, b) {
					t.Errorf("%s: column %s holds %q (output content)", label, k, b)
				}
			}
		}
		t.Logf("%s ROW %v (%d columns)", label, got[0], len(types))
		return got[0]
	}
	check := func(label string, got, want map[string]string) {
		t.Helper()
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %s, want %s (Go recompute)", label, k, got[k], v)
			}
		}
	}
	every := []string{"tipA", "tipB", "oldA", "oldB", "third", "later", "refused", "tipA2"}

	// 7. S0, stmtR, ORG1.
	s0, _, err := linkqlQuery(ctx, direct, linkqlSent(linkqlCutS0))
	if err != nil || len(s0) < 1 {
		t.Fatalf("S0: err %v rows %d, want >= 1", err, len(s0))
	}
	for _, r := range s0 {
		if r["rows"] == "0" {
			t.Errorf("S0 row %v: rows 0", r)
		}
	}
	t.Logf("PASS S0 ROWS %v", s0)
	if got, _, err := linkqlQuery(ctx, direct, linkqlSent(linkqlCutstmtR)); err != nil || len(got) != 1 || got[0]["replicas"] != "1" {
		t.Errorf("stmtR: rows %v err %v, want replicas 1", got, err)
	} else {
		t.Logf("PASS stmtR ROW %v", got[0])
	}
	if _, _, err := linkqlQuery(ctx, direct, linkqlSent(strings.Replace(linkqlCutstmtR, "'default'", "'linkql_no_such_cluster'", 1))); err == nil {
		t.Errorf("MUTANT stmtR unknown cluster: no error, want a server exception (= STOP)")
	} else {
		t.Logf("MUTANT stmtR unknown cluster: %s (= STOP)", linkqlFirstLine(err))
	}
	if got := one("PASS ORG1", linkqlCutORG1); got["orgs"] != "1" {
		t.Errorf("ORG1 orgs = %s, want 1", got["orgs"])
	}
	if got := one("MUTANT ORG1 prefix of no org", strings.Replace(linkqlCutORG1, "'"+linkqlOrgAPrefix+"'", "'00000000'", 1)); got["orgs"] != "0" {
		t.Errorf("MUTANT ORG1 prefix of no org: orgs = %s, want 0", got["orgs"])
	}

	// 8. stmt0 / stmt0h server-wide and the exact copies vs the Go recompute.
	pass0 := "linkql-pass0-" + uuid.NewString()[:8]
	passH := "linkql-passh-" + uuid.NewString()[:8]
	clean0 := one("PASS stmt0 (server-wide)", linkqlCutstmt0, pass0)
	cleanH := one("PASS stmt0h (server-wide)", linkqlCutstmt0h, passH)
	all := linkqlRecompute(t, ctx, direct, ids(every...))
	check("stmt0 server-wide", clean0, linkqlWant(all, "new", linkqlOrgAPrefix))
	check("stmt0h server-wide", cleanH, linkqlWant(all, "old", linkqlOrgAPrefix))
	check("EXACT stmt0 all", one("EXACT stmt0 all", only(linkqlCutstmt0, every...)), linkqlWant(all, "new", linkqlOrgAPrefix))
	check("EXACT stmt0h all", one("EXACT stmt0h all", only(linkqlCutstmt0h, every...)), linkqlWant(all, "old", linkqlOrgAPrefix))
	for _, k := range []string{"reach", "counts", "states"} {
		if clean0[k+"_new_finished"] == "0" || cleanH[k+"_old_finished"] == "0" {
			t.Errorf("%s: new finished %s (stmt0) old finished %s (stmt0h), want both > 0 (the CI reads)", k, clean0[k+"_new_finished"], cleanH[k+"_old_finished"])
		}
		if cleanH[k+"_old_failed_241"] == "0" && k == "reach" {
			t.Errorf("reach_old_failed_241 = 0, want >= 1 (the 1-byte cap refusal)")
		}
	}
	for _, k := range []string{"cand", "lset"} {
		if clean0[k+"_finished"] == "0" {
			t.Errorf("%s_finished = 0, want > 0", k)
		}
	}
	if clean0["moved_other_finished"] != "1" || cleanH["moved_other_finished"] != "1" {
		t.Errorf("moved_other_finished stmt0 %s stmt0h %s, want 1 / 1 (the third text)", clean0["moved_other_finished"], cleanH["moved_other_finished"])
	}
	for _, k := range []string{"reach", "counts", "states"} {
		if got := clean0[k+"_new_max_memory"]; got > "" {
			var n uint64
			fmt.Sscan(got, &n)
			if n > linkqlAPICap80 {
				t.Errorf("%s_new_max_memory %d over 80 %% of the API cap on the CI rows", k, n)
			}
		}
		if clean0[k+"_new_max_threads"] != fmt.Sprint(contextpacket.StatementMaxThreads) {
			t.Errorf("%s_new_max_threads = %s, want %d (the client's statement thread count)", k, clean0[k+"_new_max_threads"], contextpacket.StatementMaxThreads)
		}
	}

	// 9. The org rule: org B's statements count nowhere under A's prefix; B's prefix counts B only.
	rowsA := linkqlRecompute(t, ctx, direct, ids("tipA", "oldA"))
	rowsB := linkqlRecompute(t, ctx, direct, ids("tipB", "oldB"))
	isZeroCounts := func(label string, got map[string]string) {
		t.Helper()
		for k, v := range got {
			if (strings.HasSuffix(k, "_finished") || strings.HasSuffix(k, "_failed")) && v != "0" {
				t.Errorf("%s: %s = %s, want 0", label, k, v)
			}
		}
	}
	isZeroCounts("EXACT stmt0 org B ids under A's prefix", one("EXACT stmt0 org B under A", only(linkqlCutstmt0, "tipB", "oldB")))
	isZeroCounts("EXACT stmt0h org B ids under A's prefix", one("EXACT stmt0h org B under A", only(linkqlCutstmt0h, "tipB", "oldB")))
	prefB := func(text string) string {
		return strings.Replace(text, "'"+linkqlOrgAPrefix+"'", "'"+linkqlOrgBPrefix+"'", 1)
	}
	check("MUTANT stmt0 org B prefix", one("MUTANT stmt0 org B prefix", only(prefB(linkqlCutstmt0), "tipA", "tipB", "oldA", "oldB")), linkqlWant(rowsB, "new", linkqlOrgBPrefix))
	check("MUTANT stmt0h org B prefix", one("MUTANT stmt0h org B prefix", only(prefB(linkqlCutstmt0h), "tipA", "tipB", "oldA", "oldB")), linkqlWant(rowsB, "old", linkqlOrgBPrefix))
	check("EXACT stmt0 org A", one("EXACT stmt0 org A", only(linkqlCutstmt0, "tipA", "tipB", "oldA", "oldB")), linkqlWant(rowsA, "new", linkqlOrgAPrefix))

	// 10. Mutants: each changes ONE rule; the count must change.
	mut := func(label, text, from, to string, labels []string, check func(map[string]string) bool) {
		t.Helper()
		m := strings.Replace(text, from, to, 1)
		if m == text {
			t.Fatalf("%s: nothing to mutate (%q)", label, from)
		}
		if got := one(label, only(m, labels...)); !check(got) {
			t.Errorf("%s: the count did not change as the rule wants: %v", label, got)
		}
	}
	mut("MUTANT new marker literal", linkqlCutstmt0, "'"+linkqlMarkNew+"') > 0 AS m_new", "'"+linkqlMarkNew[:len(linkqlMarkNew)-1]+"z') > 0 AS m_new", []string{"tipA"},
		func(g map[string]string) bool {
			return g["reach_new_finished"] == "0" && g["counts_new_finished"] == "0" && g["states_new_finished"] == "0" && g["moved_other_finished"] != "0"
		})
	mut("MUTANT old marker literal", linkqlCutstmt0h, "'"+linkqlMarkOld+"') > 0 AS m_old", "'"+linkqlMarkOld[:len(linkqlMarkOld)-1]+"q') > 0 AS m_old", []string{"oldA"},
		func(g map[string]string) bool {
			return g["reach_old_finished"] == "0" && g["counts_old_finished"] == "0" && g["states_old_finished"] == "0" && g["moved_other_finished"] != "0"
		})
	for _, rd := range linkqlReaders {
		col := rd.key + "_new_finished"
		if rd.key == "cand" || rd.key == "lset" {
			col = rd.key + "_finished"
		}
		headSQL := strings.ReplaceAll(rd.head, "'", "''")
		mut("MUTANT head literal "+rd.key, linkqlCutstmt0, "startsWith(query, '"+headSQL+"')", "startsWith(query, '"+headSQL+"~')", []string{"tipA"},
			func(g map[string]string) bool { return g[col] == "0" })
		mut("MUTANT log_comment literal "+rd.key, linkqlCutstmt0, "log_comment = '"+rd.name+"'", "log_comment = '"+rd.name+"x'", []string{"tipA"},
			func(g map[string]string) bool { return g[col] == "0" })
	}
	// The later statement (counts name, OLD marker, the counts head NOT at the start): 0 clean; counted when startsWith becomes position.
	if g := one("EXACT later (head not at the start)", only(linkqlCutstmt0h, "later")); g["counts_old_finished"] != "0" || g["moved_other_finished"] != "0" {
		t.Errorf("EXACT later: counts_old_finished %s moved_other %s, want 0 / 0", g["counts_old_finished"], g["moved_other_finished"])
	}
	countsHead := strings.ReplaceAll(linkqlReaders[1].head, "'", "''")
	mut("MUTANT startsWith -> position (counts)", linkqlCutstmt0h, "startsWith(query, '"+countsHead+"')", "position(query, '"+countsHead+"') > 0", []string{"later"},
		func(g map[string]string) bool { return g["counts_old_finished"] == "1" })
	// A marker without a head: the link set statement holds the OLD marker but counts only as lset, never as a moved reader.
	if g := one("EXACT tip phase A (marker, no moved head)", only(linkqlCutstmt0h, "tipA")); g["reach_old_finished"] != "0" || g["counts_old_finished"] != "0" || g["states_old_finished"] != "0" {
		t.Errorf("EXACT tip under stmt0h: a moved reader counted the phase-A statements: %v", g)
	}
	// The window: a past window holds none of the CI rows.
	for _, c := range []struct{ label, text string }{{"MUTANT past window stmt0", linkqlPaststmt0}, {"MUTANT past window stmt0h", linkqlPaststmt0h}} {
		isZeroCounts(c.label, one(c.label, only(c.text, every...)))
	}

	// 11. The sent set (cell e): the NEW moved readers receive more bytes with the larger set.
	small := one("EXACT tipA (1 link)", only(linkqlCutstmt0, "tipA"))
	large := one("EXACT tipA2 (2,001 links)", only(linkqlCutstmt0, "tipA2"))
	for _, k := range []string{"reach", "counts", "states"} {
		var s, l uint64
		fmt.Sscan(small[k+"_new_max_recv_bytes"], &s)
		fmt.Sscan(large[k+"_new_max_recv_bytes"], &l)
		t.Logf("RECV %s: %d B at 1 link, %d B at 2,001 links", k, s, l)
		if l <= s {
			t.Errorf("RECV %s: %d B at 2,001 links is not above %d B at 1 link (NetworkReceiveBytes does not follow the sent set)", k, l, s)
		}
	}

	// 12. Gate fires and the pass / fire read bounds.
	type gate struct{ label, text, lit string }
	gates := []gate{{"ORG1", linkqlCutORG1, "> 5000000\n"}, {"ORG1", linkqlCutORG1, "> 5000000000,"},
		{"stmt0", linkqlCutstmt0, "> 200000000\n"}, {"stmt0", linkqlCutstmt0, "> 100000000000\n"}, {"stmt0", linkqlCutstmt0, "> 9000000,"},
		{"stmt0h", linkqlCutstmt0h, "> 200000000\n"}, {"stmt0h", linkqlCutstmt0h, "> 100000000000\n"}, {"stmt0h", linkqlCutstmt0h, "> 9000000,"}}
	fires := map[string]string{}
	for _, g := range gates {
		comment := "linkql-gate-" + uuid.NewString()[:8]
		fctx := clickhousedriver.Context(ctx, clickhousedriver.WithSettings(clickhousedriver.Settings{"log_comment": comment}))
		_, _, err := linkqlQuery(fctx, direct, linkqlSent(strings.Replace(g.text, g.lit, "> 0"+g.lit[len(g.lit)-1:], 1)))
		var gx *clickhousedriver.Exception
		if err == nil || !errors.As(err, &gx) || gx.Code != 395 || !strings.Contains(gx.Message, "STOP-COST-GATE") {
			t.Errorf("GATE %s %q lowered to 0: err = %v, want code 395 STOP-COST-GATE", g.label, g.lit, err)
			continue
		}
		t.Logf("GATE %s %q lowered to 0: fired code=%d", g.label, g.lit, gx.Code)
		fires[comment] = g.label
	}
	if len(fires) != len(gates) {
		t.Errorf("gates fired %d times, want %d", len(fires), len(gates))
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	var w, p, pr uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM system.query_log WHERE event_date >= toDate('2026-01-01') AND event_time >= toDateTime('2026-01-01 00:00:00', 'UTC')`).Scan(&w); err != nil {
		t.Fatal(err)
	}
	if err := direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE active AND database = 'system' AND table = 'query_log'`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	if err := direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE active AND database = currentDatabase() AND table = 'repos'`).Scan(&pr); err != nil {
		t.Fatal(err)
	}
	logOf := func(comment string) ([]string, uint64) {
		t.Helper()
		rows, err := direct.Query(ctx, `SELECT toString(type), read_rows FROM system.query_log WHERE log_comment = ? AND type != 'QueryStart'`, comment)
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
	slack := 2 * 8192 * (p + pr)
	for _, c := range []string{pass0, passH} {
		if types, read := logOf(c); len(types) != 1 || types[0] != "QueryFinish" || read > 2*w+2*p+1+slack {
			t.Errorf("PASS %s query_log types %v read_rows %d, want [QueryFinish] and <= %d (2W + 2P + 1 + slack, W %d P %d)", c, types, read, 2*w+2*p+1+slack, w, p)
		} else {
			t.Logf("PASS %s query_log types %v read_rows %d (2W + 2P + 1 = %d, + slack %d; W %d P %d)", c, types, read, 2*w+2*p+1, slack, w, p)
		}
	}
	for c, label := range fires {
		bound := w + 2*p + 1 + slack
		if label == "ORG1" {
			bound = 2*pr + 1 + slack
		}
		if types, read := logOf(c); len(types) != 1 || types[0] != "ExceptionBeforeStart" || read > bound {
			t.Errorf("GATE %s %s query_log types %v read_rows %d, want [ExceptionBeforeStart] and <= %d", label, c, types, read, bound)
		} else {
			t.Logf("GATE %s %s query_log types %v read_rows %d (bound %d)", label, c, types, read, bound)
		}
	}
}
