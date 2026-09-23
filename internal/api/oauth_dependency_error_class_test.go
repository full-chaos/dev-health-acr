package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// failingRegisterOAuthStore wraps a real storage.OAuthStore and fails the
// next RegisterClient call once, with the EXACT error shape
// internal/storage/postgres.sanitizeDatabaseError produces for a real
// planted Postgres CHECK constraint violation (see
// internal/storage/postgres/oauth_dependency_error_class_integration_test.go's
// TestSanitizeDatabaseError_realCheckViolationCarriesSafeErrorClass, which
// proves this shape against a genuine Postgres error) -- an operation-
// prefixed wrap of storage.ErrUnavailable plus a *storage.DependencyErrorClass.
// Using the identical shape here, rather than a bare errors.New, is what
// makes this an honest proof of the API layer's extraction logic, not just
// of the storage layer's classification.
type failingRegisterOAuthStore struct {
	storage.OAuthStore
	failNext bool
	class    *storage.DependencyErrorClass
}

func (s *failingRegisterOAuthStore) RegisterClient(ctx context.Context, client storage.OAuthClient) (storage.OAuthClient, error) {
	if s.failNext {
		s.failNext = false
		return storage.OAuthClient{}, fmt.Errorf("register oauth client: %w: %w", storage.ErrUnavailable, s.class)
	}
	return s.OAuthStore.RegisterClient(ctx, client)
}

// decodeOAuthDependencyFailureLog finds the "oauth dependency failed" JSON
// log line and returns it decoded, or fails the test.
func decodeOAuthDependencyFailureLog(t *testing.T, logs string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		entry := map[string]any{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["msg"] == "oauth dependency failed" {
			return entry
		}
	}
	t.Fatalf("no \"oauth dependency failed\" log line found in:\n%s", logs)
	return nil
}

// TestOAuthDependencyFailureLogCarriesSafeErrorClass is CHAOS-6278's
// emitted-line pin: for a real planted Postgres CHECK constraint violation
// (the SAME shape TestSanitizeDatabaseError_realCheckViolationCarriesSafeErrorClass
// proves against a genuine driver error), the "oauth dependency failed" log
// line must carry the SQLSTATE code, the closed-vocabulary class, the
// constraint name, and the table name -- and the HTTP response body must
// stay exactly {"error":"temporarily_unavailable"} with a 503, unchanged
// (the classification is safe-to-log, never safe-to-return-to-the-caller).
//
// RED on baseline: logOAuthDependencyFailure's original signature never
// received the error at all -- every one of its 6 call sites discarded it
// before this line could ever be written, so this test could not pass no
// matter what sanitizeDatabaseError classified.
func TestOAuthDependencyFailureLogCarriesSafeErrorClass(t *testing.T) {
	// Given
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	store := &failingRegisterOAuthStore{
		OAuthStore: memory.NewOAuthStore(func() time.Time { return now }),
		failNext:   true,
		class: &storage.DependencyErrorClass{
			SQLState:   "23514",
			Class:      "check_violation",
			Constraint: "oauth_clients_redirect_uris_check",
			Table:      "oauth_clients",
		},
	}
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, Store: store}, true)
	if err != nil {
		t.Fatal(err)
	}

	// When
	registration, _ := json.Marshal(map[string]any{"client_name": "c", "redirect_uris": []string{oauthTestRedirect}})
	request := httptest.NewRequest(http.MethodPost, OAuthRegisterPath, bytes.NewReader(registration))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)

	// Then: the response body carries no classification detail at all.
	var body map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusServiceUnavailable || body["error"] != "temporarily_unavailable" || len(body) != 1 {
		t.Fatalf("response = %d %v, want 503 {\"error\":\"temporarily_unavailable\"} only", recorder.Code, body)
	}

	// And the log line carries exactly the safe classification.
	entry := decodeOAuthDependencyFailureLog(t, logs.String())
	if entry["step"] != "register" || entry["failure_class"] != "oauth_dependency" {
		t.Fatalf("log entry = %v, want step=register failure_class=oauth_dependency", entry)
	}
	if entry["db_sqlstate"] != "23514" || entry["db_error_class"] != "check_violation" ||
		entry["db_constraint"] != "oauth_clients_redirect_uris_check" || entry["db_table"] != "oauth_clients" {
		t.Fatalf("log entry = %v, want db_sqlstate=23514 db_error_class=check_violation db_constraint=oauth_clients_redirect_uris_check db_table=oauth_clients", entry)
	}
	// Never a raw value: the constant sentinel text, the Go error string, or
	// anything else that isn't one of the four safe fields asserted above.
	for _, forbidden := range []string{"storage operation unavailable", "sanitizeDatabaseError"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("log leaked an internal error string %q: %s", forbidden, logs.String())
		}
	}
}

// TestOAuthDependencyFailureLogOmitsClassFieldsWhenNoneIsAvailable proves
// the extraction is additive, not fabricated: a dependency failure with no
// storage.DependencyErrorClass attached (a plain error, same as every call
// site produced before this change for a non-Postgres failure) logs exactly
// as it did before -- step/failure_class only, no db_* fields invented.
func TestOAuthDependencyFailureLogOmitsClassFieldsWhenNoneIsAvailable(t *testing.T) {
	// Given: a plain, classless dependency failure -- the shape a real
	// non-Postgres failure (e.g. a context deadline) actually has. A nil
	// *storage.DependencyErrorClass would instead make errors.As find a
	// non-nil interface wrapping a nil pointer, which is why this uses a
	// bare error rather than failingRegisterOAuthStore with class: nil.
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	store := plainFailingOAuthStore{OAuthStore: memory.NewOAuthStore(func() time.Time { return now })}
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, Store: store}, true)
	if err != nil {
		t.Fatal(err)
	}

	// When
	registration, _ := json.Marshal(map[string]any{"client_name": "c", "redirect_uris": []string{oauthTestRedirect}})
	request := httptest.NewRequest(http.MethodPost, OAuthRegisterPath, bytes.NewReader(registration))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)

	// Then
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	entry := decodeOAuthDependencyFailureLog(t, logs.String())
	if entry["step"] != "register" || entry["failure_class"] != "oauth_dependency" {
		t.Fatalf("log entry = %v, want step=register failure_class=oauth_dependency", entry)
	}
	for _, field := range []string{"db_sqlstate", "db_error_class", "db_constraint", "db_table"} {
		if _, present := entry[field]; present {
			t.Fatalf("log entry carries %s = %v with no DependencyErrorClass available -- must not fabricate a class", field, entry[field])
		}
	}
}

// plainFailingOAuthStore fails RegisterClient once with a bare error
// carrying no storage.DependencyErrorClass at all -- the shape a
// non-Postgres dependency failure (a context deadline, a network error with
// no driver-level detail) actually has.
type plainFailingOAuthStore struct {
	storage.OAuthStore
}

func (s plainFailingOAuthStore) RegisterClient(ctx context.Context, client storage.OAuthClient) (storage.OAuthClient, error) {
	return storage.OAuthClient{}, fmt.Errorf("register oauth client: %w", storage.ErrUnavailable)
}
