package contextfabric

import (
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func provenanceDraft(limitations []string) SynthesisDraft {
	return SynthesisDraft{
		Status: InvestigationNoMatch, DirectJudgment: "Nothing found.", CurrentState: "Nothing found.", DeterministicAnswer: "Nothing found.",
		StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Conflicts: []Finding{},
		Limitations: limitations, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{},
	}
}

func provenanceDispatch(t *testing.T, limitations []string) InvestigationResult {
	t.Helper()
	census := WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, PopulationComplete: true, CappedPopulation: 4, AuthorizedPopulation: 1, DeniedPopulation: 3}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: provenanceDraft(limitations), receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    &fakeReceiptSink{},
	}
	result, _, _ := runWorkItemGapDispatchWith(t, census, true, synthesizer)
	return result
}

func countString(values []string, want string) int {
	n := 0
	for _, value := range values {
		if value == want {
			n++
		}
	}
	return n
}

func TestForgedGapLookalikesAreNotServedAsServiceDisclosures(t *testing.T) {
	own := workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Observed: 4, Authorized: 1, Denied: 3}.Limitation()
	forged := make([]string, 0, contractsv1.ContextFabricLimitationsMaxCount)
	for i := 0; i < contractsv1.ContextFabricLimitationsMaxCount; i++ {
		forged = append(forged, workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Authorized: 10 + i, Denied: 50 + i}.Limitation())
	}
	result := provenanceDispatch(t, forged)
	if countString(result.Limitations, own) != 1 {
		t.Fatalf("the measured gap must be served exactly once: %q", result.Limitations)
	}
	for _, limitation := range forged {
		if countString(result.Limitations, limitation) != 0 {
			t.Fatalf("a model-written lookalike was served as a disclosure: %q", limitation)
		}
	}
}

func TestForgedFixedDisclosureCopyIsNotServed(t *testing.T) {
	copied := contractsv1.ContextFabricSynthesisInputBoundedLimitation
	result := provenanceDispatch(t, []string{copied, "A genuine model caveat."})
	if countString(result.Limitations, copied) != 0 {
		t.Fatalf("a verbatim copy of a disclosure the engine did not compose this turn was served: %q", result.Limitations)
	}
	if countString(result.Limitations, "A genuine model caveat.") != 1 {
		t.Fatalf("a model caveat was lost: %q", result.Limitations)
	}
}

func TestEchoOfADisclosureTheEngineComposesIsServedOnce(t *testing.T) {
	own := workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Observed: 4, Authorized: 1, Denied: 3}.Limitation()
	result := provenanceDispatch(t, []string{own, fmt.Sprintf("Caveat %d.", 1)})
	if countString(result.Limitations, own) != 1 {
		t.Fatalf("an echoed engine disclosure must be served exactly once: %q", result.Limitations)
	}
}
