package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

// A statement whose cost must follow its own evidence, not the size of the
// organization, is guarded twice. The slope guard is the property: each
// statement's peak memory, measured at two organization sizes, may not grow by
// more than statementMemorySlope. Statements are matched by query_log's
// normalized_query_hash, so a large statement cannot hide the growth of a
// smaller one. The ceiling keeps each statement clear of the work-item
// reader's 64 MiB per-statement memory cap.
const (
	statementMemorySlope   = 1.15
	statementMemoryCeiling = uint64(56 << 20)
)

// statementFilter picks the guarded statements in query_log: every SELECT that
// reads Table and, when QueryContains is set, whose text contains it (a
// fragment only the guarded statements carry).
type statementFilter struct {
	Table         string
	QueryContains string
}

// statementMemory is the peak memory_usage of each guarded statement (by
// normalized_query_hash), finished or failed, logged after a mark.
type statementMemory map[uint64]uint64

func statementMemorySince(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, filter statementFilter, markMicros int64) statementMemory {
	t.Helper()
	if filter.Table == "" {
		t.Fatal("statementFilter.Table is required")
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
	}
	rows, err := direct.Query(ctx, `SELECT normalized_query_hash, max(memory_usage), `+statementthreads.ThreadlessCount+` FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.', ?)) AND position(query, ?) > 0
GROUP BY normalized_query_hash`, markMicros, filter.Table, filter.QueryContains)
	if err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	defer rows.Close()
	m := statementMemory{}
	var threadless uint64
	for rows.Next() {
		var hash, peak, unbound uint64
		if err := rows.Scan(&hash, &peak, &unbound); err != nil {
			t.Fatalf("scan query_log: %v", err)
		}
		m[hash] = peak
		threadless += unbound
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read query_log rows (measurement did not happen): %v", err)
	}
	statementthreads.RequireThreadBound(t, filter.Table, threadless)
	return m
}

// assertStatementMemoryFollowsEvidence applies both guards to the statements
// measured at a smaller and a larger organization. Both sides must hold the
// same statements, at least minStatements of them, or the measurement did not
// happen. Run one unmeasured read before the smaller measurement: a cold
// first read peaks higher and would mask growth.
func assertStatementMemoryFollowsEvidence(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, label string, small, large statementMemory, minStatements int) {
	t.Helper()
	if len(small) < minStatements || len(large) != len(small) {
		t.Fatalf("%s: measurement did not happen: %d and %d distinct statements logged, want the same %d or more on both sides; recent server statements:\n%s", label, len(small), len(large), minStatements, recentServerStatements(ctx, direct))
	}
	for hash, smallPeak := range small {
		largePeak, ok := large[hash]
		if !ok {
			t.Fatalf("%s: statement %d ran at the smaller size only; recent server statements:\n%s", label, hash, recentServerStatements(ctx, direct))
		}
		if float64(largePeak) > statementMemorySlope*float64(smallPeak) {
			t.Errorf("%s: statement %d grew with the organization: peak %d bytes at the larger size, %d at the smaller, over the %.2fx slope", label, hash, largePeak, smallPeak, statementMemorySlope)
		}
		for _, peak := range []uint64{smallPeak, largePeak} {
			if peak >= statementMemoryCeiling {
				t.Errorf("%s: statement %d peaked at %d bytes, want under %d (7/8 of the 64 MiB statement cap)", label, hash, peak, statementMemoryCeiling)
			}
		}
	}
}

func recentServerStatements(ctx context.Context, direct clickhousedriver.Conn) string {
	rows, err := direct.Query(ctx, `SELECT concat(toString(type), ' code=', toString(exception_code), ' memory=', toString(memory_usage), ' tables=', arrayStringConcat(tables, ','), ' || ', substring(exception, 1, 200), ' || ', substring(query, 1, 160))
FROM system.query_log WHERE type != 'QueryStart' ORDER BY event_time_microseconds DESC LIMIT 8`)
	if err != nil {
		return "query_log unreadable: " + err.Error()
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return out.String() + "scan: " + err.Error()
		}
		out.WriteString(line + "\n")
	}
	return out.String()
}
