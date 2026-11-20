package contextpacket

import (
	"context"
	"errors"
	"testing"
)

type closeCountingScanner struct {
	emptyScanner
	closed int
}

func (s *closeCountingScanner) Close() error { s.closed++; return nil }

type erroringRowsClient struct {
	rows *closeCountingScanner
	err  error
}

func (c *erroringRowsClient) Query(context.Context, string, []ClickHouseBinding) (ClickHouseRowScanner, error) {
	return c.rows, c.err
}

func TestMemoryCappedQueryClientReturnsNoRowsWithAnErrorAndClosesThem(t *testing.T) {
	t.Parallel()
	want := errors.New("refused")
	inner := &erroringRowsClient{rows: &closeCountingScanner{}, err: want}
	rows, err := NewMemoryCappedQueryClient(inner, APIStatementMaxMemoryUsage).Query(context.Background(), "SELECT 1", nil)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if rows != nil {
		t.Fatalf("rows = %v with a non-nil error, want nil", rows)
	}
	if inner.rows.closed != 1 {
		t.Fatalf("inner rows closed %d times, want 1", inner.rows.closed)
	}
}
