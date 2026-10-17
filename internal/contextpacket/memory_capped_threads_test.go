package contextpacket

import (
	"context"
	"testing"
)

type threadsRecordingClient struct {
	threads uint64
	seen    bool
}

func (c *threadsRecordingClient) Query(ctx context.Context, _ string, _ []ClickHouseBinding) (ClickHouseRowScanner, error) {
	c.threads, c.seen = StatementMaxThreadsFromContext(ctx)
	return nil, nil
}

func TestMemoryCappedQueryClientSendsMaxThreadsOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	inner := &threadsRecordingClient{}
	if _, err := NewMemoryCappedQueryClient(inner, APIStatementMaxMemoryUsage).Query(context.Background(), "SELECT 1", nil); err != nil {
		t.Fatal(err)
	}
	if inner.seen {
		t.Errorf("a client without WithMaxThreads sent max_threads = %d", inner.threads)
	}
	if _, err := NewMemoryCappedQueryClient(inner, APIStatementMaxMemoryUsage, WithMaxThreads(4)).Query(context.Background(), "SELECT 1", nil); err != nil {
		t.Fatal(err)
	}
	if !inner.seen || inner.threads != 4 {
		t.Errorf("client with WithMaxThreads(4) sent seen=%v threads=%d, want 4", inner.seen, inner.threads)
	}
}
