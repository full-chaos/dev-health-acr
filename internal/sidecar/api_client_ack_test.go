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

const ackTestCredentialID = "cred_01J0ACR002"

func ackTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAcknowledgeOwnCredentialPostsTheCredentialIDWithTheClientBearer(t *testing.T) {
	// Given
	var gotPath, gotAuth string
	var gotRequest contractsv1.CredentialAckRequest
	client := ackTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contractsv1.CredentialAckResponse{SchemaVersion: contractsv1.CredentialAckResponseSchema, CredentialID: ackTestCredentialID, AcknowledgedAt: time.Now().UTC()})
	})

	// When
	response, err := client.AcknowledgeOwnCredential(context.Background(), ackTestCredentialID)

	// Then
	if err != nil {
		t.Fatalf("AcknowledgeOwnCredential: %v", err)
	}
	if response.CredentialID != ackTestCredentialID {
		t.Fatalf("response credential_id = %q, want %q", response.CredentialID, ackTestCredentialID)
	}
	if gotPath != "/api/v1/auth/credentials/self/ack" {
		t.Fatalf("path = %q, want the self ack endpoint", gotPath)
	}
	if gotAuth != "Bearer "+testBearerCanary {
		t.Fatal("acknowledgement did not authenticate with the client's own credential")
	}
	if gotRequest.SchemaVersion != contractsv1.CredentialAckRequestSchema || gotRequest.CredentialID != ackTestCredentialID {
		t.Fatalf("request = %#v, want the ack schema and credential id", gotRequest)
	}
}

func TestAcknowledgeOwnCredentialRejectsAResponseForAnotherCredential(t *testing.T) {
	// Given a server that acknowledges a different credential than was sent.
	client := ackTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contractsv1.CredentialAckResponse{SchemaVersion: contractsv1.CredentialAckResponseSchema, CredentialID: "cred_01J0ACR099", AcknowledgedAt: time.Now().UTC()})
	})

	// When
	_, err := client.AcknowledgeOwnCredential(context.Background(), ackTestCredentialID)

	// Then
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("AcknowledgeOwnCredential error = %v, want ErrInvalidResponse", err)
	}
}

func TestAcknowledgeOwnCredentialRefusesAnInvalidCredentialIDWithoutCalling(t *testing.T) {
	// Given
	called := false
	client := ackTestClient(t, func(http.ResponseWriter, *http.Request) { called = true })

	// When
	_, err := client.AcknowledgeOwnCredential(context.Background(), "")

	// Then
	if err == nil || called {
		t.Fatalf("err = %v called = %v, want a local refusal and no request", err, called)
	}
}

func TestAcknowledgeOwnCredentialSurfacesAServerRefusalAsAnAPIError(t *testing.T) {
	// Given
	client := ackTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no such credential"}}`))
	})

	// When
	_, err := client.AcknowledgeOwnCredential(context.Background(), ackTestCredentialID)

	// Then
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("AcknowledgeOwnCredential error = %v (%T), want *APIError", err, err)
	}
	if apiErr.HTTPStatus != http.StatusNotFound {
		t.Fatalf("APIError status = %d, want 404", apiErr.HTTPStatus)
	}
}
