package hosted

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/chtlsrig"
)

// clickhouse-go hands a connection back to the pool when it reads the last
// block of a result, so a one-block result, or any result read to its end,
// needs no rows.Close. A result a caller abandons before its end holds the
// connection until Close, so a Query site that skips Close on an early return
// leaks a pool slot per call and the (pool width + 1)th statement blocks.
// max_block_size=1 makes every row its own block, so a result of two or more
// rows has an end the early return never reaches.

const (
	// rowsCloseBlockSize turns each result row into one block.
	rowsCloseBlockSize = "max_block_size=1"
	// rowsCloseSaturationRows is one more than the 501-row limit of the
	// evidence reference read, so that read stops at its saturation row.
	rowsCloseSaturationRows = 600
	// rowsCloseStatementDeadline is far above one statement over the rig and
	// far below the pool's blocking wait, so a leaked slot shows as a deadline.
	rowsCloseStatementDeadline = 20 * time.Second
	// rowsCloseConnectionLimit is the warm connection plus the release-race
	// slack of the warm-pool guard (see warmPoolReleaseRaceSlack): sequential
	// statements reuse one connection whether or not they were abandoned.
	rowsCloseConnectionLimit = 1 + warmPoolReleaseRaceSlack
)

func openMultiBlockRig(t *testing.T) (*chtlsrig.Rig, clickHouseComponents, clickhousedriver.Conn) {
	t.Helper()
	rig := chtlsrig.Start(t, context.Background(), 0)
	tlsConfig, err := runtimeclickhouse.TLSConfigFromCABundle(rig.CAPath)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig.ServerName = chtlsrig.ServerName
	options, err := clickhousedriver.ParseDSN(rig.DSN)
	if err != nil {
		t.Fatal(err)
	}
	options.TLS = tlsConfig
	direct, err := clickhousedriver.Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	components, err := openClickHouse(context.Background(), clickHouseOpenRequest{config: config.Config{
		ClickHouseDSN: rig.DSN + "&" + rowsCloseBlockSize, ClickHouseCACertPath: rig.CAPath, ClickHouseMaxBytesToRead: 256 << 20,
		EvidenceIDActiveKID: "current", EvidenceIDKeys: map[string][]byte{"current": []byte(strings.Repeat("k", 32))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = components.close() })
	return rig, components, direct
}

// The leak is only visible if an abandoned result really holds its slot. This
// control abandons ClickHousePoolMaxOpenConns results without Close and
// requires the next statement to starve; if the setting or the driver ever
// stops making that true, the pins below could pass without pinning anything.
func TestAbandonedMultiBlockResultsStarveThePoolWithoutClose(t *testing.T) {
	_, components, _ := openMultiBlockRig(t)
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

// devhealthschema:not-a-production-replica -- the table below is the real ClickHouse table the deployment_incident_provenance.v1 catalog statement reads; this test seeds rows into the schema devhealthschema.DDL() just created, it declares no second physical schema.
const rowsCloseEdgeInsert = `INSERT INTO work_graph_deployment_incident_edges
SELECT concat('edge-', toString(number)), toUUID('10000000-0000-4000-8000-0000000000aa'), concat('dep-', toString(number)), concat('inc-', toString(number)),
  toUUID('30000000-0000-4000-8000-000000000003'), 1.0, 'native', concat('row-', toString(number)), now64(3), now64(3)
FROM numbers(%d)`

// The evidence reference read stops at its saturation row, before the end of
// a result of rowsCloseSaturationRows single-row blocks. Repeating it three
// pool widths must neither starve the pool nor open connections beyond the
// warm one: its rows.Close is what returns each abandoned connection.
func TestEvidenceReferenceReadReleasesItsConnectionOnTheSaturationReturn(t *testing.T) {
	rig, components, direct := openMultiBlockRig(t)
	setup := context.Background()
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(setup, statement); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	if err := direct.Exec(setup, fmt.Sprintf(rowsCloseEdgeInsert, rowsCloseSaturationRows)); err != nil {
		t.Fatalf("seed edges: %v", err)
	}
	rows := contextpacket.NewCatalogClickHouseRows(components.queryClient)
	scope := contractsv1.ResolvedScope{RepoID: "30000000-0000-4000-8000-000000000003", RepoSlug: "acme/close"}
	rig.Proxy.ResetStats()
	for call := range 3 * ClickHousePoolMaxOpenConns {
		ctx, cancel := context.WithTimeout(context.Background(), rowsCloseStatementDeadline)
		_, err := rows.ResolveEvidenceReference(ctx, "10000000-0000-4000-8000-0000000000aa", scope, "deployment_incident_provenance.v1", "")
		cancel()
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("call %d: got %v, want the saturation refusal (storage.ErrNotFound); a context deadline here is a leaked pool slot", call+1, err)
		}
	}
	stats := rig.Proxy.Stats()
	t.Logf("ch_rig rows_close calls=%d tcp_connections=%d tls_handshakes=%d", 3*ClickHousePoolMaxOpenConns, stats.Accepted, stats.ClientHellos)
	if limit := int64(rowsCloseConnectionLimit); stats.Accepted > limit || stats.ClientHellos > limit {
		t.Fatalf("%d abandoned reads opened %d TCP connections and %d TLS handshakes, want at most %d", 3*ClickHousePoolMaxOpenConns, stats.Accepted, stats.ClientHellos, limit)
	}
}
