package contextfabric

import (
	"strings"
	"testing"
)

// CHAOS-12289: the label for a driver whose source read was cut named ACR's
// own state word ("partial"). It now says what happened, and the served copy
// restates the words stored answers carry.

// legacyTruncatedQualifierForTest and legacyTruncatedNoteForTest are the words
// driverSourceStateCause gave a truncated source read before CHAOS-12289, as
// stored answers carry them.
const (
	legacyTruncatedQualifierForTest = " (partial: the source read was truncated)"
	legacyTruncatedNoteForTest      = "The service marks this driver as partial: the source read was truncated."
)

var everySourceState = []SourceState{SourceAvailable, SourceStale, SourceUnavailable, SourceUnconfigured, SourceUnauthorized, SourceNoData, SourceTruncated, SourceConflicted, SourceNotApplicable, SourcePruned}

func TestEverySourceStateLabelStatesNoStateWord(t *testing.T) {
	t.Parallel()
	for _, state := range everySourceState {
		for name, text := range map[string]string{
			"qualifier":            driverSourceStateQualifier(state),
			"note":                 driverSourceStateNote(state),
			"project status label": projectStatusStateLabel(state),
		} {
			if acrRawStateWord.MatchString(text) {
				t.Errorf("%s for %q = %q names a status or completeness word", name, state, text)
			}
		}
	}
	if got := driverSourceStateQualifier(SourceTruncated); got != " (truncated: the source read was cut)" {
		t.Errorf("truncated qualifier = %q", got)
	}
	if got := driverSourceStateNote(SourceTruncated); got != "The service marks this driver as truncated: the source read was cut." {
		t.Errorf("truncated note = %q", got)
	}
	if got := driverSourceStateQualifier(SourceStale); got != " (stale: the source read is out of date)" {
		t.Errorf("stale qualifier = %q, want it unchanged", got)
	}
	// The served copy's restatement never grows a field.
	if len(driverSourceStateQualifier(SourceTruncated)) > len(legacyTruncatedQualifierForTest) || len(driverSourceStateNote(SourceTruncated)) > len(legacyTruncatedNoteForTest) {
		t.Error("the current truncated words are longer than the legacy ones they replace")
	}
}

// labelledAnswer is a complete answer composed now for two principal drivers
// in state, with the project status summary and the server note on the first.
func labelledAnswer(state SourceState) InvestigationResult {
	return labelledAnswerTitled(state, "Release acceptance remains incomplete", "Health risk")
}

func labelledAnswerTitled(state SourceState, first, second string) InvestigationResult {
	open := int64(3)
	result := validInvestigationResult()
	result.Drivers = []DriverJudgment{
		{DriverID: "driver_1", Standing: DriverPrincipal, Category: "readiness", Title: first, SourceState: state},
		{DriverID: "driver_2", Standing: DriverPrincipal, Category: "health", Title: second, SourceState: state},
	}
	result.Drivers[0].Summary = result.Drivers[0].Title + projectStatusStateLabel(state)
	result.Drivers[0].SourceStateNote = driverSourceStateNote(state)
	result.DirectJudgment = composeDirectJudgmentFrom(result.Status, result.Drivers, nil, result.SubjectResolution)
	result.DeterministicAnswer = composeDeterministicAnswerFrom(result.Status, result.Drivers, nil, nil, result.SubjectResolution)
	result.CurrentState = composeCurrentState(SynthesisDraft{
		ClaimedFacts: []ClaimedFact{{ClaimID: "claim-1", Kind: FactStatus, Field: "open_items", Value: ScalarValue{Integer: &open}}},
		Drivers:      result.Drivers,
	})
	return result
}

// asStoredBefore writes a composed answer in the words the composers used
// before CHAOS-12289 (and, with notComplete, before CHAOS-12286's clause
// rename).
func asStoredBefore(result InvestigationResult, notComplete bool) InvestigationResult {
	old := func(text string) string {
		return strings.ReplaceAll(text, driverSourceStateQualifier(SourceTruncated), legacyTruncatedQualifierForTest)
	}
	result.Drivers = append([]DriverJudgment(nil), result.Drivers...)
	for index := range result.Drivers {
		result.Drivers[index].Summary = old(result.Drivers[index].Summary)
		if result.Drivers[index].SourceStateNote == driverSourceStateNote(SourceTruncated) {
			result.Drivers[index].SourceStateNote = legacyTruncatedNoteForTest
		}
	}
	result.DirectJudgment, result.DeterministicAnswer, result.CurrentState = old(result.DirectJudgment), old(result.DeterministicAnswer), old(result.CurrentState)
	if notComplete {
		result.CurrentState = strings.Replace(result.CurrentState, ". Limited read: ", ". Not complete: ", 1)
	}
	return result
}

// For every source state and every surface, the served copy of an answer
// stored in the old words is what the composers write now.
func TestALegacySourceStateLabelIsServedAsTheComposersWriteItNow(t *testing.T) {
	t.Parallel()
	for _, state := range everySourceState {
		for _, variant := range []struct {
			notComplete   bool
			first, second string
		}{
			{false, "Release acceptance remains incomplete", "Health risk"},
			{true, "Release acceptance remains incomplete", "Health risk"},
			// Titles that end in ".": the lead sentence trims it, the list keeps it.
			{false, "Release acceptance remains incomplete.", "Health risk."},
		} {
			notComplete := variant.notComplete
			current := labelledAnswerTitled(state, variant.first, variant.second)
			stored := asStoredBefore(current, notComplete)
			if state == SourceTruncated && stored.DirectJudgment == current.DirectJudgment {
				t.Fatalf("fixture: the stored answer for %q does not carry the legacy words", state)
			}
			got := restateServedStatusHead(stored)
			want := restateServedStatusHead(current)
			for name, pair := range map[string][2]string{
				"direct_judgment":      {got.DirectJudgment, want.DirectJudgment},
				"deterministic_answer": {got.DeterministicAnswer, want.DeterministicAnswer},
				"current_state":        {got.CurrentState, want.CurrentState},
				"summary":              {got.Drivers[0].Summary, want.Drivers[0].Summary},
				"source_state_note":    {got.Drivers[0].SourceStateNote, want.Drivers[0].SourceStateNote},
			} {
				if pair[0] != pair[1] {
					t.Errorf("%s, not complete %v: served %s = %q, want %q", state, notComplete, name, pair[0], pair[1])
				}
			}
			if stored.Drivers[0].SourceStateNote != asStoredBefore(current, notComplete).Drivers[0].SourceStateNote {
				t.Errorf("%s: the stored document's driver was rewritten", state)
			}
		}
	}
}

// A status floor reads a stored head in the current words before it
// recomposes, so a floored answer keeps its quick answer and drivers.
func TestAFlooredAnswerStoredWithTheLegacyLabelKeepsItsHead(t *testing.T) {
	t.Parallel()
	current := labelledAnswer(SourceTruncated)
	// A count leads the head, so composing afresh would lose it.
	current.DirectJudgment = "Counted 3 teams. " + current.DirectJudgment
	current.DeterministicAnswer = "Counted 3 teams. " + current.DeterministicAnswer
	current.Limitations = []string{poolCutAnswer("", "").Limitations[0]}
	current.Completeness = ComputeAnswerCompleteness(current)
	want := ApplyServedStatusFloors(current)
	got := ApplyServedStatusFloors(asStoredBefore(current, false))
	if got.Status != InvestigationPartial || got.DirectJudgment != want.DirectJudgment || got.DeterministicAnswer != want.DeterministicAnswer {
		t.Fatalf("floored legacy answer: %q with %q / %q, want %q / %q", got.Status, got.DirectJudgment, got.DeterministicAnswer, want.DirectJudgment, want.DeterministicAnswer)
	}
}
