package devhealthfacts

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12592: a web user reads the whole organization, so the organization-
// level reads that refuse a repository-bound caller serve it: the investment
// org mix, source health and the unit listing (sourceHealthRestricted), and the
// incidents and blockers org-wide figures (orgWideFigureAllowed). A credential
// that names repositories keeps the refusal.
func TestAWebUserReadsOrganizationLevelFacts(t *testing.T) {
	t.Parallel()
	web := userprincipal.WebUser(t, "org_1")
	bound := userprincipal.Narrowed(t, "org_1", []string{"acme/api"}, []string{"acme/api"})
	if sourceHealthRestricted(web) {
		t.Fatal("a web user is refused the organization-level facts")
	}
	if !sourceHealthRestricted(bound) {
		t.Fatal("a repository-bound credential is served the organization-level facts")
	}
	if !orgWideFigureAllowed(web, nil) {
		t.Fatal("a web user is refused the organization-wide incident and blocker figures")
	}
	if orgWideFigureAllowed(bound, nil) {
		t.Fatal("a repository-bound credential is served the organization-wide figures")
	}
	if orgWideFigureAllowed(web, []string{"acme/api"}) {
		t.Fatal("a requested repository scope must still narrow a web user's figure")
	}
}
