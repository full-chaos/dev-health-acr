package contextfabric

import (
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// WorkItemTupleDisposition is the result of the read-side tuple classifier.
// A tuple with no usable semantic reading is still identified so the caller
// can record an ordinary reuse decline; it must never be promoted to an
// eligible tuple from the public payload alone.
type WorkItemTupleDisposition string

const (
	WorkItemTupleNotApplicable WorkItemTupleDisposition = "not_applicable"
	WorkItemTupleEligible      WorkItemTupleDisposition = "eligible"
	WorkItemTupleDeclined      WorkItemTupleDisposition = "declined"
)

// WorkItemTupleClassification keeps the semantic read status alongside the
// disposition. The status is deliberately returned unchanged: absent,
// malformed and unsupported snapshots have different downstream handling even
// though each one declines reuse.
type WorkItemTupleClassification struct {
	Disposition  WorkItemTupleDisposition
	SemanticRead SemanticStateReadStatus
}

// ClassifyWorkItemTuple identifies the bounded project-to-work-item tuple.
//
// An available persisted reading is the authority for an eligible tuple. When
// the reading is unavailable, the persisted result may identify the family and
// member kind, but that shape is only a declined candidate. In particular, an
// answer plan never substitutes for an available semantic frame.
func ClassifyWorkItemTuple(result InvestigationResult, state *PersistedSemanticState, read SemanticStateReadStatus) WorkItemTupleClassification {
	classification := WorkItemTupleClassification{
		Disposition:  WorkItemTupleNotApplicable,
		SemanticRead: read,
	}

	if read == SemanticStateReadAvailable {
		if workItemTupleSemanticState(state) {
			classification.Disposition = WorkItemTupleEligible
		}
		return classification
	}

	if workItemTuplePayloadMarker(result) {
		classification.Disposition = WorkItemTupleDeclined
	}
	return classification
}

// workItemTupleSemanticState is intentionally narrower than the general frame
// validator. The validator owns the full persisted-state contract; this helper
// reads only the tuple discriminators needed before a reuse branch can run.
func workItemTupleSemanticState(state *PersistedSemanticState) bool {
	if state == nil || state.Family != QuestionFamilyScopedCohortStatus || !state.FramePresent || state.Frame == nil {
		return false
	}
	if !WorkItemTupleAnchorKind(state.ScopeAnchor.Kind) {
		return false
	}
	expression := state.Frame.SubjectExpression
	if expression.Kind != SubjectExpressionChildrenOfScope || expression.Scoped == nil {
		return false
	}
	// A persisted frame is normally validated before it reaches this helper.
	// Keep the variant check here as well so a directly supplied malformed
	// state cannot accidentally be classified from one non-nil pointer.
	if expression.Named != nil || expression.Explicit != nil || expression.Discovered != nil || expression.Grouped != nil || expression.Org != nil {
		return false
	}
	return expression.Scoped.MemberKind == SubjectWorkItem && workItemTupleQualifierServable(expression.Scoped)
}

// workItemTuplePayloadMarker is the fail-closed fallback for a stored row
// whose semantic reading is absent or unreadable. AnswerPlan is the persisted
// payload's own family/member declaration; a cohort kind by itself is not a
// family declaration and cannot be used to guess one.
func workItemTuplePayloadMarker(result InvestigationResult) bool {
	return result.AnswerPlan != nil &&
		result.AnswerPlan.Family == contractsv1.ContextFabricQuestionFamilyScopedCohortStatus &&
		result.AnswerPlan.MemberKind == contractsv1.ContextFabricSubjectWorkItem
}

// ValidateWorkItemTuplePayload checks the result shape that may be reused for
// the project-to-work-item tuple. It is a pure shape check; live anchor and
// member authorization remain separate gates. Every evidence reference in the
// payload, including candidate refs and evidence labels, must resolve to a
// retained member's canonical work-item evidence ref.
//
// principal is supplied by the current request boundary: a cardinality claim
// requires a current organization, and an empty principal organization is
// accepted only when the result carries no cardinality claim. The claim's
// SUBJECT is checked against the payload's own resolved anchor (below), a
// project this tuple is always scoped under -- never against an organization,
// since a work-item tuple's population is always anchor-bound
// (count_population_scope.go).
func ValidateWorkItemTuplePayload(result InvestigationResult, principal storage.Principal) error {
	anchor, err := validateWorkItemAnchorCandidate(result.SubjectResolution)
	if err != nil {
		return err
	}

	memberKeys, memberEvidence, err := validateWorkItemCohort(result.Cohort, anchor)
	if err != nil {
		return err
	}
	for _, ref := range result.SubjectResolution.Candidates[0].EvidenceRefIDs {
		if ref == "" {
			return workItemRuleErrorf(WorkItemRuleCandidateEmptyEvidenceRef, "work-item tuple project candidate carries an empty evidence reference")
		}
		if _, ok := memberEvidence[ref]; !ok {
			return workItemRuleErrorf(WorkItemRuleCandidateEvidenceOutsideMembers, "work-item tuple project candidate evidence reference %q is outside retained members", ref)
		}
	}

	if result.AnswerPlan != nil {
		if result.AnswerPlan.Family != contractsv1.ContextFabricQuestionFamilyScopedCohortStatus ||
			result.AnswerPlan.MemberKind != contractsv1.ContextFabricSubjectWorkItem {
			return workItemRuleErrorf(WorkItemRuleAnswerPlanFamily, "work-item tuple answer plan does not name the scoped work-item family")
		}
	}
	if len(result.Paths) != 0 {
		return workItemRuleErrorf(WorkItemRuleRelationshipPaths, "work-item tuple payload carries relationship paths")
	}

	memberClaimIDs, err := validateWorkItemClaims(result.ClaimedFacts, memberKeys, anchor, principal.OrgID)
	if err != nil {
		return err
	}

	for _, ref := range result.EvidenceRefIDs {
		if _, ok := memberEvidence[ref]; !ok {
			return workItemRuleErrorf(WorkItemRuleResultEvidenceOutsideMembers, "work-item tuple result evidence reference %q is outside retained members", ref)
		}
	}
	citableKeys := make(map[string]struct{}, len(memberKeys)+1)
	forEachCitableAnchorOrMember([]SubjectRef{anchor}, result.Cohort, func(subject SubjectRef) {
		citableKeys[workItemSubjectKey(subject)] = struct{}{}
	})
	for _, findingSet := range [][]Finding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findingSet {
			if err := validateWorkItemMemberReferences(finding.Subjects, finding.EvidenceRefIDs, finding.ClaimedFactIDs, citableKeys, memberEvidence, memberClaimIDs, "finding"); err != nil {
				return err
			}
		}
	}
	for _, driver := range result.Drivers {
		if err := validateWorkItemMemberReferences(driver.AffectedSubjects, driver.EvidenceRefIDs, driver.ClaimedFactIDs, citableKeys, memberEvidence, memberClaimIDs, "driver"); err != nil {
			return err
		}
	}

	if result.EvidenceRefLabels != nil {
		for ref := range result.EvidenceRefLabels {
			if _, ok := memberEvidence[ref]; !ok {
				return workItemRuleErrorf(WorkItemRuleEvidenceLabelOutsideMembers, "work-item tuple evidence label %q is outside retained members", ref)
			}
		}
	}
	return nil
}

// WorkItemTuplePayloadAllowed is the bool form for call sites whose only
// decision is whether to take the tuple reuse branch.
func WorkItemTuplePayloadAllowed(result InvestigationResult, principal storage.Principal) bool {
	return ValidateWorkItemTuplePayload(result, principal) == nil
}

func validateWorkItemAnchorCandidate(resolution SubjectResolution) (SubjectRef, error) {
	if len(resolution.Candidates) != 1 || len(resolution.Committed) != 1 {
		return SubjectRef{}, workItemRuleErrorf(WorkItemRuleAnchorCardinality, "work-item tuple payload must carry exactly one candidate and one committed anchor")
	}
	candidate := resolution.Candidates[0]
	anchor := resolution.Committed[0]
	if candidate.State != ResolutionCommitted || !WorkItemTupleAnchorKind(anchor.Kind) || candidate.Subject.Kind != anchor.Kind || candidate.Subject.CanonicalID == "" || anchor.CanonicalID == "" {
		return SubjectRef{}, workItemRuleErrorf(WorkItemRuleAnchorNotCommittedProject, "work-item tuple payload anchor must be one committed project or repository candidate")
	}
	if workItemSubjectKey(candidate.Subject) != workItemSubjectKey(anchor) {
		return SubjectRef{}, workItemRuleErrorf(WorkItemRuleAnchorDisagree, "work-item tuple candidate and committed anchor disagree")
	}
	if resolution.ClarificationPrompt != "" {
		return SubjectRef{}, workItemRuleErrorf(WorkItemRuleClarificationPrompt, "work-item tuple payload carries a clarification prompt")
	}
	return anchor, nil
}

func validateWorkItemCohort(cohort *Cohort, anchor SubjectRef) (map[string]struct{}, map[string]struct{}, error) {
	memberKeys := make(map[string]struct{})
	memberEvidence := make(map[string]struct{})
	if cohort == nil {
		return memberKeys, memberEvidence, nil
	}
	if cohort.Kind != SubjectWorkItem {
		return nil, nil, workItemRuleErrorf(WorkItemRuleCohortKind, "work-item tuple cohort kind is %q, want %q", cohort.Kind, SubjectWorkItem)
	}
	if len(cohort.Exclusions) != 0 {
		return nil, nil, workItemRuleErrorf(WorkItemRuleCohortExclusions, "work-item tuple payload carries cohort exclusions")
	}
	if len(cohort.Groups) != 0 {
		return nil, nil, workItemRuleErrorf(WorkItemRuleCohortGroups, "work-item tuple payload carries cohort groups")
	}
	for _, member := range cohort.Members {
		if member.Subject.Kind != SubjectWorkItem || member.Subject.CanonicalID == "" {
			return nil, nil, workItemRuleErrorf(WorkItemRuleMemberNotWorkItem, "work-item tuple cohort carries a non-work-item member")
		}
		key := workItemSubjectKey(member.Subject)
		if _, duplicate := memberKeys[key]; duplicate {
			return nil, nil, workItemRuleErrorf(WorkItemRuleMemberRepeated, "work-item tuple cohort repeats member %q", member.Subject.CanonicalID)
		}
		if key == workItemSubjectKey(anchor) {
			return nil, nil, workItemRuleErrorf(WorkItemRuleMemberIsAnchor, "work-item tuple cohort carries its project anchor as a member")
		}
		canonicalEvidenceRef, ok := canonicalWorkItemEvidenceRef(member.Subject)
		if !ok {
			return nil, nil, workItemRuleErrorf(WorkItemRuleMemberIdentityNotCanonical, "work-item tuple member %q does not carry a canonical v2 work-item identity", member.Subject.CanonicalID)
		}
		memberKeys[key] = struct{}{}
		// The member identity, not arbitrary evidence attached by a producer,
		// defines the evidence closure. Missing member refs remain valid for a
		// degraded payload; refs that are present must be the canonical source
		// ref for this member.
		memberEvidence[canonicalEvidenceRef] = struct{}{}
		for _, ref := range member.EvidenceRefIDs {
			if ref == "" {
				return nil, nil, workItemRuleErrorf(WorkItemRuleMemberEmptyEvidenceRef, "work-item tuple member %q carries an empty evidence reference", member.Subject.CanonicalID)
			}
			if ref != canonicalEvidenceRef {
				return nil, nil, workItemRuleErrorf(WorkItemRuleMemberEvidenceNotCanonical, "work-item tuple member %q carries evidence reference %q, want %q", member.Subject.CanonicalID, ref, canonicalEvidenceRef)
			}
		}
	}
	return memberKeys, memberEvidence, nil
}

// canonicalWorkItemEvidenceRef derives the only evidence reference a retained
// work item may contribute. Subject canonical IDs are decoded with the shared
// identity registry; the evidence ID then follows the existing source
// producer's EvidenceRefID(entity, repoID+":"+workItemID) format.
func canonicalWorkItemEvidenceRef(subject SubjectRef) (string, bool) {
	if subject.Kind != SubjectWorkItem {
		return "", false
	}
	segments, ok := identity.Segments(identity.KindWorkItem, subject.CanonicalID)
	if !ok || len(segments) != 2 {
		return "", false
	}
	return contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, segments[0]+":"+segments[1]), true
}

func validateWorkItemClaims(claims []ClaimedFact, memberKeys map[string]struct{}, anchor SubjectRef, organizationID string) (map[string]struct{}, error) {
	memberClaimIDs := make(map[string]struct{})
	claimIDs := make(map[string]struct{}, len(claims))
	cardinalityClaims := 0
	for _, claim := range claims {
		if claim.ClaimID == "" {
			return nil, workItemRuleErrorf(WorkItemRuleClaimWithoutId, "work-item tuple payload carries a claimed fact without an id")
		}
		if _, duplicate := claimIDs[claim.ClaimID]; duplicate {
			return nil, workItemRuleErrorf(WorkItemRuleClaimRepeated, "work-item tuple payload repeats claim %q", claim.ClaimID)
		}
		claimIDs[claim.ClaimID] = struct{}{}
		switch claim.Kind {
		case FactStatus, FactWork:
			if claim.Subject.Kind != SubjectWorkItem {
				return nil, workItemRuleErrorf(ruleForKind(claim.Kind == FactStatus, WorkItemRuleStatusClaimNotWorkItem, WorkItemRuleWorkClaimNotWorkItem), "work-item tuple %s claim is not about a retained work item", claim.Kind)
			}
			if _, ok := memberKeys[workItemSubjectKey(claim.Subject)]; !ok {
				return nil, workItemRuleErrorf(ruleForKind(claim.Kind == FactStatus, WorkItemRuleStatusClaimOutsideMembers, WorkItemRuleWorkClaimOutsideMembers), "work-item tuple %s claim is outside retained members", claim.Kind)
			}
			if workItemClaimCarriesTable(claim) {
				return nil, workItemRuleErrorf(ruleForKind(claim.Kind == FactStatus, WorkItemRuleStatusClaimTableData, WorkItemRuleWorkClaimTableData), "work-item tuple %s claim carries table data", claim.Kind)
			}
			memberClaimIDs[claim.ClaimID] = struct{}{}
		case contractsv1.ContextFabricFactCardinality:
			cardinalityClaims++
			if cardinalityClaims > 1 {
				return nil, workItemRuleErrorf(WorkItemRuleCardinalityClaimRepeated, "work-item tuple payload carries more than one cardinality claim")
			}
			if organizationID == "" {
				return nil, workItemRuleErrorf(WorkItemRuleCardinalityClaimNoOrg, "work-item tuple cardinality claim requires the current organization")
			}
			if claim.Subject.Kind != anchor.Kind || claim.Subject.CanonicalID != anchor.CanonicalID || claim.Field != "work_item_count" {
				return nil, workItemRuleErrorf(WorkItemRuleCardinalityClaimNotAnchor, "work-item tuple cardinality claim is not for the resolved anchor")
			}
			if claim.Value.Integer == nil || claim.Value.String != nil || claim.Value.Number != nil || claim.Value.Boolean != nil || claim.Value.Null || *claim.Value.Integer < 0 {
				return nil, workItemRuleErrorf(WorkItemRuleCardinalityClaimValue, "work-item tuple cardinality claim must carry one non-negative integer")
			}
			if workItemClaimCarriesTable(claim) {
				return nil, workItemRuleErrorf(WorkItemRuleCardinalityClaimTableData, "work-item tuple cardinality claim carries table data")
			}
		default:
			return nil, workItemRuleErrorf(WorkItemRuleClaimKindUnsupported, "work-item tuple payload carries unsupported claim kind %q", claim.Kind)
		}
	}
	return memberClaimIDs, nil
}

func workItemClaimCarriesTable(claim ClaimedFact) bool {
	return len(claim.Rows) != 0 || claim.Table != nil || len(claim.TimeSeriesRows) != 0 || claim.TimeSeriesTable != nil
}

// forEachCitableAnchorOrMember is the one definition of the subjects an answer
// may name beside its facts: the committed anchors of the investigation and the
// retained cohort members. The synthesis admission and the work-item tuple
// validator both read it, so a subject one admits is never a subject the other
// refuses.
func forEachCitableAnchorOrMember(committed []SubjectRef, cohort *Cohort, visit func(SubjectRef)) {
	for _, subject := range committed {
		visit(subject)
	}
	if cohort != nil {
		for _, member := range cohort.Members {
			visit(member.Subject)
		}
	}
}

func validateWorkItemMemberReferences(subjects []SubjectRef, evidence, claims []string, citableKeys, memberEvidence, memberClaimIDs map[string]struct{}, kind string) error {
	if len(subjects) == 0 {
		return workItemRuleErrorf(ruleForKind(kind == "finding", WorkItemRuleFindingNoMemberSubject, WorkItemRuleDriverNoMemberSubject), "work-item tuple %s has no retained member subject", kind)
	}
	for _, subject := range subjects {
		if _, ok := citableKeys[workItemSubjectKey(subject)]; !ok {
			return workItemRuleErrorf(ruleForKind(kind == "finding", WorkItemRuleFindingSubjectOutsideMembers, WorkItemRuleDriverSubjectOutsideMembers), "work-item tuple %s names subject %q outside retained members", kind, subject.CanonicalID)
		}
	}
	for _, ref := range evidence {
		if _, ok := memberEvidence[ref]; !ok {
			return workItemRuleErrorf(ruleForKind(kind == "finding", WorkItemRuleFindingEvidenceOutsideMembers, WorkItemRuleDriverEvidenceOutsideMembers), "work-item tuple %s evidence reference %q is outside retained members", kind, ref)
		}
	}
	for _, claimID := range claims {
		if _, ok := memberClaimIDs[claimID]; !ok {
			return workItemRuleErrorf(ruleForKind(kind == "finding", WorkItemRuleFindingClaimOutsideMembers, WorkItemRuleDriverClaimOutsideMembers), "work-item tuple %s cites claim %q outside member FactStatus/FactWork claims", kind, claimID)
		}
	}
	return nil
}

func workItemSubjectKey(subject SubjectRef) string {
	return string(subject.Kind) + "\x00" + subject.CanonicalID
}

// WorkItemTupleRule is the closed token of one rule of the strict work-item
// tuple payload validator. A rejection names the rule that fired by this token
// and nothing else: no caller text, identifier or free error text.
type WorkItemTupleRule string

const (
	WorkItemRuleCandidateEmptyEvidenceRef       WorkItemTupleRule = "candidate_empty_evidence_ref"
	WorkItemRuleCandidateEvidenceOutsideMembers WorkItemTupleRule = "candidate_evidence_outside_members"
	WorkItemRuleAnswerPlanFamily                WorkItemTupleRule = "answer_plan_family"
	WorkItemRuleRelationshipPaths               WorkItemTupleRule = "relationship_paths"
	WorkItemRuleResultEvidenceOutsideMembers    WorkItemTupleRule = "result_evidence_outside_members"
	WorkItemRuleEvidenceLabelOutsideMembers     WorkItemTupleRule = "evidence_label_outside_members"
	WorkItemRuleAnchorCardinality               WorkItemTupleRule = "anchor_cardinality"
	WorkItemRuleAnchorNotCommittedProject       WorkItemTupleRule = "anchor_not_committed_project"
	WorkItemRuleAnchorDisagree                  WorkItemTupleRule = "anchor_disagree"
	WorkItemRuleClarificationPrompt             WorkItemTupleRule = "clarification_prompt"
	WorkItemRuleCohortKind                      WorkItemTupleRule = "cohort_kind"
	WorkItemRuleCohortExclusions                WorkItemTupleRule = "cohort_exclusions"
	WorkItemRuleCohortGroups                    WorkItemTupleRule = "cohort_groups"
	WorkItemRuleMemberNotWorkItem               WorkItemTupleRule = "member_not_work_item"
	WorkItemRuleMemberRepeated                  WorkItemTupleRule = "member_repeated"
	WorkItemRuleMemberIsAnchor                  WorkItemTupleRule = "member_is_anchor"
	WorkItemRuleMemberIdentityNotCanonical      WorkItemTupleRule = "member_identity_not_canonical"
	WorkItemRuleMemberEmptyEvidenceRef          WorkItemTupleRule = "member_empty_evidence_ref"
	WorkItemRuleMemberEvidenceNotCanonical      WorkItemTupleRule = "member_evidence_not_canonical"
	WorkItemRuleClaimWithoutId                  WorkItemTupleRule = "claim_without_id"
	WorkItemRuleClaimRepeated                   WorkItemTupleRule = "claim_repeated"
	WorkItemRuleStatusClaimNotWorkItem          WorkItemTupleRule = "status_claim_not_work_item"
	WorkItemRuleWorkClaimNotWorkItem            WorkItemTupleRule = "work_claim_not_work_item"
	WorkItemRuleStatusClaimOutsideMembers       WorkItemTupleRule = "status_claim_outside_members"
	WorkItemRuleWorkClaimOutsideMembers         WorkItemTupleRule = "work_claim_outside_members"
	WorkItemRuleStatusClaimTableData            WorkItemTupleRule = "status_claim_table_data"
	WorkItemRuleWorkClaimTableData              WorkItemTupleRule = "work_claim_table_data"
	WorkItemRuleCardinalityClaimRepeated        WorkItemTupleRule = "cardinality_claim_repeated"
	WorkItemRuleCardinalityClaimNoOrg           WorkItemTupleRule = "cardinality_claim_no_org"
	WorkItemRuleCardinalityClaimNotAnchor       WorkItemTupleRule = "cardinality_claim_not_anchor"
	WorkItemRuleCardinalityClaimValue           WorkItemTupleRule = "cardinality_claim_value"
	WorkItemRuleCardinalityClaimTableData       WorkItemTupleRule = "cardinality_claim_table_data"
	WorkItemRuleClaimKindUnsupported            WorkItemTupleRule = "claim_kind_unsupported"
	WorkItemRuleFindingNoMemberSubject          WorkItemTupleRule = "finding_no_member_subject"
	WorkItemRuleDriverNoMemberSubject           WorkItemTupleRule = "driver_no_member_subject"
	WorkItemRuleFindingSubjectOutsideMembers    WorkItemTupleRule = "finding_subject_outside_members"
	WorkItemRuleDriverSubjectOutsideMembers     WorkItemTupleRule = "driver_subject_outside_members"
	WorkItemRuleFindingEvidenceOutsideMembers   WorkItemTupleRule = "finding_evidence_outside_members"
	WorkItemRuleDriverEvidenceOutsideMembers    WorkItemTupleRule = "driver_evidence_outside_members"
	WorkItemRuleFindingClaimOutsideMembers      WorkItemTupleRule = "finding_claim_outside_members"
	WorkItemRuleDriverClaimOutsideMembers       WorkItemTupleRule = "driver_claim_outside_members"
)

// WorkItemTupleRuleNone is the persistence line's token beside a save that was
// not rejected; WorkItemTupleRuleUnclassified is a rejection whose error
// carries no rule (a defect: the validator returns only typed rule errors).
const (
	WorkItemTupleRuleNone         = "none"
	WorkItemTupleRuleUnclassified = "unclassified"
)

// WorkItemTupleRules is the closed rule list, in declaration order.
func WorkItemTupleRules() []WorkItemTupleRule {
	return []WorkItemTupleRule{
		WorkItemRuleCandidateEmptyEvidenceRef,
		WorkItemRuleCandidateEvidenceOutsideMembers,
		WorkItemRuleAnswerPlanFamily,
		WorkItemRuleRelationshipPaths,
		WorkItemRuleResultEvidenceOutsideMembers,
		WorkItemRuleEvidenceLabelOutsideMembers,
		WorkItemRuleAnchorCardinality,
		WorkItemRuleAnchorNotCommittedProject,
		WorkItemRuleAnchorDisagree,
		WorkItemRuleClarificationPrompt,
		WorkItemRuleCohortKind,
		WorkItemRuleCohortExclusions,
		WorkItemRuleCohortGroups,
		WorkItemRuleMemberNotWorkItem,
		WorkItemRuleMemberRepeated,
		WorkItemRuleMemberIsAnchor,
		WorkItemRuleMemberIdentityNotCanonical,
		WorkItemRuleMemberEmptyEvidenceRef,
		WorkItemRuleMemberEvidenceNotCanonical,
		WorkItemRuleClaimWithoutId,
		WorkItemRuleClaimRepeated,
		WorkItemRuleStatusClaimNotWorkItem,
		WorkItemRuleWorkClaimNotWorkItem,
		WorkItemRuleStatusClaimOutsideMembers,
		WorkItemRuleWorkClaimOutsideMembers,
		WorkItemRuleStatusClaimTableData,
		WorkItemRuleWorkClaimTableData,
		WorkItemRuleCardinalityClaimRepeated,
		WorkItemRuleCardinalityClaimNoOrg,
		WorkItemRuleCardinalityClaimNotAnchor,
		WorkItemRuleCardinalityClaimValue,
		WorkItemRuleCardinalityClaimTableData,
		WorkItemRuleClaimKindUnsupported,
		WorkItemRuleFindingNoMemberSubject,
		WorkItemRuleDriverNoMemberSubject,
		WorkItemRuleFindingSubjectOutsideMembers,
		WorkItemRuleDriverSubjectOutsideMembers,
		WorkItemRuleFindingEvidenceOutsideMembers,
		WorkItemRuleDriverEvidenceOutsideMembers,
		WorkItemRuleFindingClaimOutsideMembers,
		WorkItemRuleDriverClaimOutsideMembers,
	}
}

// ruleForKind picks between the two rule tokens a shared check can fire as,
// so the record names the output field (finding or driver) or the claim kind
// (status or work) that was refused.
func ruleForKind(first bool, a, b WorkItemTupleRule) WorkItemTupleRule {
	if first {
		return a
	}
	return b
}

// workItemRuleError is a validator rejection that names its rule.
type workItemRuleError struct {
	rule WorkItemTupleRule
	msg  string
}

func (e *workItemRuleError) Error() string { return e.msg }

func workItemRuleErrorf(rule WorkItemTupleRule, format string, args ...any) error {
	return &workItemRuleError{rule: rule, msg: fmt.Sprintf(format, args...)}
}

// workItemTupleRuleOf reads the rule token off a save error; "none" for a
// save that was not payload-rejected.
func workItemTupleRuleOf(err error) string {
	if !errors.Is(err, errWorkItemTuplePayloadRejected) {
		return WorkItemTupleRuleNone
	}
	var ruleErr *workItemRuleError
	if errors.As(err, &ruleErr) {
		return string(ruleErr.rule)
	}
	return WorkItemTupleRuleUnclassified
}

// WorkItemTupleRuleFiredBy reads the closed rule token off a validator
// rejection anywhere in err's chain.
func WorkItemTupleRuleFiredBy(err error) (string, bool) {
	var ruleErr *workItemRuleError
	if errors.As(err, &ruleErr) {
		return string(ruleErr.rule), true
	}
	return "", false
}

// WorkItemTupleRejectReasonVocabulary is the persistence line's reject_reason vocabulary.
func WorkItemTupleRejectReasonVocabulary() []string {
	out := []string{WorkItemTupleRuleNone, WorkItemTupleRuleUnclassified}
	for _, rule := range WorkItemTupleRules() {
		out = append(out, string(rule))
	}
	return out
}
