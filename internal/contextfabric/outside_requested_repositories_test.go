package contextfabric

import (
	"context"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestANoMatchStatesTheMatchesOutsideTheRequestedRepositories (CHAOS-12360,
// lead ruling): the refusal stands, and the answer says so. A silent no_match is
// not useful partial truth (North Star checks 4 and 5). It states a count and
// never a name.
func TestANoMatchStatesTheMatchesOutsideTheRequestedRepositories(t *testing.T) {
	t.Parallel()
	sentence := contractsv1.ContextFabricOutsideRequestedRepositoriesLimitation(2)
	candidate := func(id string) SubjectCandidate {
		return SubjectCandidate{ReceiptID: "receipt_" + id, Subject: SubjectRef{Kind: SubjectRepository, CanonicalID: id, Label: id}, State: ResolutionAmbiguous,
			MatchedTerms: []string{id}, MatchReasons: []string{"matched"}, Confidence: 0.8, EvidenceRefIDs: []string{"evidence_identity_1234"}}
	}
	for _, tc := range []struct {
		name       string
		candidates []SubjectCandidate
		outside    int
		status     InvestigationStatus
		want       bool
	}{
		{"matches outside the narrowing", []SubjectCandidate{}, 2, InvestigationNoMatch, true},
		{"nothing outside the narrowing", []SubjectCandidate{}, 0, InvestigationNoMatch, false},
		{"a choice inside the narrowing, and matches outside it", []SubjectCandidate{candidate("repository_a"), candidate("repository_b")}, 2, InvestigationClarificationRequired, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: SubjectResolution{Candidates: tc.candidates, Committed: []SubjectRef{}, OutsideRequestedRepositories: tc.outside}}, &recordingTelemetry{})
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != tc.status {
				t.Fatalf("status %q, want %q", result.Status, tc.status)
			}
			if got := slices.Contains(result.Limitations, sentence); got != tc.want {
				t.Fatalf("limitations %q: stated %v, want %v", result.Limitations, got, tc.want)
			}
		})
	}
}

// TestAStoredOutsideMatchCountNeverReachesARestrictedOrProjectOnlyReader
// (CHAOS-12360): the sentence is composed for an unrestricted principal and
// stored with the result, whose names list nothing for the subject check to
// refuse. So a stored read would be the oracle the live path refuses, unless
// the gate's grant check refuses it. Every stored reader takes this one gate.
func TestAStoredOutsideMatchCountNeverReachesARestrictedOrProjectOnlyReader(t *testing.T) {
	t.Parallel()
	writer := storage.Principal{OrgID: "org_1"}
	result := gateResult()
	result.Status = InvestigationNoMatch
	result.Limitations = []string{contractsv1.ContextFabricOutsideRequestedRepositoriesLimitation(1)}
	stored := StoredInvestigationResult{Result: result, GrantDigest: StoredResultGrantDigest(writer)}
	gate := NewStoredResultGate(&gateGraph{capturingGraphReader: &capturingGraphReader{}})
	for name, tc := range map[string]struct {
		reader storage.Principal
		admit  bool
		reason StoredResultAuthorizationReason
	}{
		"an unrestricted reader": {storage.Principal{OrgID: "org_1"}, true, StoredResultReasonNoSubjects},
		"a universal reader":     {storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, true, StoredResultReasonNoSubjects},
		"a restricted reader":    {storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/tools"}}, false, StoredResultReasonGrantMismatch},
		"a project-only reader":  {storage.Principal{OrgID: "org_1", ProjectScopes: []string{"project.v2:github:P-1"}}, false, StoredResultReasonGrantMismatch},
	} {
		for _, surface := range StoredResultSurfaceVocabulary() {
			decision := gate.Authorize(context.Background(), tc.reader, stored, surface)
			if decision.Admitted() != tc.admit || decision.Reason != tc.reason {
				t.Fatalf("%s on %s: admitted %v reason %q, want %v %q", name, surface, decision.Admitted(), decision.Reason, tc.admit, tc.reason)
			}
		}
	}
}
