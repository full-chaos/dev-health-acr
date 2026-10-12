package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// A statement whose cost must follow its own evidence, not the size of the
// organization, is guarded twice. The slope guard is the property: the peak
// memory of the statements, measured at two organization sizes, may not grow
// by more than statementMemorySlope. The ceiling keeps each statement clear of
// the work-item reader's 64 MiB per-statement memory cap.
const (
	statementMemorySlope   = 1.15
	statementMemoryCeiling = uint64(56 << 20)
)

// statementMemory is what query_log holds for the finished or failed SELECTs
// over one table logged after a mark: how many there were and their peak
// memory_usage.
type statementMemory struct {
	statements uint64
	peak       uint64
}

// statementFilter picks the guarded statements in query_log: every SELECT that
// reads Table and, when QueryContains is set, whose text contains it (a
// fragment only the guarded statements carry).
type statementFilter struct {
	Table         string
	QueryContains string
}

func statementMemorySince(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, filter statementFilter, markMicros int64) statementMemory {
	t.Helper()
	if filter.Table == "" {
		t.Fatal("statementFilter.Table is required")
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
	}
	var m statementMemory
	if err := direct.QueryRow(ctx, `SELECT count(), max(memory_usage) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.', ?)) AND position(query, ?) > 0`, markMicros, filter.Table, filter.QueryContains).Scan(&m.statements, &m.peak); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	return m
}

// assertStatementMemoryFollowsEvidence applies both guards to the statements
// measured at a smaller and a larger organization. Each side must have logged
// at least minStatements, or the measurement did not happen.
func assertStatementMemoryFollowsEvidence(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, label string, small, large statementMemory, minStatements uint64) {
	t.Helper()
	if small.statements < minStatements || large.statements < minStatements {
		t.Fatalf("%s: measurement did not happen: %d and %d statements logged, want at least %d each; recent server statements:\n%s", label, small.statements, large.statements, minStatements, recentServerStatements(ctx, direct))
	}
	if float64(large.peak) > statementMemorySlope*float64(small.peak) {
		t.Errorf("%s: peak memory grew with the organization: %d bytes at the larger size, %d at the smaller, over the %.2fx slope", label, large.peak, small.peak, statementMemorySlope)
	}
	for _, m := range []statementMemory{small, large} {
		if m.peak >= statementMemoryCeiling {
			t.Errorf("%s: a statement peaked at %d bytes, want under %d (7/8 of the 64 MiB statement cap)", label, m.peak, statementMemoryCeiling)
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
