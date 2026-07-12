package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func newWritebackFixtureConfig(t *testing.T, server *httptest.Server) Config {
	t.Helper()
	cfg := newFixtureConfig(t, server)
	cfg.EnableWriteback = true
	return cfg
}

func validAgentEpisodeCreate() contractsv1.AgentEpisodeCreate {
	startedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	return contractsv1.AgentEpisodeCreate{
		SchemaVersion:   contractsv1.AgentEpisodeCreateSchema,
		ClientEpisodeID: "client_episode_01",
		IdempotencyKey:  "idempotency-key-01",
		ContextPacketID: "packet_01",
		Goal:            "record sidecar behavior",
		Repository:      contractsv1.RepositoryRef{Slug: "acme/widgets"},
		Scope:           contractsv1.EpisodeScope{Branch: "main", CommitSHA: "abcdef1"},
		Client: contractsv1.EpisodeClient{
			Name: "caller-supplied-name", Version: "caller-supplied-version", SidecarVersion: "caller-supplied-sidecar-version",
		},
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(time.Minute),
		Outcome:   "succeeded",
		Summary:   "sidecar recorded an episode",
		Artifacts: contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}},
		Transcript: contractsv1.TranscriptRef{
			Mode: "none",
		},
		RetentionClass: "default_90d",
	}
}

func validAgentEpisode(request contractsv1.AgentEpisodeCreate) contractsv1.AgentEpisode {
	request.SchemaVersion = contractsv1.AgentEpisodeSchema
	return contractsv1.AgentEpisode{
		AgentEpisodeCreate: request,
		EpisodeID:          "episode_01",
		CreatedAt:          time.Date(2026, time.July, 11, 12, 2, 0, 0, time.UTC),
		RedactionState:     "active",
	}
}

func TestClientRecordEpisodeSendsExactWriteRequestAndReturnsCreatedEpisode(t *testing.T) {
	// Given
	request := validAgentEpisodeCreate()
	var received contractsv1.AgentEpisodeCreate
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != episodesPath {
			t.Fatalf("unexpected endpoint: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != request.IdempotencyKey {
			t.Fatalf("unexpected idempotency key: %q", r.Header.Get("Idempotency-Key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		writeJSONFixture(t, w, http.StatusCreated, validAgentEpisode(received))
	}))
	defer server.Close()
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}

	// When
	result, err := client.RecordEpisode(context.Background(), request)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.NoPersist || result.Episode == nil || result.Episode.EpisodeID != "episode_01" {
		t.Fatalf("unexpected record result: %#v", result)
	}
	if received.SchemaVersion != contractsv1.AgentEpisodeCreateSchema {
		t.Fatalf("unexpected schema version: %q", received.SchemaVersion)
	}
	if received.Client.Name != "test-sidecar" || received.Client.Version != "1.0.0" || received.Client.SidecarVersion != "1.0.0" {
		t.Fatalf("client identity was not stamped: %#v", received.Client)
	}
}

func TestClientRecordEpisodeReturnsExistingEpisodeForIdempotentSuccess(t *testing.T) {
	// Given
	request := validAgentEpisodeCreate()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONFixture(t, w, http.StatusOK, validAgentEpisode(request))
	}))
	defer server.Close()
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}

	// When
	result, err := client.RecordEpisode(context.Background(), request)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.NoPersist || result.Episode == nil || result.Episode.EpisodeID != "episode_01" {
		t.Fatalf("unexpected record result: %#v", result)
	}
}

func TestClientRecordEpisodeReturnsNoPersistForNoContent(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}

	// When
	request := validAgentEpisodeCreate()
	request.RetentionClass = "no_persist"
	result, err := client.RecordEpisode(context.Background(), request)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if !result.NoPersist || result.Episode != nil {
		t.Fatalf("unexpected no-persist result: %#v", result)
	}
}

func TestClientRecordEpisodeRejectsNoContentForPersistentRetention(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}

	// When
	_, err = client.RecordEpisode(context.Background(), validAgentEpisodeCreate())

	// Then
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("expected invalid response, got %v", err)
	}
}

func TestClientRecordEpisodeRejectsMismatchedResponseAttribution(t *testing.T) {
	// Given
	request := validAgentEpisodeCreate()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := validAgentEpisode(request)
		response.ClientEpisodeID = "other-client-episode"
		response.IdempotencyKey = "other-idempotency-key"
		writeJSONFixture(t, w, http.StatusCreated, response)
	}))
	defer server.Close()
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}

	// When
	_, err = client.RecordEpisode(context.Background(), request)

	// Then
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("expected invalid response, got %v", err)
	}
}
