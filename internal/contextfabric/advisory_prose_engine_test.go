package contextfabric

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func investigateWithDraft(t *testing.T, draft SynthesisDraft) (InvestigationResult, error) {
	t.Helper()
	project := acceptanceProject()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	engine := buildAcceptanceEngine(t, graph, facts, bootstrapInterpretation(), draft, newMapResultStore())
	request := validInvestigationRequestWithConfirmedWindow()
	request.Question = "What is the actual status of Ask Dev and what is driving it?"
	return engine.Investigate(context.Background(), acceptancePrincipal(), request)
}

func TestInvestigateServesAGroundedDraftWithEmptyAdvisoryProse(t *testing.T) {
	t.Parallel()
	for name, status := range map[string]InvestigationStatus{"complete": InvestigationComplete, "partial": InvestigationPartial} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			draft := bootstrapDraft(acceptanceProject())
			draft.Status, draft.DirectJudgment, draft.CurrentState, draft.DeterministicAnswer = status, "", " ", ""
			result, err := investigateWithDraft(t, draft)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != status || IsSynthesisModelFailureAnswer(result) {
				t.Fatalf("status = %q warnings = %v, want the synthesized %q answer", result.Status, result.Warnings, status)
			}
			if strings.TrimSpace(result.DirectJudgment) == "" || strings.TrimSpace(result.DeterministicAnswer) == "" || strings.TrimSpace(result.CurrentState) == "" {
				t.Fatalf("served prose = %q / %q / %q, want the server-composed text", result.DirectJudgment, result.CurrentState, result.DeterministicAnswer)
			}
			if len(result.ClaimedFacts) != 1 || result.ClaimedFacts[0].ClaimID != "claim_readiness_bootstrap" || len(result.Drivers) != 1 {
				t.Fatalf("claims = %#v drivers = %d, want the grounded claim and its driver", result.ClaimedFacts, len(result.Drivers))
			}
		})
	}
}

func TestInvestigateStillRefusesAContradictingClaimBehindEmptyAdvisoryProse(t *testing.T) {
	t.Parallel()
	draft := bootstrapDraft(acceptanceProject())
	draft.DirectJudgment, draft.DeterministicAnswer = "", ""
	draft.ClaimedFacts[0].Value = boolScalar(true)
	result, err := investigateWithDraft(t, draft)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want the degraded answer", err)
	}
	if !IsSynthesisModelFailureAnswer(result) || result.Status != InvestigationDegraded {
		t.Fatalf("status = %q warnings = %v, want the degraded synthesis-rejected answer", result.Status, result.Warnings)
	}
	for _, claim := range result.ClaimedFacts {
		if !strings.HasPrefix(claim.ClaimID, "server:fact:") {
			t.Fatalf("claim %q is not server-minted: the refused draft's claim was served", claim.ClaimID)
		}
	}
}
