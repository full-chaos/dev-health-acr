package devhealthsource_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// d2bFakeClient is a minimal ClickHouseQueryClient double for
// RunCardinalityCensus -- a SINGLE aggregate statement, no row fetch, ever
// (the whole point of the D2(b) cardinality measurement being cheap).
type d2bFakeClient struct {
	count  uint64
	readAt time.Time
	calls  []string
}

func (c *d2bFakeClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.calls = append(c.calls, statement)
	return &d2bFakeScanner{rows: [][]any{{c.count, c.readAt}}}, nil
}

type d2bFakeScanner struct {
	rows [][]any
	row  int
}

func (s *d2bFakeScanner) Next() bool { return s.row < len(s.rows) }
func (s *d2bFakeScanner) Scan(dest ...any) error {
	row := s.rows[s.row]
	s.row++
	for i, d := range dest {
		switch target := d.(type) {
		case *uint64:
			*target = row[i].(uint64)
		case *time.Time:
			*target = row[i].(time.Time)
		}
	}
	return nil
}
func (s *d2bFakeScanner) Err() error   { return nil }
func (s *d2bFakeScanner) Close() error { return nil }

func TestRunCardinalityCensus_OpenWindow(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	client := &d2bFakeClient{count: 42, readAt: readAt}
	result, err := devhealthsource.RunCardinalityCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, devhealthsource.CardinalityWindow{})
	if err != nil {
		t.Fatalf("RunCardinalityCensus: %v", err)
	}
	if result.Count != 42 || !result.ReadAt.Equal(readAt) {
		t.Fatalf("result = %#v, want Count=42 ReadAt=%v", result, readAt)
	}
	if len(client.calls) != 1 {
		t.Fatalf("issued %d statements, want exactly 1 (aggregate only, no row fetch)", len(client.calls))
	}
	if strings.Contains(client.calls[0], "LIMIT") {
		t.Fatalf("cardinality statement issued a LIMIT clause: %s -- this measurement must never fetch rows", client.calls[0])
	}
}

func TestRunCardinalityCensus_BoundWindowAppliesPredicate(t *testing.T) {
	t.Parallel()
	client := &d2bFakeClient{count: 7, readAt: time.Now().UTC()}
	window, err := devhealthsource.BuildCardinalityWindow(contextfabric.SubjectPullRequest, timePtr(time.Now().UTC()), nil, nil)
	if err != nil {
		t.Fatalf("BuildCardinalityWindow: %v", err)
	}
	result, err := devhealthsource.RunCardinalityCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, window)
	if err != nil {
		t.Fatalf("RunCardinalityCensus: %v", err)
	}
	if result.Count != 7 {
		t.Fatalf("result.Count = %d, want 7", result.Count)
	}
	if !strings.Contains(client.calls[0], "p.last_synced >=") {
		t.Fatalf("statement missing the window predicate: %s", client.calls[0])
	}
}

func timePtr(t time.Time) *time.Time { return &t }
