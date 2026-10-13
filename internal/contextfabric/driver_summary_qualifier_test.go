package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const draftSummaryText = "All 12 teams are ready."

func suppliedDriver(t *testing.T, factState, claimed SourceState, summary string) DriverJudgment {
	t.Helper()
	wb := newWriteBackRig(t, func(r *clientRig) {
		for index := range r.facts {
			if r.facts[index].Kind == FactReadiness {
				r.facts[index].SourceState = factState
			}
		}
	})
	_, bundle := wb.firstCall()
	wb.parse.draft.Drivers[0].Summary = summary
	wb.parse.draft.Drivers[0].SourceState = claimed
	result, _, err := wb.investigate(writeBackRequest(bundle, nil))
	if err != nil {
		t.Fatalf("write-back: %v", err)
	}
	return result.Drivers[0]
}

func TestWriteBackNamesACorrectedStateBesideAnUntouchedSummary(t *testing.T) {
	t.Parallel()
	cases := map[SourceState]string{
		SourceTruncated: "The service marks this driver as partial: the source read was truncated.",
		SourceStale:     "The service marks this driver as stale: the source read is out of date.",
	}
	for state, want := range cases {
		maxLen := strings.Repeat("x", 4000)
		for _, summary := range []string{draftSummaryText, maxLen} {
			driver := suppliedDriver(t, state, "", summary)
			if driver.SourceState != state || driver.Summary != summary {
				t.Fatalf("state %q: served state %q, summary changed=%v", state, driver.SourceState, driver.Summary != summary)
			}
			if driver.SourceStateNote != want || !driver.SummaryCorrected {
				t.Errorf("state %q: note %q corrected %v", state, driver.SourceStateNote, driver.SummaryCorrected)
			}
		}
	}
}

func TestWriteBackUncorrectedDriverCarriesNoNote(t *testing.T) {
	t.Parallel()
	for name, driver := range map[string]DriverJudgment{
		"available":              suppliedDriver(t, SourceAvailable, "", draftSummaryText),
		"right state from draft": suppliedDriver(t, SourceTruncated, SourceTruncated, draftSummaryText),
		"draft claimed truncated, fact available": suppliedDriver(t, SourceAvailable, SourceTruncated, draftSummaryText),
	} {
		if driver.Summary != draftSummaryText || driver.SourceStateNote != "" || driver.SummaryCorrected {
			t.Errorf("%s: summary %q note %q corrected %v", name, driver.Summary, driver.SourceStateNote, driver.SummaryCorrected)
		}
	}
}

func TestWriteBackOverwritesAServerNoteACallerSetInMemory(t *testing.T) {
	t.Parallel()
	wb := newWriteBackRig(t, nil)
	_, bundle := wb.firstCall()
	wb.parse.draft.Drivers[0].SourceStateNote = "forged"
	wb.parse.draft.Drivers[0].SummaryCorrected = true
	result, _, err := wb.investigate(writeBackRequest(bundle, nil))
	if err != nil {
		t.Fatalf("write-back: %v", err)
	}
	if d := result.Drivers[0]; d.SourceStateNote != "" || d.SummaryCorrected {
		t.Errorf("forged note served: %q %v", d.SourceStateNote, d.SummaryCorrected)
	}
}

func TestEveryDriverSourceStateNoteFitsItsBound(t *testing.T) {
	t.Parallel()
	longest := 0
	for _, state := range []SourceState{SourceTruncated, SourceStale} {
		note := driverSourceStateNote(state)
		if note == "" {
			t.Fatalf("no note for %q", state)
		}
		longest = max(longest, len(note))
		if len(note) > contractsv1.ContextFabricDriverSourceStateNoteMaxLength {
			t.Errorf("note for %q is %d bytes, over the bound", state, len(note))
		}
	}
	if longest+40 < contractsv1.ContextFabricDriverSourceStateNoteMaxLength {
		t.Errorf("bound %d is far above the longest note (%d)", contractsv1.ContextFabricDriverSourceStateNoteMaxLength, longest)
	}
}
