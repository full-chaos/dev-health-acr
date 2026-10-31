package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12289: the label for a driver whose source read was cut named ACR's
// own state word ("(partial: the source read was truncated)"). Answers stored
// with it are served in the plain words on every read; the stored document is
// not rewritten, and a provider's text with the same words is evidence and is
// kept.

const (
	legacyTruncatedQualifier = " (partial: the source read was truncated)"
	legacyTruncatedNote      = "The service marks this driver as partial: the source read was truncated."
	truncatedQualifier       = " (truncated: the source read was cut)"
	truncatedNote            = "The service marks this driver as truncated: the source read was cut."
)

// storedTruncatedDriverAnswer is the contract example result: a complete
// answer whose rank-1 principal driver ("Release acceptance remains
// incomplete", category readiness) had its source read cut.
func storedTruncatedDriverAnswer(t *testing.T) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/examples/v1/context_fabric_investigation_result.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Drivers[0].SourceState != contractsv1.ContextFabricSourceTruncated || result.Drivers[0].Title != "Release acceptance remains incomplete" {
		t.Fatalf("fixture: the example's first driver is %+v, want the truncated readiness driver", result.Drivers[0])
	}
	result.ResultID = "result_source_state_label01"
	return result
}

func TestByIDAStoredLegacySourceStateLabelIsServedInPlainWords(t *testing.T) {
	stored := storedTruncatedDriverAnswer(t)
	const title = "Release acceptance remains incomplete"
	stored.DirectJudgment = title + legacyTruncatedQualifier + ". Principal driver: CHAOS: operational deficiencies."
	stored.DeterministicAnswer = title + legacyTruncatedQualifier + ". Principal driver(s): CHAOS: operational deficiencies."
	stored.CurrentState = "Current observed values: open items: 3. Limited read: health (stale: the source read is out of date); readiness" + legacyTruncatedQualifier + "."
	stored.Drivers[0].SourceStateNote = legacyTruncatedNote
	stored.Drivers[0].Summary = title + legacyTruncatedQualifier + "."
	stored.Completeness = contextfabric.ComputeAnswerCompleteness(stored)

	got, logs := readLeadProseByID(t, stored)
	if !strings.Contains(logs, `"legacy_source_state_words_served":false`) {
		t.Errorf("the completeness authority line must state legacy_source_state_words_served=false: %s", logs)
	}
	if want := title + truncatedQualifier + ". Principal driver: CHAOS: operational deficiencies."; got.DirectJudgment != want {
		t.Errorf("direct_judgment = %q, want %q", got.DirectJudgment, want)
	}
	if want := title + truncatedQualifier + ". Principal driver(s): CHAOS: operational deficiencies."; got.DeterministicAnswer != want {
		t.Errorf("deterministic_answer = %q, want %q", got.DeterministicAnswer, want)
	}
	if want := "Current observed values: open items: 3. Limited read: health (stale: the source read is out of date); readiness" + truncatedQualifier + "."; got.CurrentState != want {
		t.Errorf("current_state = %q, want %q", got.CurrentState, want)
	}
	if got.Drivers[0].SourceStateNote != truncatedNote {
		t.Errorf("source_state_note = %q, want %q", got.Drivers[0].SourceStateNote, truncatedNote)
	}
	if want := title + truncatedQualifier + "."; got.Drivers[0].Summary != want {
		t.Errorf("summary = %q, want %q", got.Drivers[0].Summary, want)
	}
	for field, text := range map[string]string{"direct_judgment": got.DirectJudgment, "deterministic_answer": got.DeterministicAnswer, "current_state": got.CurrentState, "source_state_note": got.Drivers[0].SourceStateNote, "summary": got.Drivers[0].Summary} {
		if strings.Contains(text, "partial") {
			t.Errorf("served %s = %q still names ACR's state word", field, text)
		}
	}
}

// The same words beside a driver whose read was not cut, or in a summary that
// is not the project status summary, stay as stored, while the cut driver's
// own qualifier beside them is restated.
func TestByIDOnlyTheCutDriversOwnWordsAreRestated(t *testing.T) {
	stored := storedTruncatedDriverAnswer(t)
	const title = "Release acceptance remains incomplete"
	other := " Principal driver: CHAOS: operational deficiencies" + legacyTruncatedQualifier + "."
	stored.DirectJudgment = title + legacyTruncatedQualifier + "." + other
	stored.Drivers[0].Summary = "The readiness feed reported" + legacyTruncatedQualifier + " twice."
	stored.Completeness = contextfabric.ComputeAnswerCompleteness(stored)

	got, _ := readLeadProseByID(t, stored)
	if want := title + truncatedQualifier + "." + other; got.DirectJudgment != want {
		t.Errorf("direct_judgment = %q, want %q", got.DirectJudgment, want)
	}
	if got.Drivers[0].Summary != stored.Drivers[0].Summary {
		t.Errorf("summary = %q, want as stored %q", got.Drivers[0].Summary, stored.Drivers[0].Summary)
	}
}

// The same words in text ACR did not write about this driver stay as stored.
func TestByIDTheSourceStateWordsInOtherTextAreKept(t *testing.T) {
	stored := storedTruncatedDriverAnswer(t)
	// The second principal driver's source read was not cut: a title that
	// carries the words is the title.
	stored.Drivers[1].Title = "CHAOS: operational deficiencies" + legacyTruncatedQualifier
	stored.DirectJudgment = "Release acceptance remains incomplete. Principal driver: CHAOS: operational deficiencies" + legacyTruncatedQualifier + "."
	stored.Drivers[0].SourceState = contractsv1.ContextFabricSourceAvailable
	stored.Drivers[0].SourceStateNote = ""
	stored.Drivers[0].SummaryCorrected = false
	// A provider value in current_state, not ACR's closing clause.
	stored.CurrentState = "Current observed values: note: readiness" + legacyTruncatedQualifier + ". Then more."
	stored.Drivers[0].Summary = "Release acceptance remains incomplete; the readiness feed reported" + legacyTruncatedQualifier + "."
	stored.Completeness = contextfabric.ComputeAnswerCompleteness(stored)

	got, logs := readLeadProseByID(t, stored)
	// The words are kept as evidence, and the Info line says they were served.
	if !strings.Contains(logs, `"legacy_source_state_words_served":true`) {
		t.Errorf("the completeness authority line must state legacy_source_state_words_served=true: %s", logs)
	}
	if got.DirectJudgment != stored.DirectJudgment {
		t.Errorf("direct_judgment = %q, want as stored %q", got.DirectJudgment, stored.DirectJudgment)
	}
	if got.CurrentState != stored.CurrentState {
		t.Errorf("current_state = %q, want as stored %q", got.CurrentState, stored.CurrentState)
	}
	if got.Drivers[0].Summary != stored.Drivers[0].Summary || got.Drivers[1].Title != stored.Drivers[1].Title {
		t.Errorf("driver text = %q / %q, want as stored", got.Drivers[0].Summary, got.Drivers[1].Title)
	}
}
