package devhealthsource_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
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
		{EpisodeID: "ep-1", RepoSlug: "example-org/widget-service", Goal: "fix flake", Outcome: "succeeded", Summary: "fixed it", StartedAt: at, EndedAt: at.Add(time.Minute), CreatedAt: at, UpdatedAt: at, RedactionState: "active"},
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
	createdAt := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	// Redaction happens strictly after creation -- CreatedAt and UpdatedAt
	// deliberately differ so this test also proves the tombstone's
	// EffectiveAt (and the candidate's cursor position) come from
	// UpdatedAt, not CreatedAt (CHAOS-3753 codex finding C4/C5): using
	// CreatedAt here would never surface a redaction that happens after a
	// caller's checkpoint already passed the row's original creation time.
	redactedAt := createdAt.Add(time.Hour)
	source, err := devhealthsource.NewEpisodesProjectionSource(&fakeEpisodeRows{rows: []storage.EpisodeProjectionRecord{
		{EpisodeID: "ep-1", RepoSlug: "example-org/widget-service", Outcome: "succeeded", StartedAt: createdAt, EndedAt: createdAt, CreatedAt: createdAt, UpdatedAt: redactedAt, RedactionState: "redacted"},
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
	if !batch.Tombstones[0].EffectiveAt.Equal(redactedAt) {
		t.Fatalf("tombstone EffectiveAt = %v, want UpdatedAt (%v), not CreatedAt (%v)", batch.Tombstones[0].EffectiveAt, redactedAt, createdAt)
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

// TestEpisodesProjectionSourceRedactionAfterProjectionSurfacesAsATombstoneInTheNextBatch
// is CHAOS-3753 codex finding C4's end-to-end regression test: project (a
// batch sees the episode active, the coordinator's checkpoint advances
// past it) -> redact -> the next batch, built from that already-advanced
// checkpoint, must contain the tombstone. This drives EpisodesProjectionSource
// against the real internal/storage/memory.EpisodeStore (not a hand-rolled
// fake) precisely because the underlying bug lived in EpisodeStore.ListSince's
// watermark column, not in this package's own tombstone-conversion logic --
// a fake that doesn't reproduce that watermark behavior couldn't have
// caught it. See internal/storage/{postgres,memory} for the lower-level
// probes against each ListSince implementation directly.
func TestEpisodesProjectionSourceRedactionAfterProjectionSurfacesAsATombstoneInTheNextBatch(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	store := memory.NewEpisodeStore(func() time.Time { return clock })
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"owner/repo"}}
	create := contractsv1.AgentEpisodeCreate{
		SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: "episode_01", IdempotencyKey: "idempotency_01", ContextPacketID: "packet_01",
		Goal: "fix the checkout flake", Summary: "fixed it", Repository: contractsv1.RepositoryRef{Slug: "owner/repo", RepoID: "repo_01"},
		Client:    contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"},
		StartedAt: clock.Add(-time.Hour), EndedAt: clock, Outcome: "succeeded", RetentionClass: "default_90d",
		Artifacts: contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}}, Transcript: contractsv1.TranscriptRef{Mode: "none"},
	}
	episode, _, err := store.CreateIdempotent(context.Background(), principal, create, nil)
	if err != nil {
		t.Fatalf("create episode: %v", err)
	}

	source, err := devhealthsource.NewEpisodesProjectionSource(store)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	// Project: first batch sees the episode active; take its NextCursor as
	// the coordinator's advanced checkpoint.
	first, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.EpisodesSourceName})
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if !available || len(first.Episodes) != 1 || first.Episodes[0].EpisodeID != "episode:"+episode.EpisodeID {
		t.Fatalf("first batch = %+v, available=%t", first, available)
	}

	// Redact strictly after the checkpoint's watermark.
	clock = clock.Add(time.Hour)
	if _, err := store.Redact(context.Background(), principal, episode.EpisodeID, "user request"); err != nil {
		t.Fatalf("redact: %v", err)
	}

	// The next batch, resuming from the already-advanced checkpoint, must
	// contain the tombstone.
	second, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.EpisodesSourceName, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second batch: %v", err)
	}
	if !available {
		t.Fatal("expected the redaction to produce an available batch")
	}
	if len(second.Tombstones) != 1 || second.Tombstones[0].CanonicalID != "episode:"+episode.EpisodeID {
		t.Fatalf("second batch tombstones = %+v, want one tombstone for the redacted episode", second.Tombstones)
	}
	if len(second.Episodes) != 0 {
		t.Fatalf("a redacted episode must not also be projected as content: %+v", second.Episodes)
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
