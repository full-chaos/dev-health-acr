package contextfabric

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12229 review r3: a requested project narrowing's resolution is not in
// the reuse key, so a stored answer made while the narrowing was proven can
// meet a request whose narrowing is not (the resolver failed, or an id no
// longer resolves). Neither reuse path may then serve the stored answer clean.

type failingProjectNarrowingGraph struct{ graphReaderStub }

func (failingProjectNarrowingGraph) ResolveProjectNarrowing(context.Context, storage.Principal, ResolvedGraphBinding, []string) (*contractsv1.ContextFabricProjectNarrowing, error) {
	return nil, errors.New("resolver unavailable")
}

// errFreshPathTaken stands in for the fresh path: a declined reuse reaches
// the interpreter, and the test stops there.
var errFreshPathTaken = errors.New("the fresh path was taken")

func freshPathInterpreter() interpreterFunc {
	return interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
		return InterpretedQuestion{}, errFreshPathTaken
	})
}

func unverifiedProjectNarrowingCoverage() Coverage {
	coverage := Coverage{DegradedReasons: []string{}, Details: []CoverageDetail{}}
	discloseUnverifiedProjectNarrowing(&coverage)
	return coverage
}

func TestAReuseDeclinesAStoredAnswerThatHidesAnUnverifiedProjectNarrowing(t *testing.T) {
	for name, tc := range map[string]struct {
		recheckUnverified, storedDiscloses bool
		citationGone                       bool
		wantReused                         bool
		wantOutcome                        AnswerReuseOutcome
	}{
		"the recheck is unverified and the stored answer is clean": {true, false, false, false, AnswerReuseMissProjectNarrowingUnverified},
		"the stored answer already discloses it":                   {true, true, false, true, AnswerReuseHit},
		"the recheck is proven":                                    {false, false, false, true, AnswerReuseHit},
		// A gone citation is refused as before, and keeps its own outcome.
		"a citation is gone as well": {true, false, true, false, AnswerReuseMissEvidenceContainment},
	} {
		t.Run(name, func(t *testing.T) {
			project, candidate := reusableCandidate()
			candidate.EvidenceRefIDs = []string{"evidence_reuse_1"}
			if tc.storedDiscloses {
				candidate.Coverage = unverifiedProjectNarrowingCoverage()
				candidate.Status = contractsv1.ContextFabricInvestigationPartial
			}
			recheck := GraphContext{EvidenceRefIDs: []string{"evidence_reuse_1"}}
			if tc.citationGone {
				recheck.EvidenceRefIDs = []string{}
			}
			if tc.recheckUnverified {
				recheck.Coverage = unverifiedProjectNarrowingCoverage()
			}
			telemetry := &recordingTelemetry{}
			deps := EngineDependencies{
				Graph: failingProjectNarrowingGraph{graphReaderStub{
					resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
					context:    recheck,
				}},
				Results: &resultStoreStub{}, Telemetry: telemetry,
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			}
			if !tc.wantReused {
				deps.Interpreter = freshPathInterpreter()
			}
			request := validInvestigationRequest()
			request.RequestedScope.ProjectIDs = []string{"proj-1"}
			result, err := mustReuseTestEngine(t, deps).Investigate(context.Background(), reusePrincipal(), request)
			assertReuseDecision(t, result, err, tc.wantReused)
			if len(telemetry.answerReuseOutcomes) == 0 || telemetry.answerReuseOutcomes[0] != tc.wantOutcome {
				t.Fatalf("reuse outcomes = %v, want first %s", telemetry.answerReuseOutcomes, tc.wantOutcome)
			}
		})
	}
}

func TestATupleReuseDeclinesAStoredAnswerThatHidesAnUnverifiedProjectNarrowing(t *testing.T) {
	for name, tc := range map[string]struct {
		currentUnverified, storedDiscloses bool
		wantReused                         bool
		wantDecision                       string
	}{
		"the re-walk is unverified and the stored answer is clean": {true, false, false, "project_narrowing_unverified"},
		"the stored answer already discloses it":                   {true, true, true, "hit"},
		"the re-walk is proven":                                    {false, false, true, "hit"},
	} {
		t.Run(name, func(t *testing.T) {
			principal, request, stored, current := tupleReuseFixture(t)
			current.ProjectNarrowingUnverified = tc.currentUnverified
			if tc.storedDiscloses {
				discloseUnverifiedProjectNarrowing(&stored.Result.Coverage)
				stored.Result.Status = contractsv1.ContextFabricInvestigationPartial
			}
			gate, err := NewWorkItemMembershipGate(1, 0)
			if err != nil {
				t.Fatal(err)
			}
			telemetry := &recordingTelemetry{}
			deps := EngineDependencies{
				ReuseGate: tupleReuseGate{stored}, Telemetry: telemetry,
				CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
					return true, CandidateVerificationValid
				},
				WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
					lease, err := gate.Acquire(ctx)
					return lease, current, err
				}),
			}
			if !tc.wantReused {
				deps.Interpreter = freshPathInterpreter()
			}
			result, err := mustReuseTestEngine(t, deps).Investigate(context.Background(), principal, request)
			assertReuseDecision(t, result, err, tc.wantReused)
			if len(telemetry.workItemReuses) != 1 || telemetry.workItemReuses[0].Decision != tc.wantDecision {
				t.Fatalf("tuple reuse lines = %+v, want one with decision %s", telemetry.workItemReuses, tc.wantDecision)
			}
		})
	}
}

// assertReuseDecision: a hit is served as reused; a decline takes the fresh
// path, which the fresh-path interpreter stops.
func assertReuseDecision(t *testing.T, result InvestigationResult, err error, wantReused bool) {
	t.Helper()
	if wantReused {
		if err != nil || !result.Reused {
			t.Fatalf("want a reuse hit: reused=%v err=%v", result.Reused, err)
		}
		return
	}
	if !errors.Is(err, errFreshPathTaken) {
		t.Fatalf("want the fresh path: reused=%v (status=%s, partial=%v) err=%v", result.Reused, result.Status, result.Coverage.Partial, err)
	}
}
