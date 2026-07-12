package episode

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestCreateAuditsAfterRequestCancellationAndTimesOutBounded(t *testing.T) {
	// Given
	audit := &blockingAuditStore{started: make(chan struct{})}
	service, err := NewService(memory.NewEpisodeStore(), audit, withPacketStore(ServiceOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, createErr := service.Create(ctx, episodePrincipal("org_1"), episodeCreate())
		result <- createErr
	}()
	<-audit.started
	cancel()

	// When
	createErr := <-result

	// Then
	if !errors.Is(createErr, context.DeadlineExceeded) {
		t.Fatalf("create error = %v, want bounded audit deadline", createErr)
	}
}

type blockingAuditStore struct{ started chan struct{} }

func (s *blockingAuditStore) Record(ctx context.Context, _ storage.AuditEvent) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}
