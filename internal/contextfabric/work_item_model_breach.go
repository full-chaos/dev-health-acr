package contextfabric

import (
	"context"
	"fmt"
	"log/slog"
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

func workItemModelBreachFailure(rule WorkItemTupleRule, stage string, inputBounded bool) *SynthesisFailure {
	return &SynthesisFailure{Class: SynthesisFailureRejected, Rule: string(rule), Stage: stage, InputBounded: inputBounded, cause: fmt.Errorf("%w: %w: work-item tuple draft breaks %s", ErrSynthesisRejected, ErrModelOutput, rule)}
}

// workItemDegradeBasis is what the validation stage keeps of the synthesis
// pass: the input the model was shown and the draft's own limitations and
// warnings, so the answer text the model wrote can be told from the text the
// server added.
type workItemDegradeBasis struct {
	Input            SynthesisInput
	DraftLimitations []string
	DraftWarnings    []string
}

// newWorkItemDegradeBasis records which limitations and warnings the model
// wrote: the service-authored limitations a synthesis adds after the call (the
// bounded-input disclosure) are the server's and are never removed.
func newWorkItemDegradeBasis(input SynthesisInput, draft InvestigationResult) *workItemDegradeBasis {
	return &workItemDegradeBasis{
		Input:            input,
		DraftLimitations: slices.DeleteFunc(slices.Clone(draft.Limitations), contractsv1.IsContextFabricServiceAuthoredLimitation),
		DraftWarnings:    slices.Clone(draft.Warnings),
	}
}

// degradeWorkItemModelBreach is the validation-time form of the same rule: the
// answer about to be served breaks a model-caused rule against the FINAL
// retained members and anchor (the set the validator reads). The facts-only
// answer is composed by the same ComposeDegraded a rejected draft is served
// with, and only its model-authored fields replace the result's; everything the
// server wrote (cohort, coverage, plan, limitations, the cardinality claim)
// stays. It returns false when the breach is server-caused, the answer cannot
// be composed or still fails the validator, so the original failure stands.
func (e *Engine) degradeWorkItemModelBreach(ctx context.Context, principal storage.Principal, result InvestigationResult, basis *workItemDegradeBasis) (InvestigationResult, bool) {
	if basis == nil {
		return result, false
	}
	token, fired := WorkItemTupleRuleFiredBy(ValidateWorkItemTuplePayload(result, principal))
	rule := WorkItemTupleRule(token)
	if !fired || !WorkItemTupleRuleModelCaused(rule) {
		return result, false
	}
	composer, ok := e.synthesizer.(DegradedSynthesizer)
	if !ok {
		return result, false
	}
	failure := workItemModelBreachFailure(rule, "validation", slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisInputBoundedLimitation))
	failure.DeferEvent = true
	composed, err := composer.ComposeDegraded(ctx, principal, basis.Input, failure)
	if err != nil {
		return result, false
	}
	out := result
	out.Status = composed.Status
	out.Coverage.Partial = out.Coverage.Partial || composed.Coverage.Partial
	out.DirectJudgment, out.CurrentState, out.DeterministicAnswer = composed.DirectJudgment, composed.CurrentState, composed.DeterministicAnswer
	out.StrongestPressures = composed.StrongestPressures
	out.Drivers, out.RemainingWork, out.ReadinessGaps, out.Conflicts = []DriverJudgment{}, []Finding{}, []Finding{}, []Finding{}
	claims := make([]ClaimedFact, 0, len(result.ClaimedFacts))
	for _, claim := range result.ClaimedFacts {
		if claim.Kind == contractsv1.ContextFabricFactCardinality {
			claims = append(claims, claim)
		}
	}
	out.ClaimedFacts = claims
	allowed := map[string]bool{}
	if out.Cohort != nil {
		for _, member := range out.Cohort.Members {
			if ref, ok := canonicalWorkItemEvidenceRef(member.Subject); ok {
				allowed[ref] = true
			}
		}
	}
	refs := make([]string, 0, len(composed.EvidenceRefIDs))
	for _, ref := range composed.EvidenceRefIDs {
		if allowed[ref] {
			refs = append(refs, ref)
		}
	}
	out.EvidenceRefIDs = refs
	labels := make(map[string]string, len(result.EvidenceRefLabels))
	for ref, label := range result.EvidenceRefLabels {
		if allowed[ref] {
			labels[ref] = label
		}
	}
	out.EvidenceRefLabels = labels
	serverLimitations := slices.DeleteFunc(slices.Clone(result.Limitations), func(limitation string) bool { return slices.Contains(basis.DraftLimitations, limitation) })
	composedLimitations, displaced := appendBoundedLimitations([]string{}, serverLimitations)
	out.Limitations = composedLimitations
	out.LimitationsDisplaced += displaced
	warnings := slices.DeleteFunc(slices.Clone(result.Warnings), func(warning string) bool { return slices.Contains(basis.DraftWarnings, warning) })
	out.Warnings = append(composed.Warnings, warnings...)
	if len(out.Warnings) > contractsv1.ContextFabricWarningsMaxCount {
		out.Warnings = out.Warnings[:contractsv1.ContextFabricWarningsMaxCount]
	}
	if ValidateWorkItemTuplePayload(out, principal) != nil {
		return result, false
	}
	event := SynthesisModelFailureEvent{Class: failure.Class, Rule: failure.Rule, Stage: failure.Stage}
	if e.telemetry != nil {
		e.telemetry.RecordSynthesisModelFailure(ctx, principal, event)
	} else {
		NewSlogEngineTelemetry(slog.Default()).RecordSynthesisModelFailure(ctx, principal, event)
	}
	return out, true
}
