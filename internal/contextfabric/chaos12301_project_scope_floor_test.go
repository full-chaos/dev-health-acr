package contextfabric

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12301, lead ruling (b): the project-scope floor is the status-only
// floor. A narrative-withholding floor keeps its own swap, as it does beside the
// cut-read floor: a floor that keeps the narrative never pre-empts one that
// withholds it.

func projectScopeRow() CoverageDetail {
	return withheldFloorResult(InvestigationComplete).Coverage.Details[0]
}

func TestAProjectOnlyCompleteAnswerKeepsItsNarrative(t *testing.T) {
	t.Parallel()
	stored := storedWithRows(t, projectScopeRow())
	if stored.Status != InvestigationComplete {
		t.Fatalf("fixture: stored status = %q, want complete", stored.Status)
	}
	served := ApplyServedStatusFloors(stored)
	if served.Status != InvestigationPartial || !served.Coverage.Partial || served.Completeness.TerminalStatus != InvestigationPartial {
		t.Fatalf("served %q, partial %v, terminal %q, want partial", served.Status, served.Coverage.Partial, served.Completeness.TerminalStatus)
	}
	if !slices.Contains(served.Limitations, contractsv1.ContextFabricProjectScopeWithheldLimitation) {
		t.Fatalf("limitations = %v, want the project-scope sentence", served.Limitations)
	}
	if slices.Contains(served.Limitations, synthesisNarrativeWithheldLimitation) {
		t.Fatalf("limitations = %v, want no narrative-withheld sentence", served.Limitations)
	}
	if !reflect.DeepEqual(served.DirectJudgment, stored.DirectJudgment) || !reflect.DeepEqual(served.DeterministicAnswer, stored.DeterministicAnswer) {
		t.Fatalf("the floor rewrote the head: %q, %q", served.DirectJudgment, served.DeterministicAnswer)
	}
	if stored.Status != InvestigationComplete || slices.Contains(stored.Limitations, contractsv1.ContextFabricProjectScopeWithheldLimitation) {
		t.Fatal("the floor rewrote the stored document")
	}
	again := ApplyServedStatusFloors(served)
	if !reflect.DeepEqual(again.Limitations, served.Limitations) || again.Status != InvestigationPartial {
		t.Fatalf("a second apply changed the answer: %q, %v", again.Status, again.Limitations)
	}
}

// The head still states the served status: restateServedStatusHead adds the
// partial sentence to a head the server composed, so nothing reads complete.
func TestAProjectOnlyServedHeadStatesPartial(t *testing.T) {
	t.Parallel()
	stored := storedWithRows(t, projectScopeRow())
	restated := restateServedStatusHead(ApplyServedStatusFloors(stored))
	assertAnswerKept(t, stored, restated)
	for name, head := range map[string]string{"direct_judgment": restated.DirectJudgment, "deterministic_answer": restated.DeterministicAnswer} {
		if strings.Contains(strings.ToLower(head), "complete") {
			t.Errorf("%s = %q reads complete", name, head)
		}
	}
}

func countOf(limitations []string, sentence string) int {
	count := 0
	for _, limitation := range limitations {
		if limitation == sentence {
			count++
		}
	}
	return count
}

// Every floor that withholds the narrative keeps its swap on a project-only
// answer, each for its own reason, whichever side of the old position it ran on.
// The answer still states the grant: partial, that floor's own disclosure and
// the project-scope sentence, each exactly once, however often it is served.
func TestANarrativeWithholdingFloorLeavesTheProjectScopeSentence(t *testing.T) {
	t.Parallel()
	poolCut := contractsv1.ContextFabricMemberStatusPoolCutLimitation("project", 10, 40)
	for name, test := range map[string]struct {
		build func(*testing.T) InvestigationResult
		own   string
	}{
		"a denied cohort": {func(t *testing.T) InvestigationResult {
			denied := detailFixture(contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, "context-fabric:graph", true, "cohort_denied:2", withCount(2))
			return storedWithRows(t, denied, projectScopeRow())
		}, ""},
		"an unverified project narrowing": {func(t *testing.T) InvestigationResult {
			return storedWithRows(t, unverifiedNarrowingDetail(), projectScopeRow())
		}, ""},
		"a status filter over a cut read": {func(t *testing.T) InvestigationResult {
			stored := storedWithRows(t, projectScopeRow())
			stored.Limitations = append(stored.Limitations, poolCut)
			return stored
		}, poolCut},
		"an organization survey": {func(t *testing.T) InvestigationResult {
			stored := storedWithRows(t, projectScopeRow())
			stored.Limitations = append(stored.Limitations, contractsv1.ContextFabricOrganizationSurveyNoMembersLimitation)
			return stored
		}, contractsv1.ContextFabricOrganizationSurveyNoMembersLimitation},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stored := test.build(t)
			if stored.Status != InvestigationComplete {
				t.Fatalf("fixture: stored status = %q", stored.Status)
			}
			served := ApplyServedStatusFloors(stored)
			for round := range 2 {
				if served.Status != InvestigationPartial {
					t.Fatalf("serve %d: status %q, want partial", round, served.Status)
				}
				if got := countOf(served.Limitations, synthesisNarrativeWithheldLimitation); got != 1 {
					t.Fatalf("serve %d: %d narrative-withheld sentences, want the sibling's one: %v", round, got, served.Limitations)
				}
				if got := countOf(served.Limitations, contractsv1.ContextFabricProjectScopeWithheldLimitation); got != 1 {
					t.Fatalf("serve %d: %d project-scope sentences, want exactly one: %v", round, got, served.Limitations)
				}
				if test.own != "" && countOf(served.Limitations, test.own) != 1 {
					t.Fatalf("serve %d: %d of the floor's own disclosure, want one: %v", round, countOf(served.Limitations, test.own), served.Limitations)
				}
				served = ApplyServedStatusFloors(served)
			}
		})
	}
}

// Already partial: the sentence joins and nothing else moves.
func TestAPartialAnswerGainsOnlyTheProjectScopeSentence(t *testing.T) {
	t.Parallel()
	before := withheldFloorResult(InvestigationPartial)
	after := before
	after.Limitations = slices.Clone(before.Limitations)
	applyProjectScopeWithheldStatusFloor(&after)
	if countOf(after.Limitations, contractsv1.ContextFabricProjectScopeWithheldLimitation) != 1 {
		t.Fatalf("limitations = %v, want the project-scope sentence once", after.Limitations)
	}
	if slices.Contains(after.Limitations, synthesisNarrativeWithheldLimitation) {
		t.Fatalf("limitations = %v, want no narrative-withheld sentence", after.Limitations)
	}
	unchanged := after
	unchanged.Limitations, unchanged.LimitationsDisplaced = before.Limitations, before.LimitationsDisplaced
	if !reflect.DeepEqual(unchanged, before) {
		t.Fatalf("something but the sentence moved: %+v, was %+v", unchanged, before)
	}
	applyProjectScopeWithheldStatusFloor(&after)
	if countOf(after.Limitations, contractsv1.ContextFabricProjectScopeWithheldLimitation) != 1 {
		t.Fatalf("a second pass added a copy: %v", after.Limitations)
	}
}

// A partial answer's full list makes room as every disclosure does, and says so.
func TestAFullPartialAnswerCountsTheCaveatTheSentenceDisplaced(t *testing.T) {
	t.Parallel()
	result := withheldFloorResult(InvestigationPartial)
	result.Limitations = result.Limitations[:0:0]
	for len(result.Limitations) < contractsv1.ContextFabricLimitationsMaxCount {
		result.Limitations = append(result.Limitations, "A model caveat "+strings.Repeat("x", len(result.Limitations)+1))
	}
	applyProjectScopeWithheldStatusFloor(&result)
	if result.LimitationsDisplaced != 1 || countOf(result.Limitations, contractsv1.ContextFabricProjectScopeWithheldLimitation) != 1 {
		t.Fatalf("displaced %d, limitations %v", result.LimitationsDisplaced, result.Limitations)
	}
}

// Everything else stays as it is: no detail, a refusal at any status, and every
// status but complete and partial.
func TestTheProjectScopeSentenceLeavesEveryOtherAnswerAlone(t *testing.T) {
	t.Parallel()
	sentence := contractsv1.ContextFabricProjectScopeWithheldLimitation
	for _, status := range []InvestigationStatus{InvestigationDegraded, InvestigationNoMatch, InvestigationClarificationRequired} {
		result := withheldFloorResult(status)
		applyProjectScopeWithheldStatusFloor(&result)
		if result.Status != status || slices.Contains(result.Limitations, sentence) {
			t.Fatalf("%q: status %q, limitations %v, want it untouched", status, result.Status, result.Limitations)
		}
	}
	refused := withheldFloorResult(InvestigationPartial)
	refused.RefusalBasis = contractsv1.ContextFabricRefusalBasisUnspecified
	applyProjectScopeWithheldStatusFloor(&refused)
	if slices.Contains(refused.Limitations, sentence) {
		t.Fatalf("a partial refusal gained the sentence: %v", refused.Limitations)
	}
	plain := withheldFloorResult(InvestigationPartial)
	plain.Coverage = Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}
	applyProjectScopeWithheldStatusFloor(&plain)
	if slices.Contains(plain.Limitations, sentence) {
		t.Fatalf("a partial answer with no refusal gained the sentence: %v", plain.Limitations)
	}
}

// Beside a cut read the answer states both causes, each once, and keeps its
// narrative: neither floor withholds it.
func TestAProjectScopeRowBesideACutReadStatesBothCauses(t *testing.T) {
	t.Parallel()
	cut := hopWalkCutCase()
	served := ApplyServedStatusFloors(storedWithRows(t, projectScopeRow(), cut.detail))
	if served.Status != InvestigationPartial || slices.Contains(served.Limitations, synthesisNarrativeWithheldLimitation) {
		t.Fatalf("served %q, limitations %v, want partial with the narrative kept", served.Status, served.Limitations)
	}
	for _, sentence := range []string{contractsv1.ContextFabricProjectScopeWithheldLimitation, cut.limitation} {
		if got := countOf(served.Limitations, sentence); got != 1 {
			t.Fatalf("%d of %q, want one: %v", got, sentence, served.Limitations)
		}
	}
}

// A service disclosure is never displaced by a later composer, and a draft that
// copies one is not kept as the model's own: that is what the authored list
// guards. Both project-only sentences are on it.
func TestTheProjectOnlySentencesAreServiceDisclosures(t *testing.T) {
	t.Parallel()
	for _, sentence := range []string{
		contractsv1.ContextFabricProjectScopeWithheldLimitation,
		contractsv1.ContextFabricRequestedProjectNarrowingUnavailableLimitation,
	} {
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Fatalf("%q is not a service disclosure", sentence)
		}
		full := make([]string, 0, contractsv1.ContextFabricLimitationsMaxCount)
		for len(full) < contractsv1.ContextFabricLimitationsMaxCount {
			full = append(full, "A model caveat "+strings.Repeat("x", len(full)+1))
		}
		composed, displaced := appendBoundedLimitations(full, []string{sentence})
		if displaced != 1 || !slices.Contains(composed, sentence) {
			t.Fatalf("%q: displaced %d, limitations %v", sentence, displaced, composed)
		}
		composed, _ = appendBoundedLimitations(composed, []string{contractsv1.ContextFabricRetrievalDegradedLimitation})
		if !slices.Contains(composed, sentence) {
			t.Fatalf("%q was displaced by a later composer: %v", sentence, composed)
		}
		if kept, dropped, recognisers := stripServiceAuthoredLookalikes([]string{sentence, "A model caveat."}); dropped != 1 || slices.Contains(kept, sentence) ||
			!reflect.DeepEqual(recognisers, []string{"fixed=1"}) {
			t.Fatalf("%q: kept %v, dropped %d, recognisers %v", sentence, kept, dropped, recognisers)
		}
	}
}
