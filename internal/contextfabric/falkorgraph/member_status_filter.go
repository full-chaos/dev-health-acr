package falkorgraph

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// memberStatusFilter is the outcome of filtering one member pool by the status
// of its project or incident members.
type memberStatusFilter struct {
	nodes []graphrank.CandidateNode
	// candidates and kept count the pool's members of the kind before and after.
	candidates, kept int
	// outsideSet counts the candidates whose stored status is outside the
	// kind's closed set (or has none): none of them can match, and the answer
	// says how many.
	outsideSet int
}

// memberStatusNoMatchRationale is the rationale of a member set in which no
// member reached holds the status asked for.
func memberStatusNoMatchRationale(kind contextfabric.SubjectKind) string {
	return "No " + string(kind) + " reached from the subject the question names holds the status asked for now; that is a count of matches, not a statement about health."
}

// filterMembersByStoredStatus keeps the pool's nodes that are not of kind and
// the members of kind whose stored status is status now. The status is read
// from each member's stored node, as of now whatever window the question
// states. A stored status outside the kind's closed set matches no status of
// the set and is counted.
func (a *Adapter) filterMembersByStoredStatus(ctx context.Context, key, orgID string, kind contextfabric.SubjectKind, status string, pool []graphrank.CandidateNode) (memberStatusFilter, error) {
	out := memberStatusFilter{}
	ids := []string{}
	for _, n := range pool {
		if subject, ok := graphrank.NodeSubject(n); ok && subject.Kind == kind {
			ids = append(ids, subject.CanonicalID)
		}
	}
	out.candidates = len(ids)
	if len(ids) == 0 {
		out.nodes = pool
		return out, nil
	}
	property := contextfabric.MemberStatusStoredProperty(kind)
	stored, err := a.readStoredNodeProperty(ctx, key, orgID, kind, storedNodePropertyCypher(property), ids, property)
	if err != nil {
		return out, err
	}
	out.nodes = make([]graphrank.CandidateNode, 0, len(pool))
	for _, n := range pool {
		subject, ok := graphrank.NodeSubject(n)
		if !ok || subject.Kind != kind {
			out.nodes = append(out.nodes, n)
			continue
		}
		value := contextfabric.NormalizeStoredMemberStatus(stored[subject.CanonicalID])
		switch {
		case !contextfabric.InMemberStatusVocabulary(kind, value):
			out.outsideSet++
		case value == status:
			out.nodes = append(out.nodes, n)
			out.kept++
		}
	}
	return out, nil
}
