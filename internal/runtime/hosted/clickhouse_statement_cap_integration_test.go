package hosted

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"

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
// carries the 256 MiB memory cap, and a statement's own SETTINGS wins.
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
	if got := readSetting(t, components.queryClient, "SELECT toString(getSetting('max_memory_usage'))"); got != "268435456" {
		t.Fatalf("server saw max_memory_usage %s through the API client, want 268435456 (256 MiB)", got)
	}
	if got := readSetting(t, components.queryClient, "SELECT toString(getSetting('max_memory_usage')) SETTINGS max_memory_usage = 12345678"); got != "12345678" {
		t.Fatalf("server saw max_memory_usage %s with a statement-level SETTINGS, want 12345678", got)
	}
}

// Against a real server, the profile events carry the statement's peak memory
// and the near-cap observer is told once the statement is closed.
func TestMemoryNearCapObserverSeesTheServersPeakMemory(t *testing.T) {
	ctx := context.Background()
	dsn := chserver.StartDSN(t, ctx)
	raw, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	var seen []contextpacket.MemoryNearCap
	unobserved := 0
	client := contextpacket.NewMemoryCappedQueryClient(raw, contextpacket.APIStatementMaxMemoryUsage,
		contextpacket.WithMemoryNearCapRatio(0.0001),
		contextpacket.WithMemoryNearCapObserver(func(_ context.Context, near contextpacket.MemoryNearCap) { seen = append(seen, near) }),
		contextpacket.WithMemoryPeakUnobservedObserver(func(context.Context, string) { unobserved++ }))
	const statement = "SELECT toString(count()) FROM (SELECT number FROM numbers(1000000) GROUP BY number)"
	if got := readSetting(t, client, statement); got != "1000000" {
		t.Fatalf("statement returned %s, want 1000000", got)
	}
	if unobserved != 0 {
		t.Fatalf("the server sent no peak memory value for %d statements; profile event names: %v", unobserved, profileEventNames(t, dsn))
	}
	if len(seen) != 1 || seen[0].PeakBytes == 0 || seen[0].CapBytes != contextpacket.APIStatementMaxMemoryUsage || seen[0].Statement != statement {
		t.Fatalf("near-cap observer saw %+v, want one call with a nonzero peak and the API cap; profile event names the server sent: %v", seen, profileEventNames(t, dsn))
	}
}

func profileEventNames(t *testing.T, dsn string) []string {
	t.Helper()
	options, err := clickhousedriver.ParseDSN(dsn)
	if err != nil {
		return []string{err.Error()}
	}
	conn, err := clickhousedriver.Open(options)
	if err != nil {
		return []string{err.Error()}
	}
	defer func() { _ = conn.Close() }()
	names := map[string]bool{}
	queryCtx := clickhousedriver.Context(context.Background(), clickhousedriver.WithProfileEvents(func(events []clickhousedriver.ProfileEvent) {
		for _, event := range events {
			names[event.Type+":"+event.Name] = true
		}
	}))
	rows, err := conn.Query(queryCtx, "SELECT count() FROM (SELECT number FROM numbers(1000000) GROUP BY number)")
	if err == nil {
		for rows.Next() {
		}
		_ = rows.Close()
	}
	out := make([]string, 0, len(names))
	for name := range names {
		if strings.Contains(strings.ToLower(name), "mem") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Against a real server, a statement the server refuses with a memory-limit
// exception is counted as near its cap with a peak that is a lower bound, and
// the caller still gets the server's error.
func TestMemoryNearCapObserverSeesAMemoryLimitRefusal(t *testing.T) {
	ctx := context.Background()
	dsn := chserver.StartDSN(t, ctx)
	raw, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	var seen []contextpacket.MemoryNearCap
	client := contextpacket.NewMemoryCappedQueryClient(raw, contextpacket.APIStatementMaxMemoryUsage,
		contextpacket.WithMemoryNearCapObserver(func(_ context.Context, near contextpacket.MemoryNearCap) { seen = append(seen, near) }))
	const statement = "SELECT toString(count()) FROM (SELECT number FROM numbers(2000000) GROUP BY number) SETTINGS max_memory_usage = 1048576"
	rows, err := client.Query(ctx, statement, nil)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
		}
		err = rows.Err()
	}
	var exception *clickhousedriver.Exception
	if !errors.As(err, &exception) || exception.Code != 241 {
		t.Fatalf("caller got %v, want the server's 241 exception", err)
	}
	if len(seen) != 1 || seen[0].CapBytes != 1048576 || seen[0].PeakBytes < 1048576 {
		t.Fatalf("observer saw %+v, want one near-cap at the 1 MiB statement cap", seen)
	}
}
