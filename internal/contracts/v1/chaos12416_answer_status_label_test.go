package v1

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// CHAOS-12416. The served word of an answer status and the correction note, by
// the rule of CHAOS-12245: one function, so the pill and the note cannot
// disagree, and the raw completeness state is never shown.

func TestTheServedAnswerStatusFollowsTheRuling(t *testing.T) {
	const notDerived = ContextFabricAnswerCompletenessNotDerived
	for _, row := range []struct {
		status ContextFabricInvestigationStatus
		state  ContextFabricAnswerCompletenessState
		want   string
	}{
		{ContextFabricInvestigationComplete, ContextFabricAnswerCompletenessComplete, "Full answer"},
		{ContextFabricInvestigationComplete, notDerived, "Answer"},
		{ContextFabricInvestigationComplete, "", "Answer"},
		{ContextFabricInvestigationComplete, "a_future_state", "Answer"},
		{ContextFabricInvestigationComplete, ContextFabricAnswerCompletenessPartial, "Answer, not fully covered"},
		{ContextFabricInvestigationComplete, ContextFabricAnswerCompletenessDegraded, "Answer, not fully covered"},
	} {
		got, ok := ContextFabricAnswerStatusLabel(row.status, row.state)
		if !ok || got != row.want {
			t.Errorf("%s with completeness %q reads %q (%v), want %q", row.status, row.state, got, ok, row.want)
		}
	}
	// Every other status reads its registry word whatever the completeness:
	// only `complete` claims coverage.
	for status, want := range map[ContextFabricInvestigationStatus]string{
		ContextFabricInvestigationPartial:               "Partial answer",
		ContextFabricInvestigationDegraded:              "Degraded answer",
		ContextFabricInvestigationClarificationRequired: "Answer needs your confirmation",
		ContextFabricInvestigationNoMatch:               "No answer",
	} {
		for _, state := range ContextFabricAnswerCompletenessStateVocabulary() {
			got, ok := ContextFabricAnswerStatusLabel(status, state)
			if !ok || got != want {
				t.Errorf("%s with completeness %q reads %q (%v), want %q", status, state, got, ok, want)
			}
		}
	}
	if got, ok := ContextFabricAnswerStatusLabel("not_a_status", ContextFabricAnswerCompletenessComplete); ok {
		t.Errorf("an unknown status read %q: the consumer takes its generic path", got)
	}
}

// The three retired tables stay retired (CHAOS-12413 ruling 3): nothing shows
// the completeness state, the member data completeness or the answer impact.
func TestTheRetiredVocabulariesServeNothing(t *testing.T) {
	names := ContextFabricVocabularyNames()
	for _, name := range []ContextFabricVocabulary{"completeness_state", "member_data_completeness", "answer_impact"} {
		if slices.Contains(names[:], name) {
			t.Errorf("vocabulary %q is still published", name)
		}
		if _, ok := contextFabricVocabularyLabels[name]; ok {
			t.Errorf("vocabulary %q still has a table", name)
		}
	}
	if _, ok := ContextFabricVocabularyLabel(ContextFabricVocabularyRequirementOutcome, string(ContextFabricRequirementSatisfied)); !ok {
		t.Error("requirement_outcome lost its table: the missing-data sentence reads it")
	}
}

func TestTheCorrectionNoteIsTheWholeSentence(t *testing.T) {
	for name, row := range map[string]struct {
		direction   ContextFabricStatusCorrectionDirection
		requirement string
		outcome     ContextFabricPlanRequirementOutcome
		state       ContextFabricAnswerCompletenessState
		want        string
	}{
		"web's fixture": {
			ContextFabricStatusCorrectionCompleteToPartial, "health/subject/team", ContextFabricRequirementNarrowed, ContextFabricAnswerCompletenessNotDerived,
			"The service lowered this answer from Answer to Partial answer: health was read only in part.",
		},
		"the pill's word beside partial completeness": {
			ContextFabricStatusCorrectionCompleteToPartial, "state/subject/project", ContextFabricRequirementUnavailable, ContextFabricAnswerCompletenessPartial,
			"The service lowered this answer from Answer, not fully covered to Partial answer: status was not available.",
		},
		"to degraded": {
			ContextFabricStatusCorrectionCompleteToDegraded, "ranking/member/team", ContextFabricRequirementNotAttempted, ContextFabricAnswerCompletenessDegraded,
			"The service lowered this answer from Answer, not fully covered to Degraded answer: ranking was not read.",
		},
		"partial to degraded": {
			ContextFabricStatusCorrectionPartialToDegraded, "evidence/subject/team", ContextFabricRequirementNotApplicable, ContextFabricAnswerCompletenessDegraded,
			"The service lowered this answer from Partial answer to Degraded answer: evidence does not apply.",
		},
		"the only raise": {
			ContextFabricStatusCorrectionDegradedToPartial, "health/subject/team", ContextFabricRequirementSatisfied, ContextFabricAnswerCompletenessPartial,
			"The service raised this answer from Degraded answer to Partial answer: health was read in full.",
		},
		"no requirement names the clause": {
			ContextFabricStatusCorrectionCompleteToPartial, "", ContextFabricRequirementNarrowed, ContextFabricAnswerCompletenessNotDerived,
			"The service lowered this answer from Answer to Partial answer.",
		},
		"an unknown obligation": {
			ContextFabricStatusCorrectionCompleteToPartial, "a_new_obligation/subject/team", ContextFabricRequirementNarrowed, ContextFabricAnswerCompletenessNotDerived,
			"The service lowered this answer from Answer to Partial answer.",
		},
		"an unknown outcome": {
			ContextFabricStatusCorrectionCompleteToPartial, "health/subject/team", "a_new_outcome", ContextFabricAnswerCompletenessNotDerived,
			"The service lowered this answer from Answer to Partial answer.",
		},
		"no outcome": {
			ContextFabricStatusCorrectionCompleteToPartial, "health/subject/team", "", ContextFabricAnswerCompletenessNotDerived,
			"The service lowered this answer from Answer to Partial answer.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := ContextFabricStatusCorrectionNote(ContextFabricStatusCorrection{
				ReplacedStatus: ContextFabricInvestigationComplete, Direction: row.direction, DecidingRequirement: row.requirement, DecidingOutcome: row.outcome,
			}, ContextFabricInvestigationPartial, row.state)
			if !ok || got != row.want {
				t.Fatalf("note = %q (%v), want %q", got, ok, row.want)
			}
		})
	}
}

// A direction this build does not know reads between the correction's own two
// statuses, and a status with no word leaves no note: a raw token is never the
// fallback.
func TestACorrectionOutsideTheVocabularyNeverShowsARawToken(t *testing.T) {
	got, ok := ContextFabricStatusCorrectionNote(ContextFabricStatusCorrection{
		ReplacedStatus: ContextFabricInvestigationPartial, Direction: "partial_to_something",
	}, ContextFabricInvestigationDegraded, ContextFabricAnswerCompletenessDegraded)
	if want := "The service changed this answer from Partial answer to Degraded answer."; !ok || got != want {
		t.Errorf("unknown direction: note = %q (%v), want %q", got, ok, want)
	}
	for name, correction := range map[string]ContextFabricStatusCorrection{
		"replaced status": {ReplacedStatus: "not_a_status", Direction: "not_a_direction"},
		"served status":   {ReplacedStatus: ContextFabricInvestigationComplete, Direction: "not_a_direction"},
	} {
		served := ContextFabricInvestigationPartial
		if name == "served status" {
			served = "not_a_status"
		}
		if got, ok := ContextFabricStatusCorrectionNote(correction, served, ContextFabricAnswerCompletenessPartial); ok {
			t.Errorf("%s with no word: note = %q, want none", name, got)
		}
	}
}

var rawStatusWord = regexp.MustCompile(`\b(complete|partial|degraded|clarification_required|no_match|not_derived)\b`)

// Over every note the service can compose: no raw status word, the served key
// shape, and the label bound. A note over the bound would fail the whole
// document on a consumer's validator.
func TestNoCorrectionNoteHoldsARawStatusWordOrOverrunsItsBound(t *testing.T) {
	notes := 0
	for _, direction := range ContextFabricStatusCorrectionDirectionVocabulary() {
		for _, obligation := range ContextFabricAnswerObligationVocabulary() {
			for _, outcome := range ContextFabricPlanRequirementOutcomeVocabulary() {
				for _, state := range ContextFabricAnswerCompletenessStateVocabulary() {
					note, ok := ContextFabricStatusCorrectionNote(ContextFabricStatusCorrection{
						ReplacedStatus: ContextFabricInvestigationComplete, Direction: direction,
						DecidingRequirement: obligation + "/subject/team", DecidingOutcome: outcome,
					}, ContextFabricInvestigationPartial, state)
					if !ok {
						t.Fatalf("no note for %s %s %s %s", direction, obligation, outcome, state)
					}
					notes++
					if rawStatusWord.MatchString(note) {
						t.Errorf("note holds a raw status word: %q", note)
					}
					if err := validateVocabularyLabels(map[string]string{ContextFabricStatusCorrectionNoteKey: note}); err != nil {
						t.Errorf("note %q: %v", note, err)
					}
				}
			}
		}
	}
	if notes < 100 {
		t.Fatalf("composed only %d notes: this measured almost nothing", notes)
	}
}

// The note's word tables are total both ways over the vocabularies they read,
// and a span agrees with the direction it names.
func TestTheCorrectionNoteTablesAreTotal(t *testing.T) {
	obligations := ContextFabricAnswerObligationVocabulary()
	if len(contextFabricCorrectionObligationWords) != len(obligations) {
		t.Errorf("%d obligation words for %d obligations", len(contextFabricCorrectionObligationWords), len(obligations))
	}
	for _, obligation := range obligations {
		if strings.TrimSpace(contextFabricCorrectionObligationWords[obligation]) == "" {
			t.Errorf("obligation %q has no word", obligation)
		}
	}
	outcomes := ContextFabricPlanRequirementOutcomeVocabulary()
	if len(contextFabricCorrectionOutcomeWords) != len(outcomes) {
		t.Errorf("%d outcome words for %d outcomes", len(contextFabricCorrectionOutcomeWords), len(outcomes))
	}
	for _, outcome := range outcomes {
		if contextFabricCorrectionOutcomeWords[outcome] == "" {
			t.Errorf("outcome %q has no word", outcome)
		}
	}
	directions := ContextFabricStatusCorrectionDirectionVocabulary()
	if len(contextFabricStatusCorrectionSpans) != len(directions) {
		t.Errorf("%d spans for %d directions", len(contextFabricStatusCorrectionSpans), len(directions))
	}
	for _, direction := range directions {
		span, ok := contextFabricStatusCorrectionSpans[direction]
		if !ok {
			t.Errorf("direction %q has no span", direction)
			continue
		}
		if string(span.from)+"_to_"+string(span.to) != string(direction) {
			t.Errorf("direction %q moves %s to %s", direction, span.from, span.to)
		}
		wantVerb := "lowered"
		if direction == ContextFabricStatusCorrectionDegradedToPartial {
			wantVerb = "raised"
		}
		if span.verb != wantVerb {
			t.Errorf("direction %q reads %q, want %q: only degraded_to_partial raises a status", direction, span.verb, wantVerb)
		}
	}
}

// The note is one sentence for one result, so it has a key and no table.
func TestTheNoteKeyIsServedAndIsNotATable(t *testing.T) {
	if !vocabularyKeyShape.MatchString(ContextFabricStatusCorrectionNoteKey) {
		t.Errorf("key %q does not have the served shape", ContextFabricStatusCorrectionNoteKey)
	}
	vocabulary, _, _ := strings.Cut(ContextFabricStatusCorrectionNoteKey, ":")
	names := ContextFabricVocabularyNames()
	if slices.Contains(names[:], ContextFabricVocabulary(vocabulary)) {
		t.Errorf("%q is a vocabulary name, but the note is no table row", vocabulary)
	}
}

func TestTheStatusTierKeepsOnlyTheStatusWordsAndTheNote(t *testing.T) {
	got := ContextFabricStatusVocabularyLabels(map[string]string{
		"answer_status:partial": "Partial answer", "answer_status:complete": "Answer",
		ContextFabricStatusCorrectionNoteKey: "A sentence.", "subject_kind:team": "team",
		"requirement_outcome:narrowed": "narrowed", "answer_statuses:partial": "another vocabulary",
	})
	want := map[string]string{"answer_status:partial": "Partial answer", "answer_status:complete": "Answer", ContextFabricStatusCorrectionNoteKey: "A sentence."}
	if !maps.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	if got := ContextFabricStatusVocabularyLabels(map[string]string{"subject_kind:team": "team"}); got != nil {
		t.Fatalf("no status word kept %v, want nil", got)
	}
	if got := ContextFabricStatusVocabularyLabels(nil); got != nil {
		t.Fatalf("an absent map kept %v, want nil", got)
	}
}

// The shed line states one closed value, and only a tier that left words out.
func TestOnlyATierThatLeftWordsOutIsAShed(t *testing.T) {
	want := map[ContextFabricVocabularyLabelsTier]bool{
		ContextFabricVocabularyLabelsOff: false, ContextFabricVocabularyLabelsFull: false,
		ContextFabricVocabularyLabelsNoFactFields: true, ContextFabricVocabularyLabelsStatusOnly: true,
		ContextFabricVocabularyLabelsNone: true,
	}
	tiers := ContextFabricVocabularyLabelsTierVocabulary()
	if len(tiers) != len(want) {
		t.Fatalf("%d tiers, want %d", len(tiers), len(want))
	}
	seen := map[ContextFabricVocabularyLabelsTier]bool{}
	for _, tier := range tiers {
		seen[tier] = true
		shed, listed := want[tier]
		if !listed || tier.Shed() != shed {
			t.Errorf("tier %q: listed=%v Shed()=%v, want %v", tier, listed, tier.Shed(), shed)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("the vocabulary names %d distinct tiers, want %d: %v", len(seen), len(want), tiers)
	}
}
