package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestStructureVetoDetailNamesTheExit(t *testing.T) {
	t.Parallel()
	verifier := func(reason CandidateVerificationReason) CandidateVerifier {
		return func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, CandidateVerificationReason) {
			return false, reason
		}
	}
	cases := []struct {
		name string
		deps func(store *staticResultStore) EngineDependencies
		edit func(*InvestigationRequest, *staticResultStore)
		want structureVetoDetail
	}{
		{"no verifier", func(s *staticResultStore) EngineDependencies { return EngineDependencies{Results: s} }, nil, structureDetailReverifyNoVerifier},
		{"claim lost", func(s *staticResultStore) EngineDependencies {
			return EngineDependencies{Results: s, CandidateVerifier: verifier(CandidateVerificationClaimLost)}
		}, nil, structureDetailReverifyClaimLost},
		{"unverifiable", func(s *staticResultStore) EngineDependencies {
			return EngineDependencies{Results: s, CandidateVerifier: verifier(CandidateVerificationGraphUnverifiable)}
		}, nil, structureDetailReverifyUnverif},
		{"stored result missing", func(s *staticResultStore) EngineDependencies { return EngineDependencies{Results: s} },
			func(r *InvestigationRequest, _ *staticResultStore) {
				r.PriorCandidateReceipts[0].ResultID = "result_absent_0001"
			}, structureDetailStoredResultGet},
		{"option not found", func(s *staticResultStore) EngineDependencies { return EngineDependencies{Results: s} },
			func(r *InvestigationRequest, _ *staticResultStore) {
				r.PriorCandidateReceipts[0].ReceiptID = "candr_absent00001"
			}, structureDetailOptionNotFound},
		{"empty ids", func(s *staticResultStore) EngineDependencies { return EngineDependencies{Results: s} },
			func(r *InvestigationRequest, _ *staticResultStore) { r.PriorCandidateReceipts[0].ReceiptID = " " }, structureDetailEmptyIDs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prior, request := candidateReceiptTestSetup()
			store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
			if tc.edit != nil {
				tc.edit(&request, store)
			}
			engine := mustReuseTestEngine(t, tc.deps(store))
			canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
			if canon.Veto != structureVetoConfirmationUnresolved || canon.Detail != tc.want {
				t.Fatalf("veto=%q detail=%q, want unresolved/%q", canon.Veto, canon.Detail, tc.want)
			}
			if got := structureVetoLimitation(canon.Veto, canon.Detail); !strings.Contains(got, "(reason: "+string(tc.want)+")") {
				t.Fatalf("limitation %q does not name %q", got, tc.want)
			}
		})
	}
}
