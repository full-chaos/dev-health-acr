package contextfabric

import (
	"log/slog"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
	if frame == nil {
		return "", ""
	}
	kind, qualifier, value, ok := frame.SubjectExpression.MemberQualification()
	if !ok || kind == SubjectPullRequest || qualifier != MemberQualifierStatus || !InMemberStatusVocabulary(kind, value) {
		return "", ""
	}
	return kind, value
}

// MemberStatusOutsideSet is how many members of a kind the pool held whose
// stored status is outside the kind's set, so none of them could match.
type MemberStatusOutsideSet struct {
	Kind  SubjectKind
	Count int
	// Filtered is true when the graph read applied the status filter, and
	// Reached when the anchor reached at least one member of Kind.
	Filtered bool
	Reached  bool
	// ReachCut: the reach probe was cut, so a false Reached is unknown.
	ReachCut bool
	// AtLeast: the count read was cut, so Count is a lower bound.
	AtLeast bool
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
	case SubjectExpressionChildrenOfScope, SubjectExpressionDiscoveredKind, SubjectExpressionOrganizationScope, SubjectExpressionGroupedMembers:
		memberKind, qualifier, value, ok := frame.SubjectExpression.MemberQualification()
		return ok && memberKind == kind && qualifier == MemberQualifierStatus && value == ""
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
	if sentence := memberStatusNotApplied(result, frame); sentence != "" {
		sentences = append(sentences, sentence)
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

// memberStatusNotApplied is the one rule for a status the engine has no filter
// for: a frame that states a status on a kind without a status set (any shape
// that carries the qualifier) is answered unfiltered, and says so. Pull
// request, project and incident statuses have filters, and work items their
// own member filter.
func memberStatusNotApplied(result InvestigationResult, frame *QuestionFrame) string {
	if frame == nil || result.Status == InvestigationClarificationRequired {
		return ""
	}
	kind, qualifier, value, ok := frame.SubjectExpression.MemberQualification()
	if !ok || qualifier != MemberQualifierStatus || kind == "" || kind == SubjectWorkItem {
		return ""
	}
	for _, filtered := range MemberStatusKinds() {
		if kind == filtered {
			return ""
		}
	}
	return contractsv1.ContextFabricMemberStatusNotAppliedLimitation(string(kind), value)
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
	if outside.AtLeast {
		sentence = contractsv1.ContextFabricMemberStatusOutsideSetAtLeastLimitation(string(outside.Kind), outside.Count)
	}
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

// statusMeasuredZero reports whether a turn with no subject still read members:
// the graph context carries the empty member set of a status read that reached
// members of the kind and kept none. That is a count of zero, not an empty
// search.
func statusMeasuredZero(frame *QuestionFrame, cohort *Cohort) bool {
	if cohort == nil || len(cohort.Members) != 0 {
		return false
	}
	if PullRequestStatusFilterOf(frame) != "" {
		return cohort.Kind == SubjectPullRequest
	}
	kind, status := MemberStatusFilterOf(frame)
	return status != "" && kind == cohort.Kind
}

// organizationInvestmentQuestion reports whether a frame asks where the
// organization's effort goes: the organization is the subject, and its own
// investment fact (one fact over every repository once, never a sum over
// teams) answers it.
func organizationInvestmentQuestion(frame *QuestionFrame) bool {
	return frame != nil && frame.SubjectExpression.Kind == SubjectExpressionOrganizationScope && frame.HasGoal(GoalAllocateInvestment)
}

// organizationSubjectAnswers reports whether a turn that resolved no subject
// is answered from the caller's organization subject: an organization
// investment question, or an organization survey that found no members (the
// answer then says so instead of ending in no_match).
func organizationSubjectAnswers(frame *QuestionFrame) bool {
	return organizationInvestmentQuestion(frame) || (frame != nil && frame.OrganizationSurveyMemberKind != "")
}

// organizationRollUpLimitation is the sentence that names an organization
// answer's basis: a survey of the members the organization-scope repair chose,
// or the organization's own investment fact.
func organizationRollUpLimitation(frame *QuestionFrame, graph GraphContext) (string, bool) {
	survey := frame != nil && frame.OrganizationSurveyMemberKind != ""
	switch {
	case graph.OrganizationSubjectRead && survey:
		return contractsv1.ContextFabricOrganizationSurveyNoMembersLimitation, true
	case graph.OrganizationSubjectRead:
		return contractsv1.ContextFabricOrganizationInvestmentRollUpLimitation, true
	case !survey:
		return "", false
	}
	return contractsv1.ContextFabricOrganizationSurveyLimitation(frame.OrganizationSurveyMemberKind)
}

// callerOrganizationSubject is the caller's organization as a subject, in the
// graph's key form.
func callerOrganizationSubject(principal storage.Principal) SubjectRef {
	return SubjectRef{Kind: SubjectOrganization, CanonicalID: "organization:" + principal.OrgID, Label: principal.OrgID}
}

// coverageReason is the closed reason phrase for the first degrading coverage
// detail of a result, or the cause a ledger row names, or the no-cause phrase.
func coverageReason(result InvestigationResult, graph Coverage) string {
	for _, detail := range append(append([]CoverageDetail{}, graph.Details...), result.Coverage.Details...) {
		if detail.Degrading {
			if reason := contractsv1.ContextFabricCoverageCauseReason(detail.Code); reason != "" {
				return reason
			}
		}
	}
	for _, row := range result.Completeness.Outcomes {
		if reason := contractsv1.ContextFabricCoverageCauseReason(row.CauseCoverage); reason != "" {
			return reason
		}
	}
	return contractsv1.ContextFabricNoCoverageCauseReason
}

// withMemberStatusNoneReached states that the status filter ran and the anchor
// reached no member of the kind, so the empty set is not read as members
// holding another status.
func withMemberStatusNoneReached(result InvestigationResult, outside MemberStatusOutsideSet, cut MemberStatusPoolCut, cohort *Cohort, anchorKind SubjectKind) InvestigationResult {
	if !outside.Filtered || outside.Reached || outside.ReachCut || outside.AtLeast || cut.Kind != "" || cohort != nil {
		return result
	}
	return withServiceLimitation(result, contractsv1.ContextFabricMemberStatusNoneReachedLimitation(string(outside.Kind), string(anchorKind)))
}

// withMemberStatusNotComputed states that a status question over projects or
// incidents was answered without any read applying the status.
func withMemberStatusNotComputed(result InvestigationResult, frame *QuestionFrame, graph Coverage) InvestigationResult {
	if result.Status == InvestigationComplete || result.Status == InvestigationClarificationRequired {
		return result
	}
	kind, _ := MemberStatusFilterOf(frame)
	if kind == "" || (result.Cohort != nil && result.Cohort.Kind == kind) {
		return result
	}
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricMemberStatusLimitation(limitation) && !strings.Contains(limitation, "outside the known set") {
			return result
		}
	}
	return withServiceLimitation(result, contractsv1.ContextFabricMemberStatusNotComputedLimitation(string(kind), coverageReason(result, graph)))
}

// withDegradedLimitationFallback is the safety net: a partial or degraded
// answer is never served with an empty limitations list. Reaching it means a
// degraded exit has no limitation of its own, so it logs.
func withDegradedLimitationFallback(result InvestigationResult) InvestigationResult {
	if (result.Status != InvestigationPartial && result.Status != InvestigationDegraded) || len(result.Limitations) > 0 {
		return result
	}
	reason := coverageReason(result, Coverage{})
	slog.Default().Warn("context_fabric: a degraded answer reached assembly without a limitation of its own",
		"status", SanitizeLogAttr(string(result.Status)), "reason", SanitizeLogAttr(reason))
	return withServiceLimitation(result, contractsv1.ContextFabricDegradedWithoutLimitation(string(result.Status), reason))
}

func withServiceLimitation(result InvestigationResult, sentence string) InvestigationResult {
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
