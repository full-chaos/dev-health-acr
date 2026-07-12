package sidecar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestClientRecordEpisodeRejectsInvalidRequestBeforeNetwork(t *testing.T) {
	// Given
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	request := validAgentEpisodeCreate()
	request.Outcome = "invalid"

	// When
	_, err = client.RecordEpisode(context.Background(), request)

	// Then
	if err == nil {
		t.Fatal("invalid episode request was accepted")
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid episode request reached the network %d times", calls.Load())
	}
}

func TestClientRecordEpisodeRejectsContractInvalidRequestBeforeNetwork(t *testing.T) {
	// Given
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	request := validAgentEpisodeCreate()
	request.IdempotencyKey = "short"

	// When
	_, err = client.RecordEpisode(context.Background(), request)

	// Then
	if err == nil {
		t.Fatal("contract-invalid episode request was accepted")
	}
	if calls.Load() != 0 {
		t.Fatalf("contract-invalid episode request reached the network %d times", calls.Load())
	}
}

func TestClientRecordEpisodeRejectsDisabledWritebackBeforeNetwork(t *testing.T) {
	// Given
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}

	// When
	_, err = client.RecordEpisode(context.Background(), validAgentEpisodeCreate())

	// Then
	if !errors.Is(err, ErrWritebackDisabled) {
		t.Fatalf("expected disabled writeback error, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("disabled writeback reached the network %d times", calls.Load())
	}
}

func TestClientRecordEpisodeRejectsTranscriptCaptureBeforeNetwork(t *testing.T) {
	// Given
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	request := validAgentEpisodeCreate()
	request.Transcript = contractsv1.TranscriptRef{Mode: "opaque_ref", OpaqueRef: "https://example.com/transcript"}

	// When
	_, err = client.RecordEpisode(context.Background(), request)

	// Then
	if !errors.Is(err, ErrTranscriptCaptureDisabled) {
		t.Fatalf("expected disabled transcript capture error, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("disabled transcript capture reached the network %d times", calls.Load())
	}
}

func TestClientRecordEpisodeRejectsMalformedAndUnknownSuccessResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "malformed", body: "{"},
		{name: "unknown field", body: `{"schema_version":"agent_episode.v1","unknown":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				if _, err := w.Write([]byte(tc.body)); err != nil {
					t.Fatal(err)
				}
			}))
			defer server.Close()
			client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
			if err != nil {
				t.Fatal(err)
			}

			// When
			_, err = client.RecordEpisode(context.Background(), validAgentEpisodeCreate())

			// Then
			if err == nil {
				t.Fatal("malformed success response was accepted")
			}
		})
	}
}

func TestClientRecordEpisodeRejectsUnexpectedSuccessStatus(t *testing.T) {
	// Given
	request := validAgentEpisodeCreate()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSONFixture(t, w, http.StatusAccepted, validAgentEpisode(request))
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
		t.Fatalf("expected an invalid response error, got %v", err)
	}
}

func TestClientRecordEpisodePreservesCallerCancellation(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("cancelled request reached the server")
	}))
	defer server.Close()
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	_, err = client.RecordEpisode(ctx, validAgentEpisodeCreate())

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestClientRecordEpisodeMapsHostedFailuresWithoutCredentialLeakage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		want   error
	}{
		{name: "bad request", status: http.StatusBadRequest, code: "invalid_request", want: ErrInvalidRequest},
		{name: "conflict", status: http.StatusConflict, code: "invalid_request", want: ErrInvalidRequest},
		{name: "unavailable", status: http.StatusServiceUnavailable, code: "upstream_unavailable", want: ErrUpstreamUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSONFixture(t, w, tc.status, contractsv1.ErrorEnvelope{
					SchemaVersion: contractsv1.ErrorSchema,
					RequestID:     "req_episode_failure",
					Error: contractsv1.ErrorDetail{
						Code: tc.code, Message: testBearerCanary, HTTPStatus: tc.status,
					},
				})
			}))
			defer server.Close()
			client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
			if err != nil {
				t.Fatal(err)
			}

			// When
			_, err = client.RecordEpisode(context.Background(), validAgentEpisodeCreate())

			// Then
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), testBearerCanary) {
				t.Fatal("credential leaked through hosted failure")
			}
		})
	}
}
