package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// ReadFallbackReason is why the read fallback (CHAOS-13002) applies to a turn.
// Empty means it does not apply.
type ReadFallbackReason = contractsv1.ContextFabricReadFallbackReason

const (
	// ReadFallbackReasonJudgmentUnservable (F1): the family ranks, and its
	// ranking measures attention, which cannot serve the judgment kind the
	// interpreter named.
	ReadFallbackReasonJudgmentUnservable = contractsv1.ContextFabricReadFallbackReasonJudgmentUnservable
	// ReadFallbackReasonKindUnservedForMember (F3, static): a kind the
	// interpreter listed has a registered capability that serves none of the
	// turn's member kinds, directly or through an enabled fact-scope policy.
	ReadFallbackReasonKindUnservedForMember = contractsv1.ContextFabricReadFallbackReasonKindUnservedForMember
)

// ReadFallbackTrigger names which test fired the fallback, for the log: the
// wire reason stays the family-fit reason. Empty means none fired.
type ReadFallbackTrigger string

const (
	// ReadFallbackTriggerJudgmentKind (F1): the interpreter named a judgment
	// kind the family's ranking cannot serve.
	ReadFallbackTriggerJudgmentKind ReadFallbackTrigger = "judgment_kind"
	// ReadFallbackTriggerRankingInputs (F4): the family ranks, the interpreter
	// listed kinds, and none of them is an input of that ranking (CHAOS-13002:
	// the same question carried a judgment kind once and none twice).
	ReadFallbackTriggerRankingInputs ReadFallbackTrigger = "ranking_inputs"
	// ReadFallbackTriggerKindUnserved (F3): a listed kind serves none of the
	// member kinds.
	ReadFallbackTriggerKindUnserved ReadFallbackTrigger = "kind_unserved"
)

// ReadFallbackTriggerVocabulary is the closed trigger vocabulary in order.
func ReadFallbackTriggerVocabulary() []ReadFallbackTrigger {
	return []ReadFallbackTrigger{ReadFallbackTriggerJudgmentKind, ReadFallbackTriggerRankingInputs, ReadFallbackTriggerKindUnserved}
}

// ReadFallbackDecisionInput is every value DecideReadFallback may read. All
// are typed; the question text is not an input.
type ReadFallbackDecisionInput struct {
	Definition       QuestionFamilyDefinition
	JudgmentKind     RequestedJudgmentKind
	InterpretedKinds []FactKind
	MemberKind       SubjectKind
	Capabilities     []FactCapability
	WorkItemList     bool
	// scopePolicies replaces the production fact-scope table in tests; nil
	// reads factScopePolicies.
	scopePolicies map[FactKind]map[SubjectKind]factScopePolicyRule
}

// DecideReadFallback decides whether the read fallback applies. A work-item
// walk reads its own fixed kinds, and a turn with no member kind has no kind
// list to offer a planner, so neither applies.
func DecideReadFallback(input ReadFallbackDecisionInput) ReadFallbackReason {
	reason, _ := DecideReadFallbackWithTrigger(input)
	return reason
}

// DecideReadFallbackWithTrigger is DecideReadFallback that also names the
// test that fired. F1 is tried first, then F4, then F3.
func DecideReadFallbackWithTrigger(input ReadFallbackDecisionInput) (ReadFallbackReason, ReadFallbackTrigger) {
	if input.WorkItemList || input.MemberKind == "" {
		return "", ""
	}
	if input.Definition.RequireRanking && !scoreMeaningSupportsJudgmentKind(CohortScoreMeaningAttention, input.JudgmentKind) {
		return ReadFallbackReasonJudgmentUnservable, ReadFallbackTriggerJudgmentKind
	}
	if input.Definition.RequireRanking && listedKindsAreNoRankingInput(input.InterpretedKinds, input.Capabilities) {
		return ReadFallbackReasonJudgmentUnservable, ReadFallbackTriggerRankingInputs
	}
	policies := input.scopePolicies
	if policies == nil {
		policies = factScopePolicies
	}
	for _, kind := range input.InterpretedKinds {
		capability, registered := readFallbackCapability(input.Capabilities, kind)
		if !registered || supportsSubjectKind(capability.SupportedSubjectKinds, input.MemberKind) {
			continue
		}
		// The fact read reaches this kind through the member's own chain.
		if rule := policies[kind][input.MemberKind]; rule.Enabled && rule.Policy != FactScopePolicyNone {
			continue
		}
		return ReadFallbackReasonKindUnservedForMember, ReadFallbackTriggerKindUnserved
	}
	return "", ""
}

// listedKindsAreNoRankingInput is F4's test over typed values: the interpreter
// listed at least one kind, every listed kind has a registered capability, and
// none is a kind the cohort ranking reads. An empty list, or any unregistered
// kind, fails closed.
func listedKindsAreNoRankingInput(kinds []FactKind, capabilities []FactCapability) bool {
	if len(kinds) == 0 {
		return false
	}
	for _, kind := range kinds {
		if _, registered := readFallbackCapability(capabilities, kind); !registered {
			return false
		}
		if _, isInput := rankingFamilyByFactKind[kind]; isInput {
			return false
		}
	}
	return true
}

func readFallbackCapability(capabilities []FactCapability, kind FactKind) (FactCapability, bool) {
	for _, capability := range capabilities {
		if capability.Kind == kind {
			return capability, true
		}
	}
	return FactCapability{}, false
}
