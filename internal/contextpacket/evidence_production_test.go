package contextpacket_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type locatorQueryClient struct{ rows *locatorQueryRows }

type highCardinalityEvidenceClient struct {
	targetEvidenceID string
}

type locatorQueryRows struct {
	count   int
	index   int
	scanErr error
	err     error
}

func (c locatorQueryClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return c.rows, nil
}

func (c highCardinalityEvidenceClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if statement == contextpacket.RepositoryScopeQueryV1 {
		return &rowScanner{rows: [][]any{{"00000000-0000-0000-0000-000000000001", "example-org/widget-service", "main"}}}, nil
	}
	if strings.HasPrefix(statement, "SELECT toString(id), repo FROM repos FINAL WHERE") {
		return &rowScanner{rows: [][]any{{"00000000-0000-0000-0000-000000000001", "example-org/widget-service"}}}, nil
	}
	if !strings.Contains(statement, "FROM work_graph_edges FINAL") {
		return &rowScanner{}, nil
	}
	rows := make([][]any, 0, 502)
	for index := range 501 {
		id := fmt.Sprintf("unrelated-%03d", index)
		rows = append(rows, []any{"acr:v1:graph:" + id, "dev_health", "work_graph_edge", id, "unrelated edge", "", "native", 1.0, "unrelated", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)})
	}
	target := []any{c.targetEvidenceID, "dev_health", "work_graph_edge", "target-edge", "target edge", "", "native", 1.0, "target", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)}
	rows = append(rows, target)
	if strings.Contains(statement, "ORDER BY observed_at DESC, evidence_ref_id ASC") {
		rows = append([][]any{target}, rows[:len(rows)-1]...)
	}
	if strings.Contains(statement, "LIMIT 501") {
		rows = rows[:501]
	}
	return &rowScanner{rows: rows}, nil
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

func TestCatalogClickHouseRows_uses_bounded_canonical_evidence_candidates(t *testing.T) {
	rows := contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 501}})
	references, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, "ci_pipeline_runs.v1")
	if err != nil || len(references) != 100 {
		t.Fatalf("references = %d, error = %v, want 100 bounded candidates", len(references), err)
	}
}

func TestClickHouseEvidenceStore_resolves_matching_locator_after_500_unrelated_rows(t *testing.T) {
	// Given
	const targetEvidenceID = "acr:v1:graph:target-edge"
	principal := fixturePrincipal()
	rows := contextpacket.NewCatalogClickHouseRows(highCardinalityEvidenceClient{targetEvidenceID: targetEvidenceID})
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(rows, contextpacket.EvidenceStoreOptions{Codec: fixtureEvidenceCodec(t)})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}
	bundle, err := store.ContextForTask(context.Background(), principal, fixtureRequest("req-high-cardinality", "main", ""))
	if err != nil {
		t.Fatalf("emit context evidence: %v", err)
	}
	handle := ""
	for _, evidence := range bundle.Evidence {
		if evidence.SourceVersion == "work_graph.v1" && evidence.Source.EntityID == "target-edge" {
			handle = evidence.EvidenceRefID
			break
		}
	}
	if handle == "" {
		t.Fatal("context evidence omitted target locator")
	}

	// When
	expanded, err := store.ResolveEvidence(context.Background(), principal, handle)

	// Then
	if err != nil {
		t.Fatalf("resolve evidence after unrelated rows: %v", err)
	}
	if expanded.Structured["edge_id"] != "target-edge" || expanded.Evidence.EvidenceRefID != handle {
		t.Fatalf("expanded evidence = %#v", expanded)
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
