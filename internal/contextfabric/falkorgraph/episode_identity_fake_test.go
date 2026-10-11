package falkorgraph

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type episodeRowsFake struct {
	rows []storage.EpisodeProjectionRecord
}

func (f *episodeRowsFake) ListSince(_ context.Context, _ string, _ time.Time, _ string, _ int) ([]storage.EpisodeProjectionRecord, error) {
	return f.rows, nil
}

// TestProducedEpisodeIsWrittenUnderOneCanonicalID drives the production episodes
// source through the adapter's ApplyProjectionBatch and reads back every
// subject id the writes carry.
func TestProducedEpisodeIsWrittenUnderOneCanonicalID(t *testing.T) {
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	const episodeUUID = "0d6f3a52-7c1e-4d8e-9a53-6f1f2b7c9e10"
	source, err := devhealthsource.NewEpisodesProjectionSource(&episodeRowsFake{rows: []storage.EpisodeProjectionRecord{
		{EpisodeID: episodeUUID, RepoSlug: "example-org/widget-service", Goal: "fix flake", Outcome: "succeeded", Summary: "fixed it", StartedAt: at, EndedAt: at.Add(time.Minute), CreatedAt: at, UpdatedAt: at, RedactionState: "active"},
	}})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, ok, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.EpisodesSourceName})
	if err != nil || !ok {
		t.Fatalf("NextProjectionBatch() ok=%v err=%v", ok, err)
	}
	var mu sync.Mutex
	ids := map[string]bool{}
	fake := &fakeConn{constraintsFunc: func(context.Context, string) ([]constraintStatus, error) {
		return []constraintStatus{
			{Type: "UNIQUE", Label: labelSubject, EntityType: "NODE", Status: "OPERATIONAL"},
			{Type: "UNIQUE", Label: labelRelation, EntityType: "RELATIONSHIP", Status: "OPERATIONAL"},
		}, nil
	}, queryFunc: func(_ context.Context, _, _ string, params map[string]interface{}, _ bool) ([]row, error) {
		mu.Lock()
		defer mu.Unlock()
		for name, value := range params {
			if s, isString := value.(string); isString && strings.HasSuffix(name, "Id") && strings.Contains(s, "episode") {
				ids[s] = true
			}
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if _, err := adapter.ApplyProjectionBatch(context.Background(), batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	want := "episode:" + episodeUUID
	if len(ids) != 1 || !ids[want] {
		t.Fatalf("episode ids written = %v, want exactly {%q}", ids, want)
	}
}
