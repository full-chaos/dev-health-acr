package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12452: the census tests below internal/api cannot reach
// requireLiveAccess, so they read userprincipal.Narrowed. This ties that helper
// to the real access read: the same grant and inventory give the same principal
// and the same census grant.
func TestTheUserPrincipalHelperIsWhatTheAccessReadProduces(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	access := grantedAccess()
	access.Repositories = []string{"acme/web", "ACME/api", "zeta/x"}
	reader := &fakePrincipalAccess{}
	reader.set(access, nil)
	app.runtime.PrincipalAccess = reader
	grant := []string{"acme/*"}
	authenticateAsCredential(t, app, storage.CredentialClientClassOAuthUser, grant)

	var read storage.Principal
	reached := false
	handler := app.authenticator.Middleware(app.requireLiveAccess(true, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		read, reached = auth.PrincipalFromContext(r.Context())
	})))
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Authorization", "Bearer "+harness.tokenA)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !reached {
		t.Fatalf("the access read refused the call: %d %s", recorder.Code, recorder.Body.String())
	}
	if auth.ClassifyGrant(read) != auth.GrantRestricted {
		t.Fatalf("class = %q, want restricted", auth.ClassifyGrant(read))
	}

	helper := userprincipal.Narrowed(t, read.OrgID, grant, []string{"acme/api", "acme/web"})
	if !slices.Equal(helper.RepositoryScopes, read.RepositoryScopes) {
		t.Fatalf("helper scopes = %v, access read scopes = %v", helper.RepositoryScopes, read.RepositoryScopes)
	}
	// The credential's own identifiers differ by fixture; every other field is
	// the shape a gate reads.
	helper.Subject, helper.CredentialID, helper.ApprovingUserID = read.Subject, read.CredentialID, read.ApprovingUserID
	if !reflect.DeepEqual(helper, read) {
		t.Fatalf("helper principal differs from the access read:\nhelper %+v\nread   %+v", helper, read)
	}
	bound, ok := contextfabric.CensusGrantFrom(contextfabric.ScopeCensusToCaller(request.Context(), read))
	if !ok || !slices.Equal(bound, []string{"acme/api", "acme/web"}) {
		t.Fatalf("census grant = %v bound %v, want the effective scope", bound, ok)
	}
}
