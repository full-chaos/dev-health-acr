package devhealthfacts_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// TestProbeReadOnceForms reports rows read, peak memory and duration of the
// scoped historical statements as served and in two forms that build the
// candidate set once, at two organization sizes, on unmerged and merged
// parts, with prod-width ids and thousands of requested work items. It fails
// on purpose so the hosted log carries the numbers.
func TestProbeReadOnceForms(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	fixture := seedEveryRootFixture(t, ctx, direct, "9e4b6c13-8a27-4f50-b1d9-3c7a5e0f2b64")
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	// devhealthschema:not-a-production-replica: test seed over a table the shared DDL creates; it declares no column, type or engine.
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', 'issue-Q', 'linear', '', 'Q', '', '', '', ?, ?, 'linear:issue-Q-add')`, []any{fixture.orgID, fixture.shared, since, since}},
		// Thousands of requested work items, ids as wide as production's.
		{`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at)
SELECT concat('req-', repeat('x', 64), '-', toString(number)), ?, ?, 'linear', 'requested', 'open', '', '', 'P', ? FROM numbers(5000)`, []any{fixture.shared, fixture.orgID, since}},
		{`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('req-', repeat('x', 64), '-', toString(number)), toUInt32(number % 1000 + 10), 1, 'native', '', ?, ? FROM numbers(5000)`, []any{fixture.shared, since, fixture.orgID}},
		{`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id)
SELECT ?, NULL, ?, 'work_item', concat('req-', repeat('x', 64), '-', toString(number)), 'linear', '', 'P', '', '', '', ?, ?, concat('req-event-', toString(number)) FROM numbers(5000)`, []any{fixture.orgID, fixture.shared, since, since}},
	} {
		if err := direct.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, seed.sql)
		}
	}
	recorder := &recordingQueryClient{inner: query}
	expander := devhealthfacts.NewScopeExpander(recorder)
	candidates := devhealthfacts.HistoricalCandidatesForTest()
	const idsParam = "{candidate_ids:Array(String)}"
	once := "(SELECT arrayJoin((SELECT groupUniqArray(work_item_id) FROM " + candidates + ")))"

	type stats struct{ memory, readRows, durationMS, scalarMiss uint64 }
	// devhealthschema:not-a-production-replica: probe statements and query_log lookups over tables the shared DDL creates; they declare none.
	statsOf := func(t *testing.T, table, fragment string, mark int64) stats {
		t.Helper()
		if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
			t.Fatalf("flush logs: %v", err)
		}
		var s stats
		if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_rows), max(query_duration_ms), max(ProfileEvents['ScalarSubqueriesCacheMiss']) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.', ?)) AND position(query, ?) > 0`, mark, table, fragment).Scan(&s.memory, &s.readRows, &s.durationMS, &s.scalarMiss); err != nil {
			t.Fatalf("read query_log: %v", err)
		}
		return s
	}
	// run sends one statement 3 warm times (after one unmeasured) and returns
	// the lowest and highest of each measure.
	run := func(t *testing.T, label, table, fragment, statement string, bindings []contextpacket.ClickHouseBinding) (low, high stats) {
		t.Helper()
		send := func() stats {
			mark := serverNowMicros(t, ctx, direct)
			rows, err := query.Query(ctx, statement, bindings)
			if err != nil {
				t.Errorf("PROBE9617 %s: %v", label, err)
				return stats{}
			}
			for rows.Next() {
			}
			_ = rows.Close()
			return statsOf(t, table, fragment, mark)
		}
		send()
		var all []stats
		for i := 0; i < 3; i++ {
			all = append(all, send())
		}
		pick := func(f func(stats) uint64, fn func(...uint64) uint64) uint64 {
			var v []uint64
			for _, s := range all {
				v = append(v, f(s))
			}
			return fn(v...)
		}
		lo := func(v ...uint64) uint64 { return slices.Min(v) }
		hi := func(v ...uint64) uint64 { return slices.Max(v) }
		low = stats{pick(func(s stats) uint64 { return s.memory }, lo), pick(func(s stats) uint64 { return s.readRows }, lo), pick(func(s stats) uint64 { return s.durationMS }, lo), pick(func(s stats) uint64 { return s.scalarMiss }, lo)}
		high = stats{pick(func(s stats) uint64 { return s.memory }, hi), pick(func(s stats) uint64 { return s.readRows }, hi), pick(func(s stats) uint64 { return s.durationMS }, hi), pick(func(s stats) uint64 { return s.scalarMiss }, hi)}
		return low, high
	}
	served := func(t *testing.T) map[string]capturedStatement {
		recorder.reset()
		for _, tc := range fixture.cases() {
			if tc.name == "project repositories as of" {
				if _, err := expander.ExpandFactScope(ctx, tc.request); err != nil {
					t.Fatalf("ExpandFactScope: %v: %s", err, readFailureCause(err))
				}
			}
		}
		out := map[string]capturedStatement{}
		for label, fragment := range map[string]string{"drop": "AS ever_matched", "touch": "AS malformed_touches"} {
			captured, ok := recorder.find(fragment)
			if !ok {
				t.Fatalf("%s statement was not sent", label)
			}
			out[label] = captured
		}
		return out
	}
	// devhealthschema:not-a-production-replica: probe statements and query_log lookups over tables the shared DDL creates; they declare none.
	measure := func(t *testing.T, state string) {
		statements := served(t)
		for _, label := range []string{"touch", "drop"} {
			captured := statements[label]
			table, fragment := "project_membership_transitions", "AS malformed_touches"
			if label == "drop" {
				table, fragment = "work_graph_issue_pr", "AS ever_matched"
			}
			if !strings.Contains(captured.statement, candidates) {
				t.Fatalf("%s: the served statement does not carry the candidate set text", label)
			}
			body := captured.statement
			if at := strings.LastIndex(body, "\nSETTINGS "); at >= 0 {
				body = body[:at]
			}
			bindings := captured.bindings
			report := func(variant string, low, high stats, extra string) {
				t.Errorf("PROBE9617 %s %s %s: memory low=%d high=%d, read_rows low=%d high=%d, duration_ms low=%d high=%d, scalar_miss=%d%s", state, label, variant, low.memory, high.memory, low.readRows, high.readRows, low.durationMS, high.durationMS, high.scalarMiss, extra)
			}
			// served
			lowS, highS := run(t, state+label+"served", table, fragment, captured.statement, bindings)
			report("served", lowS, highS, "")
			servedRows := scopeRows(t, ctx, query, captured)
			// scalar once: the candidate set read by one cached scalar subquery
			scalarStatement := strings.ReplaceAll(body, candidates, once)
			lowO, highO := run(t, state+label+"scalar", table, fragment, scalarStatement, bindings)
			report("scalar-once", lowO, highO, "")
			scalarRows := scopeRows(t, ctx, query, capturedStatement{statement: scalarStatement, bindings: bindings})
			if !slices.Equal(servedRows, scalarRows) {
				t.Errorf("PROBE9617 %s %s scalar-once rows differ: served %q scalar %q", state, label, servedRows, scalarRows)
			}
			// ids as a bound array: one extra statement reads the candidate ids once
			mark := serverNowMicros(t, ctx, direct)
			idRows, err := query.Query(ctx, `SELECT groupUniqArray(work_item_id) AS probe_ids FROM `+candidates, bindings[:2])
			if err != nil {
				t.Fatalf("candidate ids: %v", err)
			}
			var ids []string
			for idRows.Next() {
				if err := idRows.Scan(&ids); err != nil {
					t.Fatalf("scan ids: %v", err)
				}
			}
			_ = idRows.Close()
			idStats := statsOf(t, "work_items", "AS probe_ids", mark)
			arrayStatement := strings.ReplaceAll(body, candidates, idsParam)
			arrayBindings := append(append([]contextpacket.ClickHouseBinding(nil), bindings...), contextpacket.ClickHouseBinding{Name: "candidate_ids", Value: ids})
			lowA, highA := run(t, state+label+"array", table, fragment, arrayStatement, arrayBindings)
			report("array-once", lowA, highA, " (ids "+strconv.Itoa(len(ids))+"; id query memory="+strconv.Itoa(int(idStats.memory))+" read_rows="+strconv.Itoa(int(idStats.readRows))+" duration_ms="+strconv.Itoa(int(idStats.durationMS))+")")
			arrayRows := scopeRows(t, ctx, query, capturedStatement{statement: arrayStatement, bindings: arrayBindings})
			if !slices.Equal(servedRows, arrayRows) {
				t.Errorf("PROBE9617 %s %s array-once rows differ: served %q array %q", state, label, servedRows, arrayRows)
			}
		}
	}

	fillScopeOrganization(t, ctx, direct, fixture, "small", 100_000)
	measure(t, "small unmerged")
	mergeScopeTables(t, ctx, direct)
	measure(t, "small merged")
	fillScopeOrganization(t, ctx, direct, fixture, "large", 200_000)
	measure(t, "large unmerged")
	mergeScopeTables(t, ctx, direct)
	measure(t, "large merged")
}
