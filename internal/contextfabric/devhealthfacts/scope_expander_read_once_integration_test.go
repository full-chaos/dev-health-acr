package devhealthfacts_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// readOnceRowsCeiling is the most rows either historical statement may read at
// the larger size. Measured on hosted CI (5,000 requested work items with
// 70-character ids, merged and unmerged parts): the statements as sent read
// 733,757 (drop) and 656,632 (touch) rows at most; the same statements with the
// candidate set referenced as a plain subquery read 4,413,844 and 2,956,686.
// 1.0M is above the sent form with room for the 10% margin of the slope guard
// (807k) and far below the plain form, so a reference that scans the
// organization again fails here by value.
const readOnceRowsCeiling = uint64(1_000_000)

// readOnceMemoryCeiling is the most peak memory either statement may use, taken
// from the highest of the warm reads. Measured on hosted CI: the highest read of
// the sent form was 45.47 MB (drop, larger size, merged parts) and the spread of
// one statement's warm reads up to 6.0 MB, so 45.47 + 6.0 = 51.5 MB, rounded up
// to 52 MiB. The plain form peaked at 42.8 MB: the ceiling is a cap on the sent
// form, not a claim that it uses less.
const readOnceMemoryCeiling = uint64(52 << 20)

type readOnceStats struct{ memory, readRows, scalarMiss uint64 }

// TestScopedHistoricalStatementsReadTheCandidatesOnceOnRealClickHouse guards
// the candidate set of the scoped historical statements being built once per
// statement, at the width and size production has: 5,000 requested work items
// with 70-character ids, two organization sizes, unmerged and merged parts.
// Each statement sent must (1) return the rows the plain-subquery form returns,
// (2) read at most readOnceRowsCeiling rows and use at most
// readOnceMemoryCeiling bytes on its highest warm read, and (3) evaluate the
// scalar subquery once. At the larger size the plain form must break the rows
// ceiling on its lowest read, so the row can fail on this data.
func TestScopedHistoricalStatementsReadTheCandidatesOnceOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	rawQuery, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	query := statementthreads.API(rawQuery)
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	fixture := seedEveryRootFixture(t, ctx, direct, "9e4b6c13-8a27-4f50-b1d9-3c7a5e0f2b64")
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	// devhealthschema:not-a-production-replica: test seed over tables the shared DDL creates; it declares no column, type or engine.
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', 'issue-Q', 'linear', '', 'Q', '', '', '', ?, ?, 'linear:issue-Q-add')`, []any{fixture.orgID, fixture.shared, since, since}},
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
	plain, once := devhealthfacts.HistoricalCandidateSetsForTest()

	// devhealthschema:not-a-production-replica: query_log lookups over tables the shared DDL creates; they declare none.
	statsOf := func(t *testing.T, table, fragment string, mark int64) readOnceStats {
		t.Helper()
		if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
			t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
		}
		var s readOnceStats
		var threadless uint64
		if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_rows), max(ProfileEvents['ScalarSubqueriesCacheMiss']), `+statementthreads.ThreadlessCount+` FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.', ?)) AND position(query, ?) > 0`, mark, table, fragment).Scan(&s.memory, &s.readRows, &s.scalarMiss, &threadless); err != nil {
			t.Fatalf("read query_log: %v", err)
		}
		statementthreads.RequireThreadBound(t, fragment, threadless)
		return s
	}
	// sample sends a statement once unmeasured and three warm times, and
	// returns the lowest and highest read of each measure.
	sample := func(t *testing.T, table, fragment, statement string, bindings []contextpacket.ClickHouseBinding) (low, high readOnceStats) {
		t.Helper()
		send := func() readOnceStats {
			mark := serverNowMicros(t, ctx, direct)
			rows, err := query.Query(ctx, statement, bindings)
			if err != nil {
				t.Fatalf("send %q: %v: %s", fragment, err, readFailureCause(err))
			}
			for rows.Next() {
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("read %q: %v", fragment, err)
			}
			_ = rows.Close()
			return statsOf(t, table, fragment, mark)
		}
		send()
		reads := []readOnceStats{send(), send(), send()}
		for i, s := range reads {
			if i == 0 {
				low, high = s, s
				continue
			}
			low = readOnceStats{min(low.memory, s.memory), min(low.readRows, s.readRows), min(low.scalarMiss, s.scalarMiss)}
			high = readOnceStats{max(high.memory, s.memory), max(high.readRows, s.readRows), max(high.scalarMiss, s.scalarMiss)}
		}
		return low, high
	}
	served := func(t *testing.T) map[string]capturedStatement {
		t.Helper()
		recorder.reset()
		for _, tc := range fixture.cases() {
			if tc.name != "project repositories as of" {
				continue
			}
			if _, err := expander.ExpandFactScope(ctx, tc.request); err != nil {
				t.Fatalf("ExpandFactScope: %v: %s", err, readFailureCause(err))
			}
		}
		out := map[string]capturedStatement{}
		for label, fragment := range map[string]string{"drop": "AS ever_matched", "touch": "AS malformed_touches"} {
			captured, ok := recorder.find(fragment)
			if !ok {
				t.Fatalf("the %s statement was not sent", label)
			}
			if !strings.Contains(captured.statement, once) || strings.Contains(strings.ReplaceAll(captured.statement, once, ""), plain) {
				t.Fatalf("the %s statement does not carry the candidate set only in its read-once form", label)
			}
			out[label] = captured
		}
		return out
	}
	measure := func(t *testing.T, state string, larger bool) {
		t.Helper()
		statements := served(t)
		// devhealthschema:not-a-production-replica: statement fragments and query_log table names that select the guarded statements; they declare no table.
		for _, statement := range []struct{ label, table, fragment string }{
			{"touch", "project_membership_transitions", "AS malformed_touches"},
			{"drop", "work_graph_issue_pr", "AS ever_matched"},
		} {
			captured := statements[statement.label]
			low, high := sample(t, statement.table, statement.fragment, captured.statement, captured.bindings)
			t.Logf("READONCE %s %s sent: memory lowest=%d highest=%d (ceiling %d), read_rows lowest=%d highest=%d (ceiling %d), scalar cache misses lowest=%d highest=%d (want 1)", state, statement.label, low.memory, high.memory, readOnceMemoryCeiling, low.readRows, high.readRows, readOnceRowsCeiling, low.scalarMiss, high.scalarMiss)
			if high.memory == 0 || high.readRows == 0 {
				t.Fatalf("%s %s: measurement did not happen (%+v)", state, statement.label, high)
			}
			if high.memory > readOnceMemoryCeiling {
				t.Errorf("%s %s peaked at %d bytes on its highest warm read, want at most %d", state, statement.label, high.memory, readOnceMemoryCeiling)
			}
			if high.readRows > readOnceRowsCeiling {
				t.Errorf("%s %s read %d rows on its highest warm read, want at most %d: the candidate set is scanned again", state, statement.label, high.readRows, readOnceRowsCeiling)
			}
			if low.scalarMiss != 1 || high.scalarMiss != 1 {
				t.Errorf("%s %s evaluated its scalar subquery %d to %d times, want once", state, statement.label, low.scalarMiss, high.scalarMiss)
			}
			// The same statement with the candidate set referenced as a plain
			// subquery returns the same rows.
			body := captured.statement
			if at := strings.LastIndex(body, "\nSETTINGS "); at >= 0 {
				body = body[:at]
			}
			plainStatement := capturedStatement{statement: strings.ReplaceAll(body, once, plain), bindings: captured.bindings}
			sentRows, plainRows := scopeRows(t, ctx, query, capturedStatement{statement: body, bindings: captured.bindings}), scopeRows(t, ctx, query, plainStatement)
			if len(sentRows) == 0 || !slices.Equal(sentRows, plainRows) {
				t.Errorf("%s %s: rows differ from the plain-subquery form (or none were returned): sent %q, plain %q", state, statement.label, sentRows, plainRows)
			}
			// The control: the plain form breaks the rows ceiling on its
			// lowest read at the larger size.
			if larger {
				plainLow, _ := sample(t, statement.table, statement.fragment, plainStatement.statement, plainStatement.bindings)
				t.Logf("READONCE %s %s plain form: read_rows lowest=%d (ceiling %d)", state, statement.label, plainLow.readRows, readOnceRowsCeiling)
				if plainLow.readRows <= readOnceRowsCeiling {
					t.Errorf("%s %s: the plain form read %d rows on its lowest read, not above %d: the row cannot fail on this data", state, statement.label, plainLow.readRows, readOnceRowsCeiling)
				}
			}
		}
	}

	fillScopeOrganization(t, ctx, direct, fixture, "small", 100_000)
	measure(t, "smaller size, unmerged parts", false)
	mergeScopeTables(t, ctx, direct)
	measure(t, "smaller size, merged parts", false)
	fillScopeOrganization(t, ctx, direct, fixture, "large", 200_000)
	measure(t, "larger size, unmerged parts", true)
	mergeScopeTables(t, ctx, direct)
	measure(t, "larger size, merged parts", true)
}
