package contextfabric

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The work items of a named repository are the issues linked to the
// repository's pull requests: the entity tree Repository <> Pull request <>
// Issue <> Project, walked in the graph store from the repository to its
// issues (docs/design/context-fabric-architecture-diagrams.md §3). Which issues
// are members comes from the walk only. What is true of each member (its
// status, its completion) comes from the canonical facts, read for the walked
// members (TreeWorkItemFilter), never from a node's projected copy.

// TreeWorkItemWalk is one walk of the tree from a named anchor to its issues.
type TreeWorkItemWalk struct {
	// Members are the authorized issues, sorted by canonical id, at most the
	// requested limit.
	Members []SubjectRef
	// PullRequests is how many pull requests of the anchor the walk reached
	// and LinkedIssues how many distinct issues they link, both before
	// authorization.
	PullRequests, LinkedIssues int
	// Denied counts the links and issues the caller's authorization hid.
	Denied int
	// Truncated: the walk was cut at the limit or at its read bound; Members
	// is then a floor, not the population.
	Truncated bool
}

// TreeWorkItemGraph walks the entity tree in the graph store.
type TreeWorkItemGraph interface {
	// TreeWorkItemMembers walks from a named repository to its linked issues,
	// each passing the caller's authorization, at most limit members.
	TreeWorkItemMembers(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, scope RequestedScope, anchor SubjectRef, limit int) (TreeWorkItemWalk, error)
}

// TreeWorkItemFilterRequest asks which walked members match a member filter on
// their canonical facts.
type TreeWorkItemFilterRequest struct {
	Members                  []SubjectRef
	RequestedRepositoryScope []string
	// Status, when set, keeps the members whose current status is Status.
	Status string
	// CompletedStart and CompletedEnd, when both set, keep the members
	// completed in [CompletedStart, CompletedEnd).
	CompletedStart, CompletedEnd time.Time
}

// TreeWorkItemFilter reads the canonical facts of walked members.
type TreeWorkItemFilter interface {
	// FilterTreeWorkItems returns the canonical ids of the members that match,
	// sorted, and how many members it could not read (rejected or beyond a
	// read bound), which makes the kept set a floor.
	FilterTreeWorkItems(ctx context.Context, principal storage.Principal, request TreeWorkItemFilterRequest) (kept []string, unread int, err error)
}
