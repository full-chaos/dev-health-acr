package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func servedHeadCandidate(modelStatus InvestigationStatus) (SubjectRef, InvestigationResult) {
	project, candidate := outcomeAuthorityCandidate(modelStatus, contractsv1.ContextFabricAnswerCompletenessDegraded)
	candidate.DirectJudgment = composeDirectJudgmentFrom(modelStatus, candidate.Drivers, candidate.Cohort, candidate.SubjectResolution)
	candidate.DeterministicAnswer = composeDeterministicAnswerFrom(modelStatus, candidate.Drivers, candidate.ClaimedFacts, candidate.Cohort, candidate.SubjectResolution)
	return project, candidate
}

func TestServedAnswerHeadStatesServedStatusThroughTheEngine(t *testing.T) {
	t.Parallel()
	degradedHead := statusSentence(InvestigationDegraded, SubjectResolution{})
	for _, modelStatus := range []InvestigationStatus{InvestigationComplete, InvestigationPartial} {
		modelStatus := modelStatus
		t.Run(string(modelStatus), func(t *testing.T) {
			t.Parallel()
			project, candidate := servedHeadCandidate(modelStatus)
			if !strings.HasPrefix(candidate.DirectJudgment, statusSentence(modelStatus, SubjectResolution{})) {
				t.Fatalf("premise: stored head = %q, want it to open with the %s sentence", candidate.DirectJudgment, modelStatus)
			}
			engine := completenessAuthorityTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			}, true, true)
			result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != InvestigationDegraded {
				t.Fatalf("premise: served status = %q, want degraded", result.Status)
			}
			if !strings.HasPrefix(result.DirectJudgment, degradedHead) {
				t.Fatalf("DirectJudgment = %q, want it to state the served status: %q", result.DirectJudgment, degradedHead)
			}
			if !strings.HasPrefix(result.DeterministicAnswer, degradedHead) {
				t.Fatalf("DeterministicAnswer = %q, want it to state the served status: %q", result.DeterministicAnswer, degradedHead)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("restated result must validate: %v", err)
			}
		})
	}
}

func TestRestateServedStatusHeadEveryPair(t *testing.T) {
	t.Parallel()
	statuses := []InvestigationStatus{InvestigationComplete, InvestigationPartial, InvestigationDegraded}
	for _, draft := range statuses {
		for _, served := range statuses {
			draft, served := draft, served
			t.Run(string(draft)+"_to_"+string(served), func(t *testing.T) {
				t.Parallel()
				_, result := servedHeadCandidate(draft)
				result.Status = served
				drivers := result.Drivers
				got := restateServedStatusHead(result)
				// A complete head with nothing else in it is current_state's
				// first sentence (CHAOS-12286).
				orFallback := func(head string) string {
					if head == "" {
						return leadProseFallback(result.CurrentState)
					}
					return head
				}
				if want := orFallback(composeDirectJudgmentFrom(served, drivers, result.Cohort, result.SubjectResolution)); got.DirectJudgment != want {
					t.Fatalf("DirectJudgment = %q, want %q", got.DirectJudgment, want)
				}
				if want := orFallback(composeDeterministicAnswerFrom(served, drivers, result.ClaimedFacts, result.Cohort, result.SubjectResolution)); got.DeterministicAnswer != want {
					t.Fatalf("DeterministicAnswer = %q, want %q", got.DeterministicAnswer, want)
				}
			})
		}
	}
}

func TestRestateServedStatusHeadLeavesOtherTerminalsAlone(t *testing.T) {
	t.Parallel()
	for _, status := range []InvestigationStatus{InvestigationNoMatch, InvestigationClarificationRequired} {
		_, result := servedHeadCandidate(InvestigationComplete)
		result.Status = status
		got := restateServedStatusHead(result)
		if got.DirectJudgment != result.DirectJudgment || got.DeterministicAnswer != result.DeterministicAnswer {
			t.Fatalf("status %s: head rewritten to %q", status, got.DirectJudgment)
		}
	}
}

func TestRestateServedStatusHeadFindsTheFirstStatusSentenceAtASentenceBoundary(t *testing.T) {
	t.Parallel()
	complete := legacyStatusSentence(InvestigationComplete)
	partial := statusSentence(InvestigationPartial, SubjectResolution{})
	degraded := statusSentence(InvestigationDegraded, SubjectResolution{})
	for _, row := range []struct {
		name, head, want string
	}{
		{"opening sentence is replaced, the rest kept", complete + " Principal driver: A.", partial + " Principal driver: A."},
		{"no recognisable status sentence is left alone", "Available work items.", "Available work items."},
		{"empty head is left alone", "", ""},
		{"a status sentence after the quick answer is replaced", "Open items: 3 (2026-10-30). " + complete + " Principal driver: A.", "Open items: 3 (2026-10-30). " + partial + " Principal driver: A."},
		{"a status sentence after a count and a quick answer is replaced", "Counted 3 teams. Fullchaos: health risk. " + complete, "Counted 3 teams. Fullchaos: health risk. " + partial},
		{"a status sentence inside a sentence is left alone", "Note: " + complete, "Note: " + complete},
		{"a status sentence after a question mark is replaced", "Is it ready? " + complete, "Is it ready? " + partial},
		{"a status sentence after an exclamation mark is replaced", "Shipped! " + complete, "Shipped! " + partial},
		{"two status sentences: only the opening one is replaced", complete + " " + degraded, partial + " " + degraded},
		{"already the served sentence is unchanged", partial + " Principal driver: A.", partial + " Principal driver: A."},
	} {
		got := restateHeadSentence(row.head, partial, "", directJudgmentMaxLength)
		if got != row.want {
			t.Errorf("%s: got %q, want %q", row.name, got, row.want)
		}
	}
}

func TestServedStatusHeadKeepsTheAnswerInsideTheBudgetItIsMeasuredAgainst(t *testing.T) {
	t.Parallel()
	partial := statusSentence(InvestigationPartial, SubjectResolution{})
	run := func(maxBytes int64) (InvestigationResult, error) {
		calls := 0
		shape := chaos6558ProdShape
		shape.maxBytes = maxBytes
		shape.composedHead = true
		engine := chaos6558Engine(t, &calls, &recordingTelemetry{}, shape)
		return engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	}
	served, err := run(chaos6558MaxBytes)
	if err != nil {
		t.Fatalf("premise: Investigate() error = %v", err)
	}
	if served.Status != InvestigationPartial || !strings.HasPrefix(served.DirectJudgment, partial) {
		t.Fatalf("premise: status %q head %q, want the lever's partial answer stating partial (draft was complete, a shorter head)", served.Status, served.DirectJudgment)
	}
	measured, err := contractsv1.MeasureContextFabricResponse(served)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	for ceiling := measured.Bytes; ceiling > measured.Bytes-400; ceiling-- {
		result, err := run(ceiling)
		if err != nil {
			t.Fatalf("ceiling %d: refused (%v); a one-row-per-table cut fits far below it, so the lever must measure the answer with its served-status head and serve", ceiling, err)
		}
		got, err := contractsv1.MeasureContextFabricResponse(result)
		if err != nil {
			t.Fatalf("ceiling %d: measure: %v", ceiling, err)
		}
		if got.Bytes > ceiling {
			t.Fatalf("ceiling %d: served %d bytes", ceiling, got.Bytes)
		}
		if !strings.HasPrefix(result.DirectJudgment, statusSentence(result.Status, SubjectResolution{})) {
			t.Fatalf("ceiling %d: head %q does not state served status %q", ceiling, result.DirectJudgment, result.Status)
		}
	}
}

func TestRestateServedStatusHeadKeepsTheCountSentenceAtTheLengthBound(t *testing.T) {
	t.Parallel()
	count := cardinalityAnswerSentence(MembershipCardinality{Resolved: true, Kind: SubjectTeam, Served: 3, Declared: 3})
	if count == "" {
		t.Fatal("premise: no count sentence")
	}
	for _, row := range []struct {
		name  string
		maxes int
		build func(status InvestigationStatus, filler string) string
	}{
		{"deterministic answer stored with the count last", deterministicAnswerMaxLength, func(status InvestigationStatus, filler string) string {
			return storedHeadEndingWithCount(legacyStatusSentence(status)+" Principal driver(s): "+filler+".", count, deterministicAnswerMaxLength)
		}},
	} {
		for fill := row.maxes - 140; fill <= row.maxes; fill += 7 {
			stale := row.build(InvestigationComplete, strings.Repeat("a", fill-100)+"; "+strings.Repeat("b", 60))
			if len([]rune(stale)) > row.maxes {
				t.Fatalf("%s: premise: stale head is %d runes, over %d", row.name, len([]rune(stale)), row.maxes)
			}
			got := restateHeadSentence(stale, statusSentence(InvestigationPartial, SubjectResolution{}), "", row.maxes)
			if len([]rune(got)) > row.maxes {
				t.Fatalf("%s fill %d: restated head is %d runes, over %d", row.name, fill, len([]rune(got)), row.maxes)
			}
			if !strings.HasPrefix(got, statusSentence(InvestigationPartial, SubjectResolution{})) || !strings.HasSuffix(got, count) {
				t.Fatalf("%s fill %d: restated head lost the served sentence or the count sentence: %q ... %q", row.name, fill, got[:60], got[len(got)-80:])
			}
		}
	}
}

// storedHeadEndingWithCount builds a head the way the composer did before the
// count led it: the count sentence last, the earlier prose truncated to fit.
// Stored documents carry this layout and are served again.
func storedHeadEndingWithCount(answer, sentence string, maxRunes int) string {
	joined := strings.TrimSpace(answer + " " + sentence)
	if len([]rune(joined)) <= maxRunes {
		return joined
	}
	return strings.TrimSpace(truncateAtSentenceBoundary(answer, maxRunes-len([]rune(sentence))-1) + " " + sentence)
}

// TestRestateServedStatusHeadKeepsALeadingCountSentenceAtTheLengthBound: a head
// that leads with the count and the quick answer keeps both when the served
// status sentence replaces the stale one, at every length up to the bound.
func TestRestateServedStatusHeadKeepsALeadingCountSentenceAtTheLengthBound(t *testing.T) {
	t.Parallel()
	count := cardinalityAnswerSentence(MembershipCardinality{Resolved: true, Kind: SubjectTeam, Served: 3, Declared: 3})
	lead := "Fullchaos: health risk."
	partial := statusSentence(InvestigationPartial, SubjectResolution{})
	for _, maxes := range []int{directJudgmentMaxLength, deterministicAnswerMaxLength} {
		for fill := maxes - 140; fill <= maxes; fill += 7 {
			body := lead + " " + legacyStatusSentence(InvestigationComplete) + " Principal driver(s): " + strings.Repeat("a", fill-160) + "; " + strings.Repeat("b", 60) + "."
			stale := leadWithCountSentence(body, count, maxes)
			got := restateHeadSentence(stale, partial, "", maxes)
			if len([]rune(got)) > maxes {
				t.Fatalf("max %d fill %d: restated head is %d runes", maxes, fill, len([]rune(got)))
			}
			if !strings.HasPrefix(got, count+" "+lead+" "+partial) {
				t.Fatalf("max %d fill %d: restated head lost the count, the quick answer or the served sentence: %q", maxes, fill, got[:120])
			}
		}
	}
}

// TestRestateServedStatusHeadStepsOverAQuickAnswerThatReadsLikeAStatus: the
// restate replaces the composer's status sentence, never the quick answer
// that leads the head, even when the driver's title reads like a status.
func TestRestateServedStatusHeadStepsOverAQuickAnswerThatReadsLikeAStatus(t *testing.T) {
	t.Parallel()
	complete := legacyStatusSentence(InvestigationComplete)
	partial := statusSentence(InvestigationPartial, SubjectResolution{})
	result := InvestigationResult{Status: InvestigationComplete, Drivers: []DriverJudgment{{DriverID: "driver_mimic", Standing: DriverPrincipal, Title: complete}}}
	result.DirectJudgment = composeDirectJudgmentFrom(result.Status, result.Drivers, nil, result.SubjectResolution)
	result.DeterministicAnswer = composeDeterministicAnswerFrom(result.Status, result.Drivers, nil, nil, result.SubjectResolution)
	result.Status = InvestigationPartial
	served := restateServedStatusHead(result)
	for name, text := range map[string]string{"direct_judgment": served.DirectJudgment, "deterministic_answer": served.DeterministicAnswer} {
		if want := complete + " " + partial; text != want {
			t.Errorf("%s = %q, want %q (the quick answer kept, the status sentence after it restated)", name, text, want)
		}
	}
}
