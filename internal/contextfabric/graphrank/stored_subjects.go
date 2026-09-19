package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// AuthorizeStoredSubjectNodes is the per-subject decision a stored result
// read takes: the SAME AuthorizedAttributes call every live read site makes on
// a keyed graph node, with no requested scope, because a caller re-reading a
// stored answer requests nothing narrower than its own grant.
//
// nodes holds the lookup of each subject in the caller's own organization
// graph, keyed by SubjectKey. A subject with a kind has at most one node. A
// subject the result names by canonical id alone (an empty kind) holds every
// node carrying that id, and is admitted only when every one of them admits
// the caller. A subject with no node cannot be proven visible and is reported
// absent. Backends supply the lookup; the decision itself lives only here, so
// a stored read can never apply a different rule than a live turn.
func AuthorizeStoredSubjectNodes(principal storage.Principal, subjects []contextfabric.SubjectRef, nodes map[string][]CandidateNode) []contextfabric.StoredSubjectOutcome {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index, subject := range subjects {
		found := nodes[SubjectKey(subject)]
		outcomes[index] = contextfabric.StoredSubjectAdmitted
		if len(found) == 0 {
			outcomes[index] = contextfabric.StoredSubjectAbsent
			continue
		}
		for _, node := range found {
			if !AuthorizedAttributes(principal, contextfabric.RequestedScope{}, node.Attributes) {
				outcomes[index] = contextfabric.StoredSubjectDenied
			}
		}
	}
	return outcomes
}
