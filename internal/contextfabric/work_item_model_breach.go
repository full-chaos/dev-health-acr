package contextfabric

import (
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// workItemModelCausedRules are the work-item tuple rules whose only input is
// model-authored content: a driver, a finding, a claim or the evidence the
// model cited. A breach of one is a rejected draft, served as the degraded
// facts-only answer a failed model call is served with. Every other rule reads
// content the server wrote (the anchor, the cohort, the cardinality claim, the
// answer plan, the paths) and stays a hard failure.
var workItemModelCausedRules = map[WorkItemTupleRule]struct{}{
	WorkItemRuleResultEvidenceOutsideMembers:  {},
	WorkItemRuleEvidenceLabelOutsideMembers:   {},
	WorkItemRuleClaimWithoutId:                {},
	WorkItemRuleClaimRepeated:                 {},
	WorkItemRuleStatusClaimNotWorkItem:        {},
	WorkItemRuleWorkClaimNotWorkItem:          {},
	WorkItemRuleStatusClaimOutsideMembers:     {},
	WorkItemRuleWorkClaimOutsideMembers:       {},
	WorkItemRuleStatusClaimTableData:          {},
	WorkItemRuleWorkClaimTableData:            {},
	WorkItemRuleClaimKindUnsupported:          {},
	WorkItemRuleFindingNoMemberSubject:        {},
	WorkItemRuleDriverNoMemberSubject:         {},
	WorkItemRuleFindingSubjectOutsideMembers:  {},
	WorkItemRuleDriverSubjectOutsideMembers:   {},
	WorkItemRuleFindingEvidenceOutsideMembers: {},
	WorkItemRuleDriverEvidenceOutsideMembers:  {},
	WorkItemRuleFindingClaimOutsideMembers:    {},
	WorkItemRuleDriverClaimOutsideMembers:     {},
}

// WorkItemTupleRuleModelCaused reports whether a rule reads model-authored content only.
func WorkItemTupleRuleModelCaused(rule WorkItemTupleRule) bool {
	_, ok := workItemModelCausedRules[rule]
	return ok
}

// workItemModelBreach runs the tuple validator over a synthesized draft against
// the cohort the model was shown and returns the model-caused rule it breaks.
func workItemModelBreach(draft InvestigationResult, input SynthesisInput, resolution SubjectResolution, principal storage.Principal) (WorkItemTupleRule, bool) {
	probe := draft
	probe.SubjectResolution = resolution
	probe.Cohort = input.Graph.Cohort
	probe.AnswerPlan = nil
	probe.Paths = nil
	token, fired := WorkItemTupleRuleFiredBy(ValidateWorkItemTuplePayload(probe, principal))
	rule := WorkItemTupleRule(token)
	if !fired || !WorkItemTupleRuleModelCaused(rule) {
		return "", false
	}
	return rule, true
}

func workItemModelBreachFailure(rule WorkItemTupleRule) *SynthesisFailure {
	return &SynthesisFailure{Class: SynthesisFailureRejected, cause: fmt.Errorf("%w: %w: work-item tuple draft breaks %s", ErrSynthesisRejected, ErrModelOutput, rule)}
}
