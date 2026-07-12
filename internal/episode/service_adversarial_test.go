package episode

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestNewServiceRequiresPacketStore(t *testing.T) {
	// Given / When
	_, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), ServiceOptions{})

	// Then
	if err == nil {
		t.Fatal("service accepted a nil packet store")
	}
}

func TestCreateReturnsNoPersistAcceptedOnlyAfterTombstonePersistence(t *testing.T) {
	// Given
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), withPacketStore(ServiceOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	create := episodeCreate()
	create.RetentionClass = "no_persist"

	// When
	_, duplicate, err := service.Create(context.Background(), episodePrincipal("org_1"), create)

	// Then
	if duplicate || !errors.Is(err, ErrNoPersistAccepted) || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("no_persist create = (%t, %v)", duplicate, err)
	}
}

func TestCreateObservesPreflightAndAtomicStoreCalls(t *testing.T) {
	// Given
	observer := &storeObserver{}
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), withPacketStore(ServiceOptions{StoreObserver: observer, StoreBackend: StoreBackendMemory}))
	if err != nil {
		t.Fatal(err)
	}

	// When
	_, _, err = service.Create(context.Background(), episodePrincipal("org_1"), episodeCreate())

	// Then
	if err != nil || len(observer.values) != 2 {
		t.Fatalf("create observations = (%#v, %v), want two successful store calls", observer.values, err)
	}
}

func TestCreateReturnsCanonicalResponseSchemaWithoutChangingCreateDigest(t *testing.T) {
	// Given
	store := memory.NewEpisodeStore()
	service, err := NewService(store, memory.NewAuditStore(), withPacketStore(ServiceOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	create := episodeCreate()

	// When
	created, duplicate, err := service.Create(context.Background(), episodePrincipal("org_1"), create)
	replayed, replayDuplicate, replayErr := service.Create(context.Background(), episodePrincipal("org_1"), create)

	// Then
	if err != nil || duplicate || replayErr != nil || !replayDuplicate || created.SchemaVersion != contractsv1.AgentEpisodeSchema || replayed.SchemaVersion != contractsv1.AgentEpisodeSchema || created.Validate() != nil || replayed.Validate() != nil {
		t.Fatalf("schema/digest behavior = (%#v, %t, %v) / (%#v, %t, %v)", created, duplicate, err, replayed, replayDuplicate, replayErr)
	}
}
