package falkorgraph

import (
	"context"

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

// ownershipZeroOwnersAreAnAnswer reports whether an ownership read that named
// no owner states a population of zero rather than a gap. It does only when
// the read was not cut, found no owner, and the census
// holds teams at all: a census with no team is ownership that was never
// projected, which says nothing about this repository.
func ownershipZeroOwnersAreAnAnswer(owners, census int, truncated bool) bool {
	return !truncated && owners == 0 && census > 0
}

// repositoryOwnerExclusions names the teams the repository's ownership edges
// name that the count leaves out: a team the source marks inactive, and a team
// whose assertion has ended. current is the set of owners now.
func (a *Adapter) repositoryOwnerExclusions(ctx context.Context, key, orgID, repositoryID string, current map[string]bool) ([]contextfabric.CohortExclusion, error) {
	hits, _, err := a.walkStepHits(ctx, key, orgID, []string{repositoryID}, repositoryOwnersStep, temporalFilter{}, exactNameCandidateQueryLimit)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []contextfabric.CohortExclusion
	for _, h := range hits {
		id := canonicalIDOf(h.to)
		if id == "" || seen[id] {
			continue
		}
		reason := ""
		switch {
		case inactiveTeamNode(h.to):
			reason = contractsv1.CohortExclusionReasonInactiveTeam
		case !current[id]:
			reason = contractsv1.CohortExclusionReasonEndedAssertion
		default:
			continue
		}
		seen[id] = true
		out = append(out, contextfabric.CohortExclusion{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: id, Label: propStringValue(h.to.Properties[propLabel])},
			Reason:  reason,
		})
	}
	return out, nil
}

// ownerExclusionsOutsideMembers drops an exclusion that names a team the
// cohort carries, so no team is both counted and excluded.
func ownerExclusionsOutsideMembers(exclusions []contextfabric.CohortExclusion, members []contextfabric.CohortMember) []contextfabric.CohortExclusion {
	out := []contextfabric.CohortExclusion{}
	for _, e := range exclusions {
		kept := true
		for _, m := range members {
			if m.Subject.Kind == e.Subject.Kind && m.Subject.CanonicalID == e.Subject.CanonicalID {
				kept = false
			}
		}
		if kept {
			out = append(out, e)
		}
	}
	return out
}
