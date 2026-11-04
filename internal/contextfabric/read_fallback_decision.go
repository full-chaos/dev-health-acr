package contextfabric

// ReadFallbackReason is why the read fallback (CHAOS-13002) applies to a turn.
// Empty means it does not apply.
type ReadFallbackReason string

const (
	// ReadFallbackReasonJudgmentUnservable (F1): the family ranks, and its
	// ranking measures attention, which cannot serve the judgment kind the
	// interpreter named.
	ReadFallbackReasonJudgmentUnservable ReadFallbackReason = "judgment_unservable"
	// ReadFallbackReasonKindUnservedForMember (F3, static): a kind the
	// interpreter listed has a registered capability that serves none of the
	// turn's member kinds, directly or through an enabled fact-scope policy.
	ReadFallbackReasonKindUnservedForMember ReadFallbackReason = "kind_unserved_for_member"
)

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
	if input.WorkItemList || input.MemberKind == "" {
		return ""
	}
	if input.Definition.RequireRanking && !scoreMeaningSupportsJudgmentKind(CohortScoreMeaningAttention, input.JudgmentKind) {
		return ReadFallbackReasonJudgmentUnservable
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
		return ReadFallbackReasonKindUnservedForMember
	}
	return ""
}

func readFallbackCapability(capabilities []FactCapability, kind FactKind) (FactCapability, bool) {
	for _, capability := range capabilities {
		if capability.Kind == kind {
			return capability, true
		}
	}
	return FactCapability{}, false
}
