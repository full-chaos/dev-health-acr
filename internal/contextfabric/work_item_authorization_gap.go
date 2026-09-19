package contextfabric

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// workItemAuthorizationGapPrefix opens every authorization-gap limitation, so
// a stored answer can be recognised without a second persisted field.
const workItemAuthorizationGapPrefix = "Work items exist in this project that are outside this principal's authorized scope"

// workItemAuthorizationGap is the measured S1 partition between the members
// the principal may read and the members it may not. It is carried beside a
// census for one request and is never persisted.
type workItemAuthorizationGap struct {
	State      WorkItemMembershipCensusState
	Observed   int
	Authorized int
	Denied     int
}

// workItemAuthorizationGapOf returns the gap for a measured census that has
// denied members, and false when nothing was denied or nothing was measured.
func workItemAuthorizationGapOf(census WorkItemMembershipCensus) (workItemAuthorizationGap, bool) {
	if !census.PopulationMeasured || census.State == WorkItemMembershipCensusUnmeasured || census.DeniedPopulation <= 0 {
		return workItemAuthorizationGap{}, false
	}
	return workItemAuthorizationGap{State: census.State, Observed: census.CappedPopulation, Authorized: census.AuthorizedPopulation, Denied: census.DeniedPopulation}, true
}

// NoneAuthorized reports the case where every observed member is denied.
func (g workItemAuthorizationGap) NoneAuthorized() bool { return g.Authorized == 0 }

// Limitation is the answer-facing disclosure of the partition.
func (g workItemAuthorizationGap) Limitation() string {
	if g.NoneAuthorized() {
		return fmt.Sprintf("%s: %d work items were observed and none are authorized, so no work item can be listed or counted.", workItemAuthorizationGapPrefix, g.Denied)
	}
	if g.State == WorkItemMembershipCensusFloor {
		return fmt.Sprintf("%s: the census stopped at its bound, with at least %d work items authorized and at least %d more denied and not counted.", workItemAuthorizationGapPrefix, g.Authorized, g.Denied)
	}
	return fmt.Sprintf("%s: %d work items are authorized and %d more are denied and are not counted.", workItemAuthorizationGapPrefix, g.Authorized, g.Denied)
}

func hasWorkItemAuthorizationGapLimitation(limitations []string) bool {
	for _, limitation := range limitations {
		if strings.HasPrefix(limitation, workItemAuthorizationGapPrefix) {
			return true
		}
	}
	return false
}

// applyWorkItemAuthorizationGap discloses the partition on the served answer.
// A project whose members are all denied is served degraded, never as an
// empty project.
func applyWorkItemAuthorizationGap(candidate InvestigationResult, gap workItemAuthorizationGap) InvestigationResult {
	composed, displaced := appendBoundedLimitations(candidate.Limitations, []string{gap.Limitation()})
	candidate.Limitations = composed
	candidate.LimitationsDisplaced += displaced
	if gap.NoneAuthorized() {
		candidate.Status = InvestigationDegraded
	}
	return candidate
}

// WorkItemAuthorizationGapEvent is the settled disclosure decision with the
// census it was made from and the shape that was served.
type WorkItemAuthorizationGapEvent struct {
	Reason            string
	CensusState       WorkItemMembershipCensusState
	Observed          int
	Authorized        int
	Denied            int
	ServedStatus      InvestigationStatus
	ServedMembers     int
	LimitationPresent bool
}

func newWorkItemAuthorizationGapEvent(census *WorkItemTupleCensus, served InvestigationResult) (WorkItemAuthorizationGapEvent, bool) {
	if census == nil || census.gap == nil {
		return WorkItemAuthorizationGapEvent{}, false
	}
	reason := "partially_authorized"
	if census.gap.NoneAuthorized() {
		reason = "none_authorized"
	}
	return WorkItemAuthorizationGapEvent{
		Reason: reason, CensusState: census.gap.State,
		Observed: census.gap.Observed, Authorized: census.gap.Authorized, Denied: census.gap.Denied,
		ServedStatus: served.Status, ServedMembers: cohortMemberCount(served.Cohort),
		LimitationPresent: hasWorkItemAuthorizationGapLimitation(served.Limitations),
	}, true
}

// recordWorkItemAuthorizationGap emits the settled disclosure decision for the
// census that was measured on this request, on every path that measures one.
func (e *Engine) recordWorkItemAuthorizationGap(ctx context.Context, principal storage.Principal, census *WorkItemTupleCensus, served InvestigationResult) {
	if e.telemetry == nil {
		return
	}
	if event, ok := newWorkItemAuthorizationGapEvent(census, served); ok {
		e.telemetry.RecordWorkItemAuthorizationGap(ctx, principal, event)
	}
}
