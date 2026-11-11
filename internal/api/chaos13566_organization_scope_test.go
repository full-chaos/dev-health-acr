package api

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-13566 (acp ruling Q4 on CHAOS-13561): the access read's kind
// "organization" (an org-wide reader in an organization that has repositories)
// is served as today's org-wide answer. A "*" grant stays "*", exactly as with a
// non-empty inventory; a grant that names repositories or owners is the limit
// of what the caller reads, since the reader may read every repository of the
// organization. Every refusal of the live read stays as it is.

func organizationAccess() entitlements.PrincipalAccess {
	access := grantedAccess()
	access.Repositories = nil
	access.OrganizationWide = true
	return access
}

func TestAnOrganizationScopeKeepsAStarGrantAsANonEmptyInventoryDoes(t *testing.T) {
	for _, class := range userClasses() {
		t.Run(string(class), func(t *testing.T) {
			var principals []any
			for _, access := range []entitlements.PrincipalAccess{grantedAccess(), organizationAccess()} {
				harness, _ := newMemorySubmissionHarness(t, 1)
				app := harness.apps[0]
				reader := &fakePrincipalAccess{}
				reader.set(access, nil)
				app.runtime.PrincipalAccess = reader
				authenticateAsCredential(t, app, class, []string{"*"})
				recorder, code, seen := serveCapturing(t, app, harness.tokenA)
				if recorder.Code != http.StatusNoContent || seen == nil {
					t.Fatalf("organization-wide %v: %d %s, want the handler reached", access.OrganizationWide, recorder.Code, code)
				}
				if got := auth.ClassifyGrant(*seen); got != auth.GrantUniversal || !slices.Equal(seen.RepositoryScopes, []string{"*"}) {
					t.Fatalf("organization-wide %v: principal %q class %q, want the star grant kept", access.OrganizationWide, seen.RepositoryScopes, got)
				}
				copy := *seen
				copy.Subject, copy.CredentialID = "", ""
				principals = append(principals, copy)
			}
			if !reflect.DeepEqual(principals[0], principals[1]) {
				t.Fatalf("principal differs from today's non-empty inventory answer:\ntoday        %+v\norganization %+v", principals[0], principals[1])
			}
		})
	}
}

func TestAnOrganizationScopeKeepsANamedGrantAsTheLimit(t *testing.T) {
	owner, _, _ := strings.Cut(hostedTestRepository, "/")
	for _, grant := range [][]string{{hostedTestRepository, "zeta/x"}, {owner + "/*"}} {
		want, err := auth.NormalizeRepositoryScopes(grant)
		if err != nil {
			t.Fatal(err)
		}
		for _, class := range userClasses() {
			t.Run(strings.Join(grant, ",")+"/"+string(class), func(t *testing.T) {
				harness, _ := newMemorySubmissionHarness(t, 1)
				app := harness.apps[0]
				reader := &fakePrincipalAccess{}
				reader.set(organizationAccess(), nil)
				app.runtime.PrincipalAccess = reader
				authenticateAsCredential(t, app, class, grant)
				recorder, code, seen := serveCapturing(t, app, harness.tokenA)
				if recorder.Code != http.StatusNoContent || seen == nil {
					t.Fatalf("%d %s, want the handler reached", recorder.Code, code)
				}
				if got := auth.ClassifyGrant(*seen); got != auth.GrantRestricted || !slices.Equal(seen.RepositoryScopes, want) {
					t.Fatalf("principal %q class %q, want the grant %q, restricted", seen.RepositoryScopes, got, want)
				}
			})
		}
	}
}

func TestAnOrganizationScopeKeepsEveryLiveRefusal(t *testing.T) {
	for name, change := range map[string]func(*entitlements.PrincipalAccess){
		"not a member":  func(a *entitlements.PrincipalAccess) { a.Member = false },
		"impersonating": func(a *entitlements.PrincipalAccess) { a.Impersonating = true },
		"session dead":  func(a *entitlements.PrincipalAccess) { a.Session = &entitlements.SessionAccess{Live: false} },
		"runtime off":   func(a *entitlements.PrincipalAccess) { a.AgentContextRuntime = false },
		"session posing": func(a *entitlements.PrincipalAccess) {
			a.Session = &entitlements.SessionAccess{Live: true, Impersonating: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			harness, _ := newMemorySubmissionHarness(t, 1)
			app := harness.apps[0]
			access := organizationAccess()
			change(&access)
			reader := &fakePrincipalAccess{}
			reader.set(access, nil)
			app.runtime.PrincipalAccess = reader
			authenticateAsCredential(t, app, storage.CredentialClientClassBrowser, []string{"*"})
			recorder, code, seen := serveCapturing(t, app, harness.tokenA)
			if recorder.Code != http.StatusForbidden || seen != nil {
				t.Fatalf("%d %s (handler reached %v), want 403 before the handler", recorder.Code, code, seen != nil)
			}
		})
	}
}

// End to end through the real client and decoder: an ops-shaped answer of kind
// "organization" admits a "*" grant as universal.
func TestAnOpsOrganizationScopeReachesTheHandlerAsUniversal(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	app.runtime.PrincipalAccess = realAccessClient(t, http.StatusOK, strings.Replace(opsAccessAnswer(`null`),
		`"repository_scope":{"kind":"none"},"project_scope":null`, `"repository_scope":{"kind":"organization"}`, 1))
	authenticateInProjectOnlyOrg(t, app, []string{"*"})
	recorder, code, seen := serveCapturing(t, app, harness.tokenA)
	if recorder.Code != http.StatusNoContent || seen == nil {
		t.Fatalf("%d %s: %s", recorder.Code, code, recorder.Body.String())
	}
	if got := auth.ClassifyGrant(*seen); got != auth.GrantUniversal || !slices.Equal(seen.RepositoryScopes, []string{"*"}) {
		t.Fatalf("principal %q class %q, want universal", seen.RepositoryScopes, got)
	}
}
