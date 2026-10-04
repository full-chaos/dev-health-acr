package contextfabric

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TreeWorkItemMembership is the membership read of a repository anchor,
// composed on the two tree ports. Which issues are members comes from the
// graph walk only (TreeWorkItemGraph); the canonical facts (TreeWorkItemFilter)
// only qualify the walked members by status or completion. It gives the engine
// the same result a project anchor's S1 read gives (WorkItemMembershipResult)
// and holds the same bounded admission lease for the response.
type TreeWorkItemMembership struct {
	graph  TreeWorkItemGraph
	filter TreeWorkItemFilter
	gate   *WorkItemMembershipGate
}

// NewTreeWorkItemMembership composes the repository membership read. A nil
// graph, or a nil gate, gives a read that always measures nothing; a nil filter
// does the same for a filtered read only.
func NewTreeWorkItemMembership(graph TreeWorkItemGraph, filter TreeWorkItemFilter, gate *WorkItemMembershipGate) *TreeWorkItemMembership {
	return &TreeWorkItemMembership{graph: graph, filter: filter, gate: gate}
}

var errTreeWorkItemMembershipUnavailable = errors.New("repository work item membership is not available")

// Begin walks from the repository anchor to its linked issues and returns the
// measured membership. The lease is held until the response finishes, as the
// project read's is. A failure returns an unmeasured census with a closed
// reason, never an empty success.
func (m *TreeWorkItemMembership) Begin(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, scope RequestedScope, request WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
	if m == nil || m.graph == nil || m.gate == nil {
		return nil, treeUnmeasured(WorkItemMembershipUnmeasuredS1Error), errTreeWorkItemMembershipUnavailable
	}
	lease, err := m.gate.Acquire(ctx)
	if err != nil {
		return nil, treeUnmeasured(treeUnmeasuredReason(ctx)), err
	}
	result, err := m.read(ctx, principal, binding, scope, request)
	if err != nil {
		return lease, treeUnmeasured(treeUnmeasuredReason(ctx)), err
	}
	return lease, result, nil
}

func treeUnmeasuredReason(ctx context.Context) WorkItemMembershipUnmeasuredReason {
	if ctx.Err() != nil {
		return WorkItemMembershipUnmeasuredCancelled
	}
	return WorkItemMembershipUnmeasuredS1Error
}

func treeUnmeasured(reason WorkItemMembershipUnmeasuredReason) WorkItemMembershipResult {
	return WorkItemMembershipResult{Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusUnmeasured, CensusLimit: WorkItemMembershipCensusLimit, UnmeasuredReason: reason, Limitation: WorkItemMembershipLimitation()}}
}

func (m *TreeWorkItemMembership) read(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, scope RequestedScope, request WorkItemMembershipRequest) (WorkItemMembershipResult, error) {
	anchor := request.Anchor.Subject
	if anchor.Kind != SubjectRepository {
		return WorkItemMembershipResult{}, errors.New("repository work item membership anchor must be a repository")
	}
	// One past the census limit, so a population above it is told from one at it.
	walk, err := m.graph.TreeWorkItemMembers(ctx, principal, binding, scope, anchor, WorkItemMembershipCensusLimit+1)
	if err != nil {
		return WorkItemMembershipResult{}, err
	}
	overLimit := len(walk.Members) > WorkItemMembershipCensusLimit
	walked := walk.Members
	if overLimit {
		walked = walked[:WorkItemMembershipCensusLimit]
	}
	incomplete := walk.Truncated && !overLimit
	filtered := request.Status != "" || request.TimeColumn != ""

	members := make([]TreeWorkItemMember, 0, len(walked))
	if filtered {
		if m.filter == nil {
			return WorkItemMembershipResult{}, errTreeWorkItemMembershipUnavailable
		}
		if request.TimeColumn != "" && request.TimeColumn != "completed_at" {
			return WorkItemMembershipResult{}, errors.New("repository work items are filtered on completion only")
		}
		filterRequest := TreeWorkItemFilterRequest{RequestedRepositoryScope: slices.Clone(request.RequestedRepositoryScope), Status: request.Status}
		if request.TimeColumn == "completed_at" {
			filterRequest.CompletedStart, filterRequest.CompletedEnd = request.TimeStart, request.TimeEnd
		}
		for _, member := range walked {
			filterRequest.Members = append(filterRequest.Members, member.Subject)
		}
		kept, unread, err := m.filter.FilterTreeWorkItems(ctx, principal, filterRequest)
		if err != nil {
			return WorkItemMembershipResult{}, err
		}
		keptSet := make(map[string]struct{}, len(kept))
		for _, id := range kept {
			keptSet[id] = struct{}{}
		}
		for _, member := range walked {
			if _, ok := keptSet[member.Subject.CanonicalID]; ok {
				members = append(members, member)
			}
		}
		// A filtered answer cannot name a population it did not read in full.
		incomplete = incomplete || overLimit || unread > 0
		overLimit = false
	} else {
		members = walked
	}

	// The members handed on are the serving cap's, in canonical id order, as
	// the project read hands on its own; the census counts every authorized
	// member read.
	slices.SortFunc(members, func(a, b TreeWorkItemMember) int {
		return strings.Compare(a.Subject.CanonicalID, b.Subject.CanonicalID)
	})
	served := workItemTupleSelectionCap(request.PlanMaxMembers, request.RequestMaxMembers)
	authorized := 0
	out := WorkItemMembershipResult{Members: make([]WorkItemMembershipMember, 0, min(len(members), served))}
	for _, member := range members {
		segments, ok := identity.Segments(identity.KindWorkItem, member.Subject.CanonicalID)
		if !ok || len(segments) != 2 {
			// An identity the engine cannot serve is an unread member.
			incomplete = true
			continue
		}
		// RepoSlug stays empty: the engine reads neither it nor the repository
		// id of a member, and the walk carries no repository name for an
		// issue, so none is invented.
		authorized++
		if len(out.Members) >= served {
			continue
		}
		out.Members = append(out.Members, WorkItemMembershipMember{CanonicalID: member.Subject.CanonicalID, RepoID: segments[0], WorkItemID: segments[1], LinkTier: member.Tier})
	}
	census := WorkItemMembershipCensus{
		State: WorkItemMembershipCensusExact, PopulationMeasured: true, PopulationComplete: !incomplete && !overLimit, PopulationIncomplete: incomplete || overLimit,
		AuthorizedPopulation: authorized, ServedMembers: len(out.Members), CensusLimit: WorkItemMembershipCensusLimit,
		RepositoryPullRequests: walk.PullRequests, RepositoryLinkedIssues: walk.LinkedIssues,
	}
	if overLimit {
		census.State = WorkItemMembershipCensusFloor
	}
	if !filtered {
		census.DeniedPopulation = walk.Denied
	}
	census.CappedPopulation = census.AuthorizedPopulation + census.DeniedPopulation
	out.Census = census
	return out, nil
}
