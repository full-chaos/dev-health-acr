package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
