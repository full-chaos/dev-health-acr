package contextfabric

import (
	"context"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The work items of a named repository are the issues linked to the
// repository's pull requests: the entity tree Repository <> Pull request <>
// Issue <> Project, walked in the graph store from the repository to its
// issues (docs/design/context-fabric-architecture-diagrams.md §3). Which issues
// are members comes from the walk only. What is true of each member (its
// status, its completion) comes from the canonical facts, read for the walked
// members (TreeWorkItemFilter), never from a node's projected copy.

// Link tiers of the issue <> pull request link of record, strongest first.
// The tier that linked a member is named in its inclusion reason; a member
// linked only by the heuristic tier is never presented as natively linked.
const (
	TreeLinkTierNative       = contractsv1.EvidenceProvenanceNative
	TreeLinkTierExplicitText = contractsv1.EvidenceProvenanceExplicitText
	TreeLinkTierHeuristic    = contractsv1.EvidenceProvenanceHeuristic
)

// CrossedPullRequests counts, per lower link tier, the distinct pull requests
// one walk crossed while a requested project narrowing applied and their stated
// membership stands on that tier (CHAOS-12308). A walk line count only: the
// answer's count is the graph_answer_pull_requests_on_*_links coverage detail,
// which counts cohort members and edge endpoints of the general read.
type CrossedPullRequests struct {
	ExplicitText, Heuristic int
}

// TreeWorkItemMember is one walked issue and the strongest tier of the
// authorized links that reached it.
type TreeWorkItemMember struct {
	Subject SubjectRef
	Tier    string
}

// TreeWorkItemWalk is one walk of the tree from a named anchor to its issues.
type TreeWorkItemWalk struct {
	// Members are the authorized issues, sorted by canonical id, at most the
	// requested limit. When the walk is cut, the members kept are those of
	// the strongest links first.
	Members []TreeWorkItemMember
	// PullRequests is how many pull requests of the anchor the walk reached
	// and LinkedIssues how many distinct issues they link, both before
	// authorization (for a restricted caller LinkedIssues is a bounded count
	// with no grant clause). Neither is served to a restricted caller.
	PullRequests, LinkedIssues int
	// Denied counts the distinct linked issues no authorized link reached:
	// issues, never link rows, so an issue admitted through one link is not
	// denied for another.
	Denied int
	// Truncated: the walk was cut at the limit or at its read bound; Members
	// is then a floor, not the population.
	Truncated bool
	// ProjectNarrowingUnverified: a requested project narrowing admitted a
	// member only on its "*" project list (CHAOS-12229); the answer says so.
	ProjectNarrowingUnverified bool
	// AnchorOutsideTeamNarrowing: the request narrows by team and the anchor
	// is not currently owned by a requested team (CHAOS-12319). The members
	// are then only the issues whose own project a requested team owns, and
	// the anchor's own totals are not the team's.
	AnchorOutsideTeamNarrowing bool
	// TeamNarrowing is the rule that decided a requested team narrowing
	// (empty without one), TeamExcluded how many distinct issues it left out
	// of scope, and TeamFloor why it floored the walk (empty when it did
	// not). The repository walk Info line carries them (CHAOS-12319).
	TeamNarrowing TeamNarrowingBasis
	TeamExcluded  int
	TeamFloor     TeamNarrowingFloor
	// ProjectExcluded counts the distinct linked issues a requested project
	// narrowing left out of scope (CHAOS-12329). Never in Denied, which is
	// only what the caller's grants hid.
	ProjectExcluded int
	// Crossed: the pull requests this walk crossed on a lower link tier.
	Crossed CrossedPullRequests
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

// TeamNarrowingBasis is the rule that decided one walk's requested team
// narrowing (CHAOS-12319): (i) the anchor is owned by a requested team, so
// every walked issue is in scope; (ii) it is not, so each issue's own project
// decides.
type TeamNarrowingBasis string

const (
	TeamNarrowingAnchorOwned  TeamNarrowingBasis = "anchor_owned"
	TeamNarrowingIssueProject TeamNarrowingBasis = "issue_project"
)

// TeamNarrowingBasisVocabulary is the closed set of the walk line's
// team_narrowing field.
func TeamNarrowingBasisVocabulary() []string {
	return []string{string(TeamNarrowingAnchorOwned), string(TeamNarrowingIssueProject)}
}

// TeamNarrowingFloor is why a requested team narrowing floored the walk: the
// ownership it needed to decide an issue the request admits was missing.
type TeamNarrowingFloor string

const (
	TeamNarrowingFloorNone TeamNarrowingFloor = ""
	// TeamNarrowingFloorAnchorOwnershipUnknown: the anchor has no current
	// owner, so (i) could not be decided.
	TeamNarrowingFloorAnchorOwnershipUnknown TeamNarrowingFloor = "anchor_ownership_unknown"
	// TeamNarrowingFloorProjectOwnerAbsent: an issue left out has a project
	// with no current owner.
	TeamNarrowingFloorProjectOwnerAbsent TeamNarrowingFloor = "project_owner_absent"
	// TeamNarrowingFloorOwnershipReadCut: an ownership read was bounded.
	TeamNarrowingFloorOwnershipReadCut TeamNarrowingFloor = "ownership_read_cut"
)

// TeamNarrowingFloorVocabulary is the closed set of the walk line's
// team_floor_reason field.
func TeamNarrowingFloorVocabulary() []string {
	return []string{string(TeamNarrowingFloorNone), string(TeamNarrowingFloorAnchorOwnershipUnknown), string(TeamNarrowingFloorProjectOwnerAbsent), string(TeamNarrowingFloorOwnershipReadCut)}
}
