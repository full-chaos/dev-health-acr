package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// recordOutsideRequestedRepositories adds to outside the subjects a term
// matched that only the requested repository narrowing refused (CHAOS-12360,
// lead ruling): the narrowing stands, and a terminal result states the count so
// the conversation can go on. It returns outside, allocated on first use.
//
// Only an unrestricted principal is counted. Its grant already covers every
// subject, so the narrowing is the one thing that refused the node. For a
// restricted or project-only caller the count would be an existence oracle
// (repositoryNarrowedByUnrestrictedPrincipal), so it is never taken, not even
// for a subject inside its grant.
//
// A node counts exactly when it would have been offered without the narrowing:
// NodeCandidate without the requested scope refuses an invalid or bookkeeping
// node, ClassifyOffer a weak or vector-only hit, and the contest admission a
// kind the question does not ask for. A subject counts once, whatever the
// number of terms that find it. A truncated search makes the count a floor,
// which the sentence states truthfully.
func recordOutsideRequestedRepositories(principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, term string, results []CandidateNode, admission *contestAdmission, outside map[string]struct{}) map[string]struct{} {
	if !repositoryNarrowedByUnrestrictedPrincipal(principal, request) {
		return outside
	}
	for _, node := range results {
		if AuthorizedAttributes(principal, request.RequestedScope, node.Attributes) {
			continue
		}
		candidate, ok := NodeCandidate(principal, contextfabric.RequestedScope{}, term, node, deps.IsInternal, true, nil, "")
		if !ok || !ClassifyOffer(candidate).Admitted() || !admission.admits(candidate.Subject, sourceRetrieval) {
			continue
		}
		if outside == nil {
			outside = map[string]struct{}{}
		}
		outside[SubjectKey(candidate.Subject)] = struct{}{}
	}
	return outside
}
