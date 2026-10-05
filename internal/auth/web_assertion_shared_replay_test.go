package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type sharedReplayRecord struct {
	mu   sync.Mutex
	seen map[string]bool
	fail bool
}

func (s *sharedReplayRecord) Observe(_ context.Context, issuer, jti string, _, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return false, errors.New("dial tcp 10.1.2.3:5432: connection refused password=hunter2")
	}
	key := issuer + "\x00" + jti
	replay := s.seen[key]
	s.seen[key] = true
	return replay, nil
}

func newTwoPodVerifiers(t *testing.T, record WebAssertionReplayStore, logger *slog.Logger) ([2]*WebAssertionVerifier, ed25519.PrivateKey, time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := writeTestJWKS(t, "current", public)
	var pods [2]*WebAssertionVerifier
	for i := range pods {
		pods[i], err = NewWebAssertionVerifier(WebAssertionOptions{
			Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: path, Now: func() time.Time { return now },
			Replays: record, Logger: logger,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return pods, private, now
}

func signedRequest(t *testing.T, private ed25519.PrivateKey, now time.Time, jti string) *http.Request {
	t.Helper()
	body := []byte(`{}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
	claims := webAssertionClaims(now, request, body)
	claims["jti"] = jti
	request.Header.Set(WebAssertionHeader, signTestWebAssertion(t, private, "current", claims))
	return request
}

func TestWebAssertionVerifier_replayOnAnotherPodIsRefusedWhenRecordIsShared(t *testing.T) {
	pods, private, now := newTwoPodVerifiers(t, &sharedReplayRecord{seen: map[string]bool{}}, nil)

	if _, err := pods[0].Verify(signedRequest(t, private, now, "assertion_shared")); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := pods[1].Verify(signedRequest(t, private, now, "assertion_shared")); !IsWebAssertionReplay(err) {
		t.Fatalf("replay on the other pod = %v, want replay", err)
	}
}

func TestWebAssertionVerifier_perProcessRecordMissesReplayOnAnotherPod(t *testing.T) {
	// The default record is per process: documents why a shared record is needed.
	pods, private, now := newTwoPodVerifiers(t, nil, nil)

	if _, err := pods[0].Verify(signedRequest(t, private, now, "assertion_local")); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := pods[1].Verify(signedRequest(t, private, now, "assertion_local")); err != nil {
		t.Fatalf("per-process record on another pod = %v, want accepted", err)
	}
}

func TestWebAssertionVerifier_refusesAndLogsWithoutDetailWhenRecordCannotAnswer(t *testing.T) {
	var logs bytes.Buffer
	pods, private, now := newTwoPodVerifiers(t, &sharedReplayRecord{seen: map[string]bool{}, fail: true}, slog.New(slog.NewTextHandler(&logs, nil)))

	principal, err := pods[0].Verify(signedRequest(t, private, now, "assertion_secretish"))

	if !errors.Is(err, ErrWebAssertionStoreUnavailable) {
		t.Fatalf("err = %v, want store unavailable", err)
	}
	if principal.Subject != "" || principal.OrgID != "" || len(principal.Permissions) != 0 {
		t.Fatalf("a refused assertion must carry no principal: %#v", principal)
	}
	out := logs.String()
	if !strings.Contains(out, "web_assertion_store_unavailable") {
		t.Fatalf("missing class in log: %q", out)
	}
	for _, leaked := range []string{"assertion_secretish", "hunter2", "10.1.2.3", "user_123", "org_123"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("log leaks %q: %q", leaked, out)
		}
	}
}

func TestAuthenticator_answers503WhenReplayRecordCannotAnswer(t *testing.T) {
	pods, private, now := newTwoPodVerifiers(t, &sharedReplayRecord{seen: map[string]bool{}, fail: true}, nil)
	authenticator, err := NewAuthenticator(newMemoryCredentialStore(t), nil, AuthenticatorOptions{Now: func() time.Time { return now }, Limiter: NoopLimiter{}, WebAssertions: pods[0]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })

	response := httptest.NewRecorder()
	authenticator.MiddlewareFor(true, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler reached with an unchecked assertion") })).
		ServeHTTP(response, signedRequest(t, private, now, "assertion_down"))

	assertContractError(t, response, http.StatusServiceUnavailable, "temporarily_unavailable")
}
