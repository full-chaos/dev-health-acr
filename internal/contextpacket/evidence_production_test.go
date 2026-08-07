package contextpacket_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type locatorQueryClient struct {
	rows      *locatorQueryRows
	statement *string
	bindings  *[]contextpacket.ClickHouseBinding
}

type highCardinalityEvidenceClient struct {
	targetEvidenceID string
}

type locatorQueryRows struct {
	count   int
	index   int
	scanErr error
	err     error
}

func (c locatorQueryClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if c.statement != nil {
		*c.statement = statement
	}
	if c.bindings != nil {
		*c.bindings = append([]contextpacket.ClickHouseBinding(nil), bindings...)
	}
	return c.rows, nil
}

func (c highCardinalityEvidenceClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if statement == contextpacket.RepositoryScopeQueryV1 {
		return &rowScanner{rows: [][]any{{"00000000-0000-0000-0000-000000000001", "example-org/widget-service", "main"}}}, nil
	}
	if strings.HasPrefix(statement, "SELECT toString(id), repo FROM repos FINAL WHERE") {
		return &rowScanner{rows: [][]any{{"00000000-0000-0000-0000-000000000001", "example-org/widget-service"}}}, nil
	}
	if !strings.Contains(statement, "FROM ci_pipeline_runs AS c FINAL") {
		return &rowScanner{}, nil
	}
	target := []any{c.targetEvidenceID, "dev_health", "ci_pipeline_run", "target-run", "CI target-run", "", "native", 1.0, "passed", time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC), (*time.Time)(nil)}
	branch, _ := bindingValue[string](bindings, "branch")
	if branch == "main" {
		return &rowScanner{rows: [][]any{target}}, nil
	}
	rows := make([][]any, 0, 502)
	for index := range 501 {
		id := fmt.Sprintf("unrelated-%03d", index)
		rows = append(rows, []any{"acr:v1:ci:" + id, "dev_health", "ci_pipeline_run", id, "CI " + id, "", "native", 1.0, "passed", time.Date(2026, 1, 15, 12, 0, 501-index, 0, time.UTC), (*time.Time)(nil)})
	}
	rows = append(rows, target)
	if locatorHash, ok := bindingValue[string](bindings, "evidence_locator_hash"); ok {
		digest := sha256.Sum256([]byte(c.targetEvidenceID))
		if locatorHash == hex.EncodeToString(digest[:]) {
			return &rowScanner{rows: [][]any{target}}, nil
		}
		return &rowScanner{}, nil
	}
	if !strings.Contains(statement, "ORDER BY observed_at DESC, evidence_ref_id ASC") {
		return nil, fmt.Errorf("missing canonical evidence ordering")
	}
	return &rowScanner{rows: rows}, nil
}

func bindingValue[T any](bindings []contextpacket.ClickHouseBinding, name string) (T, bool) {
	for _, binding := range bindings {
		if binding.Name == name {
			value, ok := binding.Value.(T)
			return value, ok
		}
	}
	var zero T
	return zero, false
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
	values := []any{"acr:v1:ci:opaque-reference", "dev_health", "ci_pipeline_run", "run-1", "CI run", "", "native", 1.0, "fixture", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), (*time.Time)(nil)}
	for index, destination := range destinations {
		switch value := destination.(type) {
		case *string:
			*value = values[index].(string)
		case *float64:
			*value = values[index].(float64)
		case *time.Time:
			*value = values[index].(time.Time)
		case **time.Time:
			*value, _ = values[index].(*time.Time)
		default:
			return fmt.Errorf("unsupported destination %T", destination)
		}
	}
	return nil
}

func (r *locatorQueryRows) Err() error   { return r.err }
func (r *locatorQueryRows) Close() error { return nil }

func TestCatalogClickHouseRows_requires_unique_exact_locator_match(t *testing.T) {
	digest := sha256.Sum256([]byte("acr:v1:ci:opaque-reference"))
	locatorHash := hex.EncodeToString(digest[:])
	var statement string
	var bindings []contextpacket.ClickHouseBinding
	rows := contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 1}, statement: &statement, bindings: &bindings})
	references, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, "ci_pipeline_runs.v1", locatorHash)
	if err != nil || len(references) != 1 {
		t.Fatalf("references = %d, error = %v, want one exact match", len(references), err)
	}
	boundHash, ok := bindingValue[string](bindings, "evidence_locator_hash")
	if !strings.Contains(statement, "lower(hex(SHA256(evidence_ref_id))) = {evidence_locator_hash:String} LIMIT 2") || !ok || boundHash != locatorHash {
		t.Fatalf("statement = %q, locator binding = %q, present = %t", statement, boundHash, ok)
	}
	rows = contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 2}})
	if _, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, "ci_pipeline_runs.v1", locatorHash); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ambiguous exact match error = %v, want generic not found", err)
	}
}

func TestCatalogClickHouseRows_preserves_legacy_locator_saturation_guard(t *testing.T) {
	rows := contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 501}})
	if _, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, "ci_pipeline_runs.v1", ""); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("legacy saturation error = %v, want generic not found", err)
	}
}

func TestClickHouseEvidenceStore_resolves_scoped_locator_after_500_unrelated_rows(t *testing.T) {
	// Given
	const targetEvidenceID = "acr:v1:ci:target-run"
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
		if evidence.SourceVersion == "ci_pipeline_runs.v1" && evidence.Source.EntityID == "target-run" {
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
	if expanded.Structured["pipeline_run_id"] != "target-run" || expanded.Evidence.EvidenceRefID != handle {
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
				_, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, "ci_pipeline_runs.v1", "")
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
				_, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, "ci_pipeline_runs.v1", "")
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
