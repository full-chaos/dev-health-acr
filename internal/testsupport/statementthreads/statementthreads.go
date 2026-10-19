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

// ThreadlessCount is a system.query_log aggregate: how many of the selected
// statements ran with no max_threads setting, at the server default.
const ThreadlessCount = "countIf(Settings['max_threads'] = '')"

// Threadless is ThreadlessCount for one query_log row: 1 when that statement
// ran without a max_threads setting.
const Threadless = "toUInt64(Settings['max_threads'] = '')"

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

// RequireThreadBound fails the test when measured statements ran with no
// max_threads setting on a server whose default is not the statement thread
// count. query_log lists only settings that differ from the server default,
// so on a server that already defaults to StatementMaxThreads (a 4-core host)
// a statement sent with max_threads = 4 logs no setting and ran at 4 anyway.
func RequireThreadBound(tb testing.TB, ctx context.Context, direct clickhousedriver.Conn, label string, threadless uint64) {
	tb.Helper()
	if threadless == 0 {
		return
	}
	serverDefault, err := ServerDefaultThreads(ctx, direct)
	if err != nil {
		tb.Fatalf("%s: %d measured statements logged no max_threads and the server default is unreadable: %v", label, threadless, err)
	}
	if serverDefault == contextpacket.StatementMaxThreads {
		return
	}
	tb.Fatalf("%s: %d measured statements ran without max_threads, at the server default %d that follows the host's cores; the peak measures the runner, not the statement (build the measured client with statementthreads.API)", label, threadless, serverDefault)
}
