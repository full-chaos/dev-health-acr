package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// recordOutsideRequestedRepository records a node authorization refused, when
// only the requested repository narrowing refused it (CHAOS-12360, lead ruling):
// the narrowing stands, and a terminal result states the count so the
// conversation can go on. mergeSearchResults calls it, because every arm that
// finds a term's subject goes through that merge: ordinary search, a keyed
// alias, the kind-scoped search and the exact-name arm.
//
// The count holds only nodes the caller's own grant admits (CHAOS-12446, lead
// ruling (a), FINAL). A node outside the grant never counts: for it the count
// would be an existence oracle. A node inside the grant is no oracle, because
// the caller may read that subject, so the narrowing is the one thing that
// refused it. This is what reaches a web user: a user-class credential is never
// unrestricted at runtime, because the access read turns "*" into the live
// inventory on every call (auth.NarrowRepositoryScopes).
//
// A node counts when the offer rule would admit it without the narrowing:
// NodeCandidate without the requested scope applies the caller's grant and
// refuses an invalid or bookkeeping node, ClassifyOffer a weak or vector-only
// hit, and the contest admission a kind the question does not ask for. A kind-scoped semantic offer is not
// counted: a similarity hit is not a match of the name. A subject counts once,
// however many arms and terms find it. A truncated search makes the count a
// floor, which the sentence states truthfully.
func recordOutsideRequestedRepository(principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, term string, node CandidateNode, allowExactMatch bool, admission *contestAdmission) {
	if deps.outsideRequested == nil || !outsideRequestedRepositoriesApplies(principal, request) {
		return
	}
	candidate, ok := NodeCandidate(principal, contextfabric.RequestedScope{}, term, node, deps.IsInternal, allowExactMatch, nil, "")
	if !ok || !ClassifyOffer(candidate).Admitted() || !admission.admits(candidate.Subject, sourceRetrieval) {
		return
	}
	deps.outsideRequested[SubjectKey(candidate.Subject)] = struct{}{}
}

// outsideRequestedRepositoriesApplies reports whether a node the grant admits
// and the request refuses can only be outside the requested repositories, not
// outside a requested project or team. A project-only caller is never counted.
func outsideRequestedRepositoriesApplies(principal storage.Principal, request contextfabric.InvestigationRequest) bool {
	return auth.ClassifyGrant(principal) != auth.GrantProjectOnly &&
		len(request.RequestedScope.ProjectIDs) == 0 &&
		len(request.RequestedScope.TeamIDs) == 0
}
