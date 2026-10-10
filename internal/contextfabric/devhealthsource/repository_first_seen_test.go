package devhealthsource_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

const firstSeenMarker = "AS repo_id_text"

type firstSeenCountingClient struct {
	*fakeClient
	reads atomic.Int32
}

func (c *firstSeenCountingClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, firstSeenMarker) {
		c.reads.Add(1)
	}
	return c.fakeClient.Query(ctx, statement, bindings)
}

func repositoryStarts(t *testing.T, repos [][]any, evidence fakeTable) (map[string]*time.Time, int32) {
	t.Helper()
	client := &firstSeenCountingClient{fakeClient: &fakeClient{tables: []fakeTable{
		{match: "FROM repos", rows: repos, cursorOf: repoCursorOf},
		evidence,
	}}}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	starts := map[string]*time.Time{}
	for _, entity := range batch.Entities {
		if entity.Subject.Kind == contextfabric.SubjectRepository {
			starts[entity.Subject.CanonicalID] = entity.ValidFrom
		}
	}
	return starts, client.reads.Load()
}

func requireStart(t *testing.T, starts map[string]*time.Time, id string, want *time.Time) {
	t.Helper()
	got, present := starts[id]
	if !present {
		t.Fatalf("repository %q was not projected: %v", id, starts)
	}
	switch {
	case want == nil && got != nil:
		t.Fatalf("repository %q start = %s, want none", id, got.Format(time.RFC3339Nano))
	case want != nil && got == nil:
		t.Fatalf("repository %q start = none, want %s", id, want.Format(time.RFC3339Nano))
	case want != nil && !got.Equal(*want):
		t.Fatalf("repository %q start = %s, want %s", id, got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

func TestRepositoryStartIsNeverTheSyncStamp(t *testing.T) {
	t.Parallel()
	synced := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	early := synced.Add(-90 * 24 * time.Hour)
	starts, _ := repositoryStarts(t, [][]any{
		{"restamped", "example-org/restamped", "synthetic", synced, synced, ""},
		{"later", "example-org/later", "synthetic", synced, synced.Add(time.Hour), ""},
		{"epoch", "example-org/epoch", "synthetic", synced, time.Unix(0, 0).UTC(), ""},
		{"subsecond", "example-org/subsecond", "synthetic", synced, time.Unix(0, 500_000_000).UTC(), ""},
		{"real", "example-org/real", "synthetic", synced, early, ""},
	}, fakeTable{match: firstSeenMarker})
	requireStart(t, starts, "repository:restamped", nil)
	requireStart(t, starts, "repository:later", nil)
	requireStart(t, starts, "repository:epoch", nil)
	requireStart(t, starts, "repository:real", &early)
	subsecond := time.Unix(0, 500_000_000).UTC()
	requireStart(t, starts, "repository:subsecond", &subsecond)
}

func TestRepositoryStartTakesTheEarliestEvidence(t *testing.T) {
	t.Parallel()
	synced := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	firstPull := synced.Add(-200 * 24 * time.Hour)
	created := synced.Add(-30 * 24 * time.Hour)
	laterEvidence := synced.Add(-10 * 24 * time.Hour)
	starts, _ := repositoryStarts(t, [][]any{
		{"restamped", "example-org/restamped", "synthetic", synced, synced, ""},
		{"evidence-earlier", "example-org/earlier", "synthetic", synced, created, ""},
		{"evidence-later", "example-org/later", "synthetic", synced, created, ""},
		{"no-evidence", "example-org/none", "synthetic", synced, synced, ""},
	}, fakeTable{match: firstSeenMarker, rows: [][]any{
		{"restamped", firstPull, "pull_request"},
		{"evidence-earlier", firstPull, "work_item"},
		{"evidence-later", laterEvidence, "pull_request"},
	}})
	requireStart(t, starts, "repository:restamped", &firstPull)
	requireStart(t, starts, "repository:evidence-earlier", &firstPull)
	requireStart(t, starts, "repository:evidence-later", &created)
	requireStart(t, starts, "repository:no-evidence", nil)
}

func TestRepositoryEvidenceIsOneStatementPerPage(t *testing.T) {
	t.Parallel()
	synced := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	var repos [][]any
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		repos = append(repos, []any{id, "example-org/" + id, "synthetic", synced, synced, ""})
	}
	_, reads := repositoryStarts(t, repos, fakeTable{match: firstSeenMarker})
	if reads != 1 {
		t.Fatalf("evidence statements for a page of %d repositories = %d, want 1", len(repos), reads)
	}
}

func TestRepositoryStartFallsBackWhenTheEvidenceReadFails(t *testing.T) {
	t.Parallel()
	synced := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	created := synced.Add(-30 * 24 * time.Hour)
	starts, reads := repositoryStarts(t, [][]any{
		{"restamped", "example-org/restamped", "synthetic", synced, synced, ""},
		{"real", "example-org/real", "synthetic", synced, created, ""},
	}, fakeTable{match: firstSeenMarker, err: errors.New("connection reset")})
	if reads != 1 {
		t.Fatalf("evidence statements = %d, want 1", reads)
	}
	requireStart(t, starts, "repository:restamped", nil)
	requireStart(t, starts, "repository:real", &created)
}

func TestRepositoryEvidenceIsNotReadForAnEmptyPage(t *testing.T) {
	t.Parallel()
	_, reads := repositoryStarts(t, nil, fakeTable{match: firstSeenMarker})
	if reads != 0 {
		t.Fatalf("evidence statements for a page with no repository = %d, want 0", reads)
	}
}

func TestRepositoryStartDecisionIsLoggedByBasisOncePerPage(t *testing.T) {
	t.Parallel()
	synced := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	created := synced.Add(-30 * 24 * time.Hour)
	firstPull := synced.Add(-200 * 24 * time.Hour)
	firstItem := synced.Add(-100 * 24 * time.Hour)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repos", cursorOf: repoCursorOf, rows: [][]any{
			{"a", "example-org/a", "synthetic", synced, created, ""},
			{"b", "example-org/b", "synthetic", synced, synced, ""},
			{"c", "example-org/c", "synthetic", synced, synced, ""},
			{"d", "example-org/d", "synthetic", synced, synced, ""},
		}},
		{match: firstSeenMarker, rows: [][]any{{"b", firstPull, "pull_request"}, {"c", firstItem, "work_item"}}},
	}}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	if _, _, err := source.WithLogger(logger).NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}); err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	lines := decodeLines(t, buf.String(), "devhealthsource repository start decided")
	if len(lines) != 1 {
		t.Fatalf("decision lines = %d, want 1 per page: %s", len(lines), buf.String())
	}
	want := map[string]float64{"repositories": 4, "basis_created_at": 1, "basis_pull_request": 1, "basis_work_item": 1, "basis_none": 1}
	for key, value := range want {
		if got, _ := lines[0][key].(float64); got != value {
			t.Fatalf("%s = %v, want %v (line %v)", key, lines[0][key], value, lines[0])
		}
	}
	if lines[0]["evidence_read"] != true {
		t.Fatalf("evidence_read = %v, want true", lines[0]["evidence_read"])
	}
}
