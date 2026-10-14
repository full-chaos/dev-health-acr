package contextpacket_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A source-row read of a catalog statement selects the statement's typed
// columns, so the scan must be as wide as the statement: ten standard columns
// plus the typed ones.
func TestResolveSourceRowScansTheTypedColumnsOfACatalogStatement(t *testing.T) {
	client := &resolveRowClient{values: []any{"acr:v1:deployment:dep-1", "dev_health", "deployment", "dep-1", "production deployment", "", "native", 1.0, "status=failure", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), "failure", "42", "exact_merge_sha"}}
	rows := contextpacket.NewCatalogClickHouseRows(client)
	references, err := rows.ResolveSourceRow(context.Background(), "org", contractsv1.ResolvedScope{RepoID: "00000000-0000-4000-8000-000000000001", RepoSlug: "acme/api"},
		contextpacket.SourceRowRead{QueryID: "deployments.v1", Locator: "acr:v1:deployment:dep-1"})
	if err != nil {
		t.Fatalf("resolve source row: %v", err)
	}
	if len(references) != 1 || references[0].Evidence.Metadata["deployment_status"] != "failure" || references[0].Evidence.Metadata["pull_request_number"] != "42" {
		t.Fatalf("typed columns not scanned: %#v", references)
	}
}

type resolveRowClient struct{ values []any }

func (c *resolveRowClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return &resolveRowRows{values: c.values}, nil
}

type resolveRowRows struct {
	values []any
	done   bool
}

func (r *resolveRowRows) Next() bool   { first := !r.done; r.done = true; return first }
func (r *resolveRowRows) Err() error   { return nil }
func (r *resolveRowRows) Close() error { return nil }
func (r *resolveRowRows) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("expected %d destination arguments in Scan, not %d", len(r.values), len(dest))
	}
	for index, value := range r.values {
		switch target := dest[index].(type) {
		case *string:
			*target = value.(string)
		case *float64:
			*target = value.(float64)
		case *time.Time:
			*target = value.(time.Time)
		default:
			return fmt.Errorf("unexpected destination %T", dest[index])
		}
	}
	return nil
}
