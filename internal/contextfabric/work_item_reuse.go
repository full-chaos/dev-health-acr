package contextfabric

import (
	"context"
	"slices"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// WorkItemReuseEvent records the decision basis without evidence or labels.
// Team scope is forwarded to the live anchor read and recorded, but never
// becomes a member-row predicate.
type WorkItemReuseEvent struct {
	Decision         string
	SemanticRead     SemanticStateReadStatus
	CensusRead       WorkItemTupleCensusReadStatus
	RequestedTeamIDs []string
}

func (e *Engine) tryReuseWorkItemTuple(ctx context.Context, principal storage.Principal, request InvestigationRequest, binding ResolvedGraphBinding, stored StoredInvestigationResult, classification WorkItemTupleClassification) (result InvestigationResult, hit bool, servingErr error) {
	event := WorkItemReuseEvent{Decision: "reading_unavailable", SemanticRead: stored.SemanticStateRead, CensusRead: "not_checked", RequestedTeamIDs: append([]string(nil), request.RequestedScope.TeamIDs...)}
	defer func() {
		if e.telemetry != nil {
			e.telemetry.RecordWorkItemReuse(ctx, principal, event)
		}
		// Accepted candidates that fail serving are neither served hits nor
		// ordinary misses. The specific failure event above remains required.
		if servingErr != nil {
			return
		}
		outcome := AnswerReuseMissNoCandidate
		if hit {
			outcome = AnswerReuseHit
		}
		e.recordReuseOutcome(ctx, principal, outcome)
	}()
	if classification.Disposition != WorkItemTupleEligible {
		return InvestigationResult{}, false, nil
	}
	candidate := stored.Result
	event.Decision = "payload_rejected"
	if ValidateWorkItemTuplePayload(candidate, principal) != nil {
		return InvestigationResult{}, false, nil
	}
	census := stored.SemanticState.WorkItemCensus
	event.CensusRead = ValidateWorkItemTupleCensus(census)
	event.Decision = "census_unavailable"
	if event.CensusRead != WorkItemTupleCensusReadAvailable {
		return InvestigationResult{}, false, nil
	}
	event.Decision = "digest_changed"
	digest, err := WorkItemAuthorizationDigest(principal, request.RequestedScope.RepositorySlugs)
	if err != nil || digest != census.AuthorizationDigest {
		return InvestigationResult{}, false, nil
	}
	event.Decision = "anchor_unavailable"
	if e.candidateVerifier == nil {
		return InvestigationResult{}, false, nil
	}
	anchor := candidate.SubjectResolution.Committed[0]
	authorized, _ := e.candidateVerifier(ctx, principal, request.RequestedScope, binding, SubjectProject, anchor.CanonicalID)
	if !authorized || ctx.Err() != nil {
		return InvestigationResult{}, false, nil
	}
	event.Decision = "membership_unavailable"
	if e.workItemMembership == nil {
		return InvestigationResult{}, false, nil
	}
	owner, owned := WorkItemResponseOwnerFromContext(ctx)
	if !owned {
		return InvestigationResult{}, false, nil
	}
	planCap := 0
	if candidate.AnswerPlan != nil {
		planCap = candidate.AnswerPlan.Budget.MaxMembers
	}
	lease, current, err := e.workItemMembership.BeginWorkItemMembership(ctx, principal, WorkItemMembershipRequest{
		Anchor:                   WorkItemMembershipAnchor{Subject: anchor},
		RequestedRepositoryScope: append([]string(nil), request.RequestedScope.RepositorySlugs...),
		PlanMaxMembers:           planCap, RequestMaxMembers: request.Options.MaxCohortMembers,
	})
	// The production Begin registers before S1; repeating the same pointer is
	// idempotent and also covers a port that returns an acquired lease + error.
	if lease != nil {
		if retainErr := owner.Retain(lease); retainErr != nil {
			return InvestigationResult{}, false, nil
		}
		defer func() {
			if !hit {
				owner.Release(lease)
			}
		}()
	}
	if err != nil || lease == nil || ctx.Err() != nil {
		return InvestigationResult{}, false, nil
	}
	event.Decision = "membership_changed"
	if !workItemReuseMembershipEqual(candidate, census, current) {
		return InvestigationResult{}, false, nil
	}
	servingEvent := newWorkItemStoredServingEvent(StoredAnswerabilitySurfaceReuse, stored.SemanticStateRead, candidate)
	servingEvent.CensusRead = event.CensusRead
	servingEvent.Basis = "digest_matched"
	candidate = ServeWorkItemTupleCensus(candidate, census)
	servingErr = validateWorkItemStoredCoverage(candidate, &servingEvent)
	if e.telemetry != nil {
		e.telemetry.RecordWorkItemStoredServing(ctx, principal, servingEvent)
	}
	if servingErr != nil {
		event.Decision = "coverage_invalid"
		return candidate, true, servingErr
	}
	candidate.Reused = true
	event.Decision = "hit"
	if e.telemetry != nil {
		e.telemetry.RecordAnswerReuseServedRequestID(ctx, principal, candidate.RequestID, candidate.RequestID != request.RequestID)
	}
	// A reuse hit is a SETTLED admission decision too -- it serves
	// workItemTuple=true exactly as the fresh path's own settlement point
	// does (engine.go, the call beside workItemTupleStripSurveyObligations's
	// own doc comment), so it owes the same observable line and the same
	// strip, run here because this hit is this decision's only settlement
	// point: there is no later tighten call on this path to still reverse
	// it.
	var stripped []AnswerObligation
	if state := stored.SemanticState; state != nil && state.FramePresent && state.Frame != nil {
		stripped = workItemTupleStripSurveyObligations(state.Frame)
	}
	if e.telemetry != nil {
		e.telemetry.RecordWorkItemTupleAdmission(ctx, principal, WorkItemTupleAdmissionEvent{Admitted: true, StrippedObligations: stripped})
	}
	return candidate, true, nil
}

func workItemReuseMembershipEqual(candidate InvestigationResult, census *WorkItemTupleCensus, current WorkItemMembershipResult) bool {
	value := current.Census.AuthorizedPopulation
	if current.Census.State == WorkItemMembershipCensusFloor {
		value = WorkItemMembershipCensusLimit
	}
	if !current.Census.PopulationMeasured || current.Census.State != census.State || value != census.Value {
		return false
	}
	if current.Census.State == WorkItemMembershipCensusUnmeasured || len(current.Members) != census.Retained {
		return false
	}
	storedIDs := []string{}
	if candidate.Cohort != nil {
		for _, member := range candidate.Cohort.Members {
			storedIDs = append(storedIDs, member.Subject.CanonicalID)
		}
	}
	currentIDs := make([]string, 0, len(current.Members))
	for _, member := range current.Members {
		currentIDs = append(currentIDs, member.CanonicalID)
	}
	slices.Sort(storedIDs)
	slices.Sort(currentIDs)
	return len(storedIDs) == census.Retained && len(slices.Compact(slices.Clone(currentIDs))) == len(currentIDs) && slices.Equal(storedIDs, currentIDs)
}
