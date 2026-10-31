package contextfabric

import (
	"context"
	"fmt"
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

// Removing a status sentence leaves no stray space where it stood.
func TestARemovedStatusSentenceLeavesNoStraySpace(t *testing.T) {
	t.Parallel()
	complete := legacyStatusSentence(InvestigationComplete)
	for head, want := range map[string]string{
		complete + " Principal driver: A.":            "Principal driver: A.",
		"Counted 1 work item. " + complete:            "Counted 1 work item.",
		"Lead. " + complete + " Principal driver: A.": "Lead. Principal driver: A.",
		complete: "",
	} {
		if got := restateHeadSentence(head, "", "", directJudgmentMaxLength); got != want {
			t.Errorf("restateHeadSentence(%q) = %q, want %q", head, got, want)
		}
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

// A complete head with no principal driver is a composition only when it is
// the server's own lead sentences; prose the server did not write is not.
func TestOnlyTheServersLeadSentencesMakeADriverlessHeadComposed(t *testing.T) {
	t.Parallel()
	for head, want := range map[string]bool{
		"":                                  true,
		"Counted 1 work item.":              true,
		"Counted at least 2000 work items.": true,
		"Counted 3 teams of 5 found.":       true,
		"Counted 1 work item. Total of merged prs over the period: 4, summed from 30 of 30 days, every day of the period having a stored row. The period is the 30 most recent completed UTC days, 2026-10-01 to 2026-10-30; today's partial day is not in the total.": true,
		"Partial total of merged prs over the period: 3, summed from the 28 of 30 days that have a stored row; no row is stored for 2026-10-04, 2026-10-05, and those days are not counted as zero, so this is not the total of the period.":                           true,
		"The stored deploy rows for the period reached the limit of 400 rows one read returns and extend beyond the period, so some days of the period may be missing and no total is stated.":                                                                         true,
		"No stored deploy row exists for any day of the period, so no total is stated.":                       true,
		"Some stored deploy rows were withheld from this caller, so no total is stated.":                      true,
		"The period is longer than the 400 daily rows one read returns, so no deploy total is stated for it.": true,
		"Ask Dev is on track.":                      false,
		"Counted 1 work item. Ask Dev is on track.": false,
		"Ask Dev is on track. Counted 1 work item.": false,
	} {
		if got := serverLeadSentencesOnly(head); got != want {
			t.Errorf("serverLeadSentencesOnly(%q) = %v, want %v", head, got, want)
		}
	}
}

// A driverless complete head the server did not write: a floor withholds it
// (composes afresh, as its limitation says), and a served copy at a lower
// status without a floor leaves it alone.
func TestADriverlessHeadTheServerDidNotWriteIsNotRecomposed(t *testing.T) {
	t.Parallel()
	const prose = "Ask Dev is on track."
	floored := ApplyServedStatusFloors(poolCutAnswer(prose, "Current observed values: open items: 3."))
	if want := "Some canonical or graph coverage was unavailable."; floored.Status != InvestigationPartial || floored.DirectJudgment != want || floored.DeterministicAnswer != want {
		t.Fatalf("floored: %q with %q / %q, want partial with %q", floored.Status, floored.DirectJudgment, floored.DeterministicAnswer, want)
	}
	served := validInvestigationResult()
	served.Status = InvestigationPartial
	served.DirectJudgment, served.DeterministicAnswer = prose, prose
	if got := restateServedStatusHead(served); got.DirectJudgment != prose || got.DeterministicAnswer != prose {
		t.Fatalf("served lower: lead prose = %q / %q, want %q left alone", got.DirectJudgment, got.DeterministicAnswer, prose)
	}
}

// The pattern table recognizes every lead sentence the server composes, read
// from the composers themselves: a changed or new composer form fails here,
// instead of making a driverless complete head read as not composed (a floor
// would then compose it afresh and drop its count). A new absence reason
// constant must be added to the list below; an unknown reason takes the
// composer's default branch, which the list covers.
func TestThePatternTableRecognizesEveryServerLeadSentence(t *testing.T) {
	t.Parallel()
	var sentences []string
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		for _, served := range []int{0, 1, 3} {
			for _, cardinality := range []MembershipCardinality{
				{Resolved: true, Kind: kind, Served: served, Declared: served},
				{Resolved: true, Kind: kind, Served: served, Declared: served + 5},
				{Resolved: true, Kind: kind, Served: served, Declared: served, PopulationIncomplete: true},
				{Resolved: true, Kind: kind, Served: served, Declared: served + 5, PopulationIncomplete: true},
			} {
				sentences = append(sentences, cardinalityAnswerSentence(cardinality))
			}
		}
	}
	period := make([]string, 0, 30)
	for day := 1; day <= 30; day++ {
		period = append(period, fmt.Sprintf("2026-10-%02d", day))
	}
	totals := []PeriodTotal{
		{Column: "merged_prs", Sum: 4, DaysWithRow: 30, DaysInPeriod: 30, Certified: true},
		{Column: "merged_prs", Sum: 3, DaysWithRow: 28, DaysInPeriod: 30, MissingDays: period[3:5]},
		{Column: "merged_prs", Sum: 1, DaysWithRow: 10, DaysInPeriod: 30, MissingDays: period[10:]},
	}
	var absences []PeriodTotalAbsence
	for _, reason := range []string{PeriodTotalAbsenceNoRows, PeriodTotalAbsenceTruncated, PeriodTotalAbsenceTooLong, PeriodTotalAbsenceWithheld, PeriodTotalAbsenceInconsistent, "a_reason_no_branch_names"} {
		absences = append(absences, PeriodTotalAbsence{Table: "daily_deploys", Reason: reason})
	}
	sentences = append(sentences, PeriodTotalSentences(totals, absences)...)
	for id := range relativeWindowDurations {
		sentences = append(sentences, PeriodScopeSentence(&contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: id}, period))
	}
	sentences = append(sentences, PeriodScopeSentence(nil, period), PeriodScopeSentence(&contractsv1.ContextFabricEffectiveEvidenceWindow{}, period))
	for _, sentence := range sentences {
		if sentence == "" {
			t.Fatal("a composer returned no sentence for an input built to compose one")
		}
		if !serverLeadSentencesOnly(sentence) {
			t.Errorf("the server composes %q, and no pattern recognizes it", sentence)
		}
	}
	// Joined as the assembly joins them, they are still the server's own.
	if joined := strings.Join(sentences, " "); !serverLeadSentencesOnly(joined) {
		t.Error("the composed lead sentences, joined, are not recognized")
	}
	// current_state's fallback, as composeCurrentState writes it, is read as
	// an empty head, alone or after the count.
	open := int64(3)
	for _, draft := range []SynthesisDraft{{}, {ClaimedFacts: []ClaimedFact{{ClaimID: "claim-1", Kind: FactStatus, Field: "open_items", Value: ScalarValue{Integer: &open}}}}} {
		fallback := leadProseFallback(composeCurrentState(draft))
		if fallback == "" || withoutLeadProseFallback(fallback, fallback) != "" {
			t.Errorf("the fallback %q is not read as an empty head", fallback)
		}
		if head := withoutLeadProseFallback(sentences[0]+" "+fallback, fallback); head != sentences[0] || !serverLeadSentencesOnly(head) {
			t.Errorf("a count before the fallback %q leaves %q, want the count alone", fallback, head)
		}
	}
}
