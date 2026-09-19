package contextfabric

import (
	"fmt"
	"slices"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// WorkItemTupleByIDDisposition is the result of applying the stored tuple
// serving rules to a result read by id.
type WorkItemTupleByIDDisposition string

const (
	WorkItemTupleByIDNotApplicable WorkItemTupleByIDDisposition = "not_applicable"
	WorkItemTupleByIDServed        WorkItemTupleByIDDisposition = "served"
	WorkItemTupleByIDStored        WorkItemTupleByIDDisposition = "stored"
	WorkItemTupleByIDNotFound      WorkItemTupleByIDDisposition = "not_found"
)

// WorkItemTupleByIDDecision carries the transformed serving copy and the
// independent census read status used to make the decision.
type WorkItemTupleByIDDecision struct {
	Result      InvestigationResult
	Disposition WorkItemTupleByIDDisposition
	CensusRead  WorkItemTupleCensusReadStatus
	Event       WorkItemStoredServingEvent
	Err         error
}

// ServeWorkItemTupleCensus serves a tuple whose persisted census was checked
// against the current request. The census and cardinality claim are already
// part of the immutable result; serving must not manufacture a population
// from the retained cohort. The D47 served value is derived from that cohort
// as the final serving step.
func ServeWorkItemTupleCensus(candidate InvestigationResult, census *WorkItemTupleCensus) InvestigationResult {
	if census == nil || ValidateWorkItemTupleCensus(census) != WorkItemTupleCensusReadAvailable {
		return candidate
	}
	candidate.Coverage.Details = append([]CoverageDetail(nil), candidate.Coverage.Details...)
	candidate.Coverage.DegradedReasons = append([]string(nil), candidate.Coverage.DegradedReasons...)
	switch census.State {
	case WorkItemMembershipCensusExact:
		candidate.Coverage.Details = withoutWorkItemKindCensusDetails(candidate.Coverage.Details)
		candidate.Coverage.DegradedReasons = withoutWorkItemKindCensusReasons(candidate.Coverage.DegradedReasons)
	case WorkItemMembershipCensusFloor:
		candidate = withWorkItemKindCensusDetail(candidate, census.Value)
	case WorkItemMembershipCensusUnmeasured:
		candidate.Coverage.Details = withoutWorkItemKindCensusDetails(candidate.Coverage.Details)
		candidate.Coverage.DegradedReasons = withoutWorkItemKindCensusReasons(candidate.Coverage.DegradedReasons)
		if census.gap == nil && !hasWorkItemAuthorizationGapLimitation(candidate.Limitations) {
			composed, displaced := appendBoundedLimitations(candidate.Limitations, []string{WorkItemMembershipLimitation()})
			candidate.Limitations = composed
			candidate.LimitationsDisplaced += displaced
		}
	}
	if census.gap != nil {
		candidate = applyWorkItemAuthorizationGap(candidate, *census.gap)
	}
	return candidate
}

// ServeStoredWorkItemTuple applies the result-by-id rules for the bounded
// tuple. It never reads the graph or membership store. A current authorization
// digest can prove that the persisted census applies; a different digest is
// an ordinary not-found so the route does not disclose row existence.
func ServeStoredWorkItemTuple(candidate InvestigationResult, state *PersistedSemanticState, read SemanticStateReadStatus, principal storage.Principal) WorkItemTupleByIDDecision {
	classification := ClassifyWorkItemTuple(candidate, state, read)
	decision := WorkItemTupleByIDDecision{
		Result:      candidate,
		Disposition: WorkItemTupleByIDNotApplicable,
		CensusRead:  WorkItemTupleCensusReadAbsent,
	}
	if classification.Disposition == WorkItemTupleNotApplicable {
		return decision
	}

	decision.Event = newWorkItemStoredServingEvent(StoredAnswerabilitySurfaceResultByID, read, candidate)
	decision.Disposition = WorkItemTupleByIDStored
	if state != nil && read == SemanticStateReadAvailable {
		census := state.WorkItemCensus
		decision.CensusRead = ValidateWorkItemTupleCensus(census)
		decision.Event.CensusRead = decision.CensusRead
		if decision.CensusRead == WorkItemTupleCensusReadAvailable {
			digest, err := WorkItemAuthorizationDigest(principal, census.RequestedRepositoryScope)
			if err != nil || digest != census.AuthorizationDigest {
				decision.Disposition = WorkItemTupleByIDNotFound
				decision.Event.Basis = "digest_changed"
				return decision
			}
			decision.Disposition = WorkItemTupleByIDServed
			decision.Result = ServeWorkItemTupleCensus(candidate, census)
			decision.Event.Basis = "digest_matched"
			decision.Err = validateWorkItemStoredCoverage(decision.Result, &decision.Event)
			return decision
		}
	}

	// An explicit current universal grant proves content authorization after
	// the caller's org-scoped Get. Restricted grants cannot authorize opaque
	// repository IDs once the independent census proof is unavailable.
	if principal.OrgID == "" || !slices.Contains(principal.RepositoryScopes, "*") {
		decision.Disposition = WorkItemTupleByIDNotFound
		decision.Event.Basis = "authorization_unverifiable"
		return decision
	}
	decision.Event.Basis = "universal_grant"
	decision.Result = serveWorkItemTupleWithoutCensus(candidate)
	decision.Err = validateWorkItemStoredCoverage(decision.Result, &decision.Event)
	return decision
}

// serveWorkItemTupleWithoutCensus serves the stored public result while
// disclosing that its membership count cannot be checked on this read. The
// stored row is never changed. A census detail is not served without its
// independent census input, and its paired legacy reason is removed from the
// serving copy as well.
func serveWorkItemTupleWithoutCensus(candidate InvestigationResult) InvestigationResult {
	candidate.Coverage.Details = withoutWorkItemKindCensusDetails(candidate.Coverage.Details)
	candidate.Coverage.DegradedReasons = withoutWorkItemKindCensusReasons(candidate.Coverage.DegradedReasons)
	composed, displaced := appendBoundedLimitations(candidate.Limitations, []string{WorkItemMembershipLimitation()})
	candidate.Limitations = composed
	candidate.LimitationsDisplaced += displaced
	return candidate
}

func withoutWorkItemKindCensusDetails(details []CoverageDetail) []CoverageDetail {
	if len(details) == 0 {
		return details
	}
	kept := make([]CoverageDetail, 0, len(details))
	for _, detail := range details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated && detail.Kind == SubjectWorkItem {
			continue
		}
		kept = append(kept, detail)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func withoutWorkItemKindCensusReasons(reasons []string) []string {
	if len(reasons) == 0 {
		return reasons
	}
	kept := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if strings.HasPrefix(reason, "kind_census_truncated:work_item:") {
			continue
		}
		kept = append(kept, reason)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func withWorkItemKindCensusDetail(candidate InvestigationResult, declared int) InvestigationResult {
	served := 0
	if candidate.Cohort != nil {
		served = len(candidate.Cohort.Members)
	}
	raw := fmt.Sprintf("kind_census_truncated:%s:%d:%d", SubjectWorkItem, declared, served)

	var existing *CoverageDetail
	for index := range candidate.Coverage.Details {
		if candidate.Coverage.Details[index].Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated && candidate.Coverage.Details[index].Kind == SubjectWorkItem {
			copy := candidate.Coverage.Details[index]
			existing = &copy
			break
		}
	}
	if existing == nil {
		existing = &CoverageDetail{Source: "context-fabric:graph"}
	}
	existing.Code = contractsv1.ContextFabricCoverageDetailKindCensusTruncated
	existing.Degrading = true
	existing.Kind = SubjectWorkItem
	declaredCopy := declared
	servedCopy := served
	existing.Declared = &declaredCopy
	existing.Served = &servedCopy
	existing.FactKind = ""
	existing.SourceState = ""
	existing.ScopeOutcome = ""
	existing.OriginKind = ""
	existing.SupportedKinds = nil
	existing.SkippedKinds = nil
	existing.Policy = ""
	existing.Basis = ""
	existing.Count = nil
	existing.Narrowed = false
	existing.Raw = raw
	existing.Label = contractsv1.ComposeCoverageDetailLabel(*existing)

	details := withoutWorkItemKindCensusDetails(candidate.Coverage.Details)
	if existing.DetailID == "" {
		existing.DetailID = nextWorkItemCoverageDetailID(details)
	}
	detailIndex := len(details)
	for index, detail := range details {
		if !detail.Degrading || detail.Raw > existing.Raw {
			detailIndex = index
			break
		}
	}
	details = append(details, CoverageDetail{})
	copy(details[detailIndex+1:], details[detailIndex:])
	details[detailIndex] = *existing
	candidate.Coverage.Details = details

	reasons := withoutWorkItemKindCensusReasons(candidate.Coverage.DegradedReasons)
	reasonIndex := len(reasons)
	for index, reason := range reasons {
		if reason > raw {
			reasonIndex = index
			break
		}
	}
	reasons = append(reasons, "")
	copy(reasons[reasonIndex+1:], reasons[reasonIndex:])
	reasons[reasonIndex] = raw
	candidate.Coverage.DegradedReasons = reasons
	candidate.Coverage.Partial = true
	return candidate
}

func nextWorkItemCoverageDetailID(details []CoverageDetail) string {
	for index := 1; ; index++ {
		candidate := fmt.Sprintf("cov-%02d", index)
		found := false
		for _, detail := range details {
			if detail.DetailID == candidate {
				found = true
				break
			}
		}
		if !found {
			return candidate
		}
	}
}
