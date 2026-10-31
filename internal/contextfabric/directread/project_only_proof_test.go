package directread

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 r2: a gate proof is bound to the project grant as well as the
// repository grant. A proof issued under {P-1, P-2} is not issued to a
// principal holding {P-1} with the same repository list, nor the reverse; a
// principal with no project grant is decided as before.
func TestAGateProofIsBoundToTheProjectGrant(t *testing.T) {
	subjects := []contextfabric.SubjectRef{subject(contractsv1.ContextFabricSubjectProject, "project.v2:github:P-1")}
	base := storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c"}
	wide, narrow := base, base
	wide.ProjectScopes = []string{"project.v2:github:P-1", "project.v2:github:P-2"}
	narrow.ProjectScopes = []string{"project.v2:github:P-1"}
	proof := issue(context.Background(), wide, subjects, time.Now())
	if !proof.IssuedTo(wide) {
		t.Fatal("a proof is not issued to the principal it was issued under")
	}
	if proof.IssuedTo(narrow) || issue(context.Background(), narrow, subjects, time.Now()).IssuedTo(wide) {
		t.Fatal("a proof crossed between two different project grants")
	}
	for _, scopes := range [][]string{nil, {"*"}, {"acme/a"}} {
		principal := base
		principal.RepositoryScopes = scopes
		if !issue(context.Background(), principal, subjects, time.Now()).IssuedTo(principal) {
			t.Errorf("%q: a principal with no project grant no longer holds its own proof", scopes)
		}
	}
	other := base
	other.RepositoryScopes = []string{"acme/a"}
	if issue(context.Background(), base, subjects, time.Now()).IssuedTo(other) {
		t.Error("a proof crossed between two different repository grants")
	}
}
