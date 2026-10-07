package falkorgraph

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// anchorReachDeniedCount is the number of distinct subjects of the cohort kind
// that a committed anchor's walk reached only through an authorization denial,
// joined with the members the pool already counted as denied. The walk drops a
// denied endpoint before it becomes a pool node, so the pool count alone never
// sees it. A subject reached through an admitted edge is not denied, and one
// the pool and the walk both name counts once. Without a denied reach the
// pool's own count is returned unchanged. The second result is true when the
// walk itself reached a denied subject of the kind.
func anchorReachDeniedCount(kind contextfabric.SubjectKind, pool []graphrank.CandidateNode, principal storage.Principal, request contextfabric.GraphDiscoveryRequest, reachDenied map[string]struct{}, poolCount int) (int, bool) {
	denied := make(map[string]struct{})
	for uuid := range reachDenied {
		deniedKind, id := splitSubjectUUID(uuid)
		if contextfabric.SubjectKind(deniedKind) != kind || id == "" {
			continue
		}
		denied[uuid] = struct{}{}
	}
	if len(denied) == 0 {
		return poolCount, false
	}
	for _, node := range pool {
		subject, ok := graphrank.NodeSubject(node)
		if !ok || subject.Kind != kind {
			continue
		}
		if !graphrank.AuthorizedAttributes(principal, request.Request.RequestedScope, node.Attributes) {
			denied[subjectUUID(string(subject.Kind), subject.CanonicalID)] = struct{}{}
		}
	}
	return len(denied), true
}
