package contextfabric

import (
	"slices"
	"testing"
)

func readsStep(kinds ...FactKind) ReadPlanStep {
	return ReadPlanStep{Tool: ReadPlanToolReadFacts, Kinds: kinds}
}

func TestReadPlanOfferedKinds(t *testing.T) {
	t.Parallel()
	capabilities := readFallbackTestCapabilities()
	// Declared out of vocabulary order on purpose: the list is in
	// vocabulary order whatever order the registry declares.
	got := ReadPlanOfferedKinds(capabilities, SubjectTeam)
	want := []FactKind{FactBlockers, FactMetrics, FactHealth, FactLandscape}
	if !slices.Equal(got, want) {
		t.Fatalf("ReadPlanOfferedKinds(team) = %v, want %v", got, want)
	}
	if got := ReadPlanOfferedKinds(capabilities, SubjectWorkItem); !slices.Equal(got, []FactKind{FactStatus, FactBlockers}) {
		t.Fatalf("ReadPlanOfferedKinds(work_item) = %v, want [status blockers]", got)
	}
	if got := ReadPlanOfferedKinds(capabilities, ""); len(got) != 0 {
		t.Fatalf("ReadPlanOfferedKinds(\"\") = %v, want none", got)
	}
}

// TestValidateReadPlan pins the validator: the ordered kinds of a valid plan,
// and the whole plan refused on the first invalid step with its class.
func TestValidateReadPlan(t *testing.T) {
	t.Parallel()
	capabilities := readFallbackTestCapabilities()
	tooManySteps := ReadPlan{}
	for range ReadPlanMaxSteps + 1 {
		tooManySteps.Steps = append(tooManySteps.Steps, ReadPlanStep{Tool: ReadPlanToolFindSubjects, SubjectKind: SubjectTeam})
	}
	maxSteps := ReadPlan{Steps: []ReadPlanStep{
		readsStep(FactMetrics), readsStep(FactHealth), readsStep(FactBlockers),
		{Tool: ReadPlanToolFindSubjects, SubjectKind: SubjectTeam},
	}}
	for _, tc := range []struct {
		name      string
		plan      ReadPlan
		want      []FactKind
		rejection ReadPlanRejection
	}{
		{name: "the cycle-time plan", plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactMetrics)}}, want: []FactKind{FactMetrics}},
		{name: "steps keep their order", plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactHealth), readsStep(FactMetrics, FactBlockers)}}, want: []FactKind{FactHealth, FactMetrics, FactBlockers}},
		{name: "find_subjects is lowered to no step", plan: ReadPlan{Steps: []ReadPlanStep{{Tool: ReadPlanToolFindSubjects, SubjectKind: SubjectTeam}, readsStep(FactMetrics)}}, want: []FactKind{FactMetrics}},
		{name: "exactly the step limit", plan: maxSteps, want: []FactKind{FactMetrics, FactHealth, FactBlockers}},
		{name: "exactly the kind limit of a step", plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactMetrics, FactHealth, FactBlockers, FactLandscape)}}, want: []FactKind{FactMetrics, FactHealth, FactBlockers, FactLandscape}},
		{name: "no steps", plan: ReadPlan{}, rejection: ReadPlanRejectionEmpty},
		{name: "more steps than the limit", plan: tooManySteps, rejection: ReadPlanRejectionTooManySteps},
		{name: "a tool outside the vocabulary", plan: ReadPlan{Steps: []ReadPlanStep{{Tool: "run_operation", Kinds: []FactKind{FactMetrics}}}}, rejection: ReadPlanRejectionUnknownTool},
		{name: "a read_facts step with no kinds", plan: ReadPlan{Steps: []ReadPlanStep{readsStep()}}, rejection: ReadPlanRejectionStepShape},
		{name: "a read_facts step carrying an inert subject kind", plan: ReadPlan{Steps: []ReadPlanStep{{Tool: ReadPlanToolReadFacts, Kinds: []FactKind{FactMetrics}, SubjectKind: SubjectTeam}}}, want: []FactKind{FactMetrics}},
		{name: "a find_subjects step carrying inert kinds", plan: ReadPlan{Steps: []ReadPlanStep{{Tool: ReadPlanToolFindSubjects, SubjectKind: SubjectTeam, Kinds: []FactKind{FactHealth}}, readsStep(FactMetrics)}}, want: []FactKind{FactMetrics}},
		{name: "a find_subjects step with no subject kind is inert", plan: ReadPlan{Steps: []ReadPlanStep{{Tool: ReadPlanToolFindSubjects}, readsStep(FactMetrics)}}, want: []FactKind{FactMetrics}},
		{name: "a find_subjects step with an unknown subject kind", plan: ReadPlan{Steps: []ReadPlanStep{{Tool: ReadPlanToolFindSubjects, SubjectKind: "person"}, readsStep(FactMetrics)}}, rejection: ReadPlanRejectionUnknownSubjectKind},
		{name: "more kinds in a step than the limit", plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactMetrics, FactHealth, FactBlockers, FactFlow, FactLandscape)}}, rejection: ReadPlanRejectionTooManyKinds},
		{name: "a kind outside the vocabulary", plan: ReadPlan{Steps: []ReadPlanStep{readsStep("cycle_time")}}, rejection: ReadPlanRejectionUnknownKind},
		{name: "a kind no capability serves for the member kind", plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactFileHotspots)}}, rejection: ReadPlanRejectionKindNotOffered},
		{name: "a kind with no registered capability", plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactFlow)}}, rejection: ReadPlanRejectionKindNotOffered},
		{name: "a duplicate kind in one step", plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactMetrics, FactMetrics)}}, rejection: ReadPlanRejectionDuplicateKind},
		{name: "a duplicate kind across steps", plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactMetrics), readsStep(FactHealth, FactMetrics)}}, rejection: ReadPlanRejectionDuplicateKind},
		{name: "find_subjects alone reads nothing", plan: ReadPlan{Steps: []ReadPlanStep{{Tool: ReadPlanToolFindSubjects, SubjectKind: SubjectTeam}}}, rejection: ReadPlanRejectionNoReads},
		{name: "a valid step before an invalid one does not survive", plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactMetrics), readsStep(FactFileHotspots)}}, rejection: ReadPlanRejectionKindNotOffered},
		{name: "the first invalid step names the class", plan: ReadPlan{Steps: []ReadPlanStep{readsStep("cycle_time"), {Tool: "graphql_query"}}}, rejection: ReadPlanRejectionUnknownKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, rejection := ValidateReadPlan(tc.plan, SubjectTeam, capabilities)
			if rejection != tc.rejection {
				t.Fatalf("rejection = %q, want %q", rejection, tc.rejection)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("kinds = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReadPlanRejectionVocabularyIsClosedAndUnique(t *testing.T) {
	t.Parallel()
	vocabulary := ReadPlanRejectionVocabulary()
	seen := map[ReadPlanRejection]bool{}
	for _, member := range vocabulary {
		if member == "" || seen[member] {
			t.Fatalf("rejection vocabulary member %q is empty or repeated", member)
		}
		seen[member] = true
	}
}
