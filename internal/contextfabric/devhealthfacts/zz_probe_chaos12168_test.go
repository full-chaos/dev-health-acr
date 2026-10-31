package devhealthfacts_test

// INVESTIGATION PROBE (CHAOS-12168), throwaway branch only, never merged.
// ACR_PROBE_UNTRACKED=1 gives the shared ClickHouse container a users.d
// profile with max_untracked_memory = 1, so the per-query memory tracker sees
// every allocation instead of per-thread 4 MiB steps. probeWindow logs every
// statement a memory measurement's query_log window holds (both modes), and in
// probe mode fails unless the server really runs with the setting (a probe
// that did not happen must fail, not pass).

import (
	"context"
	"os"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/testcontainers/testcontainers-go"
)

func probeUntracked() bool { return os.Getenv("ACR_PROBE_UNTRACKED") == "1" }

const probeUntrackedProfile = `<clickhouse><profiles><default><max_untracked_memory>1</max_untracked_memory></default></profiles></clickhouse>`

// probeContainerFiles is the users.d file the probe adds to the shared container.
func probeContainerFiles() []testcontainers.ContainerFile {
	if !probeUntracked() {
		return nil
	}
	return []testcontainers.ContainerFile{{
		Reader:            strings.NewReader(probeUntrackedProfile),
		ContainerFilePath: "/etc/clickhouse-server/users.d/zz-probe-untracked.xml",
		FileMode:          0o644,
	}}
}

// probeWindow logs the statements of one measurement window and, in probe
// mode, requires the server-side setting.
func probeWindow(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, label, fragment string, mark int64) {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("probe: flush logs: %v", err)
	}
	if probeUntracked() {
		var effective string
		if err := direct.QueryRow(ctx, `SELECT toString(getSetting('max_untracked_memory'))`).Scan(&effective); err != nil {
			t.Fatalf("probe: read max_untracked_memory: %v", err)
		}
		if effective != "1" {
			t.Fatalf("probe did not happen: the server runs max_untracked_memory = %s, want 1", effective)
		}
	}
	rows, err := direct.Query(ctx, `SELECT memory_usage, Settings['max_untracked_memory'], Settings['max_threads'], peak_threads_usage, replaceRegexpAll(substring(query, 1, 90), '\\s+', ' ')
FROM system.query_log WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select' AND position(query, ?) > 0 ORDER BY event_time_microseconds`, mark, fragment)
	if err != nil {
		t.Fatalf("probe: read query_log: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var memory, peakThreads uint64
		var untracked, threads, head string
		if err := rows.Scan(&memory, &untracked, &threads, &peakThreads, &head); err != nil {
			t.Fatalf("probe: scan: %v", err)
		}
		lines = append(lines, strings.Join([]string{"mem=" + probeItoa(memory), "untracked=" + untracked, "max_threads=" + threads, "peak_threads=" + probeItoa(peakThreads), "head=" + head}, " "))
	}
	t.Logf("PROBE WINDOW %s n=%d probe=%v | %s", label, len(lines), probeUntracked(), strings.Join(lines, " || "))
}

func probeItoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
