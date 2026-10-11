package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestPreCommittedHintDigestCarriesTheIdentityLookupState(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		complete bool
		want     string
	}{{true, "complete"}, {false, "not_wired"}} {
		candidate := corroborationCandidate("digest_lookup", 1, contextfabric.MatchExact)
		candidate.State = contextfabric.ResolutionCommitted
		_, _, digests := ResolveFromMergedCandidatesWithGateAndBasis(
			map[string]contextfabric.SubjectCandidate{SubjectKey(candidate.Subject): candidate}, map[string]string{}, map[string]bool{}, 10, true, false,
			nil, 0, false, 10, 20, true,
			DefaultCommitGatePolicy(), nil, nil, row.complete, nil, "req-digest-lookup", "", false, false, nil)
		got := digests.For(candidate.Subject)
		if got.CommitGate != "pre_committed_exact_hint" || got.IdentityLookup != row.want || got.AliasLookupComplete != row.complete {
			t.Fatalf("complete=%t digest = %+v, want pre_committed_exact_hint with identity lookup %q", row.complete, got, row.want)
		}
	}
}
