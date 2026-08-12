package pgprojection_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	"github.com/stretchr/testify/require"
)

// These tests seed acr.agent_episodes through the real production writer
// (internal/storage/postgres.EpisodeStore, the same path
// internal/storage.EpisodeStore.CreateIdempotent uses) rather than hand-
// written INSERTs, so they prove pgprojection.EpisodeStore reads the actual
// stored wire shape, not an assumed one.
func episodeTestPrincipal(orgID, repoSlug string) storage.Principal {
	return storage.Principal{OrgID: orgID, RepositoryScopes: []string{repoSlug}}
}

func TestEpisodeStore_projectsActiveEpisodeSeededByProductionWriter(t *testing.T) {
	ctx := context.Background()
	db := newCheckpointTestDatabase(t, ctx)
	writer, err := storagepostgres.NewEpisodeStore(db)
	require.NoError(t, err)
	principal := episodeTestPrincipal("11111111-1111-1111-1111-111111111111", "example-org/widget-service")
	create := contractsv1.AgentEpisodeCreate{
		SchemaVersion: "agent_episode.v1", ClientEpisodeID: "client-1", IdempotencyKey: "idem-1", ContextPacketID: "packet-1",
		Goal: "fix the checkout flake", Repository: contractsv1.RepositoryRef{Slug: "example-org/widget-service"},
		StartedAt: time.Now().UTC().Add(-time.Hour), EndedAt: time.Now().UTC(), Outcome: "succeeded", Summary: "fixed it",
		RetentionClass: "default_90d",
	}
	created, duplicate, err := writer.CreateIdempotent(ctx, principal, create, nil)
	require.NoError(t, err)
	require.False(t, duplicate)

	reader, err := pgprojection.NewEpisodeStore(db)
	require.NoError(t, err)
	rows, err := reader.EpisodesSince(ctx, principal.OrgID, time.Time{}, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, created.EpisodeID, rows[0].EpisodeID)
	require.Equal(t, "active", rows[0].RedactionState)
	require.Equal(t, "fix the checkout flake", rows[0].Goal)
	require.Equal(t, "fixed it", rows[0].Summary)
	require.Equal(t, "example-org/widget-service", rows[0].RepoSlug)
}

func TestEpisodeStore_redactedEpisodeReportsRedactionStateNotContent(t *testing.T) {
	ctx := context.Background()
	db := newCheckpointTestDatabase(t, ctx)
	writer, err := storagepostgres.NewEpisodeStore(db)
	require.NoError(t, err)
	principal := episodeTestPrincipal("11111111-1111-1111-1111-111111111111", "example-org/widget-service")
	create := contractsv1.AgentEpisodeCreate{
		SchemaVersion: "agent_episode.v1", ClientEpisodeID: "client-2", IdempotencyKey: "idem-2", ContextPacketID: "packet-2",
		Goal: "sensitive goal text", Repository: contractsv1.RepositoryRef{Slug: "example-org/widget-service"},
		StartedAt: time.Now().UTC().Add(-time.Hour), EndedAt: time.Now().UTC(), Outcome: "succeeded", Summary: "sensitive summary",
		RetentionClass: "default_90d",
	}
	created, _, err := writer.CreateIdempotent(ctx, principal, create, nil)
	require.NoError(t, err)
	_, err = writer.Redact(ctx, principal, created.EpisodeID, "user request")
	require.NoError(t, err)

	reader, err := pgprojection.NewEpisodeStore(db)
	require.NoError(t, err)
	rows, err := reader.EpisodesSince(ctx, principal.OrgID, time.Time{}, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "redacted", rows[0].RedactionState)
	require.Empty(t, rows[0].Goal, "a redacted episode must not surface goal content to the projection source")
}

func TestEpisodeStore_ordersByCreatedAtThenEpisodeIDForPagination(t *testing.T) {
	ctx := context.Background()
	db := newCheckpointTestDatabase(t, ctx)
	writer, err := storagepostgres.NewEpisodeStore(db)
	require.NoError(t, err)
	principal := episodeTestPrincipal("11111111-1111-1111-1111-111111111111", "example-org/widget-service")
	for i := 0; i < 3; i++ {
		create := contractsv1.AgentEpisodeCreate{
			SchemaVersion: "agent_episode.v1", ClientEpisodeID: "client-" + string(rune('a'+i)), IdempotencyKey: "idem-" + string(rune('a'+i)),
			ContextPacketID: "packet-1", Goal: "goal", Repository: contractsv1.RepositoryRef{Slug: "example-org/widget-service"},
			StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(), Outcome: "succeeded", Summary: "summary", RetentionClass: "default_90d",
		}
		_, _, err := writer.CreateIdempotent(ctx, principal, create, nil)
		require.NoError(t, err)
	}
	reader, err := pgprojection.NewEpisodeStore(db)
	require.NoError(t, err)
	first, err := reader.EpisodesSince(ctx, principal.OrgID, time.Time{}, "", 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	second, err := reader.EpisodesSince(ctx, principal.OrgID, first[len(first)-1].CreatedAt, first[len(first)-1].EpisodeID, 2)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.NotEqual(t, first[0].EpisodeID, second[0].EpisodeID)
	require.NotEqual(t, first[1].EpisodeID, second[0].EpisodeID)
}
