package directread

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12592: the direct-read gates (catalog, subjects, facts, relationships)
// take a web user as universal; a credential that names repositories stays
// restricted.
func TestAWebUserIsUniversalAtTheDirectReadGates(t *testing.T) {
	t.Parallel()
	if got := ClassifyPrincipal(userprincipal.WebUser(t, "org_1")); got != ClassUniversal {
		t.Errorf("web user class %q, want %q", got, ClassUniversal)
	}
	if got := ClassifyPrincipal(userprincipal.Narrowed(t, "org_1", []string{"acme/api"}, []string{"acme/api"})); got != ClassRestricted {
		t.Errorf("repository-bound class %q, want %q", got, ClassRestricted)
	}
}
