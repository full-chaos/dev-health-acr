package factoracle

import (
	"context"
	"strconv"
	"testing"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/chserver"
)

// Against a real server, a statement the capture sends carries the API
// statement memory cap, the same cap as the statements it reproduces.
func TestVenueQuerierSendsTheAPIStatementMemoryCap(t *testing.T) {
	dsn := chserver.StartDSN(t, context.Background())
	options, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := clickhouse.Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	marker := "o4capprobe" + strconv.FormatInt(time.Now().UnixNano(), 10)
	err = clickHouseQuerier{conn: conn}.QueryStrings(context.Background(), "SELECT /* "+marker+" */ toString(1)", nil, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(context.Background(), "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	var sent string
	row := conn.QueryRow(context.Background(),
		"SELECT Settings['max_memory_usage'] FROM system.query_log WHERE type = 'QueryFinish' AND query LIKE {pattern:String} AND query NOT LIKE '%system.query_log%' ORDER BY event_time DESC LIMIT 1",
		clickhouse.Named("pattern", "%"+marker+"%"))
	if err := row.Scan(&sent); err != nil {
		t.Fatalf("the capture statement is not in system.query_log: %v", err)
	}
	want := strconv.FormatUint(contextpacket.APIStatementMaxMemoryUsage, 10)
	if sent != want {
		t.Fatalf("query_log shows max_memory_usage %q on the capture statement, want %s", sent, want)
	}
}
