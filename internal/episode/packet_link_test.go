package episode

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestCreateReturnsIndistinguishableNotFoundForInvalidOrMismatchedPacket(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	packetStore := memory.NewPacketStore(func() time.Time { return now })
	principal := episodePrincipal("org_1")
	principal.RepositoryScopes = []string{"*"}
	mismatched := episodePacket(now, "packet_mismatched", "owner/other")
	if err := packetStore.SaveSnapshot(context.Background(), principal, mismatched, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), ServiceOptions{Now: func() time.Time { return now }, PacketStore: packetStore})
	if err != nil {
		t.Fatal(err)
	}
	unknown := episodeCreate()
	unknown.ContextPacketID = "malformed packet id"
	wrongRepository := episodeCreate()
	wrongRepository.ContextPacketID = mismatched.ContextPacketID

	// When
	_, _, unknownErr := service.Create(context.Background(), principal, unknown)
	_, _, mismatchErr := service.Create(context.Background(), principal, wrongRepository)

	// Then
	if !errors.Is(unknownErr, storage.ErrNotFound) || !errors.Is(mismatchErr, storage.ErrNotFound) || unknownErr.Error() != mismatchErr.Error() {
		t.Fatalf("packet link errors = (%v, %v), want indistinguishable not found", unknownErr, mismatchErr)
	}
}

func TestCreateReplaysIdenticalEpisodeAfterPacketExpires(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	packetStore := memory.NewPacketStore(func() time.Time { return now })
	principal := episodePrincipal("org_1")
	create := episodeCreate()
	if err := packetStore.SaveSnapshot(context.Background(), principal, episodePacket(now, create.ContextPacketID, create.Repository.Slug), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), ServiceOptions{Now: func() time.Time { return now }, PacketStore: packetStore})
	if err != nil {
		t.Fatal(err)
	}
	created, duplicate, err := service.Create(context.Background(), principal, create)
	if err != nil || duplicate {
		t.Fatalf("create = (%#v, %t, %v)", created, duplicate, err)
	}
	now = now.Add(2 * time.Minute)

	// When
	replayed, duplicate, err := service.Create(context.Background(), principal, create)

	// Then
	if err != nil || !duplicate || replayed.EpisodeID != created.EpisodeID {
		t.Fatalf("expired packet replay = (%#v, %t, %v)", replayed, duplicate, err)
	}
}

func TestCreateReturnsCancellationBeforePacketPreflightOrPersistence(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	packetStore := memory.NewPacketStore(func() time.Time { return now })
	principal := episodePrincipal("org_1")
	create := episodeCreate()
	if err := packetStore.SaveSnapshot(context.Background(), principal, episodePacket(now, create.ContextPacketID, create.Repository.Slug), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	episodeStore := memory.NewEpisodeStore()
	auditStore := memory.NewAuditStore()
	service, err := NewService(episodeStore, auditStore, ServiceOptions{Now: func() time.Time { return now }, PacketStore: packetStore})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	_, _, err = service.Create(ctx, principal, create)

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled create error = %v", err)
	}
	if events := auditStore.Events(); len(events) != 0 {
		t.Fatalf("canceled create audit events = %#v", events)
	}
	if _, getErr := episodeStore.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID); !errors.Is(getErr, storage.ErrNotFound) {
		t.Fatalf("canceled create persisted episode: %v", getErr)
	}
}

func TestCreateUsesAtomicStoreAuthorityWhenConcurrentPreflightsMiss(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	principal := episodePrincipal("org_1")
	create := episodeCreate()
	packetStore := &blockingPacketStore{PacketStore: memory.NewPacketStore(func() time.Time { return now }), entered: make(chan struct{}, 2), release: make(chan struct{})}
	if err := packetStore.SaveSnapshot(context.Background(), principal, episodePacket(now, create.ContextPacketID, create.Repository.Slug), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), ServiceOptions{Now: func() time.Time { return now }, PacketStore: packetStore})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		duplicate bool
		err       error
	}
	results := make(chan result, 2)
	var calls sync.WaitGroup
	calls.Add(2)
	for range 2 {
		go func() {
			defer calls.Done()
			_, duplicate, err := service.Create(context.Background(), principal, create)
			results <- result{duplicate: duplicate, err: err}
		}()
	}
	for range 2 {
		<-packetStore.entered
	}
	close(packetStore.release)
	calls.Wait()
	close(results)

	// When
	duplicates := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.duplicate {
			duplicates++
		}
	}

	// Then
	if duplicates != 1 {
		t.Fatalf("duplicate count = %d, want 1", duplicates)
	}
}

func episodePacket(now time.Time, packetID, repository string) contractsv1.ContextPacket {
	return contractsv1.ContextPacket{SchemaVersion: contractsv1.ContextPacketSchema, ContextPacketID: packetID, RequestID: "req-episode-001", GeneratedAt: now, Status: contractsv1.PacketComplete, Goal: "Investigate the fixture", Repository: contractsv1.RepositoryRef{RepoID: "repo-001", Slug: repository}, ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo-001", RepoSlug: repository, Resolution: contractsv1.ScopeBranchFiltered, FallbackReasons: []string{}}, QueryVersion: "context-query.v1", RankingVersion: "context-ranking.v1", Summary: "Untrusted packet summary remains packet data.", Items: []contractsv1.ContextPacketItem{}, RequiredChecks: []contractsv1.RequiredCheck{}, RecommendedNextSteps: []contractsv1.RecommendedStep{}, Freshness: contractsv1.Freshness{AsOf: now, Watermarks: []contractsv1.SourceWatermark{}}, Coverage: contractsv1.Coverage{SourcesConsidered: []string{}, SourcesAvailable: []string{}, SourcesUnavailable: []contractsv1.UnavailableSource{}, DegradedReasons: []string{}}, Budget: contractsv1.PacketBudget{MaxItems: 1, MaxOutputTokens: 500, MaxSerializedBytes: 8192}, Warnings: []string{}, Compatibility: contractsv1.Compatibility{ServiceVersion: "test", MinimumSidecarVersion: "0.1.0", SupportedSchemaVersions: []string{contractsv1.ContextPacketSchema}}}
}

type blockingPacketStore struct {
	storage.PacketStore
	entered chan struct{}
	release chan struct{}
}

func (s *blockingPacketStore) GetSnapshot(ctx context.Context, principal storage.Principal, contextPacketID string) (contractsv1.ContextPacket, error) {
	select {
	case s.entered <- struct{}{}:
	case <-ctx.Done():
		return contractsv1.ContextPacket{}, ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return contractsv1.ContextPacket{}, ctx.Err()
	}
	return s.PacketStore.GetSnapshot(ctx, principal, contextPacketID)
}
