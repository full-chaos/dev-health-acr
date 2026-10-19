package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func gapLookalikes(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, contractsv1.ContextFabricWorkItemAuthorizationGapLimitation(false, 10+i, 50+i, false))
	}
	return out
}

func storedFloodCase(t *testing.T, limitations []string, principal storage.Principal, censusPrincipal storage.Principal) WorkItemTupleByIDDecision {
	t.Helper()
	result := workItemTuplePayloadFixture(t)
	result.Coverage = Coverage{Sources: []SourceObservation{}}
	result.Limitations = limitations
	state := workItemTupleSemanticStateFixture()
	state.WorkItemCensus = validStoredWorkItemCensus(t, censusPrincipal, WorkItemMembershipCensusExact, 1, 1, []string{"org/repo"})
	return proofServeStoredWorkItemTuple(t, result, state, SemanticStateReadAvailable, principal)
}

func TestStoredTupleWithFloodedLimitationsIsNotServed(t *testing.T) {
	principal := storage.Principal{RepositoryScopes: []string{"org/repo"}}
	got := storedFloodCase(t, gapLookalikes(contractsv1.ContextFabricLimitationsMaxCount), principal, principal)
	if got.Disposition != WorkItemTupleByIDNotFound || got.Event.Basis != "limitations_flooded" {
		t.Fatalf("disposition = %q basis = %q, want not_found limitations_flooded", got.Disposition, got.Event.Basis)
	}
	if got.CensusRead != WorkItemTupleCensusReadAvailable || got.Event.CensusRead != WorkItemTupleCensusReadAvailable {
		t.Fatalf("census read = %q event %q, want available", got.CensusRead, got.Event.CensusRead)
	}
}

func TestStoredTupleWithFloodedLimitationsUnderUniversalGrantIsNotServed(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	result.Coverage = Coverage{Sources: []SourceObservation{}}
	result.Limitations = gapLookalikes(contractsv1.ContextFabricLimitationsMaxCount)
	got := proofServeStoredWorkItemTuple(t, result, workItemTupleSemanticStateFixture(), SemanticStateReadAvailable, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}})
	if got.Disposition != WorkItemTupleByIDNotFound || got.Event.Basis != "limitations_flooded" {
		t.Fatalf("disposition = %q basis = %q, want not_found limitations_flooded", got.Disposition, got.Event.Basis)
	}
}

func TestStoredTupleFloodDoesNotMaskADigestChange(t *testing.T) {
	other := storage.Principal{RepositoryScopes: []string{"other/repo"}}
	got := storedFloodCase(t, gapLookalikes(contractsv1.ContextFabricLimitationsMaxCount), storage.Principal{RepositoryScopes: []string{"org/repo"}}, other)
	if got.Disposition != WorkItemTupleByIDNotFound || got.Event.Basis != "digest_changed" {
		t.Fatalf("disposition = %q basis = %q, want not_found digest_changed", got.Disposition, got.Event.Basis)
	}
}

func TestStoredTupleFullListWithAModelCaveatIsServed(t *testing.T) {
	principal := storage.Principal{RepositoryScopes: []string{"org/repo"}}
	limitations := append(gapLookalikes(contractsv1.ContextFabricLimitationsMaxCount-1), "The provider returned a stale status.")
	got := storedFloodCase(t, limitations, principal, principal)
	if got.Disposition != WorkItemTupleByIDServed || got.Event.Basis != "digest_matched" {
		t.Fatalf("disposition = %q basis = %q, want served digest_matched", got.Disposition, got.Event.Basis)
	}
}

func TestStoredTupleLookalikesUnderTheCapAreServed(t *testing.T) {
	principal := storage.Principal{RepositoryScopes: []string{"org/repo"}}
	got := storedFloodCase(t, gapLookalikes(contractsv1.ContextFabricLimitationsMaxCount-1), principal, principal)
	if got.Disposition != WorkItemTupleByIDServed || got.Event.Basis != "digest_matched" {
		t.Fatalf("disposition = %q basis = %q, want served digest_matched", got.Disposition, got.Event.Basis)
	}
}
