package falkorgraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var _ contextfabric.ServedEdgeReader = (*Adapter)(nil)

// servedEdgeReadLimit bounds the one read of ServedEdgeEvidence below
// FalkorDB's result-set size, which drops rows past it with no error. A read
// that reaches it is reported Cut, and proves nothing.
const servedEdgeReadLimit = falkorResultSetSize - 1

// ServedEdgeEvidence implements contextfabric.ServedEdgeReader (CHAOS-12537).
//
// It reads every relationship between the endpoints of the given edges, in the
// stored direction, in one keyed read with the edge's valid-time predicate, and
// then sends each one through the walk's own code path: resolveEdge (the edge
// gate, both endpoint reads under the same window, both endpoint gates) and
// graphrank.AdmitEdges (the same exclusions and the same evidence closure). No
// gate is copied here. What differs from the walk is only what the walk adds
// and the recheck does not need: the per-origin edge budget, hop two, the text
// arm and ranking. An edge the walk's budget would have cut is therefore proven
// here, and that is the one difference this read allows.
//
// It runs under the caller's project reach, like DiscoverContext and
// RemovedTeams. An endpoint read that fails is an error, never a quiet miss.
func (a *Adapter) ServedEdgeEvidence(ctx context.Context, principal storage.Principal, request contextfabric.GraphDiscoveryRequest, edges []contextfabric.RelationshipEdge) (contextfabric.ServedEdgeProof, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return contextfabric.ServedEdgeProof{}, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.ServedEdgeProof{}, err
	}
	pairs := servedEdgePairs(edges)
	if len(pairs) == 0 {
		return contextfabric.ServedEdgeProof{}, nil
	}
	key, err := a.effectiveKey(ctx, orgID, request.Binding)
	if err != nil {
		return contextfabric.ServedEdgeProof{}, err
	}
	ctx, finish, err := a.withProjectReach(ctx, key, principal)
	if err != nil {
		return contextfabric.ServedEdgeProof{}, err
	}
	defer finish()
	temporal := newTemporalFilter(request.Interpretation.TimeContext)
	rows, err := a.api.query(ctx, key, servedEdgesCypher(temporal), temporal.bind(map[string]interface{}{
		"org": orgID, "pairs": pairs, "lim": servedEdgeReadLimit + 1,
	}), true)
	if err != nil {
		return contextfabric.ServedEdgeProof{}, safeDependencyError("read served edges", err)
	}
	if len(rows) > servedEdgeReadLimit {
		return contextfabric.ServedEdgeProof{Cut: true}, nil
	}
	scope := request.Request.RequestedScope
	resolved := make([]graphrank.ResolvedEdge, 0, len(rows))
	for _, candidate := range nodeEdgesFromRows(rows) {
		edge, resolution, _, err := a.resolveEdge(ctx, key, orgID, principal, scope, candidate, temporal)
		if err != nil {
			return contextfabric.ServedEdgeProof{}, err
		}
		if resolution == edgeAdmitted {
			resolved = append(resolved, edge)
		}
	}
	admission := graphrank.AdmitEdges(principal.OrgID, resolved, request.Request.Options, isInternalSubject)
	return contextfabric.ServedEdgeProof{Context: contextfabric.GraphContext{
		Paths: admission.Paths, DriverCandidates: admission.Drivers, EvidenceRefIDs: admission.EvidenceRefIDs,
	}}, nil
}

// servedEdgePairs is the distinct (from, to) endpoint pairs of the edges, in
// the stored direction, as the bound list of servedEdgesCypher.
func servedEdgePairs(edges []contextfabric.RelationshipEdge) []interface{} {
	seen := make(map[[4]string]struct{}, len(edges))
	pairs := make([]interface{}, 0, len(edges))
	for _, edge := range edges {
		pair := [4]string{string(edge.From.Kind), edge.From.CanonicalID, string(edge.To.Kind), edge.To.CanonicalID}
		if pair[0] == "" || pair[1] == "" || pair[2] == "" || pair[3] == "" {
			continue
		}
		if _, ok := seen[pair]; ok {
			continue
		}
		seen[pair] = struct{}{}
		pairs = append(pairs, map[string]interface{}{"fk": pair[0], "fi": pair[1], "tk": pair[2], "ti": pair[3]})
	}
	return pairs
}

// servedEdgesCypher reads the relationships of each bound pair, source to
// target, in the caller's organization graph, with the edge's valid-time
// predicate. The endpoints' own windows are applied by resolveEdge, which reads
// both nodes under the same filter, as it does for the walk. The rows have the
// shape nodeEdgesFromRows decodes; the order is total (relationship_id is
// unique), so a repeated read returns the same result.
func servedEdgesCypher(temporal temporalFilter) string {
	return fmt.Sprintf(
		"UNWIND $pairs AS p "+
			"MATCH (a:%s {%s:$org, %s:p.fk, %s:p.fi})-[r:%s]->(b:%s {%s:$org, %s:p.tk, %s:p.ti}) WHERE true%s "+
			"RETURN r, p.fk AS srcKind, p.fi AS srcId, p.tk AS dstKind, p.ti AS dstId ORDER BY r.%s ASC LIMIT $lim",
		labelSubject, propOrgID, propKind, propCanonicalID, labelRelation, labelSubject, propOrgID, propKind, propCanonicalID,
		temporal.predicate("r"), propRelationshipID,
	)
}
