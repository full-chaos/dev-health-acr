package devhealthsource_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type fakeEpisodeRows struct {
	rows []storage.EpisodeProjectionRecord
	err  error
}

func (f *fakeEpisodeRows) ListSince(_ context.Context, _ string, _ time.Time, _ string, limit int) ([]storage.EpisodeProjectionRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.rows) > limit {
		return f.rows[:limit], nil
	}
	return f.rows, nil
}

func TestEpisodesProjectionSourceProjectsActiveEpisodes(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	source, err := devhealthsource.NewEpisodesProjectionSource(&fakeEpisodeRows{rows: []storage.EpisodeProjectionRecord{
		{EpisodeID: "ep-1", RepoSlug: "example-org/widget-service", Goal: "fix flake", Outcome: "succeeded", Summary: "fixed it", StartedAt: at, EndedAt: at.Add(time.Minute), CreatedAt: at, RedactionState: "active"},
	}})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.EpisodesSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}
	if len(batch.Episodes) != 1 || batch.Episodes[0].Goal != "fix flake" {
		t.Fatalf("episodes = %+v", batch.Episodes)
	}
	if len(batch.Tombstones) != 0 {
		t.Fatalf("unexpected tombstones: %+v", batch.Tombstones)
	}
}

func TestEpisodesProjectionSourceRedactedEpisodeBecomesTombstone(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	source, err := devhealthsource.NewEpisodesProjectionSource(&fakeEpisodeRows{rows: []storage.EpisodeProjectionRecord{
		{EpisodeID: "ep-1", RepoSlug: "example-org/widget-service", Outcome: "succeeded", StartedAt: at, EndedAt: at, CreatedAt: at, RedactionState: "redacted"},
	}})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.EpisodesSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if len(batch.Episodes) != 0 {
		t.Fatalf("redacted episode must not be projected as content: %+v", batch.Episodes)
	}
	if len(batch.Tombstones) != 1 || batch.Tombstones[0].CanonicalID != "episode:ep-1" {
		t.Fatalf("tombstones = %+v", batch.Tombstones)
	}
}

func TestEpisodesProjectionSourceNoRowsReturnsUnavailable(t *testing.T) {
	t.Parallel()
	source, err := devhealthsource.NewEpisodesProjectionSource(&fakeEpisodeRows{})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	_, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.EpisodesSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if available {
		t.Fatal("expected no batch to be available")
	}
}

func TestEpisodesProjectionSourceWrapsFailureAsUnavailable(t *testing.T) {
	t.Parallel()
	source, err := devhealthsource.NewEpisodesProjectionSource(&fakeEpisodeRows{err: errors.New("connection reset")})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	_, _, err = source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.EpisodesSourceName})
	if !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got: %v", err)
	}
}
