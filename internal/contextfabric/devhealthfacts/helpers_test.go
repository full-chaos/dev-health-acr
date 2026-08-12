package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// findProvider locates the one provider registered for kind out of
// devhealthfacts.NewProviders' slice, so tests don't depend on that slice's
// element order.
func findProvider(t *testing.T, providers []contextfabric.FactProvider, kind contextfabric.FactKind) contextfabric.FactProvider {
	t.Helper()
	for _, provider := range providers {
		if provider.Capability().Kind == kind {
			return provider
		}
	}
	t.Fatalf("no provider registered for fact kind %q", kind)
	return nil
}

// fakeTable is one canned response a fakeClient.Query call can match against,
// mirroring the pattern devhealthsource's own tests use
// (internal/contextfabric/devhealthsource/clickhouse_test.go's fakeTable) --
// devhealthsource's version is unexported inside a _test.go file, so it
// can't be imported; this is a small equivalent for this package.
type fakeTable struct {
	match string
	rows  [][]any
	err   error
}

type capturedQuery struct {
	statement string
	bindings  []contextpacket.ClickHouseBinding
}

type fakeClient struct {
	tables  []fakeTable
	queries []capturedQuery
}

func (c *fakeClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.queries = append(c.queries, capturedQuery{statement: statement, bindings: bindings})
	for _, table := range c.tables {
		if strings.Contains(statement, table.match) {
			if table.err != nil {
				return nil, table.err
			}
			return &fakeScanner{rows: table.rows}, nil
		}
	}
	return &fakeScanner{}, nil
}

// idsBinding returns the "ids" binding value of the last captured query, so
// a test can assert the query was scoped to exactly the subjects it asked
// about, never the whole organization.
func (c *fakeClient) idsBinding() []string {
	if len(c.queries) == 0 {
		return nil
	}
	last := c.queries[len(c.queries)-1]
	for _, binding := range last.bindings {
		if binding.Name == "ids" {
			if ids, ok := binding.Value.([]string); ok {
				return ids
			}
		}
	}
	return nil
}

func (c *fakeClient) orgIDBinding() string {
	if len(c.queries) == 0 {
		return ""
	}
	last := c.queries[len(c.queries)-1]
	for _, binding := range last.bindings {
		if binding.Name == "org_id" {
			if orgID, ok := binding.Value.(string); ok {
				return orgID
			}
		}
	}
	return ""
}

type fakeScanner struct {
	rows [][]any
	row  int
}

func (s *fakeScanner) Next() bool { return s.row < len(s.rows) }

func (s *fakeScanner) Scan(dest ...any) error {
	row := s.rows[s.row]
	for index, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = row[index].(string)
		case *int64:
			*value = row[index].(int64)
		case *uint8:
			*value = row[index].(uint8)
		case *time.Time:
			*value = row[index].(time.Time)
		default:
			return errors.New("devhealthfacts_test: unsupported scan destination")
		}
	}
	s.row++
	return nil
}

func (s *fakeScanner) Err() error   { return nil }
func (s *fakeScanner) Close() error { return nil }
