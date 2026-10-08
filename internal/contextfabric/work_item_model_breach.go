package contextfabric

import (
	"fmt"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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

// degradeWorkItemModelBreach is the validation-time form of the same rule: the
// answer about to be served breaks a model-caused rule against the FINAL
// retained members and anchor (the set the validator reads), so the
// model-authored content is withheld and the facts-only degraded answer is
// served instead. It returns false when the breach is server-caused, so the
// original failure stands.
func degradeWorkItemModelBreach(result InvestigationResult, principal storage.Principal) (InvestigationResult, bool) {
	token, fired := WorkItemTupleRuleFiredBy(ValidateWorkItemTuplePayload(result, principal))
	if !fired || !WorkItemTupleRuleModelCaused(WorkItemTupleRule(token)) {
		return result, false
	}
	result.Status = InvestigationDegraded
	result.Drivers = []DriverJudgment{}
	result.RemainingWork, result.ReadinessGaps, result.Conflicts = []Finding{}, []Finding{}, []Finding{}
	claims := make([]ClaimedFact, 0, len(result.ClaimedFacts))
	for _, claim := range result.ClaimedFacts {
		if claim.Kind == contractsv1.ContextFabricFactCardinality {
			claims = append(claims, claim)
		}
	}
	result.ClaimedFacts = claims
	allowed := map[string]bool{}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			if ref, ok := canonicalWorkItemEvidenceRef(member.Subject); ok {
				allowed[ref] = true
			}
		}
	}
	refs := make([]string, 0, len(result.EvidenceRefIDs))
	for _, ref := range result.EvidenceRefIDs {
		if allowed[ref] {
			refs = append(refs, ref)
		}
	}
	result.EvidenceRefIDs = refs
	labels := make(map[string]string, len(result.EvidenceRefLabels))
	for ref, label := range result.EvidenceRefLabels {
		if allowed[ref] {
			labels[ref] = label
		}
	}
	result.EvidenceRefLabels = labels
	result.Warnings = append(slices.Clone(result.Warnings), synthesisFailureWarning(SynthesisFailureRejected))
	if ValidateWorkItemTuplePayload(result, principal) != nil {
		return result, false
	}
	return result, true
}
