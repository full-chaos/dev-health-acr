package contextpacket_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type typedRows struct {
	values []any
	done   bool
}

func (r *typedRows) Next() bool   { first := !r.done; r.done = true; return first }
func (r *typedRows) Err() error   { return nil }
func (r *typedRows) Close() error { return nil }
func (r *typedRows) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan %d destinations for %d columns", len(dest), len(r.values))
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

type typedClient struct{ statement string }

func (c *typedClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statement = statement
	return &typedRows{values: []any{"acr:v1:work-item-dependency.v2:abc", "dev_health", "work_item_dependency", "a:b:blocks:fwd", "a blocks b", "", "native", 1.0, "BLOCKS", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), "blocks:fwd", "a", "in_progress"}}, nil
}

func TestDependencySourceCarriesTypedBlockerColumnsToTheEvidence(t *testing.T) {
	client := &typedClient{}
	var query contextpacket.SourceQuery
	for _, candidate := range contextpacket.SourceQueryCatalogV1 {
		if candidate.ID == "work_item_dependencies.v1" {
			query = candidate
		}
	}
	rows, err := contextpacket.NewClickHouseSourceExecutor(client).QueryEvidence(context.Background(), query, nil)
	if err != nil {
		t.Fatalf("query evidence: %v", err)
	}
	if len(rows) != 1 || rows[0].Metadata["relation_key"] != "blocks:fwd" || rows[0].Metadata["blocker_work_item_id"] != "a" || rows[0].Metadata["blocker_status"] != "in_progress" {
		t.Fatalf("typed columns not carried: %#v", rows)
	}
	for _, column := range []string{"relation_key", "blocker_work_item_id", "blocker_status"} {
		if !strings.Contains(query.Statement, column) {
			t.Fatalf("statement lacks typed column %s", column)
		}
	}
}
