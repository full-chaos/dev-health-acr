package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestCredentialLookupFailureNamesCauseAndFailsClosed(t *testing.T) {
	connection := fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08006", Class: "connection_failure"})
	cases := []struct {
		name      string
		err       error
		wantLevel string
		wantCause string
		wantClass string
	}{
		{"canceled", fmt.Errorf("find credential: %w", context.Canceled), "INFO", "context_canceled", "caller_canceled"},
		{"deadline", fmt.Errorf("find credential: %w", context.DeadlineExceeded), "ERROR", "deadline_exceeded", "credential_store"},
		{"connection", connection, "ERROR", "conn_reset", "credential_store"},
		{"other", errors.New("postgres://operator:secret@example"), "ERROR", "other", "credential_store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &failingCredentialStore{CredentialStore: newMemoryCredentialStore(t), err: tc.err}
			var buf bytes.Buffer
			authenticator, err := NewAuthenticator(store, memory.NewAuditStore(), AuthenticatorOptions{
				Now: func() time.Time { return time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC) }, Limiter: NoopLimiter{}, Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			raw := make([]byte, tokenSecretBytes)
			for i := range raw {
				raw[i] = 42
			}
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", "Bearer "+TokenPrefix+base64.RawURLEncoding.EncodeToString(raw))
			request.Header.Set("X-Request-ID", "req_cause")
			response := httptest.NewRecorder()
			authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("store fault admitted the request")
			})).ServeHTTP(response, request)
			assertContractError(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
			line := buf.String()
			for _, want := range []string{`"level":"` + tc.wantLevel + `"`, `"cause":"` + tc.wantCause + `"`, `"failure_class":"` + tc.wantClass + `"`, `"request_id":"req_cause"`} {
				if !strings.Contains(line, want) {
					t.Fatalf("log line missing %s: %s", want, line)
				}
			}
			if strings.Contains(line, "secret") {
				t.Fatalf("raw error text leaked: %s", line)
			}
		})
	}
}
