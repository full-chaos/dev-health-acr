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

func TestServiceCreateObservesOneBoundedTerminalOutcome(t *testing.T) {
	tests := []struct {
		name    string
		store   storage.EpisodeStore
		audit   storage.AuditStore
		create  contractsv1.AgentEpisodeCreate
		prepare func(*Service, storage.Principal, contractsv1.AgentEpisodeCreate) error
		want    TerminalObservation
	}{
		{
			name:   "success",
			store:  memory.NewEpisodeStore(),
			audit:  memory.NewAuditStore(),
			create: episodeCreate(),
			want:   TerminalObservation{Outcome: TerminalOutcomeSuccess, AuditDelivery: AuditDeliveryDelivered},
		},
		{
			name:   "duplicate",
			store:  memory.NewEpisodeStore(),
			audit:  memory.NewAuditStore(),
			create: episodeCreate(),
			prepare: func(service *Service, principal storage.Principal, create contractsv1.AgentEpisodeCreate) error {
				_, _, err := service.Create(context.Background(), principal, create)
				return err
			},
			want: TerminalObservation{Outcome: TerminalOutcomeDuplicate, AuditDelivery: AuditDeliveryDelivered},
		},
		{
			name:   "storage failure",
			store:  failingEpisodeStore{EpisodeStore: memory.NewEpisodeStore()},
			audit:  memory.NewAuditStore(),
			create: episodeCreate(),
			want:   TerminalObservation{Outcome: TerminalOutcomeFailure, AuditDelivery: AuditDeliveryDelivered},
		},
		{
			name:   "preflight audit failure",
			store:  memory.NewEpisodeStore(),
			audit:  failingAuditStore{},
			create: episodeCreate(),
			want:   TerminalObservation{Outcome: TerminalOutcomeFailure, AuditDelivery: AuditDeliveryFailed},
		},
		{
			name:   "validation failure",
			store:  memory.NewEpisodeStore(),
			audit:  memory.NewAuditStore(),
			create: contractsv1.AgentEpisodeCreate{},
			want:   TerminalObservation{Outcome: TerminalOutcomeFailure, AuditDelivery: AuditDeliverySkipped},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			observer := newTerminalObserver()
			service, err := NewService(test.store, test.audit, ServiceOptions{
				Now:              func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) },
				TerminalObserver: observer,
			})
			if err != nil {
				t.Fatal(err)
			}
			principal := episodePrincipal("org_1")
			if test.prepare != nil {
				if err := test.prepare(service, principal, test.create); err != nil {
					t.Fatal(err)
				}
				observer.waitForCount(t, 1)
				observer.reset()
			}

			// When
			_, _, _ = service.Create(context.Background(), principal, test.create)

			// Then
			observer.waitForCount(t, 1)
			observations := observer.observations()
			if len(observations) != 1 || !sameTerminalDimensions(observations[0], test.want) {
				t.Fatalf("observations = %#v, want one %#v", observations, test.want)
			}
		})
	}
}

func TestServiceRedactObservesRedactionAndAuditDelivery(t *testing.T) {
	// Given
	store := memory.NewEpisodeStore()
	creator, err := NewService(store, memory.NewAuditStore(), ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	principal := episodePrincipal("org_1")
	created, _, err := creator.Create(context.Background(), principal, episodeCreate())
	if err != nil {
		t.Fatal(err)
	}
	observer := newTerminalObserver()
	service, err := NewService(store, memory.NewAuditStore(), ServiceOptions{TerminalObserver: observer})
	if err != nil {
		t.Fatal(err)
	}

	// When
	_, err = service.Redact(context.Background(), principal, created.EpisodeID, "customer request")

	// Then
	if err != nil {
		t.Fatal(err)
	}
	observer.waitForCount(t, 1)
	observations := observer.observations()
	want := TerminalObservation{Outcome: TerminalOutcomeRedacted, AuditDelivery: AuditDeliveryDelivered}
	if len(observations) != 1 || !sameTerminalDimensions(observations[0], want) {
		t.Fatalf("observations = %#v, want one %#v", observations, want)
	}
}

func TestServiceCreateObservesCompletionAuditFailure(t *testing.T) {
	// Given
	observer := newTerminalObserver()
	service, err := NewService(memory.NewEpisodeStore(), &failOnNthAudit{store: memory.NewAuditStore(), failAt: 2}, ServiceOptions{TerminalObserver: observer})
	if err != nil {
		t.Fatal(err)
	}

	// When
	_, _, err = service.Create(context.Background(), episodePrincipal("org_1"), episodeCreate())

	// Then
	if err != nil {
		t.Fatalf("create returned completion audit failure: %v", err)
	}
	observer.waitForCount(t, 1)
	observations := observer.observations()
	want := TerminalObservation{Outcome: TerminalOutcomeSuccess, AuditDelivery: AuditDeliveryFailed}
	if len(observations) != 1 || !sameTerminalDimensions(observations[0], want) {
		t.Fatalf("observations = %#v, want one %#v", observations, want)
	}
}

func TestServiceCreateRecoversTerminalObserverPanic(t *testing.T) {
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), ServiceOptions{TerminalObserver: panickingTerminalObserver{}, StoreObserver: panickingStoreObserver{}})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = service.Create(context.Background(), episodePrincipal("org_1"), episodeCreate())

	if err != nil {
		t.Fatalf("observer panic changed create result: %v", err)
	}
}

func TestServiceObservesOnlyActualStoreCalls(t *testing.T) {
	tests := []struct {
		name   string
		store  storage.EpisodeStore
		audit  storage.AuditStore
		create contractsv1.AgentEpisodeCreate
		want   int
	}{
		{name: "success", store: memory.NewEpisodeStore(), audit: memory.NewAuditStore(), create: episodeCreate(), want: 1},
		{name: "storage failure", store: failingEpisodeStore{EpisodeStore: memory.NewEpisodeStore()}, audit: memory.NewAuditStore(), create: episodeCreate(), want: 1},
		{name: "audit failure", store: memory.NewEpisodeStore(), audit: failingAuditStore{}, create: episodeCreate()},
		{name: "validation failure", store: memory.NewEpisodeStore(), audit: memory.NewAuditStore(), create: contractsv1.AgentEpisodeCreate{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := &storeObserver{}
			service, err := NewService(test.store, test.audit, ServiceOptions{StoreObserver: observer, StoreBackend: StoreBackendMemory})
			if err != nil {
				t.Fatal(err)
			}
			_, _, _ = service.Create(context.Background(), episodePrincipal("org_1"), test.create)
			if len(observer.values) != test.want {
				t.Fatalf("store observations = %#v, want %d", observer.values, test.want)
			}
		})
	}
}

type failingEpisodeStore struct{ storage.EpisodeStore }

func (failingEpisodeStore) CreateIdempotent(context.Context, storage.Principal, contractsv1.AgentEpisodeCreate, *time.Time) (contractsv1.AgentEpisode, bool, error) {
	return contractsv1.AgentEpisode{}, false, errors.New("storage unavailable")
}

type terminalObserver struct {
	mu       sync.Mutex
	values   []TerminalObservation
	observed chan struct{}
}

func newTerminalObserver() *terminalObserver {
	return &terminalObserver{observed: make(chan struct{}, 1)}
}

type panickingTerminalObserver struct{}

type panickingStoreObserver struct{}

type storeObserver struct{ values []StoreCallObservation }

func (panickingTerminalObserver) ObserveEpisodeTerminal(context.Context, TerminalObservation) {
	panic("observer")
}

func (panickingStoreObserver) ObserveEpisodeStore(context.Context, StoreCallObservation) {
	panic("observer")
}

func (o *storeObserver) ObserveEpisodeStore(_ context.Context, observation StoreCallObservation) {
	o.values = append(o.values, observation)
}

func sameTerminalDimensions(got, want TerminalObservation) bool {
	return got.Outcome == want.Outcome && got.AuditDelivery == want.AuditDelivery && got.Duration >= 0
}

func (o *terminalObserver) ObserveEpisodeTerminal(_ context.Context, observation TerminalObservation) {
	o.mu.Lock()
	o.values = append(o.values, observation)
	o.mu.Unlock()
	select {
	case o.observed <- struct{}{}:
	default:
	}
}

func (o *terminalObserver) observations() []TerminalObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]TerminalObservation(nil), o.values...)
}

func (o *terminalObserver) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.values = nil
}

func (o *terminalObserver) waitForCount(t *testing.T, count int) {
	t.Helper()
	for len(o.observations()) < count {
		select {
		case <-o.observed:
		case <-time.After(time.Second):
			t.Fatalf("received %d observations, want %d", len(o.observations()), count)
		}
	}
}
