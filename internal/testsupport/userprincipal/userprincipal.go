// Package userprincipal builds the principal a user-class credential reaches
// the resolver with: the credential row goes through the real authenticator
// and the real narrowing, as a call does after its access read. A test of a
// grant must not hand-build storage.Principal{OrgID: ...}: that shape holds no
// credential and has shown a gate open that no real caller reaches.
// TestTheUserPrincipalHelperIsWhatTheAccessReadProduces (internal/api) ties
// this helper to requireLiveAccess.
package userprincipal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// credentialStore authenticates every bearer as its one row.
type credentialStore struct{ row contractsv1.ClientCredential }

func (s credentialStore) List(context.Context, string) ([]contractsv1.ClientCredential, error) {
	return nil, nil
}

func (s credentialStore) GetByID(context.Context, string, string) (contractsv1.ClientCredential, error) {
	return s.row, nil
}

func (s credentialStore) FindByTokenHash(context.Context, string) (contractsv1.ClientCredential, error) {
	return s.row, nil
}

func (s credentialStore) TouchLastUsed(context.Context, string, string, string, string, time.Time) error {
	return nil
}

// Authenticated is the principal of a credential holding grant, as the
// authenticator leaves it. A credential outside the user classes reaches the
// resolver in this shape; requireLiveAccess does not narrow it.
func Authenticated(tb testing.TB, orgID string, grant []string) storage.Principal {
	tb.Helper()
	return through(tb, orgID, grant, nil)
}

// Narrowed is the principal of an oauth-user credential holding grant, after
// the access read narrowed it to effective, the grant inside the live inventory.
func Narrowed(tb testing.TB, orgID string, grant, effective []string) storage.Principal {
	tb.Helper()
	if len(effective) == 0 {
		tb.Fatal("an empty effective scope is refused with no_repository: no principal reaches the resolver")
	}
	return through(tb, orgID, grant, effective)
}

// ProjectOnly is a project-only caller's principal: a real authenticated
// credential with the two fields the access-read decoder sets (CHAOS-12301).
// Nothing in production sets them yet, so this is the one shape a test can read.
func ProjectOnly(tb testing.TB, orgID string, projects []string) storage.Principal {
	tb.Helper()
	principal := Authenticated(tb, orgID, nil)
	principal.RepositoryScopes = []string{contractsv1.ProjectOnlyRepositoryScope}
	principal.ProjectScopes = append([]string(nil), projects...)
	return principal
}

func through(tb testing.TB, orgID string, grant, effective []string) storage.Principal {
	tb.Helper()
	token, err := auth.GenerateToken()
	if err != nil {
		tb.Fatal(err)
	}
	authenticator, err := auth.NewAuthenticator(credentialStore{row: contractsv1.ClientCredential{
		CredentialID: "user_principal_credential", OrgID: orgID, RepositoryScopes: grant,
		Scopes:     []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeEpisodeWrite, auth.ScopeDataRead},
		Provenance: contractsv1.CredentialProvenance{ClientClass: string(storage.CredentialClientClassOAuthUser), ApprovingUserID: "user_principal_user"},
	}}, nil, auth.AuthenticatorOptions{Now: time.Now})
	if err != nil {
		tb.Fatal(err)
	}
	var principal storage.Principal
	var narrowErr error
	reached := false
	handler := authenticator.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if effective != nil {
			ctx, narrowErr = auth.NarrowRepositoryScopes(ctx, effective)
			if narrowErr != nil {
				return
			}
		}
		principal, reached = auth.PrincipalFromContext(ctx)
	}))
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if narrowErr != nil {
		tb.Fatalf("narrow %v inside %v: %v", effective, grant, narrowErr)
	}
	if !reached {
		tb.Fatalf("the credential was not authenticated: status %d", recorder.Code)
	}
	return principal
}
