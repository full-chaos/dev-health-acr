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

// TestAStoredOutsideMatchCountNeverReachesAReaderOfAnotherGrant (CHAOS-12360,
// CHAOS-12446): the sentence is stored with the result, whose names list nothing
// for the subject check to refuse. So a stored read would be the oracle the live
// path refuses, unless the gate's grant check refuses it. Every stored reader
// takes this one gate.
func TestAStoredOutsideMatchCountNeverReachesAReaderOfAnotherGrant(t *testing.T) {
	t.Parallel()
	result := gateResult()
	result.Status = InvestigationNoMatch
	result.Limitations = []string{contractsv1.ContextFabricOutsideRequestedRepositoriesLimitation(1)}
	gate := NewStoredResultGate(&gateGraph{capturingGraphReader: &capturingGraphReader{}})
	// CHAOS-12446: a restricted caller can now write this sentence, about a
	// subject its own grant covers. A reader with another grant must not read it.
	writerGrant := []string{"acme/tools", "acme/worker"}
	for _, writer := range []storage.Principal{{OrgID: "org_1"}, {OrgID: "org_1", RepositoryScopes: writerGrant}} {
		stored := StoredInvestigationResult{Result: result, GrantDigest: StoredResultGrantDigest(writer)}
		for name, tc := range map[string]struct {
			reader storage.Principal
			admit  bool
			reason StoredResultAuthorizationReason
		}{
			"an unrestricted reader":    {storage.Principal{OrgID: "org_1"}, true, StoredResultReasonNoSubjects},
			"a universal reader":        {storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, true, StoredResultReasonNoSubjects},
			"a reader of another grant": {storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/tools"}}, false, StoredResultReasonGrantMismatch},
			"a project-only reader":     {storage.Principal{OrgID: "org_1", ProjectScopes: []string{"project.v2:github:P-1"}}, false, StoredResultReasonGrantMismatch},
		} {
			for _, surface := range StoredResultSurfaceVocabulary() {
				decision := gate.Authorize(context.Background(), tc.reader, stored, surface)
				if decision.Admitted() != tc.admit || decision.Reason != tc.reason {
					t.Fatalf("%s, writer %q, on %s: admitted %v reason %q, want %v %q", name, writer.RepositoryScopes, surface, decision.Admitted(), decision.Reason, tc.admit, tc.reason)
				}
			}
		}
	}
}

// TestARefusedGroupAxisKeepsTheCountOfMatchesOutside (CHAOS-12360, codex r1):
// this refusal empties the resolution it serves, and the count is not part of
// what it empties: the narrowing refused a real match all the same.
func TestARefusedGroupAxisKeepsTheCountOfMatchesOutside(t *testing.T) {
	t.Parallel()
	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	members := []CohortMember{{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_security"), Label: "Security"}}}
	engine, request := groupReadEngineFixtureConfigured(t, &recordingTelemetry{}, recorder, members, nil, SubjectTeam, nil, nil, func(config *groupReadFixtureConfig) {
		config.graph.resolution.OutsideRequestedRepositories = 2
		config.graph.resolution.WorkItemLinkLimitation = contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation
		config.graph.resolution.CensusLimitation = contractsv1.ContextFabricCensusUnlistedLimitation(4)
	})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Fatalf("refusal basis %q: the fixture must reach the collapsed-axis refusal", result.RefusalBasis)
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricOutsideRequestedRepositoriesLimitation(2)) {
		t.Fatalf("limitations %q: the count is gone", result.Limitations)
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricCensusUnlistedLimitation(4)) {
		t.Fatalf("limitations %q: the census count is gone", result.Limitations)
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation) {
		t.Fatalf("limitations %q: the work item sentence is gone", result.Limitations)
	}
}
