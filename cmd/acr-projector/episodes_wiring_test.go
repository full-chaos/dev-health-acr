package main

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type unreadableEpisodeRows struct{ t *testing.T }

func (r unreadableEpisodeRows) ListSince(context.Context, string, time.Time, string, int) ([]storage.EpisodeProjectionRecord, error) {
	r.t.Fatal("an episodes source disabled by the write-back flag must not read acr.agent_episodes")
	return nil, nil
}

// The write-back flag must reach the source: with it off the source idles and
// never reads the table (the runtime role has no grant on it); with it on the
// source reports enabled. It stays registered in both states so its
// checkpoint is never stranded.
func TestEpisodesSourceIsGatedByWritebackAndAlwaysRegistered(t *testing.T) {
	t.Parallel()
	off, err := newEpisodesSource(unreadableEpisodeRows{t: t}, false)
	if err != nil {
		t.Fatalf("newEpisodesSource(false): %v", err)
	}
	if off.Enabled() {
		t.Fatal("write-back off must disable the episodes source")
	}
	if _, available, err := off.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.EpisodesSourceName}); err != nil || available {
		t.Fatalf("disabled source must idle silently; got available=%v err=%v", available, err)
	}
	on, err := newEpisodesSource(unreadableEpisodeRows{t: t}, true)
	if err != nil {
		t.Fatalf("newEpisodesSource(true): %v", err)
	}
	if !on.Enabled() {
		t.Fatal("write-back on must enable the episodes source")
	}
	for _, source := range []*devhealthsource.EpisodesProjectionSource{off, on} {
		var found bool
		for _, pair := range projectionSources(nil, source, nil) {
			if pair.Name == devhealthsource.EpisodesSourceName && pair.Source != nil {
				found = true
			}
		}
		if !found {
			t.Fatal("episodes source must be registered regardless of the write-back flag")
		}
	}
}
