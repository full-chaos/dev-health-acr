package contextpacket_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type locatorQueryClient struct{ rows *locatorQueryRows }

type locatorQueryRows struct {
	count   int
	index   int
	scanErr error
	err     error
}

func (c locatorQueryClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return c.rows, nil
}

func (r *locatorQueryRows) Next() bool {
	if r.index >= r.count {
		return false
	}
	r.index++
	return true
}

func (r *locatorQueryRows) Scan(destinations ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	values := []any{"acr:v1:ci:opaque-reference", "dev_health", "ci_pipeline_run", "run-1", "CI run", "", "native", 1.0, "fixture", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)}
	for index, destination := range destinations {
		switch value := destination.(type) {
		case *string:
			*value = values[index].(string)
		case *float64:
			*value = values[index].(float64)
		case *time.Time:
			*value = values[index].(time.Time)
		default:
			return fmt.Errorf("unsupported destination %T", destination)
		}
	}
	return nil
}

func (r *locatorQueryRows) Err() error   { return r.err }
func (r *locatorQueryRows) Close() error { return nil }

func TestCatalogClickHouseRows_rejects_saturated_locator_results(t *testing.T) {
	rows := contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 501}})
	_, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, "ci_pipeline_runs.v1")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("error = %v, want generic not found", err)
	}
}

func TestCatalogClickHouseRows_preserves_production_iterator_failures(t *testing.T) {
	boom := errors.New("iterator stopped")
	tests := []struct {
		name string
		run  func(*contextpacket.CatalogClickHouseRows) error
	}{
		{
			name: "authorized repositories",
			run: func(rows *contextpacket.CatalogClickHouseRows) error {
				_, err := rows.AuthorizedRepositories(context.Background(), "org-fixture", []string{"example-org/widget-service"})
				return err
			},
		},
		{
			name: "evidence locator",
			run: func(rows *contextpacket.CatalogClickHouseRows) error {
				_, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, "ci_pipeline_runs.v1")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{err: boom}})
			err := test.run(rows)
			if err == nil || errors.Is(err, storage.ErrNotFound) || !errors.Is(err, boom) {
				t.Fatalf("error = %v, want wrapped iterator failure", err)
			}
		})
	}
}

func TestCatalogClickHouseRows_preserves_production_scan_failures(t *testing.T) {
	boom := errors.New("scan failed")
	tests := []struct {
		name string
		run  func(*contextpacket.CatalogClickHouseRows) error
	}{
		{
			name: "authorized repositories",
			run: func(rows *contextpacket.CatalogClickHouseRows) error {
				_, err := rows.AuthorizedRepositories(context.Background(), "org-fixture", []string{"example-org/widget-service"})
				return err
			},
		},
		{
			name: "evidence locator",
			run: func(rows *contextpacket.CatalogClickHouseRows) error {
				_, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, "ci_pipeline_runs.v1")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 1, scanErr: boom}})
			err := test.run(rows)
			if err == nil || errors.Is(err, storage.ErrNotFound) || !errors.Is(err, boom) {
				t.Fatalf("error = %v, want wrapped scan failure", err)
			}
		})
	}
}
