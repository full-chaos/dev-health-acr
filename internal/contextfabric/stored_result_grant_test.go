package contextfabric

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7145: a stored result carries no fact rows, but its prose was built
// from the computing principal's fact set. A restricted reader is served only
// a result computed under its OWN grant.
func TestStoredResultGrantGate(t *testing.T) {
	restrictedA := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/tools"}}
	restrictedARe := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{" acme/tools", "acme/tools"}}
	restrictedB := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/web"}}
	superset := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/tools", "acme/web"}}
	universal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}
	unrestricted := storage.Principal{OrgID: "org_1"}

	cases := []struct {
		name     string
		computed string
		reader   storage.Principal
		decision StoredResultDecision
		reason   StoredResultAuthorizationReason
	}{
		{"same grant", StoredResultGrantDigest(restrictedA), restrictedA, StoredResultAdmitted, StoredResultReasonNoSubjects},
		{"same grant reordered/deduped", StoredResultGrantDigest(restrictedA), restrictedARe, StoredResultAdmitted, StoredResultReasonNoSubjects},
		{"restricted A result, restricted B reader", StoredResultGrantDigest(restrictedA), restrictedB, StoredResultDenied, StoredResultReasonGrantMismatch},
		{"restricted A result, wider restricted reader", StoredResultGrantDigest(restrictedA), superset, StoredResultDenied, StoredResultReasonGrantMismatch},
		{"unrestricted result, restricted reader", StoredResultGrantDigest(unrestricted), restrictedA, StoredResultDenied, StoredResultReasonGrantMismatch},
		{"universal result, restricted reader", StoredResultGrantDigest(universal), restrictedA, StoredResultDenied, StoredResultReasonGrantMismatch},
		{"pre-migration NULL, restricted reader", "", restrictedA, StoredResultDenied, StoredResultReasonGrantUnrecorded},
		{"restricted result, unrestricted reader", StoredResultGrantDigest(restrictedA), unrestricted, StoredResultAdmitted, StoredResultReasonNoSubjects},
		{"restricted result, universal reader", StoredResultGrantDigest(restrictedA), universal, StoredResultAdmitted, StoredResultReasonNoSubjects},
		{"pre-migration NULL, unrestricted reader", "", unrestricted, StoredResultAdmitted, StoredResultReasonNoSubjects},
		{"pre-migration NULL, universal reader", "", universal, StoredResultAdmitted, StoredResultReasonNoSubjects},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, surface := range StoredResultSurfaceVocabulary() {
				decision := NewStoredResultGate(&capturingGraphReader{}).Authorize(context.Background(), c.reader, StoredInvestigationResult{Result: gateResult(), GrantDigest: c.computed}, surface)
				if decision.Decision != c.decision || decision.Reason != c.reason {
					t.Fatalf("%s: decision = %s/%s, want %s/%s", surface, decision.Decision, decision.Reason, c.decision, c.reason)
				}
				if c.decision == StoredResultDenied {
					if !errors.Is(decision.ServingError(), ErrInvestigationResultNotFound) {
						t.Fatalf("ServingError() = %v, want the unknown-id answer", decision.ServingError())
					}
					if decision.SubjectCount != 0 || len(decision.RefusedKinds) != 0 {
						t.Fatalf("a grant refusal carries result content: %+v", decision)
					}
				}
			}
		})
	}
}

// The digest is the one CHAOS-7127 keys answer reuse with, never a second one.
func TestStoredResultGrantDigestIsTheReuseDigest(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/web", "acme/tools"}}
	if got, want := StoredResultGrantDigest(principal), "g:"+reuseGrantDigest(principal.RepositoryScopes); got != want {
		t.Fatalf("StoredResultGrantDigest = %q, want %q", got, want)
	}
}

type digestResultStore struct{ stored StoredInvestigationResult }

func (s digestResultStore) Save(context.Context, storage.Principal, InvestigationResult, SourceWatermarkSnapshot, RebuildEpoch, string, ReuseRetrievalIdentity, ReusePromptVersions, ReuseVersionAuthorities, int64, string, SemanticStateWrite) error {
	return nil
}
func (s digestResultStore) Get(context.Context, storage.Principal, string) (StoredInvestigationResult, error) {
	return s.stored, nil
}

// Every engine read of a prior result (parent_result_id, receipts, carries)
// goes through authorizedResultStore: a result computed for principal A is the
// unknown-id answer for restricted principal B.
func TestEngineResultStoreDeniesACrossPrincipalPriorResult(t *testing.T) {
	a := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/tools"}}
	b := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/web"}}
	store := authorizedResultStore{
		InvestigationResultStore: digestResultStore{stored: StoredInvestigationResult{Result: gateResult(), GrantDigest: StoredResultGrantDigest(a)}},
		gate:                     NewStoredResultGate(&capturingGraphReader{}),
	}
	if _, err := store.Get(context.Background(), a, "result_computed_for_a"); err != nil {
		t.Fatalf("computing principal: %v", err)
	}
	if _, err := store.Get(context.Background(), b, "result_computed_for_a"); !errors.Is(err, ErrInvestigationResultNotFound) {
		t.Fatalf("restricted principal B read A's result: err = %v", err)
	}
}
