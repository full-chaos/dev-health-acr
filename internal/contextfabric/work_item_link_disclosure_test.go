package contextfabric

import (
	"context"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A failed link read is stated on an answer too, whichever adapter call it failed
// in: a committed subject does not mean nothing was left out. The answer is partial.
func TestAFailedLinkReadIsStatedOnACommittedAnswer(t *testing.T) {
	t.Parallel()
	for name, fail := range map[string]func(*SubjectResolution, *GraphContext){
		"in the resolution": func(resolution *SubjectResolution, _ *GraphContext) {
			resolution.WorkItemLinkLimitation = contractsv1.ContextFabricWorkItemLinksUnreadLimitation
		},
		"in the discovery": func(_ *SubjectResolution, discovered *GraphContext) { discovered.WorkItemLinksUnread = true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			engine, request := engineForDegradationEdited(t, false, []string{}, TimeContext{Axis: TemporalCurrent}, fail)
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if !slices.Contains(result.Limitations, contractsv1.ContextFabricWorkItemLinksUnreadLimitation) {
				t.Fatalf("limitations %q, want the unread sentence", result.Limitations)
			}
			if !result.Coverage.Partial {
				t.Fatal("an answer that may leave a work item out must mark its coverage partial")
			}
		})
	}
}

// The other sentence is about a turn that resolved no work item. A committed
// answer does not carry it, and a read that worked adds nothing.
func TestACommittedAnswerCarriesNoOtherLinkSentence(t *testing.T) {
	t.Parallel()
	for name, sentence := range map[string]string{"no link": contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation, "reads worked": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			engine, request := engineForDegradationEdited(t, false, []string{}, TimeContext{Axis: TemporalCurrent}, func(resolution *SubjectResolution, _ *GraphContext) {
				resolution.WorkItemLinkLimitation = sentence
			})
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			for _, limitation := range result.Limitations {
				if contractsv1.IsContextFabricWorkItemLinkLimitation(limitation) {
					t.Fatalf("limitations %q", result.Limitations)
				}
			}
			if result.Coverage.Partial {
				t.Fatal("coverage partial with no failed read")
			}
		})
	}
}

// CHAOS-12456: a work item a restricted caller could not reach stays unresolved,
// and the terminal says so. It never reads as "does not exist".
func TestAnUnresolvedWorkItemIsStatedOnTheTerminal(t *testing.T) {
	t.Parallel()
	for _, sentence := range []string{contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation, contractsv1.ContextFabricWorkItemLinksUnreadLimitation} {
		engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, WorkItemLinkLimitation: sentence}}, nil)
		result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, validInvestigationRequest())
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if result.Status != InvestigationNoMatch || !slices.Contains(result.Limitations, sentence) {
			t.Fatalf("status %q limitations %q, want %q stated", result.Status, result.Limitations, sentence)
		}
		for _, limitation := range result.Limitations {
			text := strings.ToLower(limitation)
			if strings.Contains(text, "does not exist") && !strings.Contains(text, "does not show that") {
				t.Fatalf("limitation %q reads as an absence", limitation)
			}
		}
	}
}

func TestNoLimitationIsStatedForAResolvedTurnsEmptyField(t *testing.T) {
	t.Parallel()
	engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}}, nil)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricWorkItemLinkLimitation(limitation) {
			t.Fatalf("limitation %q with nothing to state", limitation)
		}
	}
}

// A model's copy of the sentence is not a disclosure the engine made.
func TestAWorkItemLinkSentenceIsServiceAuthored(t *testing.T) {
	t.Parallel()
	for _, sentence := range []string{contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation, contractsv1.ContextFabricWorkItemLinksUnreadLimitation} {
		if !contractsv1.IsContextFabricWorkItemLinkLimitation(sentence) {
			t.Fatalf("%q is not recognised", sentence)
		}
		if got := serviceLimitationRecogniserID(sentence); got != "work_item_link" {
			t.Fatalf("recogniser = %q", got)
		}
		if kept, dropped, _ := stripServiceAuthoredLookalikes([]string{sentence}); dropped != 1 || len(kept) != 0 {
			t.Fatalf("a draft copy was kept: %q", kept)
		}
	}
}
