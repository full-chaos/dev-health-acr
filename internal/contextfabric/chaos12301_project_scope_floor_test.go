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

// Every floor that withholds the narrative keeps its swap on a project-only
// answer, each for its own reason, whichever side of the old position it ran on.
func TestAProjectScopeRowLeavesANarrativeWithholdingFloorItsOwnSwap(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func(*testing.T) InvestigationResult{
		"an unverified project narrowing": func(t *testing.T) InvestigationResult {
			return storedWithRows(t, unverifiedNarrowingDetail(), projectScopeRow())
		},
		"a denied cohort": func(t *testing.T) InvestigationResult {
			denied := detailFixture(contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, "context-fabric:graph", true, "cohort_denied:2", withCount(2))
			return storedWithRows(t, denied, projectScopeRow())
		},
		"a status filter over a cut read": func(t *testing.T) InvestigationResult {
			stored := storedWithRows(t, projectScopeRow())
			stored.Limitations = append(stored.Limitations, contractsv1.ContextFabricMemberStatusPoolCutLimitation("project", 10, 40))
			return stored
		},
		"an organization survey": func(t *testing.T) InvestigationResult {
			stored := storedWithRows(t, projectScopeRow())
			stored.Limitations = append(stored.Limitations, contractsv1.ContextFabricOrganizationSurveyNoMembersLimitation)
			return stored
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stored := build(t)
			if stored.Status != InvestigationComplete {
				t.Fatalf("fixture: stored status = %q", stored.Status)
			}
			served := ApplyServedStatusFloors(stored)
			if served.Status != InvestigationPartial || !slices.Contains(served.Limitations, synthesisNarrativeWithheldLimitation) {
				t.Fatalf("served %q, limitations %v, want the sibling floor's own withheld sentence", served.Status, served.Limitations)
			}
		})
	}
}

// Beside a cut read the answer states the grant, the cause the caller cannot
// clear, and keeps its narrative either way.
func TestAProjectScopeRowBesideACutReadKeepsTheNarrative(t *testing.T) {
	t.Parallel()
	served := ApplyServedStatusFloors(storedWithRows(t, projectScopeRow(), hopWalkCutCase().detail))
	if served.Status != InvestigationPartial || slices.Contains(served.Limitations, synthesisNarrativeWithheldLimitation) {
		t.Fatalf("served %q, limitations %v, want partial with the narrative kept", served.Status, served.Limitations)
	}
	if !slices.Contains(served.Limitations, contractsv1.ContextFabricProjectScopeWithheldLimitation) {
		t.Fatalf("limitations = %v, want the project-scope sentence", served.Limitations)
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
