package devhealthfacts_test

// INVESTIGATION PROBE (CHAOS-12168), throwaway branch only, never merged.
// ACR_PROBE_UNTRACKED=1 runs every measured statement of the two memory
// tests with max_untracked_memory = 1, so the per-query memory tracker sees
// every allocation instead of per-thread 4 MiB steps. probeRequireApplied
// fails the test when a probed statement did not carry the setting (a probe
// that did not happen must fail, not pass).

import (
	"context"
	"os"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

func probeUntracked() bool { return os.Getenv("ACR_PROBE_UNTRACKED") == "1" }

// probeClause turns a file-hotspots SETTINGS clause ("" or "\nSETTINGS ...")
// into the probed one.
func probeClause(clause string) string {
	if !probeUntracked() {
		return clause
	}
	if clause == "" {
		return "\nSETTINGS max_untracked_memory = 1"
	}
	return clause + ", max_untracked_memory = 1"
}

// probeStatement appends max_untracked_memory = 1 to a statement's top-level
// SETTINGS clause (the last SETTINGS after the last parenthesis), or adds one.
func probeStatement(statement string) string {
	if !probeUntracked() {
		return statement
	}
	trimmed := strings.TrimRight(statement, " \n\t;")
	at := strings.LastIndex(trimmed, "SETTINGS ")
	if at >= 0 && !strings.Contains(trimmed[at:], ")") {
		return trimmed + ", max_untracked_memory = 1"
	}
	return trimmed + "\nSETTINGS max_untracked_memory = 1"
}

// probeRequireApplied checks query_log: every Select since mark whose text
// contains fragment carries max_untracked_memory = 1 (probe mode only).
func probeRequireApplied(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, fragment string, mark int64) {
	t.Helper()
	if !probeUntracked() {
		return
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("probe: flush logs: %v", err)
	}
	var total, applied uint64
	if err := direct.QueryRow(ctx, `SELECT count(), countIf(Settings['max_untracked_memory'] = '1') FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select' AND position(query, ?) > 0`, mark, fragment).Scan(&total, &applied); err != nil {
		t.Fatalf("probe: read query_log: %v", err)
	}
	if total == 0 || applied != total {
		t.Fatalf("probe did not happen: %d of %d measured statements carried max_untracked_memory = 1", applied, total)
	}
}
