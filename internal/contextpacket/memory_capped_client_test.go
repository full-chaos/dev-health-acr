package contextpacket

import (
	"context"
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
