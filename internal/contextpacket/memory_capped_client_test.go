package contextpacket

import (
	"context"
	"log/slog"
	"strings"
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
