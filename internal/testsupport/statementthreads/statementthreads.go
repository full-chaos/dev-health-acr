// Package statementthreads builds the ClickHouse query client a memory test
// measures through, with the per-statement thread count production sends.
// ClickHouse's default max_threads follows the server's cores, and per-thread
// join and sort buffers follow max_threads, so a peak measured at the server
// default measures the runner (4 cores hosted, 64 on the self-hosted pool),
// not the statement.
package statementthreads

import (
	"testing"

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

// RequireThreadBound fails the test when any measured statement ran at the
// server's default thread count.
func RequireThreadBound(tb testing.TB, label string, threadless uint64) {
	tb.Helper()
	if threadless > 0 {
		tb.Fatalf("%s: %d measured statements ran without max_threads, at the server default that follows the host's cores; the peak measures the runner, not the statement (build the measured client with statementthreads.API)", label, threadless)
	}
}
