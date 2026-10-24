package hosted

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/chtlsrig"
)

// clickhouse-go hands a connection back to the pool when it reads the last
// block of a result, so a one-block result, or any result read to its end,
// needs no rows.Close. A result a caller abandons before its end holds the
// connection until Close, so a layer that does not forward Close leaks a pool
// slot per call and the (pool width + 1)th statement blocks. max_block_size=1
// makes every row its own block, so a result of two or more rows has an end
// that stopping after one row never reaches. The evidence reference read and
// the other Query sites stop at the last row their LIMIT returns, so that end
// is already consumed there; the shared row wrappers are what is pinned.

const (
	// rowsCloseBlockSize turns each result row into one block.
	rowsCloseBlockSize = "max_block_size=1"
	// rowsCloseCalls is three pool widths of sequential abandoned reads.
	rowsCloseCalls = 3 * ClickHousePoolMaxOpenConns
	// rowsCloseStatementDeadline is far above one statement over the rig and
	// far below the pool's blocking wait, so a leaked slot shows as a deadline.
	rowsCloseStatementDeadline = 20 * time.Second
	// rowsCloseConnectionLimit is the warm connection plus the release-race
	// slack of the warm-pool guard (see warmPoolReleaseRaceSlack): sequential
	// statements reuse one connection whether or not they were abandoned.
	rowsCloseConnectionLimit = 1 + warmPoolReleaseRaceSlack
)

func openMultiBlockRig(t *testing.T) (*chtlsrig.Rig, clickHouseComponents) {
	t.Helper()
	rig := chtlsrig.Start(t, context.Background(), 0)
	components, err := openClickHouse(context.Background(), clickHouseOpenRequest{config: config.Config{
		ClickHouseDSN: rig.DSN + "&" + rowsCloseBlockSize, ClickHouseCACertPath: rig.CAPath, ClickHouseMaxBytesToRead: 256 << 20,
		EvidenceIDActiveKID: "current", EvidenceIDKeys: map[string][]byte{"current": []byte(strings.Repeat("k", 32))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = components.close() })
	return rig, components
}

// The leak is only visible if an abandoned result really holds its slot. This
// control abandons ClickHousePoolMaxOpenConns results without Close and
// requires the next statement to starve; if the setting or the driver ever
// stops making that true, the pin below could pass without pinning anything.
func TestAbandonedMultiBlockResultsStarveThePoolWithoutClose(t *testing.T) {
	_, components := openMultiBlockRig(t)
	for range ClickHousePoolMaxOpenConns {
		ctx, cancel := context.WithTimeout(context.Background(), rowsCloseStatementDeadline)
		rows, err := components.queryClient.Query(ctx, "SELECT number FROM numbers(1000)", nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if !rows.Next() {
			cancel()
			t.Fatalf("statement returned no row: %v", rows.Err())
		}
		t.Cleanup(func() { _ = rows.Close(); cancel() })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := components.queryClient.Query(ctx, "SELECT 1", nil)
	if err == nil {
		_ = rows.Close()
		t.Fatalf("a statement ran with %d unclosed multi-block results holding every pool slot", ClickHousePoolMaxOpenConns)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("starved statement failed with %v, want the context deadline", err)
	}
}

// components.queryClient is the measured wrapper over the memory-capped
// wrapper over the native client; the evidence store reads through the capped
// wrapper alone. Each wrapper's rows.Close must forward to the driver, because
// a result a caller abandons before its end holds its connection until that
// Close. Reading one row of a multi-block result and closing it, repeated three
// pool widths, must neither starve the pool nor open connections beyond the
// warm one.
func TestAbandonedMultiBlockResultsReleaseTheirConnectionThroughTheQueryClient(t *testing.T) {
	rig, components := openMultiBlockRig(t)
	rig.Proxy.ResetStats()
	for call := range rowsCloseCalls {
		ctx, cancel := context.WithTimeout(context.Background(), rowsCloseStatementDeadline)
		rows, err := components.queryClient.Query(ctx, "SELECT number FROM numbers(1000)", nil)
		if err != nil {
			cancel()
			t.Fatalf("call %d: %v; a failure after the first pool width of calls means the pool is starved by unreleased connections", call+1, err)
		}
		if !rows.Next() {
			cancel()
			t.Fatalf("call %d returned no row: %v", call+1, rows.Err())
		}
		if err := rows.Close(); err != nil {
			cancel()
			t.Fatalf("call %d: close: %v", call+1, err)
		}
		cancel()
	}
	stats := rig.Proxy.Stats()
	t.Logf("ch_rig rows_close calls=%d tcp_connections=%d tls_handshakes=%d", rowsCloseCalls, stats.Accepted, stats.ClientHellos)
	if limit := int64(rowsCloseConnectionLimit); stats.Accepted > limit || stats.ClientHellos > limit {
		t.Fatalf("%d abandoned reads opened %d TCP connections and %d TLS handshakes, want at most %d", rowsCloseCalls, stats.Accepted, stats.ClientHellos, limit)
	}
}
