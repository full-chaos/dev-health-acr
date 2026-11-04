package v1

import "fmt"

// CHAOS-13002: the read fallback record on an answer plan. When no fixed
// question family serves what a question asks, the model plans which
// canonical fact kinds the server reads, and the plan records that it did.

// ContextFabricReadFallbackReason is why the read fallback applied.
type ContextFabricReadFallbackReason string

const (
	// ContextFabricReadFallbackReasonJudgmentUnservable: the family ranks by
	// an attention measure, which cannot serve the judgment kind asked for.
	ContextFabricReadFallbackReasonJudgmentUnservable ContextFabricReadFallbackReason = "judgment_unservable"
	// ContextFabricReadFallbackReasonKindUnservedForMember: a listed kind is
	// served for none of the answer's member kinds.
	ContextFabricReadFallbackReasonKindUnservedForMember ContextFabricReadFallbackReason = "kind_unserved_for_member"
)

// ContextFabricReadFallbackReasonVocabulary is the reason vocabulary in order.
func ContextFabricReadFallbackReasonVocabulary() []ContextFabricReadFallbackReason {
	return []ContextFabricReadFallbackReason{
		ContextFabricReadFallbackReasonJudgmentUnservable, ContextFabricReadFallbackReasonKindUnservedForMember,
	}
}

// ValidContextFabricReadFallbackReason reports membership.
func ValidContextFabricReadFallbackReason(value ContextFabricReadFallbackReason) bool {
	for _, member := range ContextFabricReadFallbackReasonVocabulary() {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricReadFallbackOutcome is what became of the fallback. A plan
// records the fallback only when its planned kinds were applied; a planner
// failure or a refused plan leaves the plan as the family made it.
type ContextFabricReadFallbackOutcome string

const ContextFabricReadFallbackOutcomeApplied ContextFabricReadFallbackOutcome = "applied"

// ContextFabricReadFallbackOutcomeVocabulary is the outcome vocabulary.
func ContextFabricReadFallbackOutcomeVocabulary() []ContextFabricReadFallbackOutcome {
	return []ContextFabricReadFallbackOutcome{ContextFabricReadFallbackOutcomeApplied}
}

// ValidContextFabricReadFallbackOutcome reports membership.
func ValidContextFabricReadFallbackOutcome(value ContextFabricReadFallbackOutcome) bool {
	for _, member := range ContextFabricReadFallbackOutcomeVocabulary() {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricAnswerPlanFallback is the plan's record of a read fallback:
// why it applied, the kinds the model planned, in plan order, and the outcome.
type ContextFabricAnswerPlanFallback struct {
	Reason       ContextFabricReadFallbackReason  `json:"reason"`
	PlannedKinds []ContextFabricFactKind          `json:"planned_kinds"`
	Outcome      ContextFabricReadFallbackOutcome `json:"outcome"`
}

func (f ContextFabricAnswerPlanFallback) validate(bounds contextFabricBounds) error {
	if !bounds.additiveOK(ValidContextFabricReadFallbackReason(f.Reason), string(f.Reason)) {
		return fmt.Errorf("answer plan fallback reason %q is not a member of the vocabulary", f.Reason)
	}
	if !bounds.additiveOK(ValidContextFabricReadFallbackOutcome(f.Outcome), string(f.Outcome)) {
		return fmt.Errorf("answer plan fallback outcome %q is not a member of the vocabulary", f.Outcome)
	}
	if len(f.PlannedKinds) == 0 || len(f.PlannedKinds) > bounds.factKindListMax() {
		return fmt.Errorf("answer plan fallback names %d planned kinds, want 1 to %d", len(f.PlannedKinds), bounds.factKindListMax())
	}
	seen := make(map[ContextFabricFactKind]struct{}, len(f.PlannedKinds))
	for _, kind := range f.PlannedKinds {
		if !bounds.additiveOK(validFactKind(kind), string(kind)) {
			return fmt.Errorf("answer plan fallback planned kind %q is not a member of the closed vocabulary", kind)
		}
		if _, repeated := seen[kind]; repeated {
			return fmt.Errorf("answer plan fallback plans kind %q twice", kind)
		}
		seen[kind] = struct{}{}
	}
	return nil
}

// ContextFabricReadFallbackLimitation is the service-authored sentence an
// answer carries when its reads were planned by the model.
const ContextFabricReadFallbackLimitation = "The reads of this answer were chosen by the model, for a question that no fixed question family serves."
