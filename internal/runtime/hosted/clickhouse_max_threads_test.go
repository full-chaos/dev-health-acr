package hosted

import (
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
)

type threadsSeenClient struct {
	threads uint64
	seen    bool
}

func (c *threadsSeenClient) Query(ctx context.Context, _ string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.threads, c.seen = contextpacket.StatementMaxThreadsFromContext(ctx)
	return nil, nil
}

// The API's data reads go through the same per-statement thread default as
// the projector's: at the server default thread count a 14-row team read used
// 313 MB, over the API's 256 MiB statement cap.
func TestAPIClickHouseReadsSendTheSharedThreadDefault(t *testing.T) {
	inner := &threadsSeenClient{}
	client := contextpacket.NewMemoryCappedQueryClient(inner, contextpacket.APIStatementMaxMemoryUsage, apiCapOptions(slog.Default(), &hostedmetrics.Instruments{})...)
	if _, err := client.Query(context.Background(), "SELECT 1", nil); err != nil {
		t.Fatal(err)
	}
	if !inner.seen || inner.threads != contextpacket.StatementMaxThreads {
		t.Fatalf("API statement max_threads seen=%v threads=%d, want %d", inner.seen, inner.threads, contextpacket.StatementMaxThreads)
	}
}
