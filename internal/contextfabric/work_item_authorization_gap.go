package contextfabric

import (
	"context"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// workItemAuthorizationGapPrefix opens every authorization-gap limitation, so
// a stored answer can be recognised without a second persisted field. The
// repository form names the anchor a repository-anchored answer was read on.
const (
	workItemAuthorizationGapPrefix           = "Work items exist in this project that are outside this principal's authorized scope"
	workItemRepositoryAuthorizationGapPrefix = "Work items exist in this repository that are outside this principal's authorized scope"
)

// workItemAuthorizationGap is the measured S1 partition between the members
// the principal may read and the members it may not. It is carried beside a
// census for one request and is never persisted.
type workItemAuthorizationGap struct {
	AnchorKind SubjectKind
	State      WorkItemMembershipCensusState
	Observed   int
	Authorized int
	Denied     int
	// Incomplete: the census did not read every member (a cut walk or an
	// unread member below the bound), so the counts are lower bounds and the
	// sentence says "at least", as a floor census does.
	Incomplete bool
}

// workItemAuthorizationGapOf returns the gap for a measured census that has
// denied members, and false when nothing was denied or nothing was measured.
func workItemAuthorizationGapOf(census WorkItemMembershipCensus, anchorKind SubjectKind) (workItemAuthorizationGap, bool) {
	if !census.PopulationMeasured || census.State == WorkItemMembershipCensusUnmeasured || census.DeniedPopulation <= 0 {
		return workItemAuthorizationGap{}, false
	}
	return workItemAuthorizationGap{AnchorKind: anchorKind, State: census.State, Observed: census.CappedPopulation, Authorized: census.AuthorizedPopulation, Denied: census.DeniedPopulation, Incomplete: census.PopulationIncomplete}, true
}

// NoneAuthorized reports the case where every observed member is denied.
func (g workItemAuthorizationGap) NoneAuthorized() bool { return g.Authorized == 0 }

// Limitation is the answer-facing disclosure of the partition.
func (g workItemAuthorizationGap) Limitation() string {
	return contractsv1.ContextFabricWorkItemAuthorizationGapLimitation(g.AnchorKind == SubjectRepository, g.Authorized, g.Denied, g.State == WorkItemMembershipCensusFloor || g.Incomplete)
}

func hasWorkItemAuthorizationGapLimitation(limitations []string) bool {
	for _, limitation := range limitations {
		if strings.HasPrefix(limitation, workItemAuthorizationGapPrefix) || strings.HasPrefix(limitation, workItemRepositoryAuthorizationGapPrefix) {
			return true
		}
	}
	return false
}

// applyWorkItemAuthorizationGap discloses the partition on the served answer.
// A full list made only of gap-shaped sentences cannot be told apart from the
// measured one by shape, so one of them is model text the engine gives way to:
// the measured sentence is composed here and must always be present.
// A project whose members are all denied is served degraded, never as an
// empty project.
func applyWorkItemAuthorizationGap(candidate InvestigationResult, gap workItemAuthorizationGap) InvestigationResult {
	own := gap.Limitation()
	limitations := candidate.Limitations
	if len(limitations) >= contractsv1.ContextFabricLimitationsMaxCount && !alreadyStates(limitations, own) && lastModelAuthoredLimitation(limitations) < 0 {
		for i := len(limitations) - 1; i >= 0; i-- {
			if contractsv1.IsContextFabricWorkItemAuthorizationGapLimitation(limitations[i]) {
				limitations = append(append([]string(nil), limitations[:i]...), limitations[i+1:]...)
				candidate.LimitationsDisplaced++
				break
			}
		}
	}
	composed, displaced := appendBoundedLimitations(limitations, []string{own})
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
