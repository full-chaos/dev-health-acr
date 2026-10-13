package hosted

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/chserver"
)

func readSetting(t *testing.T, client contextpacket.ClickHouseQueryClient, statement string) string {
	t.Helper()
	rows, err := client.Query(context.Background(), statement, nil)
	if err != nil {
		t.Fatalf("query %q: %v", statement, err)
	}
	defer func() { _ = rows.Close() }()
	var value string
	if !rows.Next() {
		t.Fatalf("query %q returned no row (err=%v)", statement, rows.Err())
	}
	if err := rows.Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// Against a real server, a statement sent through the production API client
// carries the 64 MiB memory cap, and a statement's own SETTINGS wins.
func TestOpenClickHouseSendsTheStatementMemoryCapToTheServer(t *testing.T) {
	dsn := chserver.StartDSN(t, context.Background())
	components, err := openClickHouse(context.Background(), clickHouseOpenRequest{config: config.Config{
		ClickHouseDSN: dsn, ClickHouseMaxBytesToRead: 256 << 20,
		EvidenceIDActiveKID: "current", EvidenceIDKeys: map[string][]byte{"current": []byte(strings.Repeat("k", 32))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = components.close() })
	if got := readSetting(t, components.queryClient, "SELECT toString(getSetting('max_memory_usage'))"); got != "67108864" {
		t.Fatalf("server saw max_memory_usage %s through the API client, want 67108864 (64 MiB)", got)
	}
	if got := readSetting(t, components.queryClient, "SELECT toString(getSetting('max_memory_usage')) SETTINGS max_memory_usage = 12345678"); got != "12345678" {
		t.Fatalf("server saw max_memory_usage %s with a statement-level SETTINGS, want 12345678", got)
	}
}
