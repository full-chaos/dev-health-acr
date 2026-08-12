package devhealthsource_test

import (
	"context"
	"errors"
	"fmt"
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

// TestEpisodesProjectionSourcePagesToCompletionAfterARebuildWhenOversized
// is CHAOS-3753 codex round-2 finding K3's regression test: NextProjectionBatch
// hard-errored when a from-scratch (cursor == "") read exceeded
// episodesSnapshotCap (500) approved episodes, instead of paging like the
// ClickHouse source's C6 fix. A rebuild always resets the checkpoint to
// the zero cursor, so for an organization above that size the error was
// permanent, not transient: every subsequent tick re-attempted the exact
// same oversized from-scratch read and failed the exact same way forever.
// Seeds 501 episodes (one over the cap) in a real memory.EpisodeStore
// (cursor-filtering fidelity matters here, unlike a fake that ignores its
// since/after arguments) and drives NextProjectionBatch across ticks,
// each resuming from the previous tick's NextCursor -- exactly how
// projectionrun.Coordinator actually calls it after a rebuild -- until
// every episode has been projected exactly once.
func TestEpisodesProjectionSourcePagesToCompletionAfterARebuildWhenOversized(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	store := memory.NewEpisodeStore(func() time.Time { return clock })
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"owner/repo"}}
	const episodeCount = 501 // one over episodesSnapshotCap (500)
	for i := 0; i < episodeCount; i++ {
		create := contractsv1.AgentEpisodeCreate{
			SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: fmt.Sprintf("episode-%04d", i), IdempotencyKey: fmt.Sprintf("idem-%04d", i),
			ContextPacketID: "packet_01", Goal: "fix the checkout flake", Summary: "fixed it", Repository: contractsv1.RepositoryRef{Slug: "owner/repo", RepoID: "repo_01"},
			Client:    contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"},
			StartedAt: clock, EndedAt: clock, Outcome: "succeeded", RetentionClass: "default_90d",
			Artifacts: contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}}, Transcript: contractsv1.TranscriptRef{Mode: "none"},
		}
		if _, _, err := store.CreateIdempotent(context.Background(), principal, create, nil); err != nil {
			t.Fatalf("create episode %d: %v", i, err)
		}
		clock = clock.Add(time.Second) // distinct UpdatedAt per episode
	}

	source, err := devhealthsource.NewEpisodesProjectionSource(store)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	// A rebuild resets the checkpoint to the zero cursor.
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.EpisodesSourceName}
	seen := map[string]bool{}
	pages := 0
	for pages = 0; pages < episodeCount; pages++ {
		batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err != nil {
			t.Fatalf("page %d: next projection batch: %v", pages, err)
		}
		if !available {
			break
		}
		for _, episode := range batch.Episodes {
			if seen[episode.EpisodeID] {
				t.Fatalf("page %d: episode %s was projected twice", pages, episode.EpisodeID)
			}
			seen[episode.EpisodeID] = true
		}
		checkpoint.Cursor = batch.NextCursor
	}
	if len(seen) != episodeCount {
		t.Fatalf("expected all %d episodes to be projected across %d pages, got %d", episodeCount, pages, len(seen))
	}
	if pages < 2 {
		t.Fatalf("expected catch-up to take more than one page given the oversized organization, took %d", pages)
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
