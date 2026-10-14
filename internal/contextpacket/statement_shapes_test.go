package contextpacket_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type shapeRows struct {
	probe bool
	count uint64
	read  bool
}

func (r *shapeRows) Next() bool   { first := !r.read; r.read = true; return first && r.probe }
func (r *shapeRows) Err() error   { return nil }
func (r *shapeRows) Close() error { return nil }
func (r *shapeRows) Scan(dest ...any) error {
	if len(dest) == 1 {
		if target, ok := dest[0].(*uint64); ok {
			*target = r.count
		}
	}
	return nil
}

type shapeClient struct {
	mu         sync.Mutex
	columns    uint64
	statements []string
}

func (c *shapeClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Contains(statement, "FROM system.columns") {
		return &shapeRows{probe: true, count: c.columns}, nil
	}
	c.statements = append(c.statements, statement)
	return &shapeRows{}, nil
}

func (c *shapeClient) lastEvidenceStatement() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := len(c.statements) - 1; index >= 0; index-- {
		if strings.Contains(c.statements[index], "deployments") {
			return c.statements[index]
		}
	}
	return ""
}

func deploymentsQuery(t *testing.T) contextpacket.SourceQuery {
	t.Helper()
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.ID == "deployments.v1" {
			return query
		}
	}
	t.Fatal("deployments.v1 is not in the catalog")
	return contextpacket.SourceQuery{}
}

func TestTheDeploymentsStatementNamesTheTierColumnOnlyWhenTheTableHasIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		columns uint64
		want    string
		absent  string
	}{
		{"table with the column", 1, "d.pull_request_tier pull_request_tier", "'column_absent' pull_request_tier"},
		{"table before the migration", 0, "'column_absent' pull_request_tier", "d.pull_request_tier pull_request_tier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contextpacket.ResetStatementShapeProbesForTest()
			client := &shapeClient{columns: tc.columns}
			if _, err := contextpacket.NewClickHouseSourceExecutor(client).QueryEvidence(context.Background(), deploymentsQuery(t), nil); err != nil {
				t.Fatalf("query evidence: %v", err)
			}
			statement := client.lastEvidenceStatement()
			if !strings.Contains(statement, tc.want) || strings.Contains(statement, tc.absent) {
				t.Fatalf("statement shape wrong for %s:\n%s", tc.name, statement)
			}
		})
	}
	contextpacket.ResetStatementShapeProbesForTest()
}

func TestAFailedColumnProbeFallsBackToTheOlderShapeWithoutPinningIt(t *testing.T) {
	contextpacket.ResetStatementShapeProbesForTest()
	defer contextpacket.ResetStatementShapeProbesForTest()
	failing := &failingProbeClient{}
	if _, err := contextpacket.NewClickHouseSourceExecutor(failing).QueryEvidence(context.Background(), deploymentsQuery(t), nil); err != nil {
		t.Fatalf("query evidence: %v", err)
	}
	if !strings.Contains(failing.evidence, "'column_absent' pull_request_tier") {
		t.Fatalf("a failed probe did not read the older shape: %s", failing.evidence)
	}
	healthy := &shapeClient{columns: 1}
	if _, err := contextpacket.NewClickHouseSourceExecutor(healthy).QueryEvidence(context.Background(), deploymentsQuery(t), nil); err != nil {
		t.Fatalf("query evidence: %v", err)
	}
	if !strings.Contains(healthy.lastEvidenceStatement(), "d.pull_request_tier pull_request_tier") {
		t.Fatal("a failed probe was cached: the next call did not ask again")
	}
	_ = time.Now
}

type failingProbeClient struct{ evidence string }

func (c *failingProbeClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "FROM system.columns") {
		return nil, context.DeadlineExceeded
	}
	c.evidence = statement
	return &shapeRows{}, nil
}
