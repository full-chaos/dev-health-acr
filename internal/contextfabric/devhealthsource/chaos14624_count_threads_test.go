package devhealthsource

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type c14624RecordingClient struct{ statements []string }

func (c *c14624RecordingClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements = append(c.statements, statement)
	return &c14624OneCount{}, nil
}

type c14624OneCount struct{ done bool }

func (r *c14624OneCount) Next() bool {
	if r.done {
		return false
	}
	r.done = true
	return true
}

func (r *c14624OneCount) Scan(dest ...any) error {
	*(dest[0].(*uint64)) = 7
	return nil
}

func (r *c14624OneCount) Err() error   { return nil }
func (r *c14624OneCount) Close() error { return nil }

// Every total count statement both sources send runs at 2 threads: its text
// is the count statement followed by the pin, one statement per kind.
func TestCHAOS14624EveryTotalCountStatementPinsTwoThreads(t *testing.T) {
	t.Parallel()
	const pin = "\nSETTINGS max_threads = 2"
	ctx := context.Background()
	clickhouse := &c14624RecordingClient{}
	source, err := NewClickHouseProjectionSource(clickhouse)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ProjectionSourceCounts(ctx, "org-a"); err != nil {
		t.Fatal(err)
	}
	teams := &c14624RecordingClient{}
	teamsSource, err := NewTeamsProjectsSource(teams, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teamsSource.ProjectionSourceCounts(ctx, "org-a"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		label      string
		sent       []string
		statements []kindCountStatement
	}{{"clickhouse", clickhouse.statements, clickHouseKindCountStatements}, {"teams_projects", teams.statements, teamsProjectsKindCountStatements}} {
		if len(c.sent) != len(c.statements) {
			t.Fatalf("%s sent %d statements, want %d (one per kind)", c.label, len(c.sent), len(c.statements))
		}
		for i, sent := range c.sent {
			if !strings.HasSuffix(sent, pin) || strings.TrimSuffix(sent, pin) != c.statements[i].sql {
				t.Errorf("%s %s count statement does not end in %q:\n%s", c.label, c.statements[i].kind, pin, sent)
			}
		}
	}
}
