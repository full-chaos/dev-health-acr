package contextfabric

import (
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
	if state.ScopeAnchor.Kind != SubjectProject {
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
	return expression.Scoped.MemberKind == SubjectWorkItem && expression.Scoped.MemberQualifier == ""
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
// principal is supplied by the current request boundary so a cardinality
// claim cannot name an arbitrary organization. An empty principal organization
// is accepted only when the result carries no cardinality claim.
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
			return fmt.Errorf("work-item tuple project candidate carries an empty evidence reference")
		}
		if _, ok := memberEvidence[ref]; !ok {
			return fmt.Errorf("work-item tuple project candidate evidence reference %q is outside retained members", ref)
		}
	}

	if result.AnswerPlan != nil {
		if result.AnswerPlan.Family != contractsv1.ContextFabricQuestionFamilyScopedCohortStatus ||
			result.AnswerPlan.MemberKind != contractsv1.ContextFabricSubjectWorkItem {
			return fmt.Errorf("work-item tuple answer plan does not name the scoped work-item family")
		}
	}
	if len(result.Paths) != 0 {
		return fmt.Errorf("work-item tuple payload carries relationship paths")
	}

	memberClaimIDs, err := validateWorkItemClaims(result.ClaimedFacts, memberKeys, principal.OrgID)
	if err != nil {
		return err
	}

	for _, ref := range result.EvidenceRefIDs {
		if _, ok := memberEvidence[ref]; !ok {
			return fmt.Errorf("work-item tuple result evidence reference %q is outside retained members", ref)
		}
	}
	for _, findingSet := range [][]Finding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findingSet {
			if err := validateWorkItemMemberReferences(finding.Subjects, finding.EvidenceRefIDs, finding.ClaimedFactIDs, memberKeys, memberEvidence, memberClaimIDs, "finding"); err != nil {
				return err
			}
		}
	}
	for _, driver := range result.Drivers {
		if err := validateWorkItemMemberReferences(driver.AffectedSubjects, driver.EvidenceRefIDs, driver.ClaimedFactIDs, memberKeys, memberEvidence, memberClaimIDs, "driver"); err != nil {
			return err
		}
	}

	if result.EvidenceRefLabels != nil {
		for ref := range result.EvidenceRefLabels {
			if _, ok := memberEvidence[ref]; !ok {
				return fmt.Errorf("work-item tuple evidence label %q is outside retained members", ref)
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
		return SubjectRef{}, fmt.Errorf("work-item tuple payload must carry exactly one candidate and one committed anchor")
	}
	candidate := resolution.Candidates[0]
	anchor := resolution.Committed[0]
	if candidate.State != ResolutionCommitted || candidate.Subject.Kind != SubjectProject || anchor.Kind != SubjectProject || candidate.Subject.CanonicalID == "" || anchor.CanonicalID == "" {
		return SubjectRef{}, fmt.Errorf("work-item tuple payload anchor must be one committed project candidate")
	}
	if workItemSubjectKey(candidate.Subject) != workItemSubjectKey(anchor) {
		return SubjectRef{}, fmt.Errorf("work-item tuple candidate and committed anchor disagree")
	}
	if resolution.ClarificationPrompt != "" {
		return SubjectRef{}, fmt.Errorf("work-item tuple payload carries a clarification prompt")
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
		return nil, nil, fmt.Errorf("work-item tuple cohort kind is %q, want %q", cohort.Kind, SubjectWorkItem)
	}
	if len(cohort.Exclusions) != 0 {
		return nil, nil, fmt.Errorf("work-item tuple payload carries cohort exclusions")
	}
	if len(cohort.Groups) != 0 {
		return nil, nil, fmt.Errorf("work-item tuple payload carries cohort groups")
	}
	for _, member := range cohort.Members {
		if member.Subject.Kind != SubjectWorkItem || member.Subject.CanonicalID == "" {
			return nil, nil, fmt.Errorf("work-item tuple cohort carries a non-work-item member")
		}
		key := workItemSubjectKey(member.Subject)
		if _, duplicate := memberKeys[key]; duplicate {
			return nil, nil, fmt.Errorf("work-item tuple cohort repeats member %q", member.Subject.CanonicalID)
		}
		if key == workItemSubjectKey(anchor) {
			return nil, nil, fmt.Errorf("work-item tuple cohort carries its project anchor as a member")
		}
		canonicalEvidenceRef, ok := canonicalWorkItemEvidenceRef(member.Subject)
		if !ok {
			return nil, nil, fmt.Errorf("work-item tuple member %q does not carry a canonical v2 work-item identity", member.Subject.CanonicalID)
		}
		memberKeys[key] = struct{}{}
		// The member identity, not arbitrary evidence attached by a producer,
		// defines the evidence closure. Missing member refs remain valid for a
		// degraded payload; refs that are present must be the canonical source
		// ref for this member.
		memberEvidence[canonicalEvidenceRef] = struct{}{}
		for _, ref := range member.EvidenceRefIDs {
			if ref == "" {
				return nil, nil, fmt.Errorf("work-item tuple member %q carries an empty evidence reference", member.Subject.CanonicalID)
			}
			if ref != canonicalEvidenceRef {
				return nil, nil, fmt.Errorf("work-item tuple member %q carries evidence reference %q, want %q", member.Subject.CanonicalID, ref, canonicalEvidenceRef)
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

func validateWorkItemClaims(claims []ClaimedFact, memberKeys map[string]struct{}, organizationID string) (map[string]struct{}, error) {
	memberClaimIDs := make(map[string]struct{})
	claimIDs := make(map[string]struct{}, len(claims))
	cardinalityClaims := 0
	for _, claim := range claims {
		if claim.ClaimID == "" {
			return nil, fmt.Errorf("work-item tuple payload carries a claimed fact without an id")
		}
		if _, duplicate := claimIDs[claim.ClaimID]; duplicate {
			return nil, fmt.Errorf("work-item tuple payload repeats claim %q", claim.ClaimID)
		}
		claimIDs[claim.ClaimID] = struct{}{}
		switch claim.Kind {
		case FactStatus, FactWork:
			if claim.Subject.Kind != SubjectWorkItem {
				return nil, fmt.Errorf("work-item tuple %s claim is not about a retained work item", claim.Kind)
			}
			if _, ok := memberKeys[workItemSubjectKey(claim.Subject)]; !ok {
				return nil, fmt.Errorf("work-item tuple %s claim is outside retained members", claim.Kind)
			}
			if workItemClaimCarriesTable(claim) {
				return nil, fmt.Errorf("work-item tuple %s claim carries table data", claim.Kind)
			}
			memberClaimIDs[claim.ClaimID] = struct{}{}
		case contractsv1.ContextFabricFactCardinality:
			cardinalityClaims++
			if cardinalityClaims > 1 {
				return nil, fmt.Errorf("work-item tuple payload carries more than one cardinality claim")
			}
			if organizationID == "" || claim.Subject.Kind != SubjectOrganization || claim.Subject.CanonicalID != organizationID || claim.Field != "work_item_count" {
				return nil, fmt.Errorf("work-item tuple cardinality claim is not for the current organization")
			}
			if claim.Value.Integer == nil || claim.Value.String != nil || claim.Value.Number != nil || claim.Value.Boolean != nil || claim.Value.Null || *claim.Value.Integer < 0 {
				return nil, fmt.Errorf("work-item tuple cardinality claim must carry one non-negative integer")
			}
			if workItemClaimCarriesTable(claim) {
				return nil, fmt.Errorf("work-item tuple cardinality claim carries table data")
			}
		default:
			return nil, fmt.Errorf("work-item tuple payload carries unsupported claim kind %q", claim.Kind)
		}
	}
	return memberClaimIDs, nil
}

func workItemClaimCarriesTable(claim ClaimedFact) bool {
	return len(claim.Rows) != 0 || claim.Table != nil || len(claim.TimeSeriesRows) != 0 || claim.TimeSeriesTable != nil
}

func validateWorkItemMemberReferences(subjects []SubjectRef, evidence, claims []string, memberKeys, memberEvidence, memberClaimIDs map[string]struct{}, kind string) error {
	if len(subjects) == 0 {
		return fmt.Errorf("work-item tuple %s has no retained member subject", kind)
	}
	for _, subject := range subjects {
		if _, ok := memberKeys[workItemSubjectKey(subject)]; !ok {
			return fmt.Errorf("work-item tuple %s names subject %q outside retained members", kind, subject.CanonicalID)
		}
	}
	for _, ref := range evidence {
		if _, ok := memberEvidence[ref]; !ok {
			return fmt.Errorf("work-item tuple %s evidence reference %q is outside retained members", kind, ref)
		}
	}
	for _, claimID := range claims {
		if _, ok := memberClaimIDs[claimID]; !ok {
			return fmt.Errorf("work-item tuple %s cites claim %q outside member FactStatus/FactWork claims", kind, claimID)
		}
	}
	return nil
}

func workItemSubjectKey(subject SubjectRef) string {
	return string(subject.Kind) + "\x00" + subject.CanonicalID
}
