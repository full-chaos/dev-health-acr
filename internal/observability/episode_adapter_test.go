package observability

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/episode"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestEpisodeTerminalObserverEmitsBoundedDimensions(t *testing.T) {
	tests := []struct {
		name        string
		observation episode.TerminalObservation
		wantOutcome Outcome
		wantEpisode EpisodeOutcome
		wantAudit   AuditDelivery
	}{
		{
			name:        "success",
			observation: episode.TerminalObservation{Outcome: episode.TerminalOutcomeSuccess, AuditDelivery: episode.AuditDeliveryDelivered},
			wantOutcome: OutcomeSuccess,
			wantEpisode: EpisodeOutcomeSuccess,
			wantAudit:   AuditDeliveryDelivered,
		},
		{
			name:        "failure",
			observation: episode.TerminalObservation{Outcome: episode.TerminalOutcomeFailure, AuditDelivery: episode.AuditDeliveryFailed},
			wantOutcome: OutcomeFailure,
			wantEpisode: EpisodeOutcomeFailure,
			wantAudit:   AuditDeliveryFailed,
		},
		{
			name:        "duplicate",
			observation: episode.TerminalObservation{Outcome: episode.TerminalOutcomeDuplicate, AuditDelivery: episode.AuditDeliveryDelivered},
			wantOutcome: OutcomeSuccess,
			wantEpisode: EpisodeOutcomeDuplicate,
			wantAudit:   AuditDeliveryDelivered,
		},
		{
			name:        "redacted",
			observation: episode.TerminalObservation{Outcome: episode.TerminalOutcomeRedacted, AuditDelivery: episode.AuditDeliverySkipped},
			wantOutcome: OutcomeSuccess,
			wantEpisode: EpisodeOutcomeRedacted,
			wantAudit:   AuditDeliverySkipped,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			sink := NewMemorySink(1)
			observer := NewEpisodeTerminalObserver(NewHooks(sink, nil))

			// When
			observer.ObserveEpisodeTerminal(context.Background(), test.observation)

			// Then
			snapshots := sink.Snapshots()
			if len(snapshots) != 1 {
				t.Fatalf("snapshots = %#v", snapshots)
			}
			snapshot := snapshots[0]
			if snapshot.Kind != KindEpisode || snapshot.Outcome != test.wantOutcome || snapshot.EpisodeOutcome != test.wantEpisode || snapshot.AuditDelivery != test.wantAudit {
				t.Fatalf("snapshot = %#v", snapshot)
			}
		})
	}
}

func TestEpisodeStoreObserverEmitsActualStoreDimensions(t *testing.T) {
	sink := NewMemorySink(1)
	observer := NewEpisodeStoreObserver(NewHooks(sink, nil))

	observer.ObserveEpisodeStore(context.Background(), episode.StoreCallObservation{
		Outcome: episode.StoreCallFailure, Backend: episode.StoreBackendPostgres, Duration: 3 * time.Second, TimedOut: true,
	})

	snapshots := sink.Snapshots()
	if len(snapshots) != 1 || snapshots[0].Kind != KindStore || snapshots[0].StoreQueryClass != StoreQueryEpisode || snapshots[0].StoreBackend != StoreBackendPostgres || snapshots[0].Outcome != OutcomeFailure || !snapshots[0].QueryTimedOut {
		t.Fatalf("snapshots = %#v", snapshots)
	}
}

func TestEpisodeTerminalObserverInstrumentsRealService(t *testing.T) {
	sink := NewMemorySink(3)
	hooks := NewHooks(sink, nil)
	service, err := episode.NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), episode.ServiceOptions{
		Now:              func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) },
		TerminalObserver: NewEpisodeTerminalObserver(hooks), StoreObserver: NewEpisodeStoreObserver(hooks), StoreBackend: episode.StoreBackendMemory,
		PacketStore: observationPacketStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{OrgID: "org_1", CredentialID: "credential_1", RepositoryScopes: []string{"owner/repo"}, Permissions: []string{auth.ScopeEpisodeWrite}, ProductEntitlements: []string{"agent_context_runtime"}}
	create := contractsv1.AgentEpisodeCreate{
		SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: "client_episode_01", IdempotencyKey: "idempotency_01", ContextPacketID: "packet_01",
		Goal: "Persist a scoped episode", Repository: contractsv1.RepositoryRef{Slug: "owner/repo", RepoID: "00000000-0000-0000-0000-000000000010"},
		Scope: contractsv1.EpisodeScope{Branch: "main", CommitSHA: "aabbccddeeff"}, Client: contractsv1.EpisodeClient{Name: "test", Version: "1.0", SidecarVersion: "1.0"},
		StartedAt: time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC), EndedAt: time.Date(2026, 7, 10, 11, 1, 0, 0, time.UTC), Outcome: "succeeded", Summary: "Completed persistence",
		Artifacts: contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}}, Transcript: contractsv1.TranscriptRef{Mode: "none"}, RetentionClass: "default_90d",
	}

	_, _, err = service.Create(context.Background(), principal, create)

	if err != nil {
		t.Fatal(err)
	}
	snapshots := sink.Snapshots()
	if len(snapshots) != 3 || snapshots[0].StoreBackend != StoreBackendMemory || snapshots[0].StoreQueryClass != StoreQueryEpisode || snapshots[1].StoreQueryClass != StoreQueryEpisode || snapshots[2].EpisodeOutcome != EpisodeOutcomeSuccess {
		t.Fatalf("snapshots = %#v", snapshots)
	}
}

type observationPacketStore struct{}

func (observationPacketStore) SaveSnapshot(context.Context, storage.Principal, contractsv1.ContextPacket, time.Time) error {
	return nil
}

func (observationPacketStore) GetSnapshot(context.Context, storage.Principal, string) (contractsv1.ContextPacket, error) {
	return contractsv1.ContextPacket{Repository: contractsv1.RepositoryRef{Slug: "owner/repo"}}, nil
}

func (observationPacketStore) PurgeExpired(context.Context, time.Time, int) (int, error) {
	return 0, nil
}
