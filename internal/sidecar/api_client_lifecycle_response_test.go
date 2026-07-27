package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func invalidLifecycleFixture(t *testing.T, name string) []byte {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(readGoldenFixture(t, name), &response); err != nil {
		t.Fatalf("decode golden lifecycle fixture: %v", err)
	}
	response["schema_version"] = "unsupported.v2"
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode invalid lifecycle fixture: %v", err)
	}
	return raw
}

func newRawLifecycleClient(t *testing.T, raw []byte) *LifecycleClient {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	client, err := NewLifecycleClient(newFixtureConfig(t, server))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestLifecycleClientClassifiesSemanticResponseFailures(t *testing.T) {
	t.Run("device authorization", func(t *testing.T) {
		client := newRawLifecycleClient(t, invalidLifecycleFixture(t, "device_authorization_response.v1.json"))
		_, err := client.StartDeviceAuthorization(context.Background(), nil, nil)
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("StartDeviceAuthorization error = %v, want ErrInvalidResponse", err)
		}
	})

	t.Run("device token", func(t *testing.T) {
		client := newRawLifecycleClient(t, invalidLifecycleFixture(t, "device_token_response.v1.json"))
		_, err := client.PollDeviceToken(context.Background(), strings.Repeat("d", 32))
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("PollDeviceToken error = %v, want ErrInvalidResponse", err)
		}
	})
}

func TestCredentialLifecycleOperationsClassifyHTTPAndInvalidSuccessResponses(t *testing.T) {
	errorFixture := loadContractFixture[contractsv1.ErrorEnvelope](t, "error.v1.json")
	errorFixture.Error.Code = "invalid_token"
	errorFixture.Error.HTTPStatus = http.StatusUnauthorized
	semanticError, err := json.Marshal(errorFixture)
	if err != nil {
		t.Fatalf("marshal semantic error fixture: %v", err)
	}

	rotate := lifecycleResponseMap(t, "credential_rotate_response.v1.json")
	revoke := lifecycleResponseMap(t, "credential_revoke_response.v1.json")
	delete(rotate, "credential")
	delete(revoke, "credential")
	missingRotate, err := json.Marshal(rotate)
	if err != nil {
		t.Fatalf("marshal rotate missing-field fixture: %v", err)
	}
	missingRevoke, err := json.Marshal(revoke)
	if err != nil {
		t.Fatalf("marshal revoke missing-field fixture: %v", err)
	}

	tests := []struct {
		name       string
		body       []byte
		status     int
		want       error
		operations []string
	}{
		{name: "semantic HTTP failure", body: semanticError, status: http.StatusUnauthorized, want: ErrInvalidToken, operations: []string{"rotate", "revoke", "rollback"}},
		{name: "malformed success", body: []byte(`{"schema_version":`), status: http.StatusOK, want: ErrInvalidResponse, operations: []string{"rotate", "revoke", "rollback"}},
		{name: "missing rotate credential", body: missingRotate, status: http.StatusOK, want: ErrInvalidResponse, operations: []string{"rotate"}},
		{name: "missing revoke credential", body: missingRevoke, status: http.StatusOK, want: ErrInvalidResponse, operations: []string{"revoke", "rollback"}},
	}

	for _, tc := range tests {
		for _, operation := range tc.operations {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				client := rawLifecycleOperationClient(t, tc.status, tc.body)
				_, callErr := callLifecycleOperation(t, client, operation)
				if !errors.Is(callErr, tc.want) {
					t.Fatalf("%s error = %v, want %v", operation, callErr, tc.want)
				}
			})
		}
	}
}

func TestRotateOwnCredentialPreservesRecoverablePartialResponseOnValidationFailures(t *testing.T) {
	for _, mutate := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing receipt", mutate: func(response map[string]any) { delete(response, "receipt") }},
		{name: "receipt binds another successor", mutate: func(response map[string]any) {
			receipt := response["receipt"].(map[string]any)
			receipt["replacement_credential_id"] = "cred_01J0ACR099"
			response["receipt"] = receipt
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			response := lifecycleResponseMap(t, "credential_rotate_response.v1.json")
			response["access_token"] = testBearerCanary
			mutate.mutate(response)
			body, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("marshal invalid rotation response: %v", err)
			}

			result, callErr := rawLifecycleOperationClient(t, http.StatusOK, body).RotateOwnCredential(context.Background())
			if !errors.Is(callErr, ErrInvalidResponse) {
				t.Fatalf("RotateOwnCredential error = %v, want ErrInvalidResponse", callErr)
			}
			if result.AccessToken != testBearerCanary || result.Credential.CredentialID != "cred_01J0ACR002" {
				t.Fatalf("RotateOwnCredential discarded recovery material: %#v", result)
			}
		})
	}
}

func lifecycleResponseMap(t *testing.T, name string) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(readGoldenFixture(t, name), &response); err != nil {
		t.Fatalf("decode lifecycle fixture %s: %v", name, err)
	}
	return response
}

func rawLifecycleOperationClient(t *testing.T, status int, body []byte) *Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func callLifecycleOperation(t *testing.T, client *Client, operation string) (any, error) {
	t.Helper()
	switch operation {
	case "rotate":
		return client.RotateOwnCredential(context.Background())
	case "revoke":
		return client.RevokeOwnCredential(context.Background())
	case "rollback":
		return client.RollbackOwnCredential(context.Background(), contractsv1.CredentialRotationReceipt{
			SourceCredentialID: "cred_01J0ACR001", ReplacementCredentialID: "cred_01J0ACR002",
			RollbackUntil: time.Now().UTC().Add(time.Minute),
		})
	default:
		t.Fatalf("unknown lifecycle operation %q", operation)
		return nil, nil
	}
}
