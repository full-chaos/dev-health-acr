package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// TestMiddlewareSanitizesRequestIDOnCredentialLookupFailure is the
// CHAOS-5558 real-handler pin for middleware.go:117's ErrorContext site
// (alert #6): a malicious X-Request-ID header, through the REAL
// Authenticator.Middleware and a real slog.JSONHandler, must not fracture
// the emitted line -- requestID(r) here is the raw header value, never
// validated by any layer above internal/api's own middleware (auth has no
// equivalent check), so this is the genuinely reachable case, not a
// defense-in-depth one.
func TestMiddlewareSanitizesRequestIDOnCredentialLookupFailure(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	store := &failingCredentialStore{CredentialStore: newMemoryCredentialStore(t), err: errors.New("database unavailable")}
	var buf bytes.Buffer
	authenticator, err := NewAuthenticator(store, memory.NewAuditStore(), AuthenticatorOptions{
		Now: func() time.Time { return now }, Limiter: NoopLimiter{}, Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, tokenSecretBytes)
	for index := range raw {
		raw[index] = 42
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+TokenPrefix+base64.RawURLEncoding.EncodeToString(raw))
	request.Header.Set("X-Request-ID", "evil\nFAKE_LOG_LINE=injected\r\n")
	response := httptest.NewRecorder()
	authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("failed credential lookup reached handler")
	})).ServeHTTP(response, request)

	assertSingleCleanJSONLineWithRequestID(t, buf.String())
}

// TestMiddlewareSanitizesRequestIDOnAuthenticationFailure is the sibling
// pin for middleware.go:214's WarnContext site (alert #7,
// recordUnknownFailure) -- reached by an absent/malformed bearer token,
// the class every anonymous caller can trigger without a valid credential
// at all.
func TestMiddlewareSanitizesRequestIDOnAuthenticationFailure(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	store := newMemoryCredentialStore(t)
	var buf bytes.Buffer
	authenticator, err := NewAuthenticator(store, memory.NewAuditStore(), AuthenticatorOptions{
		Now: func() time.Time { return now }, Limiter: NoopLimiter{}, Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil) // no Authorization header -- malformed bearer path
	request.Header.Set("X-Request-ID", "evil\nFAKE_LOG_LINE=injected\r\n")
	response := httptest.NewRecorder()
	authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("malformed bearer request reached handler")
	})).ServeHTTP(response, request)

	assertSingleCleanJSONLineWithRequestID(t, buf.String())
}

func assertSingleCleanJSONLineWithRequestID(t *testing.T, logged string) {
	t.Helper()
	text := strings.TrimRight(logged, "\n")
	if text == "" {
		t.Fatal("nothing was logged")
	}
	lines := strings.Split(text, "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d log line(s), want exactly 1 -- a forged line break would split the record: %q", len(lines), logged)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("line is not valid JSON (a forged line break split the record): %q: %v", lines[0], err)
	}
	requestID, ok := record["request_id"].(string)
	if !ok {
		t.Fatalf("record has no string request_id field: %v", record)
	}
	if strings.ContainsAny(requestID, "\n\r") {
		t.Fatalf("request_id = %q still carries a line break", requestID)
	}
}
