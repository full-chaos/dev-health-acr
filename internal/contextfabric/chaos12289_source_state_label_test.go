package contextfabric

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
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

// Review r1 P3: a driver titled with a cut driver's title plus the legacy
// words keeps its title; only the cut driver's own qualifier is restated.
func TestATitleThatIsACutTitlePlusTheLegacyWordsIsKept(t *testing.T) {
	t.Parallel()
	result := validInvestigationResult()
	result.Drivers = []DriverJudgment{
		{DriverID: "driver_1", Standing: DriverPrincipal, Category: "readiness", Title: "Release acceptance", SourceState: SourceTruncated},
		{DriverID: "driver_2", Standing: DriverPrincipal, Category: "health", Title: "Release acceptance" + legacyTruncatedQualifierForTest, SourceState: SourceAvailable},
	}
	stored := result
	stored.DirectJudgment = "Release acceptance" + legacyTruncatedQualifierForTest + ". Principal driver: Release acceptance" + legacyTruncatedQualifierForTest + "."
	stored.DeterministicAnswer = "Release acceptance" + legacyTruncatedQualifierForTest + ". Principal driver(s): Release acceptance" + legacyTruncatedQualifierForTest + "."
	want := result
	want.DirectJudgment = composeDirectJudgmentFrom(result.Status, result.Drivers, nil, result.SubjectResolution)
	want.DeterministicAnswer = composeDeterministicAnswerFrom(result.Status, result.Drivers, nil, nil, result.SubjectResolution)
	if want.DirectJudgment != "Release acceptance (truncated: the source read was cut). Principal driver: Release acceptance"+legacyTruncatedQualifierForTest+"." {
		t.Fatalf("fixture: the composer writes %q", want.DirectJudgment)
	}
	got := restateServedStatusHead(stored)
	if got.DirectJudgment != want.DirectJudgment || got.DeterministicAnswer != want.DeterministicAnswer {
		t.Fatalf("lead prose = %q / %q, want %q / %q", got.DirectJudgment, got.DeterministicAnswer, want.DirectJudgment, want.DeterministicAnswer)
	}
}

// Through the engine's reuse path: the authority observation says whether the
// served copy still carries the legacy words.
func TestAReusedAnswerCountsTheLegacySourceStateWordsItStillServes(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		currentState string
		want         bool
	}{
		"no legacy words":     {"Current observed values: open items: 3.", false},
		"kept provider value": {"Current observed values: note: readiness" + legacyTruncatedQualifierForTest + ". Then more.", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			project, candidate := servedHeadCandidate(InvestigationComplete)
			candidate.CurrentState = tc.currentState
			telemetry := &recordingTelemetry{}
			engine := completenessAuthorityTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
				Telemetry: telemetry,
			}, false, false)
			if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			n := len(telemetry.completenessAuthorities)
			if n == 0 || telemetry.completenessAuthorities[n-1].LegacySourceStateWordsServed != tc.want {
				t.Fatalf("authority observations = %+v, want legacy_source_state_words_served=%v", telemetry.completenessAuthorities, tc.want)
			}
		})
	}
}

// Each surface the Info flag reads.
func TestTheLegacyWordsFlagReadsEverySurface(t *testing.T) {
	t.Parallel()
	base := validInvestigationResult()
	base.Drivers = []DriverJudgment{{DriverID: "driver_1", Standing: DriverPrincipal, Category: "readiness", Title: "Readiness", SourceState: SourceAvailable}}
	if LegacySourceStateWordsServed(base) {
		t.Fatal("a result with no legacy words is flagged")
	}
	for name, set := range map[string]func(*InvestigationResult){
		"direct_judgment":      func(r *InvestigationResult) { r.DirectJudgment += legacyTruncatedQualifierForTest },
		"deterministic_answer": func(r *InvestigationResult) { r.DeterministicAnswer += legacyTruncatedQualifierForTest },
		"current_state":        func(r *InvestigationResult) { r.CurrentState += legacyTruncatedQualifierForTest },
		"summary": func(r *InvestigationResult) {
			r.Drivers[0].Summary = "Readiness" + legacyTruncatedQualifierForTest + "."
		},
		"note": func(r *InvestigationResult) { r.Drivers[0].SourceStateNote = legacyTruncatedNoteForTest },
	} {
		result := base
		result.Drivers = append([]DriverJudgment(nil), base.Drivers...)
		set(&result)
		if !LegacySourceStateWordsServed(result) {
			t.Errorf("%s: the legacy words are not flagged", name)
		}
	}
}

// A fragment is replaced only where it stands as whole sentences.
func TestAFragmentIsReplacedOnlyAtASentenceBoundary(t *testing.T) {
	t.Parallel()
	const from, to = "A (old).", "A (new)."
	for head, want := range map[string]string{
		"A (old).":                      "A (new).",
		"Counted 1 work item. A (old).": "Counted 1 work item. A (new).",
		"A (old). Principal driver: B.": "A (new). Principal driver: B.",
		"Is it? A (old).":               "Is it? A (new).",
		"Note: A (old).":                "Note: A (old).",
		"XA (old).":                     "XA (old).",
		"A (old).B":                     "A (old).B",
		"Title A (old). A (old).":       "Title A (old). A (new).",
	} {
		if got := replaceAtSentenceBoundary(head, from, to); got != want {
			t.Errorf("replaceAtSentenceBoundary(%q) = %q, want %q", head, got, want)
		}
	}
}
