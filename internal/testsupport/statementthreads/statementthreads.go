// Package statementthreads builds the ClickHouse query client a memory test
// measures through, with the per-statement thread count production sends.
// ClickHouse's default max_threads follows the server's cores, and per-thread
// join and sort buffers follow max_threads, so a peak measured at the server
// default measures the runner (4 cores hosted, 64 on the self-hosted pool),
// not the statement.
package statementthreads

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// API wraps inner the way the hosted API wraps every statement: the API
// statement memory cap and max_threads = contextpacket.StatementMaxThreads.
func API(inner contextpacket.ClickHouseQueryClient) contextpacket.ClickHouseQueryClient {
	return Capped(inner, contextpacket.APIStatementMaxMemoryUsage)
}

// Capped is API with a caller-chosen memory cap (a test that pins a smaller
// statement cap, or the projector's).
func Capped(inner contextpacket.ClickHouseQueryClient, capBytes uint64) contextpacket.ClickHouseQueryClient {
	return contextpacket.NewMemoryCappedQueryClient(inner, capBytes, contextpacket.WithMaxThreads(contextpacket.StatementMaxThreads))
}

// Unbounded is Capped with no practical cap: production threads for a
// log-only reading whose statement may exceed the API cap (the reading itself
// shows by how much).
func Unbounded(inner contextpacket.ClickHouseQueryClient) contextpacket.ClickHouseQueryClient {
	return Capped(inner, 1<<40)
}

// unboundThreads is the query_log condition of a measured statement that did
// not run at a thread count of its own choosing: query_log's Settings map
// logs no max_threads for it on a server whose default is not the statement
// thread count. The map lists every setting that differs from the server
// default, whether the client sent it or the statement's own SETTINGS set it,
// so a statement's own SETTINGS (its production choice) logs there even when
// query_log cuts the text (log_queries_cut_to_length) and drops the clause.
// The text is not read: a client at another count and a statement's own
// SETTINGS log the same, so both count as bound.
var unboundThreads = fmt.Sprintf("Settings['max_threads'] = '' AND (SELECT value FROM system.settings WHERE name = 'max_threads') NOT IN ('%[1]d', 'auto(%[1]d)')", contextpacket.StatementMaxThreads)

// UnboundThreadsCount is a system.query_log aggregate: how many of the
// selected statements did not run at the statement thread count.
var UnboundThreadsCount = "countIf(" + unboundThreads + ")"

// UnboundThreads is UnboundThreadsCount for one query_log row.
var UnboundThreads = "toUInt64(" + unboundThreads + ")"

// ServerDefaultThreads reads the server's own max_threads default on a
// connection that sends no statement settings.
func ServerDefaultThreads(ctx context.Context, direct clickhousedriver.Conn) (uint64, error) {
	var value string
	if err := direct.QueryRow(ctx, "SELECT value FROM system.settings WHERE name = 'max_threads'").Scan(&value); err != nil {
		return 0, err
	}
	return ParseThreads(value)
}

// ParseThreads reads a max_threads setting value: "4" or the server's
// "auto(4)" default.
func ParseThreads(value string) (uint64, error) {
	digits := strings.TrimSuffix(strings.TrimPrefix(value, "auto("), ")")
	threads, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("max_threads value %q: %w", value, err)
	}
	return threads, nil
}

// RequireThreadBound fails the test when any measured statement did not run
// at the statement thread count (UnboundThreadsCount / UnboundThreads).
func RequireThreadBound(tb testing.TB, ctx context.Context, direct clickhousedriver.Conn, label string, unbound uint64) {
	tb.Helper()
	if unbound == 0 {
		return
	}
	serverDefault, err := ServerDefaultThreads(ctx, direct)
	if err != nil {
		tb.Fatalf("%s: %d measured statements did not run at max_threads %d (server default unreadable: %v); the peak measures the runner, not the statement (build the measured client with statementthreads.API)", label, unbound, contextpacket.StatementMaxThreads, err)
	}
	tb.Fatalf("%s: %d measured statements did not run at max_threads %d (server default %d follows the host's cores); the peak measures the runner, not the statement (build the measured client with statementthreads.API); recent such statements:\n%s", label, unbound, contextpacket.StatementMaxThreads, serverDefault, recentUnbound(ctx, direct))
}

// recentUnbound lists the latest logged statements off the statement threads,
// with the max_threads each logged, for a failure message.
func recentUnbound(ctx context.Context, direct clickhousedriver.Conn) string {
	rows, err := direct.Query(ctx, "SELECT concat('max_threads=', Settings['max_threads'], ' ', toString(type), ' len=', toString(length(query)), ' ', substring(replaceRegexpAll(query, '\\\\s+', ' '), 1, 160), ' ... ', right(replaceRegexpAll(query, '\\\\s+', ' '), 80)) FROM system.query_log WHERE event_time > now() - INTERVAL 15 MINUTE AND type != 'QueryStart' AND query_kind = 'Select' AND position(query, 'system.query_log') = 0 AND "+unboundThreads+" ORDER BY event_time_microseconds DESC LIMIT 8")
	if err != nil {
		return "query_log unreadable: " + err.Error()
	}
	defer func() { _ = rows.Close() }()
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
