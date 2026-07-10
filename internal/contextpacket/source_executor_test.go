package contextpacket_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestCatalogRows_maps_scanned_rows_and_discloses_missing_sources(t *testing.T) {
	// Given
	plan, err := contextpacket.BuildReadPlanV1(fixturePrincipal(), fixtureRequest("catalog-rows", "main", "commit-1"))
	if err != nil {
		t.Fatalf("build read plan: %v", err)
	}
	plan.RepoID = "00000000-0000-0000-0000-000000000001"
	client := &rowClient{fail: map[string]error{"pull_requests.v1": errors.New("unknown table")}}

	// When
	result, err := contextpacket.ExecuteCatalog(context.Background(), contextpacket.NewClickHouseSourceExecutor(client), plan)

	// Then
	if err != nil {
		t.Fatalf("execute catalog: %v", err)
	}
	if len(result.Evidence) == 0 || result.Evidence[0].EvidenceRefID == "" || result.Evidence[0].SchemaVersion == "" {
		t.Fatalf("scanned evidence was not mapped: %#v", result.Evidence)
	}
	if !containsUnavailable(result.Unavailable, "pull_requests.v1", "source_unavailable") {
		t.Fatalf("missing source was not disclosed: %#v", result.Unavailable)
	}
	if !containsUnavailable(result.Unavailable, "work_items.v1", "repo_fallback_branch_not_supported") {
		t.Fatalf("repo fallback was not disclosed: %#v", result.Unavailable)
	}
	if len(result.Watermarks) != len(contextpacket.SourceQueryCatalogV1) {
		t.Fatalf("watermarks=%d, want one per catalog source (%d)", len(result.Watermarks), len(contextpacket.SourceQueryCatalogV1))
	}
	if watermarkStatus(result.Watermarks, "ai_workflow_runs.v1") != "unavailable" {
		t.Fatalf("missing source watermark: %#v", result.Watermarks)
	}
}

type rowClient struct{ fail map[string]error }

func (c *rowClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.Statement == statement {
			if err := c.fail[query.ID]; err != nil {
				return nil, err
			}
			return &rowScanner{rows: [][]any{{"acr:v1:test:1", "dev_health", "test", "1", query.ID, "", "native", 0.9, "citation", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}}, nil
		}
	}
	return nil, errors.New("unexpected query")
}

type rowScanner struct {
	rows [][]any
	row  int
}

func (s *rowScanner) Next() bool { return s.row < len(s.rows) }

func (s *rowScanner) Scan(dest ...any) error {
	for index, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = s.rows[s.row][index].(string)
		case *float64:
			*value = s.rows[s.row][index].(float64)
		case *time.Time:
			*value = s.rows[s.row][index].(time.Time)
		default:
			return errors.New("unexpected destination")
		}
	}
	s.row++
	return nil
}

func (s *rowScanner) Err() error   { return nil }
func (s *rowScanner) Close() error { return nil }

func containsUnavailable(values []contractsv1.UnavailableSource, source, reason string) bool {
	for _, value := range values {
		if value.Source == source && value.Reason == reason {
			return true
		}
	}
	return false
}

func watermarkStatus(values []contractsv1.SourceWatermark, source string) string {
	for _, value := range values {
		if value.Source == source {
			return value.Status
		}
	}
	return ""
}
