package falkorgraph

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

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

// repositoryOwnersStep reads the teams a repository's ownership edges name.
var repositoryOwnersStep = walkStep{
	fromKind: contractsv1.ContextFabricSubjectRepository, toKind: contractsv1.ContextFabricSubjectTeam,
	relation: contractsv1.ContextFabricRelationshipOwnedByTeam, direction: walkOut,
}

// repositoryOwningTeams returns the canonical ids of the teams the
// repository's OWNED_BY_TEAM edges name, bounded as the ownership census is;
// a cut read is reported.
func (a *Adapter) repositoryOwningTeams(ctx context.Context, key, orgID, repositoryID string, temporal temporalFilter) (map[string]bool, bool, error) {
	hits, cut, err := a.walkStepHits(ctx, key, orgID, []string{repositoryID}, repositoryOwnersStep, temporal, exactNameCandidateQueryLimit)
	if err != nil {
		return nil, false, err
	}
	owners := make(map[string]bool, len(hits))
	for _, h := range hits {
		owners[canonicalIDOf(h.to)] = true
	}
	return owners, cut, nil
}

// currentOwnership is the window ownership edges are read under: the
// question's window, or the adapter clock for a question about now. An
// ownership edge carries the period it held; an ended one is history, and a
// team that owned a repository once does not own it now.
func currentOwnership(temporal temporalFilter, now time.Time) temporalFilter {
	if temporal.active {
		return temporal
	}
	return newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &now})
}
