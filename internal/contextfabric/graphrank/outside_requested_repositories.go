package graphrank

import (
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
// Only an unrestricted principal is counted. Its grant already covers every
// subject, so the narrowing is the one thing that refused the node. For a
// restricted or project-only caller the count would be an existence oracle
// (repositoryNarrowedByUnrestrictedPrincipal), so it is never taken, not even
// for a subject inside its grant.
//
// A node counts when the offer rule would admit it without the narrowing:
// NodeCandidate without the requested scope refuses an invalid or bookkeeping
// node, ClassifyOffer a weak or vector-only hit, and the contest admission a
// kind the question does not ask for. A kind-scoped semantic offer is not
// counted: a similarity hit is not a match of the name. A subject counts once,
// however many arms and terms find it. A truncated search makes the count a
// floor, which the sentence states truthfully.
func recordOutsideRequestedRepository(principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, term string, node CandidateNode, allowExactMatch bool, admission *contestAdmission) {
	if deps.outsideRequested == nil || !repositoryNarrowedByUnrestrictedPrincipal(principal, request) {
		return
	}
	candidate, ok := NodeCandidate(principal, contextfabric.RequestedScope{}, term, node, deps.IsInternal, allowExactMatch, nil, "")
	if !ok || !ClassifyOffer(candidate).Admitted() || !admission.admits(candidate.Subject, sourceRetrieval) {
		return
	}
	deps.outsideRequested[SubjectKey(candidate.Subject)] = struct{}{}
}
