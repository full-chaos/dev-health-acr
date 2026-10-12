package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// MemberStatusKinds are the member kinds whose status qualifier a read applies
// as a pool filter, in the order their disclosures are composed.
func MemberStatusKinds() []SubjectKind {
	return []SubjectKind{SubjectPullRequest, SubjectProject, SubjectIncident}
}

// MemberStatusVocabulary returns the closed status set of a member kind in
// declaration order, or nil for a kind without one.
func MemberStatusVocabulary(kind SubjectKind) []string {
	if kind == SubjectPullRequest {
		return PullRequestStatusVocabulary()
	}
	return contractsv1.ContextFabricMemberStatusVocabulary(string(kind))
}

// InMemberStatusVocabulary reports whether value is in the closed status set
// of kind.
func InMemberStatusVocabulary(kind SubjectKind, value string) bool {
	for _, member := range MemberStatusVocabulary(kind) {
		if member == value {
			return true
		}
	}
	return false
}

// MemberStatusStoredProperty is the property of a project or incident node that holds
// its stored status: projects.state and the incident's normalized status.
func MemberStatusStoredProperty(kind SubjectKind) string {
	if kind == SubjectProject {
		return "state"
	}
	return "status"
}

// MemberStatusFilterOf is the member kind and status the project or incident
// member read must apply for frame, or empty. It is set only for a
// children_of_scope frame whose status qualifier carries a value of the set of
// its member kind. Pull requests are PullRequestStatusFilterOf.
func MemberStatusFilterOf(frame *QuestionFrame) (SubjectKind, string) {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope || frame.SubjectExpression.Scoped == nil {
		return "", ""
	}
	scoped := frame.SubjectExpression.Scoped
	if scoped.MemberKind == SubjectPullRequest || scoped.MemberQualifier != MemberQualifierStatus || !InMemberStatusVocabulary(scoped.MemberKind, scoped.MemberQualifierValue) {
		return "", ""
	}
	return scoped.MemberKind, scoped.MemberQualifierValue
}

// MemberStatusOutsideSet is how many members of a kind the pool held whose
// stored status is outside the kind's set, so none of them could match.
type MemberStatusOutsideSet struct {
	Kind  SubjectKind
	Count int
}

// MemberStatusPoolCut is a status filter decided within a cut read: Read
// members of Kind were checked and members past the cut were not; Pool, when
// not zero, is how many members of the kind the caller may see the read could
// have checked. An empty Kind states nothing.
type MemberStatusPoolCut struct {
	Kind SubjectKind
	Read int
	Pool int
}

// MemberStatusUnappliedOf reports whether frame states a status on members of
// kind that no read applies: a status qualifier with no value, or a value on
// an operand of an explicit set.
func MemberStatusUnappliedOf(frame *QuestionFrame, kind SubjectKind) bool {
	if frame == nil {
		return false
	}
	switch frame.SubjectExpression.Kind {
	case SubjectExpressionChildrenOfScope:
		scoped := frame.SubjectExpression.Scoped
		return scoped != nil && scoped.MemberKind == kind && scoped.MemberQualifier == MemberQualifierStatus && scoped.MemberQualifierValue == ""
	case SubjectExpressionExplicitSet:
		if frame.SubjectExpression.Explicit == nil {
			return false
		}
		for _, operand := range frame.SubjectExpression.Explicit.Operands {
			if operand.Kind == SubjectOperandScoped && operand.Scoped != nil && operand.Scoped.MemberKind == kind && operand.Scoped.MemberQualifier == MemberQualifierStatus {
				return true
			}
		}
	}
	return false
}

// MemberStatusFilterDisclosure is the sentence a served project or incident
// member set states for the status it was filtered by.
func MemberStatusFilterDisclosure(kind SubjectKind, value string) string {
	return contractsv1.ContextFabricMemberStatusFilterLimitation(string(kind), value)
}

// withMemberStatusDisclosure states, on a document that serves members of a
// kind with a status qualifier, the status they were filtered by, or that the
// state the question named was not applied. A status on an operand of an explicit set is never
// applied, whatever the document serves, so it is stated on every such
// document. A sentence is added once; a re-finalized document keeps the one it
// has.
func withMemberStatusDisclosure(result InvestigationResult, frame *QuestionFrame) InvestigationResult {
	sentences := []string{}
	for _, kind := range MemberStatusKinds() {
		serves := result.Cohort != nil && result.Cohort.Kind == kind
		explicitOperand := frame != nil && frame.SubjectExpression.Kind == SubjectExpressionExplicitSet && MemberStatusUnappliedOf(frame, kind)
		filterKind, status := SubjectKind(""), ""
		if kind == SubjectPullRequest {
			if status = PullRequestStatusFilterOf(frame); status != "" {
				filterKind = kind
			}
		} else if filterKind, status = MemberStatusFilterOf(frame); filterKind != kind {
			filterKind, status = "", ""
		}
		switch {
		case status != "" && serves:
			if kind == SubjectPullRequest {
				sentences = append(sentences, PullRequestStatusFilterDisclosure(status))
			} else {
				sentences = append(sentences, MemberStatusFilterDisclosure(kind, status))
			}
		case explicitOperand || (serves && MemberStatusUnappliedOf(frame, kind)):
			sentences = append(sentences, contractsv1.ContextFabricMemberStatusUnappliedLimitation(string(kind)))
		}
	}
	for _, sentence := range sentences {
		if sentence == "" {
			continue
		}
		present := false
		for _, limitation := range result.Limitations {
			if limitation == sentence {
				present = true
				break
			}
		}
		if present {
			continue
		}
		composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
		result.Limitations = composed
		result.LimitationsDisplaced += displaced
	}
	return result
}

// withMemberStatusPoolCut states that the status filter was decided within
// the first members a cut read reached.
func withMemberStatusPoolCut(result InvestigationResult, cut MemberStatusPoolCut) InvestigationResult {
	sentence := contractsv1.ContextFabricMemberStatusPoolCutLimitation(string(cut.Kind), cut.Read, cut.Pool)
	if sentence == "" {
		return result
	}
	for _, limitation := range result.Limitations {
		if limitation == sentence {
			return result
		}
	}
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	return result
}

// withMemberStatusOutsideSet states how many members of the kind the status
// filter could not match because their stored status is outside the kind's set.
func withMemberStatusOutsideSet(result InvestigationResult, outside MemberStatusOutsideSet) InvestigationResult {
	sentence := contractsv1.ContextFabricMemberStatusOutsideSetLimitation(string(outside.Kind), outside.Count)
	if sentence == "" {
		return result
	}
	for _, limitation := range result.Limitations {
		if limitation == sentence {
			return result
		}
	}
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	return result
}
