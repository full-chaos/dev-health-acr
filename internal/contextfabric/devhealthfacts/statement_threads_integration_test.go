package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

// The client a memory row measures through runs each statement at the
// production thread count, whatever the server's own default (which follows
// the host's cores). The server default is logged so a runner's figure can be
// read beside the hosted one.
func TestMeasuredClientRunsAtTheStatementThreadsOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	raw, _ := sharedClickHouseFixture(t)
	threads := func(client contextpacket.ClickHouseQueryClient) (value string, changed uint8) {
		t.Helper()
		rows, err := client.Query(ctx, "SELECT value, toUInt8(changed) FROM system.settings WHERE name = 'max_threads'", nil)
		if err != nil {
			t.Fatalf("read max_threads: %v", err)
		}
		defer func() { _ = rows.Close() }()
		if !rows.Next() {
			t.Fatal("system.settings has no max_threads row: the measurement did not happen")
		}
		if err := rows.Scan(&value, &changed); err != nil {
			t.Fatalf("scan max_threads: %v", err)
		}
		return value, changed
	}
	serverDefault, _ := threads(raw)
	measured, changed := threads(statementthreads.API(raw))
	t.Logf("STATEMENT-THREADS server default max_threads %s; measured client %s (changed=%d)", serverDefault, measured, changed)
	got, err := statementthreads.ParseThreads(measured)
	if err != nil {
		t.Fatal(err)
	}
	if got != contextpacket.StatementMaxThreads {
		t.Fatalf("the measured client ran at max_threads %s, want %d", measured, contextpacket.StatementMaxThreads)
	}
	// A setting equal to the server default reads back unchanged; only a
	// server whose own default is the statement thread count may show that.
	if defaultThreads, _ := statementthreads.ParseThreads(serverDefault); changed != 1 && defaultThreads != contextpacket.StatementMaxThreads {
		t.Fatalf("the measured client's max_threads %s is not marked as sent (changed=%d) on a server defaulting to %s", measured, changed, serverDefault)
	}
}
