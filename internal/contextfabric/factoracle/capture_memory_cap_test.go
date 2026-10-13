package factoracle

import (
	"context"
	"strconv"
	"testing"

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
	var got []string
	err = clickHouseQuerier{conn: conn}.QueryStrings(context.Background(), "SELECT toString(getSetting('max_memory_usage'))", nil, func(line string) error {
		got = append(got, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strconv.FormatUint(contextpacket.APIStatementMaxMemoryUsage, 10)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("server saw max_memory_usage %v through the capture, want %s", got, want)
	}
}
