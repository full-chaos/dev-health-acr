package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/episode"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestService_Create_normalizesRepositorySlugSoPostgresListStillFindsIt is
// the regression test for the Codex cross-system review finding X5:
// episode.Service.Create passed create.Repository.Slug through to the store
// verbatim, relying entirely on the HTTP handler (episode_routes.go's
// handleEpisode) having already normalized it via auth.NormalizeRepositorySlug
// before Service.Create is ever reached. Called directly (bypassing the
// handler -- a future non-HTTP caller, or a test), a mixed-case slug was
// stored as-is; List's SQL EXISTS clause -- and purgeExpired's identical
// clause -- compares repo_slug by exact, case-sensitive SQL equality against
// the credential's scope (always lowercase, per auth.NormalizeRepositoryScopes
// at credential-issuance time), so a mixed-case-slugged row silently vanished
// from List even though scanAuthorizedEpisode's own Go-side re-check
// (episodeRepositoryAllowed, which lowercases both sides before comparing)
// would have allowed it. memory.EpisodeStore's equivalent check
// (episodeRepositoryAllowed in that package) is case-insensitive throughout,
// so the identical input never diverges there -- this is exactly why the
// backends disagreed. Normalizing at the Service layer, not just the HTTP
// handler, makes both backends agree regardless of caller (the same
// underlying pattern as CHAOS-3599 item 2).
//
// This uses a real PostgreSQL container (like the NEW-2/NEW-3 tests): the
// bug is specifically about what postgres's case-sensitive SQL does with an
// unnormalized slug, which a fake driver or the memory store can't exercise.
func TestService_Create_normalizesRepositorySlugSoPostgresListStillFindsIt(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := NewAuditStore(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := episode.NewService(store, audit, episode.ServiceOptions{
		Now: func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }, PacketStore: slugNormalizationPacketStore{},
	})
	if err != nil {
		t.Fatal(err)
	}

	const orgID = "00000000-0000-0000-0000-0000000000ee"
	writer := storage.Principal{
		OrgID: orgID, RepositoryScopes: []string{"acme/mixed-repo"},
		Permissions: []string{auth.ScopeEpisodeWrite}, ProductEntitlements: []string{"agent_context_runtime"},
	}
	create := slugNormalizationEpisodeCreate()
	create.Repository = contractsv1.RepositoryRef{Slug: "Acme/Mixed-Repo"} // mixed case, as a non-HTTP caller might pass unnormalized

	created, duplicate, err := service.Create(ctx, writer, create)
	if err != nil || duplicate {
		t.Fatalf("create = (%#v, %t, %v)", created, duplicate, err)
	}

	// The reader's credential is scoped the way every real credential is --
	// lowercase, per auth.NormalizeRepositoryScopes.
	reader := storage.Principal{
		OrgID: orgID, RepositoryScopes: []string{"acme/mixed-repo"},
		Permissions: []string{auth.ScopeEpisodeRead}, ProductEntitlements: []string{"agent_context_runtime"},
	}
	listed, err := service.List(ctx, reader, "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 || listed[0].EpisodeID != created.EpisodeID {
		t.Fatalf("list = %#v, want exactly the mixed-case-input episode %q -- a lowercase-scoped credential must find an episode created with a mixed-case repository slug", listed, created.EpisodeID)
	}

	got, err := service.GetByID(ctx, reader, created.EpisodeID)
	if err != nil || got.EpisodeID != created.EpisodeID {
		t.Fatalf("get by id = (%#v, %v), want the mixed-case-input episode", got, err)
	}
}

type slugNormalizationPacketStore struct{}

func (slugNormalizationPacketStore) SaveSnapshot(context.Context, storage.Principal, contractsv1.ContextPacket, time.Time) error {
	return nil
}

func (slugNormalizationPacketStore) GetSnapshot(context.Context, storage.Principal, string) (contractsv1.ContextPacket, error) {
	return contractsv1.ContextPacket{Repository: contractsv1.RepositoryRef{Slug: "acme/mixed-repo"}}, nil
}

func (slugNormalizationPacketStore) PurgeExpired(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

func slugNormalizationEpisodeCreate() contractsv1.AgentEpisodeCreate {
	now := time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC)
	return contractsv1.AgentEpisodeCreate{
		SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: "episode_slug_norm", IdempotencyKey: "idempotency_slug_norm",
		ContextPacketID: "packet_slug_norm_01",
		Goal:            "verify slug normalization at the service layer", Summary: "mixed-case repository slug",
		Repository: contractsv1.RepositoryRef{Slug: "acme/mixed-repo"},
		Client:     contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"},
		StartedAt:  now, EndedAt: now, Outcome: "succeeded", RetentionClass: "default_90d",
		Artifacts:  contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}},
		Transcript: contractsv1.TranscriptRef{Mode: "none"},
	}
}
