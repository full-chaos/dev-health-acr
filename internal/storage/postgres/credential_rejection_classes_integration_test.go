package postgres

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

const rejectionClassesOrgID = "44444444-4444-4444-4444-444444444444"

// storeCalls records every credential-store and audit-store call the
// authenticator makes, with the error class of each lookup.
type storeCalls struct {
	mu    sync.Mutex
	calls []string
}

func (c *storeCalls) add(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

func (c *storeCalls) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	calls := c.calls
	c.calls = nil
	return calls
}

type countedCredentialStore struct {
	storage.CredentialStore
	calls *storeCalls
}

func (s countedCredentialStore) FindByTokenHash(ctx context.Context, hash string) (contractsv1.ClientCredential, error) {
	credential, err := s.CredentialStore.FindByTokenHash(ctx, hash)
	class := "found"
	switch {
	case errors.Is(err, storage.ErrNotFound):
		class = "not_found"
	case err != nil:
		class = "error"
	}
	s.calls.add("FindByTokenHash:" + class)
	return credential, err
}

func (s countedCredentialStore) TouchLastUsed(ctx context.Context, credentialID, ip, userAgent string, usedAt time.Time) error {
	s.calls.add("TouchLastUsed")
	return s.CredentialStore.TouchLastUsed(ctx, credentialID, ip, userAgent, usedAt)
}

type countedAuditStore struct {
	storage.AuditStore
	calls *storeCalls
}

func (s countedAuditStore) Record(ctx context.Context, event storage.AuditEvent) error {
	s.calls.add("AuditRecord:" + event.Action)
	return s.AuditStore.Record(ctx, event)
}

// On the Postgres store, an unknown, a revoked and an expired well-formed
// bearer make the same store calls (one lookup answering not-found, no audit
// write), write no denial audit row, and get the same HTTP answer: 401 under
// the failure budget and 429 from the over-budget verification slot. So the
// answer and the work done never tell a guesser whether a credential exists.
func TestRejectedCredentialClassesDoTheSameStoreWorkAndGetTheSameAnswer(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(credentials, auth.ServiceOptions{})
	require.NoError(t, err)
	issue := func(name string) auth.IssuedCredential {
		issued, err := service.Create(ctx, auth.CreateCredentialRequest{
			OrgID: rejectionClassesOrgID, Name: name, RepositoryScopes: []string{"*"}, Scopes: []string{auth.ScopeContextRead}, CreatedBy: "test_actor",
		})
		require.NoError(t, err)
		return issued
	}
	revoked := issue("revoked")
	_, err = credentials.RevokeCredential(ctx, storage.CredentialRevocationInput{OrgID: rejectionClassesOrgID, CredentialID: revoked.Credential.CredentialID, ActorID: "test_actor"})
	require.NoError(t, err)
	expired := issue("expired")
	_, err = db.ExecContext(ctx, `UPDATE acr.client_credentials SET expires_at = now() - interval '1 minute' WHERE credential_id = $1`, expired.Credential.CredentialID)
	require.NoError(t, err)
	unknown, err := auth.GenerateToken()
	require.NoError(t, err)
	valid := issue("valid")

	deniedRows := func() int {
		var n int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.audit_events WHERE action = 'credential_auth_denied'`).Scan(&n))
		return n
	}
	deniedBefore := deniedRows()

	type outcome struct {
		status int
		header http.Header
		body   string
		calls  []string
	}
	for _, phase := range []struct {
		name    string
		prefill int
		want    int
	}{{"under budget", 0, http.StatusUnauthorized}, {"over budget", 3, http.StatusTooManyRequests}} {
		outcomes := map[string]outcome{}
		for name, token := range map[string]string{"unknown": unknown, "revoked": revoked.Token, "expired": expired.Token} {
			calls := &storeCalls{}
			authenticator, err := auth.NewAuthenticator(countedCredentialStore{credentials, calls}, countedAuditStore{audit, calls}, auth.AuthenticatorOptions{
				Limiter: auth.NewMemoryLimiter(time.Minute, 100, 3), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = authenticator.Close() })
			handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			call := func(bearer string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodGet, "/", nil)
				request.RemoteAddr = "192.0.2.60:4000"
				request.Header.Set("Authorization", "Bearer "+bearer)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
			}
			for i := 0; i < phase.prefill; i++ {
				call("junk")
			}
			calls.take()
			response := call(token)
			outcomes[name] = outcome{response.Code, response.Header().Clone(), response.Body.String(), calls.take()}
			if phase.prefill > 0 {
				if served := call(valid.Token); served.Code != http.StatusOK {
					t.Fatalf("%s: valid bearer from the over-budget address = %d, want 200", phase.name, served.Code)
				}
			}
		}
		reference := outcomes["unknown"]
		require.Equal(t, phase.want, reference.status, "%s: unknown status", phase.name)
		require.Equal(t, []string{"FindByTokenHash:not_found"}, reference.calls, "%s: unknown store calls", phase.name)
		for name, got := range outcomes {
			require.Equal(t, reference.calls, got.calls, "%s: %s store calls", phase.name, name)
			require.Equal(t, reference.status, got.status, "%s: %s status", phase.name, name)
			require.Equal(t, reference.body, got.body, "%s: %s body", phase.name, name)
			require.True(t, sameHeaderValues(reference.header, got.header), "%s: %s headers %v, unknown %v", phase.name, name, got.header, reference.header)
		}
	}
	require.Equal(t, deniedBefore, deniedRows(), "rejected credentials wrote denial audit rows")
}

func sameHeaderValues(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for name, values := range a {
		if strings.Join(values, "\x00") != strings.Join(b.Values(name), "\x00") {
			return false
		}
	}
	return true
}
