package devhealthsource_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type fakeTable struct {
	match string
	rows  [][]any
	err   error
}

type fakeClient struct {
	tables []fakeTable
}

func (c *fakeClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
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
			return errors.New("devhealthsource_test: unsupported scan destination")
		}
	}
	s.row++
	return nil
}

func (s *fakeScanner) Err() error   { return nil }
func (s *fakeScanner) Close() error { return nil }

func repoRow(id, slug, provider string, at time.Time) fakeTable {
	return fakeTable{match: "FROM repos", rows: [][]any{{id, slug, provider, at}}}
}

func baseTables(at time.Time) []fakeTable {
	return []fakeTable{
		repoRow("repo-1", "example-org/widget-service", "synthetic", at),
		{match: "FROM work_items AS w", rows: [][]any{{"WIDGET-101", "repo-1", "example-org/widget-service", "Investigate checkout flake", "in_progress", "", at}}},
		{match: "FROM git_pull_requests AS p", rows: [][]any{{"repo-1", "example-org/widget-service", int64(1042), "Typed session tokens", "open", at}}},
		{match: "FROM deployments AS d", rows: [][]any{{"repo-1", "example-org/widget-service", "deploy-1", "success", "production", at}}},
		{match: "FROM operational_incidents AS i", rows: [][]any{{"incident-1", "repo-1", "example-org/widget-service", "Widget incident", "open", "low", at, uint8(0)}}},
		{match: "FROM work_item_dependencies AS d", rows: [][]any{{"WIDGET-101", "WIDGET-099", "blocks", "example-org/widget-service", at}}},
		{match: "FROM work_graph_deployment_incident_edges AS e", rows: [][]any{{"edge-1", "deploy-1", "incident-1", "example-org/widget-service", at}}},
	}
}

func TestClickHouseProjectionSourceFullSnapshotIsOneCompleteBatch(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: baseTables(at)}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if !batch.FullSnapshot || !batch.CompleteEnumeration {
		t.Fatalf("full snapshot batch must claim complete enumeration: %+v", batch)
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}
	// organization + repository + work item + pull request + deployment + incident
	if len(batch.Entities) != 6 {
		t.Fatalf("entities = %d, want 6: %+v", len(batch.Entities), batch.Entities)
	}
	// 4 BELONGS_TO_REPOSITORY (work item, PR, deployment, incident) + 1 work item dependency + 1 deployment/incident edge
	if len(batch.Relationships) != 6 {
		t.Fatalf("relationships = %d, want 6: %+v", len(batch.Relationships), batch.Relationships)
	}
	if len(batch.Tombstones) != 0 {
		t.Fatalf("unexpected tombstones: %+v", batch.Tombstones)
	}
	if batch.Cursor != "" || batch.NextCursor == "" {
		t.Fatalf("cursor = %q, next_cursor = %q", batch.Cursor, batch.NextCursor)
	}
}

func TestClickHouseProjectionSourceIncidentTombstoneOnSoftDelete(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM operational_incidents AS i" {
			tables[index].rows = [][]any{{"incident-1", "repo-1", "example-org/widget-service", "Widget incident", "open", "low", at, uint8(1)}}
		}
	}
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	found := false
	for _, tombstone := range batch.Tombstones {
		if tombstone.Kind == "incident" && tombstone.CanonicalID == "incident:incident-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("deleted incident did not become a tombstone: %+v", batch.Tombstones)
	}
	for _, entity := range batch.Entities {
		if entity.Subject.CanonicalID == "incident:incident-1" {
			t.Fatalf("deleted incident must not also be projected as a live entity: %+v", entity)
		}
	}
	// A tombstoned incident has no BELONGS_TO_REPOSITORY relationship either.
	for _, relationship := range batch.Relationships {
		if relationship.From.CanonicalID == "incident:incident-1" {
			t.Fatalf("deleted incident must not carry a live relationship: %+v", relationship)
		}
	}
}

func TestClickHouseProjectionSourceFullSnapshotRejectsOversizedOrganization(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	rows := make([][]any, 0, 200)
	for i := 0; i < 200; i++ {
		rows = append(rows, []any{"repo-1", "example-org/widget-service", "synthetic", at.Add(time.Duration(i) * time.Second)})
	}
	tables := baseTables(at)
	tables[0] = fakeTable{match: "FROM repos", rows: rows}
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	_, _, err = source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err == nil || !strings.Contains(err.Error(), "exceeds full-snapshot capacity") {
		t.Fatalf("expected a bounded full-snapshot capacity error, got: %v", err)
	}
}

func TestClickHouseProjectionSourceIncrementalNoNewRowsReturnsUnavailable(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: baseTables(at)})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	snapshot, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("snapshot batch: %v", err)
	}
	// Same cursor, now against an empty backend (nothing new since the
	// snapshot): no batch should be available.
	empty, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := empty.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: snapshot.NextCursor})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if available {
		t.Fatalf("expected no batch to be available, got: %+v", batch)
	}
}

func TestClickHouseProjectionSourceReplayIsDeterministic(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: baseTables(at)}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}
	first, _, err := source.NextProjectionBatch(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	second, _, err := source.NextProjectionBatch(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("replayed batch: %v", err)
	}
	if first.BatchID != second.BatchID {
		t.Fatalf("replay must be idempotent: batch IDs %q != %q", first.BatchID, second.BatchID)
	}
}

func TestClickHouseProjectionSourceRejectsEmptyOrganization(t *testing.T) {
	t.Parallel()
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	if _, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{}); err == nil {
		t.Fatal("expected an error for a missing organization")
	}
}

func TestClickHouseProjectionSourceWrapsQueryFailureAsUnavailable(t *testing.T) {
	t.Parallel()
	tables := baseTables(time.Now())
	tables[0] = fakeTable{match: "FROM repos", err: errors.New("connection reset")}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	_, _, err = source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("expected a retryable ErrUnavailable, got: %v", err)
	}
}
