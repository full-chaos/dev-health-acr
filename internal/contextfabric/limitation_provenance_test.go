package contextfabric

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"

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
	result, _ := provenanceDispatchObserved(t, limitations)
	return result
}

func provenanceDispatchObserved(t *testing.T, limitations []string) (InvestigationResult, *recordingTelemetry) {
	t.Helper()
	census := WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, PopulationComplete: true, CappedPopulation: 4, AuthorizedPopulation: 1, DeniedPopulation: 3}
	observed := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: provenanceDraft(limitations), receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: observed,
	}
	result, _, _ := runWorkItemGapDispatchWith(t, census, true, synthesizer)
	return result, observed
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

func TestDroppedLookalikesAreCountedByRecogniser(t *testing.T) {
	gap := workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Authorized: 10, Denied: 50}.Limitation()
	copied := contractsv1.ContextFabricSynthesisInputBoundedLimitation
	_, telemetry := provenanceDispatchObserved(t, []string{gap, copied, "A genuine model caveat."})
	want := []serviceLimitationLookalikeRecord{{dropped: 2, recognisers: []string{"fixed", "work_item_authorization_gap"}}}
	if !reflect.DeepEqual(telemetry.serviceLimitationLookalikes, want) {
		t.Fatalf("lookalikes = %+v want %+v", telemetry.serviceLimitationLookalikes, want)
	}
	_, quiet := provenanceDispatchObserved(t, []string{"A genuine model caveat."})
	if len(quiet.serviceLimitationLookalikes) != 0 {
		t.Fatalf("a draft without lookalikes was reported: %+v", quiet.serviceLimitationLookalikes)
	}
}

func TestSuppliedDraftLookalikesAreNotServedAsServiceDisclosures(t *testing.T) {
	copied := contractsv1.ContextFabricSynthesisInputBoundedLimitation
	wb := newWriteBackRig(t, nil)
	_, bundle := wb.firstCall()
	draft := bootstrapDraft(wb.project)
	draft.Limitations = []string{copied, "A supplied model caveat."}
	wb.parse.draft = draft
	result, _, err := wb.investigate(writeBackRequest(bundle, nil))
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if countString(result.Limitations, copied) != 0 {
		t.Fatalf("a verbatim disclosure copy in a supplied draft was served: %q", result.Limitations)
	}
	if countString(result.Limitations, "A supplied model caveat.") != 1 {
		t.Fatalf("a supplied caveat was lost: %q", result.Limitations)
	}
	if len(wb.telemetry.serviceLimitationLookalikes) != 1 || wb.telemetry.serviceLimitationLookalikes[0].dropped != 1 {
		t.Fatalf("lookalikes = %+v want one dropped", wb.telemetry.serviceLimitationLookalikes)
	}
}

func TestDegradedAndBoundedCompositionKeepTheirServiceDisclosures(t *testing.T) {
	failure := &SynthesisFailure{Class: SynthesisFailureModelOutputInvalid, DeferEvent: true, Bound: &synthesisInputBound{}, cause: fmt.Errorf("model output")}
	result, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, SynthesisInput{}, failure)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{contractsv1.ContextFabricSynthesisFailedNoFactsLimitation, contractsv1.ContextFabricSynthesisInputBoundedLimitation} {
		if !slices.Contains(result.Limitations, want) {
			t.Fatalf("a degraded or bounded answer lost %q: %q", want, result.Limitations)
		}
	}
}

func TestUnnamedRecogniserIsLabelledOther(t *testing.T) {
	if got := serviceLimitationRecogniserID("A genuine model caveat."); got != unnamedLimitationRecogniser {
		t.Fatalf("id = %q", got)
	}
}

func TestNoLookalikeSurvivesTheStripSoTheClassifierSeesOnlyEngineText(t *testing.T) {
	draft := append([]string{}, contractsv1.ContextFabricServiceAuthoredLimitations()...)
	for i := 0; i < 20; i++ {
		draft = append(draft, workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Authorized: 10 + i, Denied: 50 + i}.Limitation())
	}
	draft = append(draft, "A genuine model caveat.")
	kept, dropped, _ := stripServiceAuthoredLookalikes(draft)
	if dropped != len(draft)-1 || !slices.Equal(kept, []string{"A genuine model caveat."}) {
		t.Fatalf("kept = %q dropped = %d", kept, dropped)
	}
	for _, limitation := range kept {
		if isServiceAuthoredLimitation(limitation) {
			t.Fatalf("a recogniser-accepted entry survived the strip: %q", limitation)
		}
	}
	if lastModelAuthoredLimitation(kept) != len(kept)-1 {
		t.Fatal("the displacement classifier sees a service entry in stripped model text")
	}
}
