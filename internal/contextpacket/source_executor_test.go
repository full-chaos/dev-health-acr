package contextpacket_test

import (
	"context"
	"errors"
	"strings"
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
	observer := &catalogObserver{}

	// When
	result, err := contextpacket.ExecuteCatalogObserved(context.Background(), contextpacket.NewClickHouseSourceExecutor(client), plan, observer)

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
	workItemsReachable := false
	for _, ref := range result.Evidence {
		if ref.SourceVersion == "work_items.v1" {
			workItemsReachable = ref.Source.DisplayLabel == "work_items.v1 (repository-wide)" && ref.Metadata["scope_breadth"] == "repository-wide"
			break
		}
	}
	if !workItemsReachable {
		t.Fatalf("repository-wide work-item evidence was not reachable and labeled: %#v", result.Evidence)
	}
	if len(result.Watermarks) != len(contextpacket.SourceQueryCatalogV1) {
		t.Fatalf("watermarks=%d, want one per catalog source (%d)", len(result.Watermarks), len(contextpacket.SourceQueryCatalogV1))
	}
	// ai_workflow_runs.v1 is repo-scoped and now runs under a branch-scoped
	// request. Its fixture rows are dated 2026-01-01, so the honest watermark is
	// stale: reachable, but not recent. A status of unavailable or missing would
	// mean the scope gate had silently excluded it again.
	if status := watermarkStatus(result.Watermarks, "ai_workflow_runs.v1"); status != "stale" {
		t.Fatalf("repository-wide source watermark = %q, want stale: %#v", status, result.Watermarks)
	}
	if watermarkStatus(result.Watermarks, "pull_requests.v1") != "unavailable" {
		t.Fatalf("missing source watermark: %#v", result.Watermarks)
	}
	if len(observer.store) == 0 {
		t.Fatal("source queries emitted no observations")
	}
	failures := 0
	for _, observation := range observer.store {
		if observation.Backend != contextpacket.StoreBackendClickHouse {
			t.Fatalf("backend = %q", observation.Backend)
		}
		if observation.Outcome == contextpacket.OperationFailure {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("failure observations = %d", failures)
	}
}

func TestObservedCatalogRowsReportsEveryDirectQueryTimeout(t *testing.T) {
	plan, err := contextpacket.BuildReadPlanV1(fixturePrincipal(), fixtureRequest("observed-direct-queries", "main", "commit-1"))
	if err != nil {
		t.Fatal(err)
	}
	observer := &catalogObserver{}
	rows := contextpacket.NewObservedCatalogClickHouseRows(timeoutQueryClient{}, observer)

	_, _ = rows.ResolveEvidenceScope(context.Background(), plan)
	_, _ = rows.AuthorizedRepositories(context.Background(), plan.OrgID, []string{plan.RepoSlug})
	_, _ = rows.ResolveEvidenceReference(context.Background(), plan.OrgID, contractsv1.ResolvedScope{RepoID: "repo_1", RepoSlug: plan.RepoSlug}, contextpacket.SourceQueryCatalogV1[0].ID)

	if len(observer.store) != 3 {
		t.Fatalf("observations = %#v", observer.store)
	}
	for _, observation := range observer.store {
		if observation.Backend != contextpacket.StoreBackendClickHouse || observation.Outcome != contextpacket.OperationTimeout {
			t.Fatalf("observation = %#v", observation)
		}
	}
}

func TestObservedEvidenceStoreFactoryInjectsScopeQueryObserver(t *testing.T) {
	observer := &catalogObserver{}
	rows := contextpacket.NewCatalogClickHouseRows(timeoutQueryClient{})
	store, err := contextpacket.NewObservedEvidenceStoreFactory(fixtureEvidenceCodec(t), nil, observer)(rows)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = store.ResolveScope(context.Background(), fixturePrincipal(), fixtureRequest("factory-scope-observation", "main", "commit-1"))

	if len(observer.store) != 1 || observer.store[0].Operation != contextpacket.StoreOperationScope || observer.store[0].Outcome != contextpacket.OperationTimeout {
		t.Fatalf("observations = %#v", observer.store)
	}
}

func TestClickHouseSourceExecutor_bounds_source_rows_before_ranking(t *testing.T) {
	// Given
	rows := make([][]any, 101)
	for index := range rows {
		rows[index] = []any{"acr:v1:test:1", "dev_health", "test", "1", "test", "", "native", 0.9000000000000001, "citation", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	}
	client := &boundedRowClient{rows: &rowScanner{rows: rows}}
	executor := contextpacket.NewClickHouseSourceExecutor(client)

	// When
	evidence, err := executor.QueryEvidence(context.Background(), contextpacket.SourceQueryCatalogV1[0], nil)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 100 || client.rows.row != 100 {
		t.Fatalf("evidence=%d scanned=%d, want 100", len(evidence), client.rows.row)
	}
	if evidence[0].Confidence != 0.9000000000000001 {
		t.Fatalf("confidence = %.17g, want Float64 precision", evidence[0].Confidence)
	}
	if !strings.Contains(client.statement, "ORDER BY observed_at DESC, evidence_ref_id ASC LIMIT {source_row_limit:UInt32}") {
		t.Fatalf("query is not deterministically bounded: %s", client.statement)
	}
	if len(client.bindings) != 1 || client.bindings[0].Name != "source_row_limit" || client.bindings[0].Value != uint32(100) {
		t.Fatalf("row limit binding = %#v", client.bindings)
	}
}

type catalogObserver struct {
	store []contextpacket.StoreQueryObservation
}

func (o *catalogObserver) ObserveStoreQuery(_ context.Context, observation contextpacket.StoreQueryObservation) {
	o.store = append(o.store, observation)
}

func (*catalogObserver) ObserveRanking(context.Context, contextpacket.RankingObservation) {}
func (*catalogObserver) ObservePacket(context.Context, contextpacket.PacketObservation)   {}

type rowClient struct{ fail map[string]error }

type timeoutQueryClient struct{}

type boundedRowClient struct {
	statement string
	bindings  []contextpacket.ClickHouseBinding
	rows      *rowScanner
}

func (c *boundedRowClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statement = statement
	c.bindings = bindings
	return c.rows, nil
}

func (timeoutQueryClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return nil, context.DeadlineExceeded
}

func (c *rowClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if strings.Contains(statement, query.Statement) {
			if err := c.fail[query.ID]; err != nil {
				return nil, err
			}
			return &rowScanner{rows: [][]any{{"acr:v1:test:1", "dev_health", "test", "1", query.ID, "", "native", 0.9000000000000001, "citation", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}}, nil
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
