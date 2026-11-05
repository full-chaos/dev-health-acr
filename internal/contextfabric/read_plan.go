package contextfabric

import (
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ReadPlanTool is the closed tool vocabulary a read plan step may name.
type ReadPlanTool string

const (
	ReadPlanToolReadFacts ReadPlanTool = "read_facts"
	// ReadPlanToolFindSubjects is accepted and lowered to a no-op in v1: the
	// engine's own discovery finds the members.
	ReadPlanToolFindSubjects ReadPlanTool = "find_subjects"
)

const (
	ReadPlanMaxSteps        = 4
	ReadPlanMaxKindsPerStep = 4
	// ReadPlanMaxKinds is the most kinds a valid plan can add.
	ReadPlanMaxKinds = ReadPlanMaxSteps * ReadPlanMaxKindsPerStep
)

// ReadPlanStep is one step of a model-planned read. A read_facts step names
// kinds only; a find_subjects step names a subject kind only.
type ReadPlanStep struct {
	Tool        ReadPlanTool
	Kinds       []FactKind
	SubjectKind SubjectKind
}

// ReadPlan is the planner's output, unvalidated.
type ReadPlan struct {
	Steps []ReadPlanStep
}

// ReadPlanRejection is the closed class of the first invalid step that
// refused a whole plan. Empty means the plan is valid.
type ReadPlanRejection string

const (
	ReadPlanRejectionEmpty              ReadPlanRejection = "empty_plan"
	ReadPlanRejectionTooManySteps       ReadPlanRejection = "too_many_steps"
	ReadPlanRejectionUnknownTool        ReadPlanRejection = "unknown_tool"
	ReadPlanRejectionStepShape          ReadPlanRejection = "step_shape_invalid"
	ReadPlanRejectionTooManyKinds       ReadPlanRejection = "too_many_kinds"
	ReadPlanRejectionUnknownKind        ReadPlanRejection = "unknown_kind"
	ReadPlanRejectionKindNotOffered     ReadPlanRejection = "kind_not_offered"
	ReadPlanRejectionDuplicateKind      ReadPlanRejection = "duplicate_kind"
	ReadPlanRejectionUnknownSubjectKind ReadPlanRejection = "unknown_subject_kind"
	ReadPlanRejectionNoReads            ReadPlanRejection = "no_reads"
)

// ReadPlanRejectionVocabulary is the closed rejection vocabulary in order.
func ReadPlanRejectionVocabulary() []ReadPlanRejection {
	return []ReadPlanRejection{
		ReadPlanRejectionEmpty, ReadPlanRejectionTooManySteps, ReadPlanRejectionUnknownTool,
		ReadPlanRejectionStepShape, ReadPlanRejectionTooManyKinds, ReadPlanRejectionUnknownKind,
		ReadPlanRejectionKindNotOffered, ReadPlanRejectionDuplicateKind,
		ReadPlanRejectionUnknownSubjectKind, ReadPlanRejectionNoReads,
	}
}

// ReadPlanOfferedKinds is the closed kind list a planner may choose from for
// memberKind: every kind a registered capability serves for that member
// kind, in fact-kind vocabulary order.
func ReadPlanOfferedKinds(capabilities []FactCapability, memberKind SubjectKind) []FactKind {
	var offered []FactKind
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		if capability, _ := readFallbackCapability(capabilities, kind); supportsSubjectKind(capability.SupportedSubjectKinds, memberKind) {
			offered = append(offered, kind)
		}
	}
	return offered
}

// ValidateReadPlan returns the ordered kinds a valid plan adds, or the class
// of the first invalid step. The whole plan is refused on that step; nothing
// is repaired.
func ValidateReadPlan(plan ReadPlan, memberKind SubjectKind, capabilities []FactCapability) ([]FactKind, ReadPlanRejection) {
	if len(plan.Steps) == 0 {
		return nil, ReadPlanRejectionEmpty
	}
	if len(plan.Steps) > ReadPlanMaxSteps {
		return nil, ReadPlanRejectionTooManySteps
	}
	offered := ReadPlanOfferedKinds(capabilities, memberKind)
	vocabulary := contractsv1.ContextFabricFactKindVocabulary()
	var kinds []FactKind
	for _, step := range plan.Steps {
		switch step.Tool {
		case ReadPlanToolReadFacts:
			if step.SubjectKind != "" || len(step.Kinds) == 0 {
				return nil, ReadPlanRejectionStepShape
			}
			if len(step.Kinds) > ReadPlanMaxKindsPerStep {
				return nil, ReadPlanRejectionTooManyKinds
			}
			for _, kind := range step.Kinds {
				switch {
				case !slices.Contains(vocabulary[:], kind):
					return nil, ReadPlanRejectionUnknownKind
				case !slices.Contains(offered, kind):
					return nil, ReadPlanRejectionKindNotOffered
				case slices.Contains(kinds, kind):
					return nil, ReadPlanRejectionDuplicateKind
				}
				kinds = append(kinds, kind)
			}
		case ReadPlanToolFindSubjects:
			if len(step.Kinds) > 0 {
				return nil, ReadPlanRejectionStepShape
			}
			if !validSubjectKind(step.SubjectKind) {
				return nil, ReadPlanRejectionUnknownSubjectKind
			}
		default:
			return nil, ReadPlanRejectionUnknownTool
		}
	}
	if len(kinds) == 0 {
		return nil, ReadPlanRejectionNoReads
	}
	return kinds, ""
}
