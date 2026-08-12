package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/episode"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestCreateEpisodeWithRealServiceSeparatesPacketNotFoundFromNoPersist(t *testing.T) {
	tests := []struct {
		name       string
		packetErr  error
		retention  string
		wantStatus int
	}{
		{name: "packet not found", packetErr: storage.ErrNotFound, retention: "default_90d", wantStatus: http.StatusNotFound},
		{name: "no persist", retention: "no_persist", wantStatus: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			service, err := episode.NewService(memory.NewEpisodeStore(nil), memory.NewAuditStore(), episode.ServiceOptions{PacketStore: episodePacketStore{err: test.packetErr}})
			if err != nil {
				t.Fatal(err)
			}
			app, token := hostedEpisodeServiceApp(t, service)
			create := episodeCreate()
			create.RetentionClass = test.retention

			// When
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, create, "idempotency_01"))

			// Then
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func TestCreateEpisodeWithRealServiceReturnsCanonicalConflictEnvelope(t *testing.T) {
	// Given
	service, err := episode.NewService(memory.NewEpisodeStore(nil), memory.NewAuditStore(), episode.ServiceOptions{PacketStore: episodePacketStore{}})
	if err != nil {
		t.Fatal(err)
	}
	app, token := hostedEpisodeServiceApp(t, service)
	created := httptest.NewRecorder()
	app.Handler().ServeHTTP(created, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))
	conflicting := episodeCreate()
	conflicting.Summary = "different bounded summary"

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, conflicting, "idempotency_01"))

	// Then
	var envelope contractsv1.ErrorEnvelope
	if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.SchemaVersion != contractsv1.ErrorSchema || envelope.Error.Code != "invalid_request" || envelope.Error.HTTPStatus != http.StatusConflict || envelope.Error.Retryable || len(envelope.Error.Details) != 0 {
		t.Fatalf("status=%d response=%s", response.Code, response.Body.String())
	}
}

func hostedEpisodeServiceApp(t *testing.T, creator EpisodeCreator) (*App, string) {
	t.Helper()
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeEpisodeWrite}, nil, nil)
	app.runtime.Episodes = creator
	return app, token
}

type episodePacketStore struct{ err error }

func (s episodePacketStore) SaveSnapshot(context.Context, storage.Principal, contractsv1.ContextPacket, time.Time) error {
	return errors.New("not implemented")
}

func (s episodePacketStore) GetSnapshot(_ context.Context, _ storage.Principal, id string) (contractsv1.ContextPacket, error) {
	if s.err != nil {
		return contractsv1.ContextPacket{}, s.err
	}
	return contractsv1.ContextPacket{ContextPacketID: id, Repository: contractsv1.RepositoryRef{Slug: hostedTestRepository}}, nil
}

func (s episodePacketStore) PurgeExpired(context.Context, time.Time, int) (int, error) {
	return 0, nil
}
