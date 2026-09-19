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
// nodes holds the keyed lookup of each subject in the caller's own
// organization graph (SubjectKey -> node). A subject with no node cannot be
// proven visible and is reported absent; the caller decides what absent means.
// Backends supply the lookup; the decision itself lives only here, so a stored
// read can never apply a different rule than a live turn.
func AuthorizeStoredSubjectNodes(principal storage.Principal, subjects []contextfabric.SubjectRef, nodes map[string]CandidateNode) []contextfabric.StoredSubjectOutcome {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index, subject := range subjects {
		node, found := nodes[SubjectKey(subject)]
		switch {
		case !found:
			outcomes[index] = contextfabric.StoredSubjectAbsent
		case AuthorizedAttributes(principal, contextfabric.RequestedScope{}, node.Attributes):
			outcomes[index] = contextfabric.StoredSubjectAdmitted
		default:
			outcomes[index] = contextfabric.StoredSubjectDenied
		}
	}
	return outcomes
}
