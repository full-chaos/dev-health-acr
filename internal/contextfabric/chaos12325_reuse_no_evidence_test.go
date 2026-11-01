package contextfabric

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12325: a stored answer with no evidence references is rechecked
// without a graph context read, so the CHAOS-12229 reuse guard, which reads
// that coverage, cannot see an unverified project narrowing there. The
// branch decides from the request's own resolution instead: a requested
// narrowing the resolution did not prove (no resolution, or an incomplete
// one) is not served from a stored answer that does not disclose it.

// narrowingResolverGraph resolves every requested project narrowing to a
// fixed result.
type narrowingResolverGraph struct {
	graphReaderStub
	narrowing *contractsv1.ContextFabricProjectNarrowing
	err       error
}

func (g narrowingResolverGraph) ResolveProjectNarrowing(context.Context, storage.Principal, ResolvedGraphBinding, []string) (*contractsv1.ContextFabricProjectNarrowing, error) {
	return g.narrowing, g.err
}

func TestAReuseWithNoEvidenceDeclinesAnUnprovenProjectNarrowing(t *testing.T) {
	complete := &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:proj-1"}, Complete: true}
	incomplete := &contractsv1.ContextFabricProjectNarrowing{Identities: []string{}, Complete: false}
	failed := errors.New("resolver unavailable")
	for name, tc := range map[string]struct {
		projectIDs      []string
		narrowing       *contractsv1.ContextFabricProjectNarrowing
		err             error
		storedDiscloses bool
		wantReused      bool
		wantOutcome     AnswerReuseOutcome
	}{
		"the resolver failed and the stored answer is clean": {[]string{"proj-1"}, nil, failed, false, false, AnswerReuseMissProjectNarrowingUnverified},
		"the resolution is incomplete":                       {[]string{"proj-1"}, incomplete, nil, false, false, AnswerReuseMissProjectNarrowingUnverified},
		"the resolution is complete":                         {[]string{"proj-1"}, complete, nil, false, true, AnswerReuseHit},
		"the stored answer already discloses it":             {[]string{"proj-1"}, nil, failed, true, true, AnswerReuseHit},
		"no project narrowing":                               {nil, nil, failed, false, true, AnswerReuseHit},
		"a requested wildcard narrows nothing":               {[]string{"proj-1", "*"}, nil, failed, false, true, AnswerReuseHit},
	} {
		t.Run(name, func(t *testing.T) {
			project, candidate := reusableCandidate()
			if len(candidate.EvidenceRefIDs) != 0 {
				t.Fatalf("premise: the stored answer cites %v", candidate.EvidenceRefIDs)
			}
			if tc.storedDiscloses {
				candidate.Coverage = unverifiedProjectNarrowingCoverage()
				candidate.Status = contractsv1.ContextFabricInvestigationPartial
			}
			telemetry := &recordingTelemetry{}
			deps := EngineDependencies{
				Graph: narrowingResolverGraph{graphReaderStub: graphReaderStub{
					resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
					context:    GraphContext{EvidenceRefIDs: []string{}, Coverage: unverifiedProjectNarrowingCoverage()},
				}, narrowing: tc.narrowing, err: tc.err},
				Results: &resultStoreStub{}, Telemetry: telemetry,
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			}
			if !tc.wantReused {
				deps.Interpreter = freshPathInterpreter()
			}
			request := validInvestigationRequest()
			request.RequestedScope.ProjectIDs = tc.projectIDs
			result, err := mustReuseTestEngine(t, deps).Investigate(context.Background(), reusePrincipal(), request)
			assertReuseDecision(t, result, err, tc.wantReused)
			if len(telemetry.answerReuseOutcomes) == 0 || telemetry.answerReuseOutcomes[0] != tc.wantOutcome {
				t.Fatalf("reuse outcomes = %v, want first %s", telemetry.answerReuseOutcomes, tc.wantOutcome)
			}
		})
	}
}
