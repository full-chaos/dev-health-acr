package falkorgraph

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// OwnershipRoutingOutcome is the closed outcome of one team-members discovery
// over a named anchor with respect to ownership routing.
type OwnershipRoutingOutcome string

const (
	// OwnershipRoutingNotRouted: the frame asks for the team members of a
	// named anchor and the ownership read did not run, because no committed
	// repository is the anchor.
	OwnershipRoutingNotRouted OwnershipRoutingOutcome = "not_routed"
	// OwnershipRoutingOwners: the ownership read found at least one team that
	// owns the repository.
	OwnershipRoutingOwners OwnershipRoutingOutcome = "owners"
	// OwnershipRoutingNoOwner: the ownership read ran and no team owns the
	// repository.
	OwnershipRoutingNoOwner OwnershipRoutingOutcome = "no_owner"
	// OwnershipRoutingReadFailed: the ownership read failed and the call ends.
	OwnershipRoutingReadFailed OwnershipRoutingOutcome = "read_failed"
)

// OwnershipRoutingOutcomeVocabulary returns every declared outcome, in
// declaration order.
func OwnershipRoutingOutcomeVocabulary() []OwnershipRoutingOutcome {
	return []OwnershipRoutingOutcome{OwnershipRoutingNotRouted, OwnershipRoutingOwners, OwnershipRoutingNoOwner, OwnershipRoutingReadFailed}
}

// OwnershipRoutingDecision is one decision line of ownership routing: counts
// and closed values only, never a name or an id.
type OwnershipRoutingDecision struct {
	Outcome     OwnershipRoutingOutcome
	AnchorKind  contextfabric.SubjectKind
	AnchorBasis AnchorBasis
	// Committed is how many subjects the resolution committed.
	Committed int
	// Census is how many teams the ownership read returned and Owners how
	// many of them own the repository, before the caller's authorization;
	// Truncated reports a cut read. They describe a read that ran.
	Census, Owners int
	Truncated      bool
	// Err is the failed read.
	Err error
}

func ownershipRoutingOutcome(owners int, err error) OwnershipRoutingOutcome {
	switch {
	case err != nil:
		return OwnershipRoutingReadFailed
	case owners > 0:
		return OwnershipRoutingOwners
	default:
		return OwnershipRoutingNoOwner
	}
}
