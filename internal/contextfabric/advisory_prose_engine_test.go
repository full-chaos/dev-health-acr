package contextfabric

import (
	"context"
	"reflect"
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

func TestEmptyAdvisoryProseServesTheSameEvidenceCoverageAndCompleteness(t *testing.T) {
	t.Parallel()
	withProse, err := investigateWithDraft(t, bootstrapDraft(acceptanceProject()))
	if err != nil {
		t.Fatalf("Investigate(non-empty prose) error = %v", err)
	}
	empty := bootstrapDraft(acceptanceProject())
	empty.DirectJudgment, empty.CurrentState, empty.DeterministicAnswer = "", "", ""
	withoutProse, err := investigateWithDraft(t, empty)
	if err != nil {
		t.Fatalf("Investigate(empty prose) error = %v", err)
	}
	if withProse.Status != withoutProse.Status {
		t.Fatalf("status = %q with prose, %q without", withProse.Status, withoutProse.Status)
	}
	pairs := map[string][2]any{
		"EvidenceRefIDs": {withProse.EvidenceRefIDs, withoutProse.EvidenceRefIDs},
		"Coverage":       {withProse.Coverage, withoutProse.Coverage},
		"Completeness":   {withProse.Completeness, withoutProse.Completeness},
		"Limitations":    {withProse.Limitations, withoutProse.Limitations},
		"ReadinessGaps":  {withProse.ReadinessGaps, withoutProse.ReadinessGaps},
		"ClaimedFacts":   {withProse.ClaimedFacts, withoutProse.ClaimedFacts},
		"Drivers":        {withProse.Drivers, withoutProse.Drivers},
		"Warnings":       {withProse.Warnings, withoutProse.Warnings},
		"RefusalBasis":   {withProse.RefusalBasis, withoutProse.RefusalBasis},
	}
	for field, pair := range pairs {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s differs: with prose %#v, without %#v", field, pair[0], pair[1])
		}
	}
}
