package postgres

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func listSinceEpisodeCreate(clientEpisodeID string) contractsv1.AgentEpisodeCreate {
	return contractsv1.AgentEpisodeCreate{
		SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: clientEpisodeID, IdempotencyKey: "idem-" + clientEpisodeID, ContextPacketID: "packet_01",
		Goal: "fix the checkout flake", Summary: "fixed it", Repository: contractsv1.RepositoryRef{Slug: "owner/repo", RepoID: "repo_01"},
		Client:    contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"},
		StartedAt: time.Now().UTC().Add(-time.Hour), EndedAt: time.Now().UTC(), Outcome: "succeeded", RetentionClass: "default_90d",
		Artifacts: contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}}, Transcript: contractsv1.TranscriptRef{Mode: "none"},
	}
}

// These tests seed acr.agent_episodes through the real production writer
// (CreateIdempotent/Redact) so ListSince is proven against the actual
// stored wire shape, not an assumed one -- mirroring how the CHAOS-3753
// projection-source tests seed episodes.
func TestEpisodeStore_ListSinceOrdersPaginatesAndIsolatesByOrganization(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	store, err := NewEpisodeStore(db)
	require.NoError(t, err)
	orgA := storage.Principal{OrgID: "11111111-1111-1111-1111-111111111111", RepositoryScopes: []string{"owner/repo"}}
	orgB := storage.Principal{OrgID: "22222222-2222-2222-2222-222222222222", RepositoryScopes: []string{"owner/repo"}}

	first, _, err := store.CreateIdempotent(ctx, orgA, listSinceEpisodeCreate("ep-1"), nil)
	require.NoError(t, err)
	second, _, err := store.CreateIdempotent(ctx, orgA, listSinceEpisodeCreate("ep-2"), nil)
	require.NoError(t, err)
	_, _, err = store.CreateIdempotent(ctx, orgB, listSinceEpisodeCreate("ep-3"), nil)
	require.NoError(t, err)

	all, err := store.ListSince(ctx, orgA.OrgID, time.Time{}, "", 10)
	require.NoError(t, err)
	require.Len(t, all, 2, "org B's episode must not appear")
	require.True(t, all[0].CreatedAt.Before(all[1].CreatedAt) || all[0].CreatedAt.Equal(all[1].CreatedAt))

	page1, err := store.ListSince(ctx, orgA.OrgID, time.Time{}, "", 1)
	require.NoError(t, err)
	require.Len(t, page1, 1)
	require.Equal(t, first.EpisodeID, page1[0].EpisodeID)

	page2, err := store.ListSince(ctx, orgA.OrgID, page1[0].CreatedAt, page1[0].EpisodeID, 10)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	require.Equal(t, second.EpisodeID, page2[0].EpisodeID)
}

func TestEpisodeStore_ListSinceReportsRedactionAndPurgeStateWithoutLeakingContent(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	store, err := NewEpisodeStore(db)
	require.NoError(t, err)
	principal := storage.Principal{OrgID: "11111111-1111-1111-1111-111111111111", RepositoryScopes: []string{"owner/repo"}}

	active, _, err := store.CreateIdempotent(ctx, principal, listSinceEpisodeCreate("ep-active"), nil)
	require.NoError(t, err)
	redacted, _, err := store.CreateIdempotent(ctx, principal, listSinceEpisodeCreate("ep-redacted"), nil)
	require.NoError(t, err)
	_, err = store.Redact(ctx, principal, redacted.EpisodeID, "user request")
	require.NoError(t, err)
	noPersist := listSinceEpisodeCreate("ep-purged")
	noPersist.RetentionClass = "no_persist"
	_, _, err = store.CreateIdempotent(ctx, principal, noPersist, nil)
	require.NoError(t, err)

	rows, err := store.ListSince(ctx, principal.OrgID, time.Time{}, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 3)

	byID := map[string]storage.EpisodeProjectionRecord{}
	for _, row := range rows {
		byID[row.EpisodeID] = row
	}
	require.Equal(t, "active", byID[active.EpisodeID].RedactionState)
	require.NotEmpty(t, byID[active.EpisodeID].Goal)

	require.Equal(t, "redacted", byID[redacted.EpisodeID].RedactionState)
	require.Empty(t, byID[redacted.EpisodeID].Goal, "a redacted episode must not surface goal content")

	foundPurged := false
	for _, row := range rows {
		if row.RedactionState == "purged_tombstone" {
			foundPurged = true
			require.Empty(t, row.Goal)
			require.Empty(t, row.Summary)
		}
	}
	require.True(t, foundPurged, "expected a purged_tombstone row: %+v", rows)
}

func TestEpisodeStore_ListSinceRejectsEmptyOrganization(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	store, err := NewEpisodeStore(db)
	require.NoError(t, err)
	_, err = store.ListSince(ctx, "", time.Time{}, "", 10)
	require.Error(t, err)
}
