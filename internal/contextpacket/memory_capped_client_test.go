package contextpacket

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
)

type capRecordingClient struct {
	capBytes uint64
	seen     bool
}

func (c *capRecordingClient) Query(ctx context.Context, _ string, _ []ClickHouseBinding) (ClickHouseRowScanner, error) {
	c.capBytes, c.seen = StatementMemoryCapFromContext(ctx)
	return nil, nil
}

func TestMemoryCappedQueryClientSendsTheCapWithEveryStatement(t *testing.T) {
	t.Parallel()
	inner := &capRecordingClient{}
	client := NewMemoryCappedQueryClient(inner, APIStatementMaxMemoryUsage)
	for _, statement := range []string{"SELECT 1", "SELECT 2"} {
		inner.seen = false
		if _, err := client.Query(context.Background(), statement, nil); err != nil {
			t.Fatal(err)
		}
		if !inner.seen || inner.capBytes != APIStatementMaxMemoryUsage {
			t.Fatalf("statement %q reached the inner client with cap (%d, seen=%v), want %d", statement, inner.capBytes, inner.seen, APIStatementMaxMemoryUsage)
		}
	}
}

func TestMemoryCappedQueryClientWithoutAWrapHasNoCap(t *testing.T) {
	t.Parallel()
	inner := &capRecordingClient{}
	if _, err := ClickHouseQueryClient(inner).Query(context.Background(), "SELECT 1", nil); err != nil {
		t.Fatal(err)
	}
	if inner.seen {
		t.Fatal("an unwrapped client must not report a cap")
	}
}

func TestSlogMemoryNearCapObserverLogsTheStatementByHashAndCounts(t *testing.T) {
	t.Parallel()
	var logged strings.Builder
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	counted := 0
	observe := NewSlogMemoryNearCapObserver(logger, func(context.Context) { counted++ })
	observe(context.Background(), MemoryNearCap{Statement: "SELECT  a\nFROM t WHERE x = {x:String}", PeakBytes: 140 << 20, CapBytes: 256 << 20})
	line := logged.String()
	for _, want := range []string{"level=WARN", `msg="clickhouse statement memory near cap"`, "peak_bytes=146800640", "cap_bytes=268435456", `statement_head="SELECT a FROM t WHERE x = {x:String}"`, "statement_sha="} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q lacks %q", line, want)
		}
	}
	if counted != 1 {
		t.Fatalf("counter called %d times, want 1", counted)
	}
}

func TestMemoryCappedQueryClientCountsAStatementWithNoPeakValue(t *testing.T) {
	t.Parallel()
	inner := &scannerClient{}
	var counted []string
	client := NewMemoryCappedQueryClient(inner, APIStatementMaxMemoryUsage,
		WithMemoryPeakUnobservedObserver(func(_ context.Context, statement string) { counted = append(counted, statement) }))
	rows, err := client.Query(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(counted) != 1 || counted[0] != "SELECT 1" {
		t.Fatalf("unobserved statements = %v, want exactly [SELECT 1] (a fake server sends no profile events)", counted)
	}
}

type scannerClient struct{}

func (*scannerClient) Query(context.Context, string, []ClickHouseBinding) (ClickHouseRowScanner, error) {
	return emptyScanner{}, nil
}

type emptyScanner struct{}

func (emptyScanner) Next() bool        { return false }
func (emptyScanner) Scan(...any) error { return nil }
func (emptyScanner) Err() error        { return nil }
func (emptyScanner) Close() error      { return nil }

// A statement with its own lower cap is judged against that cap, not the
// client default: 60% of its own 64 MiB limit warns although it is 15% of the
// 256 MiB default.
func TestNearCapComparesAgainstTheStatementsOwnCap(t *testing.T) {
	t.Parallel()
	statement := "SELECT 1\nSETTINGS max_memory_usage = 67108864"
	var seen []MemoryNearCap
	client := memoryCappedQueryClient{
		inner: &scannerClient{}, capBytes: APIStatementMaxMemoryUsage, ratio: NearCapRatio,
		observer: func(_ context.Context, near MemoryNearCap) { seen = append(seen, near) },
	}
	var peak atomic.Uint64
	peak.Store(64 << 20 * 6 / 10)
	rows := &memoryObservedRows{ClickHouseRowScanner: emptyScanner{}, ctx: context.Background(), statement: statement, client: client, peak: &peak}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].CapBytes != 64<<20 {
		t.Fatalf("observer saw %+v, want one call against the statement's own 64 MiB cap", seen)
	}

	seen = nil
	peak.Store(64 << 20 * 6 / 10)
	plain := &memoryObservedRows{ClickHouseRowScanner: emptyScanner{}, ctx: context.Background(), statement: "SELECT 1", client: client, peak: &peak}
	if err := plain.Close(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("a statement on the 256 MiB default at 38 MiB must not warn, saw %+v", seen)
	}
}

func TestEffectiveStatementCapTakesTheStatementsLastLimit(t *testing.T) {
	t.Parallel()
	for statement, want := range map[string]uint64{
		"SELECT 1": 7, "SELECT 1 SETTINGS max_memory_usage = 100": 100,
		"SELECT 1 SETTINGS MAX_MEMORY_USAGE=5 , max_memory_usage = 9": 9, "SELECT 1 SETTINGS max_memory_usage = 0": 7,
	} {
		if got := effectiveStatementCap(statement, 7); got != want {
			t.Errorf("effectiveStatementCap(%q) = %d, want %d", statement, got, want)
		}
	}
}
