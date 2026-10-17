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

func TestMemoryCappedQueryClientSendsTheReaderAsLogComment(t *testing.T) {
	t.Parallel()
	client := memoryCappedQueryClient{capBytes: APIStatementMaxMemoryUsage}
	if _, present := client.statementSettings(context.Background())["log_comment"]; present {
		t.Error("a statement without a named reader sent log_comment")
	}
	settings := client.statementSettings(WithStatementReader(context.Background(), "ReadTeamProjectReachedRepositories"))
	if settings["log_comment"] != "ReadTeamProjectReachedRepositories" {
		t.Errorf("log_comment = %v, want the reader name", settings["log_comment"])
	}
	if settings["max_memory_usage"] != APIStatementMaxMemoryUsage {
		t.Errorf("max_memory_usage = %v, want the cap", settings["max_memory_usage"])
	}
}
