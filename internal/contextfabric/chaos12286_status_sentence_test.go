package contextfabric

import (
	"context"
	"regexp"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12286: ACR's own lead prose never restates the answer status or the
// completeness state in its machine word (chris 2026-10-31: a person is never
// shown one). The status sentences say what is known instead; a complete
// answer has none.

// legacyStatusSentence is the sentence statusSentence wrote for status before
// CHAOS-12286, as stored answers carry it.
func legacyStatusSentence(status InvestigationStatus) string {
	switch status {
	case InvestigationComplete:
		return legacyModelPathStatusSentences[0]
	case InvestigationPartial:
		return legacyModelPathStatusSentences[1]
	case InvestigationDegraded:
		return legacyModelPathStatusSentences[2]
	}
	return ""
}

// acrRawStateWord matches an answer status or completeness state word.
var acrRawStateWord = regexp.MustCompile(`(?i)\b(complete|partial|degraded|not_derived|no_match|clarification_required)\b`)

func TestEachStatusSentenceStatesNoStatusWord(t *testing.T) {
	t.Parallel()
	want := map[InvestigationStatus]string{
		InvestigationComplete:              "",
		InvestigationPartial:               "Some canonical or graph coverage was unavailable.",
		InvestigationDegraded:              "Coverage was limited.",
		InvestigationClarificationRequired: "Clarification is required before this question can be answered.",
	}
	for status, text := range want {
		if got := statusSentence(status, SubjectResolution{}); got != text {
			t.Errorf("statusSentence(%q) = %q, want %q", status, got, text)
		}
	}
	resolutions := map[string]SubjectResolution{
		"none":          {},
		"committed":     {Committed: []SubjectRef{{Kind: SubjectProject, CanonicalID: "p", Label: "P"}}},
		"inactive":      {InactiveHints: []contractsv1.ContextFabricInactiveHint{{}}},
		"one candidate": {Candidates: []SubjectCandidate{{}}},
		"candidates":    {Candidates: []SubjectCandidate{{}, {}}},
	}
	for _, status := range []InvestigationStatus{InvestigationComplete, InvestigationPartial, InvestigationDegraded, InvestigationClarificationRequired, InvestigationNoMatch} {
		for name, resolution := range resolutions {
			if got := statusSentence(status, resolution); acrRawStateWord.MatchString(got) || strings.Contains(got, "This investigation is") {
				t.Errorf("statusSentence(%q, %s) = %q states a status word", status, name, got)
			}
		}
	}
}

// composeCurrentState's own clause for drivers whose source read was cut or is
// out of date names no completeness word.
func TestTheLimitedReadClauseStatesNoCompletenessWord(t *testing.T) {
	t.Parallel()
	open := int64(3)
	draft := SynthesisDraft{
		ClaimedFacts: []ClaimedFact{{ClaimID: "claim-1", Kind: FactStatus, Field: "open_items", Value: ScalarValue{Integer: &open}}},
		Drivers:      []DriverJudgment{{Category: "readiness", SourceState: SourceTruncated}},
	}
	got := composeCurrentState(draft)
	if !strings.Contains(got, " Limited read: readiness (") {
		t.Fatalf("current_state = %q, want the driver under \"Limited read:\"", got)
	}
	if strings.Contains(got, "Not complete:") {
		t.Fatalf("current_state = %q restates a completeness word", got)
	}
}

func poolCutAnswer(head, currentState string) InvestigationResult {
	result := validInvestigationResult()
	result.DirectJudgment, result.DeterministicAnswer, result.CurrentState = head, head, currentState
	result.Limitations = []string{contractsv1.ContextFabricMemberStatusPoolCutLimitation("project", 50, 120)}
	result.Completeness = ComputeAnswerCompleteness(result)
	return result
}

// A complete head has no status sentence, so a floor that lowers it states the
// served status where the composer would: after the count, before nothing.
// The count is kept; a head that was current_state's first sentence gives way.
func TestAFlooredCompleteHeadGainsTheServedStatusSentence(t *testing.T) {
	t.Parallel()
	const observed = "Current observed values: open items: 3."
	const partial = "Some canonical or graph coverage was unavailable."
	cases := map[string]struct{ head, want string }{
		"a count, composed now":             {"Counted 1 work item.", "Counted 1 work item. " + partial},
		"a count, stored before":            {"Counted 1 work item. This investigation is complete.", "Counted 1 work item. " + partial},
		"current_state's sentence":          {observed, partial},
		"the stored sentence alone, before": {"This investigation is complete.", partial},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ApplyServedStatusFloors(poolCutAnswer(tc.head, observed+" Release acceptance remains open."))
			if got.Status != InvestigationPartial {
				t.Fatalf("status = %q, want the pool-cut floor's partial", got.Status)
			}
			if got.DirectJudgment != tc.want || got.DeterministicAnswer != tc.want {
				t.Fatalf("lead prose = %q / %q, want %q", got.DirectJudgment, got.DeterministicAnswer, tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("served result invalid: %v", err)
			}
		})
	}
}

// Fresh and stored alike: a complete head with nothing else in it is
// current_state's first sentence, as composed; a head with other content
// keeps only that content.
func TestACompleteHeadWithNothingElseIsCurrentStatesFirstSentence(t *testing.T) {
	t.Parallel()
	const observed = "Current observed values: status: Complete."
	result := validInvestigationResult()
	result.CurrentState = observed + " Release acceptance remains open."
	for name, head := range map[string]string{"composed now": "", "stored before": "This investigation is complete."} {
		result.DirectJudgment, result.DeterministicAnswer = head, head
		got := restateServedStatusHead(result)
		if got.DirectJudgment != observed || got.DeterministicAnswer != observed {
			t.Fatalf("%s: lead prose = %q / %q, want %q", name, got.DirectJudgment, got.DeterministicAnswer, observed)
		}
	}
	// A reused answer gains its count in front of that sentence; the
	// sentence then gives way to it.
	result.DirectJudgment, result.DeterministicAnswer = "Counted 2 work items. "+observed, "Counted 2 work items. "+observed
	if got := restateServedStatusHead(result); got.DirectJudgment != "Counted 2 work items." {
		t.Fatalf("lead prose = %q, want the count alone", got.DirectJudgment)
	}
}

// A head no composer of this package wrote (a client-written answer) is not
// touched.
func TestAHeadWithoutAStatusSentenceIsLeftAlone(t *testing.T) {
	t.Parallel()
	result := validInvestigationResult()
	result.Status = InvestigationPartial
	result.DirectJudgment = contractsv1.ContextFabricClientSynthesisAnswer
	result.DeterministicAnswer = contractsv1.ContextFabricClientSynthesisAnswer
	result.CurrentState = contractsv1.ContextFabricClientSynthesisAnswer
	got := restateServedStatusHead(result)
	if got.DirectJudgment != contractsv1.ContextFabricClientSynthesisAnswer || got.DeterministicAnswer != contractsv1.ContextFabricClientSynthesisAnswer {
		t.Fatalf("lead prose = %q / %q, want the client synthesis text unchanged", got.DirectJudgment, got.DeterministicAnswer)
	}
}

// Through the engine's reuse path: a stored complete head with nothing to
// state in its place is served as stored and counted on the authority line;
// with a current_state to fall back to, it is not.
func TestAReusedHeadWithNothingInItsPlaceIsServedAsStoredAndCounted(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		currentState string
		wantHead     string
		wantCounted  bool
	}{
		"no current_state":   {"", legacyStatusSentence(InvestigationComplete), true},
		"with current_state": {"Current observed values: open items: 3.", "Current observed values: open items: 3.", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			project, candidate := servedHeadCandidate(InvestigationComplete)
			candidate.DirectJudgment = legacyStatusSentence(InvestigationComplete)
			candidate.DeterministicAnswer = legacyStatusSentence(InvestigationComplete)
			candidate.CurrentState = tc.currentState
			telemetry := &recordingTelemetry{}
			engine := completenessAuthorityTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
				Telemetry: telemetry,
			}, false, false)
			result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != InvestigationComplete || result.DirectJudgment != tc.wantHead || result.DeterministicAnswer != tc.wantHead {
				t.Fatalf("served %q with lead prose %q / %q, want complete with %q", result.Status, result.DirectJudgment, result.DeterministicAnswer, tc.wantHead)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("served result invalid: %v", err)
			}
			n := len(telemetry.completenessAuthorities)
			if n == 0 || telemetry.completenessAuthorities[n-1].LeadProseServedAsStored != tc.wantCounted {
				t.Fatalf("authority observations = %+v, want the last to count lead_prose_served_as_stored=%v", telemetry.completenessAuthorities, tc.wantCounted)
			}
		})
	}
}
