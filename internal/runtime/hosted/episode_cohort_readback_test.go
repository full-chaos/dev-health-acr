package hosted

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/episode"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// TestCohortScopedWriteback_writtenEpisodesAreReadableViaThe3564ReadPath
// verifies the pairing CHAOS-3565 calls for explicitly: an episode written
// by a design-partner cohort org through the cohort-scoped creator must be
// readable back through episode.Service's GetByID/List (CHAOS-3564), and an
// org outside the cohort must be unable to write at all -- proven against
// the real episode.Service and memory store wiring, the same shape Open()
// composes, not a hand-rolled fake standing in for the read/write path.
func TestCohortScopedWriteback_writtenEpisodesAreReadableViaThe3564ReadPath(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := memory.NewEpisodeStore()
	service, err := episode.NewService(store, memory.NewAuditStore(), episode.ServiceOptions{
		Now: func() time.Time { return now }, PacketStore: readbackPacketStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	creator := newCohortScopedEpisodeCreator(service, []string{"org_design_partner_1"})

	writer := storage.Principal{
		OrgID: "org_design_partner_1", CredentialID: "cred_writer", RepositoryScopes: []string{"owner/repo"},
		Permissions: []string{auth.ScopeEpisodeWrite}, ProductEntitlements: []string{"agent_context_runtime"},
	}
	created, duplicate, err := creator.Create(context.Background(), writer, readbackEpisodeCreate())
	if err != nil || duplicate {
		t.Fatalf("cohort org create = (%#v, %t, %v)", created, duplicate, err)
	}

	// The read path (CHAOS-3564) is never cohort-restricted: the same
	// service, called directly (as internal/runtime/hosted.open wires
	// EpisodeReader), must read back what the cohort-scoped creator wrote.
	reader := storage.Principal{
		OrgID: "org_design_partner_1", CredentialID: "cred_reader", RepositoryScopes: []string{"owner/repo"},
		Permissions: []string{auth.ScopeEpisodeRead}, ProductEntitlements: []string{"agent_context_runtime"},
	}
	got, err := service.GetByID(context.Background(), reader, created.EpisodeID)
	if err != nil || got.EpisodeID != created.EpisodeID || got.Goal != readbackEpisodeCreate().Goal {
		t.Fatalf("read back written episode = (%#v, %v)", got, err)
	}
	listed, err := service.List(context.Background(), reader, "owner/repo", 10)
	if err != nil || len(listed) != 1 || listed[0].EpisodeID != created.EpisodeID {
		t.Fatalf("list after cohort write = (%#v, %v)", listed, err)
	}

	// An org outside the cohort must never be able to write in the first
	// place -- not merely fail some downstream check.
	outsider := storage.Principal{
		OrgID: "org_not_in_cohort", CredentialID: "cred_outsider", RepositoryScopes: []string{"owner/repo"},
		Permissions: []string{auth.ScopeEpisodeWrite}, ProductEntitlements: []string{"agent_context_runtime"},
	}
	outsiderCreate := readbackEpisodeCreate()
	outsiderCreate.ClientEpisodeID, outsiderCreate.IdempotencyKey = "episode_outsider", "idempotency_outsider"
	if _, _, err := creator.Create(context.Background(), outsider, outsiderCreate); !errors.Is(err, ErrWritebackNotEnabledForOrg) {
		t.Fatalf("outsider create error = %v, want ErrWritebackNotEnabledForOrg", err)
	}
}

type readbackPacketStore struct{}

func (readbackPacketStore) SaveSnapshot(context.Context, storage.Principal, contractsv1.ContextPacket, time.Time) error {
	return nil
}

func (readbackPacketStore) GetSnapshot(context.Context, storage.Principal, string) (contractsv1.ContextPacket, error) {
	return contractsv1.ContextPacket{Repository: contractsv1.RepositoryRef{Slug: "owner/repo"}}, nil
}

func (readbackPacketStore) PurgeExpired(context.Context, time.Time, int) (int, error) { return 0, nil }

func readbackEpisodeCreate() contractsv1.AgentEpisodeCreate {
	now := time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC)
	return contractsv1.AgentEpisodeCreate{
		SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: "episode_readback", IdempotencyKey: "idempotency_readback",
		ContextPacketID: "packet_readback_01",
		Goal:            "verify cohort writeback is readable", Summary: "design-partner cohort round trip",
		Repository: contractsv1.RepositoryRef{Slug: "owner/repo"},
		Client:     contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"},
		StartedAt:  now, EndedAt: now, Outcome: "succeeded", RetentionClass: "default_90d",
		Artifacts:  contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}},
		Transcript: contractsv1.TranscriptRef{Mode: "none"},
	}
}
