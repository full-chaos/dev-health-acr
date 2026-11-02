package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12592: a web user is a universal reader everywhere a grant class
// decides: stored results, the census grant, work-item dispatch, the direct-read
// gates and an explicit repository. A credential that names repositories keeps
// the restricted arm of each.
func TestAWebUserIsAUniversalReaderAtEveryGate(t *testing.T) {
	t.Parallel()
	web := userprincipal.WebUser(t, "org_1")
	bound := userprincipal.Narrowed(t, "org_1", []string{"acme/api"}, []string{"acme/api"})
	if got := classifyStoredResultPrincipalScope(web); got != StoredResultScopeUniversal {
		t.Errorf("stored-result scope %q, want universal", got)
	}
	// A result a repository-bound reader stored stays readable to a web user; a
	// result a web user stored is refused to the bound reader.
	if reason, refused := storedResultGrantRefusal(web, StoredInvestigationResult{GrantDigest: StoredResultGrantDigest(bound)}); refused {
		t.Errorf("a web user is refused a result stored under a narrower grant: %s", reason)
	}
	if _, refused := storedResultGrantRefusal(bound, StoredInvestigationResult{GrantDigest: StoredResultGrantDigest(web)}); !refused {
		t.Error("a repository-bound reader is served a result a web user stored")
	}
	if bound, ok := CensusGrantFrom(ScopeCensusToCaller(context.Background(), web)); ok {
		t.Errorf("census grant %v: a web user's census binds no grant", bound)
	}
	if workItemRepositoryRestricted(web, nil) || !workItemRepositoryRestricted(web, []string{"acme/api"}) || !workItemRepositoryRestricted(bound, nil) {
		t.Error("work-item dispatch: want the web user unrestricted unless a repository is requested, and the bound credential restricted")
	}
	// An explicit repository request: every repository of the organization, one
	// outside the live inventory included; the store reads stay scoped to the
	// caller's organization.
	if err := auth.AuthorizeRepository(web, "acme/disconnected"); err != nil {
		t.Errorf("a web user is refused a repository of its organization: %v", err)
	}
	if err := auth.AuthorizeRepository(bound, "acme/disconnected"); err == nil {
		t.Error("a repository-bound credential is admitted outside its grant")
	}
}
