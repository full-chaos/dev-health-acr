package contextfabric

import (
	"maps"
	"slices"
)

// The fact outcome ledger (CHAOS-7073, design CHAOS-7036 section C.5).
//
// Coverage.Sources holds ONE observation per fact kind for the whole read.
// It cannot say what happened to each subject: a provider that classifies
// its subjects (operational_deficiencies: fired, measured_zero, stale,
// before_range, never_evaluated, withheld_capped_read) handed those states to
// the registry, which logged them and dropped them. A direct read serves one
// coverage row per (kind, subject), so it needs them kept.
//
// The ledger keeps, per planned kind, which branch of ReadFacts' plan loop
// decided it, the coverage state and reason it minted, the subjects it was
// planned for and actually queried with, and the provider's own per-subject
// states. It is `json:"-"` on the bundle: read bookkeeping, never part of
// the evidence a bundle carries, so no engine answer changes shape.

// FactKindOutcome is one planned kind's entry in the ledger.
type FactKindOutcome struct {
	// Branch is the plan-loop branch that decided the kind: unconfigured,
	// scope_gap, pruned, failed, rejected or completed.
	Branch string
	// State and Reason are the coverage observation the branch minted.
	State  SourceState
	Reason string
	// Planned are the subjects the planner kept for this kind; Queried are
	// the subjects the provider was actually asked about (empty unless the
	// provider ran).
	Planned []SubjectRef
	Queried []SubjectRef
	// Members are the provider's own per-subject states, keyed by
	// FactSubjectKey. Only a provider that classifies its subjects fills it.
	Members map[string]string
	// MemberReasons are reasons true of one subject, keyed by FactSubjectKey.
	MemberReasons map[string]string
	// NotApplicable are subjects the provider marked not applicable, keyed by
	// FactSubjectKey.
	NotApplicable map[string]struct{}
	// Evaluated are subjects the provider showed it evaluated with no fact
	// (a measured zero), keyed by FactSubjectKey.
	Evaluated map[string]struct{}
	// ScopeExpansionUnavailable is set on a scope_gap branch whose policy is
	// declared and enabled but has no scope expander on this read path.
	ScopeExpansionUnavailable bool
	// WindowUnsupported is set on a scope_gap branch whose policy is
	// configured and enabled but does not serve the requested window.
	WindowUnsupported bool
	// Scope is the scope-expansion decision behind a scope_gap branch.
	Scope *FactScopeNote
	// TruncatedOnlyByScopeGap is set on a completed read whose truncated state
	// came from the scope gap alone: the provider returned every row it was
	// asked for and nothing was cut by a cap.
	TruncatedOnlyByScopeGap bool
	// UnreachedRoot is set when a kind read some subjects but a scope gap left
	// the roots of another kind unreached. It carries that gap's own cause.
	UnreachedRoot *FactUnreachedRoot
}

// FactUnreachedRoot is the cause a scope gap gives for the root subjects of
// one kind that a partially served kind did not reach.
type FactUnreachedRoot struct {
	Kind                      SubjectKind
	State                     SourceState
	ScopeExpansionUnavailable bool
	WindowUnsupported         bool
	Reason                    string
	Scope                     *FactScopeNote
}

// FactScopeNote is the structured form of a scope-expansion decision: the
// outcome and policy a reader needs, kept out of the reason sentence.
type FactScopeNote struct {
	Outcome string
	Policy  string
}

func scopeNoteOf(gap FactScopeGap) *FactScopeNote {
	return &FactScopeNote{Outcome: string(gap.Outcome), Policy: string(gap.Policy)}
}

func (bundle *CanonicalFactBundle) noteUnreachedRoot(kind FactKind, planned factPlanEntry, capability FactCapability) {
	if planned.ScopeGap == nil {
		return
	}
	outcome, ok := bundle.Outcomes[kind]
	if !ok {
		return
	}
	gap := *planned.ScopeGap
	outcome.UnreachedRoot = &FactUnreachedRoot{
		Kind:                      gap.OriginKind,
		State:                     factScopeGapSourceState(gap.Outcome),
		ScopeExpansionUnavailable: gap.ScopeExpansionUnavailable(),
		WindowUnsupported:         gap.WindowUnsupported(),
		Reason:                    unexpandedReason(capability, gap, false),
		Scope:                     scopeNoteOf(gap),
	}
	bundle.Outcomes[kind] = outcome
}

// FactOutcomeLedger maps a planned fact kind to its outcome.
type FactOutcomeLedger map[FactKind]FactKindOutcome

// record stores one kind's outcome. The state and reason are read back off
// the coverage observation just appended for the kind, so the ledger and the
// coverage can never say different things.
func (bundle *CanonicalFactBundle) recordOutcome(kind FactKind, branch factReadOutcome, planned, queried []SubjectRef, result *FactProviderResult) {
	if bundle.Outcomes == nil {
		bundle.Outcomes = FactOutcomeLedger{}
	}
	outcome := FactKindOutcome{
		Branch:  string(branch),
		Planned: slices.Clone(planned),
		Queried: slices.Clone(queried),
	}
	source := "canonical_fact:" + string(kind)
	for index := len(bundle.Coverage.Sources) - 1; index >= 0; index-- {
		if bundle.Coverage.Sources[index].Source == source {
			outcome.State = bundle.Coverage.Sources[index].State
			outcome.Reason = bundle.Coverage.Sources[index].Reason
			break
		}
	}
	if result != nil {
		if reason, ok := bundle.kindReasons[kind]; ok {
			outcome.Reason = reason
		}
		if len(result.SubjectReasons) > 0 {
			outcome.MemberReasons = maps.Clone(result.SubjectReasons)
			for key, reason := range outcome.MemberReasons {
				outcome.MemberReasons[key] = withoutProviderPackageLabel(reason)
			}
		}
		if len(result.NotApplicableSubjects) > 0 {
			outcome.NotApplicable = maps.Clone(result.NotApplicableSubjects)
		}
		if result.Evaluation != nil && len(result.Evaluation.Members) > 0 {
			outcome.Members = make(map[string]string, len(result.Evaluation.Members))
			for _, member := range result.Evaluation.Members {
				outcome.Members[FactSubjectKey(member.Subject)] = member.State
			}
		}
		if outcome.State == SourceAvailable && len(result.EvaluatedSubjects) > 0 {
			outcome.Evaluated = make(map[string]struct{}, len(result.EvaluatedSubjects))
			for _, subject := range result.EvaluatedSubjects {
				outcome.Evaluated[FactSubjectKey(subject)] = struct{}{}
			}
		}
	}
	bundle.Outcomes[kind] = outcome
}
