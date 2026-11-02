package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12592 (chris on CHAOS-4390: "a user can see all the data in an org"):
// a user-class credential issued "*" (every web user) stays "*" once the live
// read passes, so it is universal, not restricted. The live read still runs on
// every request; a grant that names repositories or owners is still narrowed to
// the live inventory and keeps every restricted rule.

func TestAStarGrantStaysOrgWideAfterTheLiveRead(t *testing.T) {
	for _, class := range userClasses() {
		t.Run(string(class), func(t *testing.T) {
			harness, _ := newMemorySubmissionHarness(t, 1)
			app := harness.apps[0]
			reader := &fakePrincipalAccess{}
			access := grantedAccess()
			access.Repositories = []string{hostedTestRepository, "acme/other"}
			reader.set(access, nil)
			app.runtime.PrincipalAccess = reader
			authenticateAsCredential(t, app, class, []string{"*"})
			recorder, code, seen := serveCapturing(t, app, harness.tokenA)
			if recorder.Code != http.StatusNoContent || seen == nil {
				t.Fatalf("%d %s, want the handler reached", recorder.Code, code)
			}
			if got := auth.ClassifyGrant(*seen); got != auth.GrantUniversal || !slices.Equal(seen.RepositoryScopes, []string{"*"}) {
				t.Fatalf("principal %q class %q, want the star grant kept, universal", seen.RepositoryScopes, got)
			}
			if bound, ok := contextfabric.CensusGrantFrom(contextfabric.ScopeCensusToCaller(context.Background(), *seen)); ok {
				t.Fatalf("census grant %v: a universal caller's census binds no grant", bound)
			}
			// The live read runs on every request: a lost membership refuses the
			// next call, whatever the grant.
			lost := access
			lost.Member = false
			reader.set(lost, nil)
			if recorder, code, seen := serveCapturing(t, app, harness.tokenA); recorder.Code != http.StatusForbidden || seen != nil {
				t.Fatalf("after the membership was lost: %d %s (handler reached %v), want 403 before the handler", recorder.Code, code, seen != nil)
			}
			if len(reader.calls) != 2 {
				t.Fatalf("%d access reads, want one for each request", len(reader.calls))
			}
		})
	}
}

// A grant that names repositories or owners is still narrowed to the live
// inventory, and an explicit request for a repository outside it is still 403.
func TestAGrantThatNamesRepositoriesKeepsTheInventoryAndThe403(t *testing.T) {
	owner, _, _ := strings.Cut(hostedTestRepository, "/")
	for _, grant := range [][]string{{hostedTestRepository, "zeta/x"}, {owner + "/*"}} {
		for _, class := range userClasses() {
			t.Run(strings.Join(grant, ",")+"/"+string(class), func(t *testing.T) {
				harness, _ := newMemorySubmissionHarness(t, 1)
				app := harness.apps[0]
				reader := &fakePrincipalAccess{}
				reader.set(grantedAccess(), nil)
				app.runtime.PrincipalAccess = reader
				authenticateAsCredential(t, app, class, grant)
				recorder, code, seen := serveCapturing(t, app, harness.tokenA)
				if recorder.Code != http.StatusNoContent || seen == nil {
					t.Fatalf("%d %s, want the handler reached", recorder.Code, code)
				}
				if got := auth.ClassifyGrant(*seen); got != auth.GrantRestricted || !slices.Equal(seen.RepositoryScopes, []string{hostedTestRepository}) {
					t.Fatalf("principal %q class %q, want the grant within the live inventory, restricted", seen.RepositoryScopes, got)
				}
				served := httptest.NewRecorder()
				app.Handler().ServeHTTP(served, contextPacketRequest(t, app, harness.tokenA, contextRequestFor("zeta/x")))
				var envelope struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				_ = json.Unmarshal(served.Body.Bytes(), &envelope)
				if served.Code != http.StatusForbidden || envelope.Error.Code != "repo_forbidden" {
					t.Fatalf("context packet outside the live inventory under a named grant: %d %s, want 403 repo_forbidden", served.Code, envelope.Error.Code)
				}
			})
		}
	}
}

// The helper other packages use as "the web user" is the principal the access
// read produces for a "*" user-class credential, field for field.
func TestTheWebUserHelperIsWhatTheAccessReadProduces(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	reader.set(grantedAccess(), nil)
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassOAuthUser, []string{"*"})
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
	helper := userprincipal.WebUser(t, read.OrgID)
	helper.Subject, helper.CredentialID, helper.ApprovingUserID = read.Subject, read.CredentialID, read.ApprovingUserID
	if !reflect.DeepEqual(helper, read) {
		t.Fatalf("helper principal differs from the access read:\nhelper %+v\nread   %+v", helper, read)
	}
}
