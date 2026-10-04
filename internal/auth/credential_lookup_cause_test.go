package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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
		{"connection", connection, "ERROR", "connection_failure", "credential_store"},
		{"connection exception", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08000", Class: "connection_exception"}), "ERROR", "connection_failure", "credential_store"},
		{"connection does not exist", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08003", Class: "connection_does_not_exist"}), "ERROR", "connection_failure", "credential_store"},
		{"connection failure to establish", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08001", Class: "unclassified"}), "ERROR", "connection_failure", "credential_store"},
		{"connection rejected", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08004", Class: "unclassified"}), "ERROR", "connection_failure", "credential_store"},
		{"transaction resolution unknown", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08007", Class: "unclassified"}), "ERROR", "connection_failure", "credential_store"},
		{"protocol violation", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08P01", Class: "unclassified"}), "ERROR", "other", "credential_store"},
		{"non connection class", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "42501", Class: "insufficient_privilege"}), "ERROR", "other", "credential_store"},
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
			// Exactly one record, at the wanted level: a caller cancel writes
			// the INFO record and no ERROR record beside it.
			var records []map[string]any
			for _, raw := range strings.Split(strings.TrimSpace(line), "\n") {
				var entry map[string]any
				if err := json.Unmarshal([]byte(raw), &entry); err != nil {
					t.Fatalf("log line is not a JSON record %q: %v", raw, err)
				}
				records = append(records, entry)
				if cause, _ := entry["cause"].(string); cause != tc.wantCause {
					t.Fatalf("record cause = %q, want %q: %s", cause, tc.wantCause, raw)
				}
			}
			if len(records) != 1 || records[0]["level"] != tc.wantLevel {
				t.Fatalf("records = %v, want exactly one at %s", records, tc.wantLevel)
			}
			if tc.wantLevel == "INFO" && strings.Contains(line, `"level":"ERROR"`) {
				t.Fatalf("a caller cancel wrote an ERROR record: %s", line)
			}
			if strings.Contains(line, "secret") {
				t.Fatalf("raw error text leaked: %s", line)
			}
		})
	}
}
