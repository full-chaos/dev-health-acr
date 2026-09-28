package falkorgraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var _ directread.EdgeGraph = (*Adapter)(nil)

// DirectEdgePage is the bounded edge read of read_relationships (CHAOS-7074).
// It is NOT edgesOfNode: that read has no limit and no keyset, and the engine
// path depends on its exact shape, so it stays as it is.
//
// One Cypher statement, in the caller's own organization graph:
//
//   - every node and the edge carry the org_id predicate;
//   - the origins are matched by (subject_kind, canonical_id) from a bound
//     list, and the arms follow Direction (out: origin is the stored start;
//     in: origin is the stored end; both: the UNION of the two, which removes
//     an edge both arms reach);
//   - the valid-time predicate is ALWAYS applied, at query.ValidAt, to the
//     edge and to both end nodes: a current read passes "now", so an ended
//     edge (valid_to in the past) is never read as current;
//   - the keyset is relationship_id, strict ">", and the outer ORDER BY is
//     the same key (the CALL{} wrapper is what makes FalkorDB honor an
//     ORDER BY over a UNION; see edgesOfNode). relationship_id carries a
//     UNIQUE constraint (bootstrapSchema), so the order is total;
//   - LIMIT is query.Limit+1: the extra row only tells the page that more
//     follow, and is dropped.
//
// It applies NO authorization: every returned edge still goes through the
// edge gate in package directread, which is the only caller.
func (a *Adapter) DirectEdgePage(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, query directread.EdgePageQuery) (directread.EdgePage, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return directread.EdgePage{}, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	if len(query.Origins) == 0 || len(query.Origins) > directread.MaxEdgeFrontier {
		return directread.EdgePage{}, fmt.Errorf("direct edge page: %d origins outside 1..%d", len(query.Origins), directread.MaxEdgeFrontier)
	}
	if query.Limit < 1 || query.Limit > directread.MaxEdgePageLimit {
		return directread.EdgePage{}, fmt.Errorf("direct edge page: limit %d outside 1..%d", query.Limit, directread.MaxEdgePageLimit)
	}
	if query.ValidAt.IsZero() {
		return directread.EdgePage{}, fmt.Errorf("direct edge page: a valid-time instant is required")
	}
	key, err := a.effectiveKey(ctx, orgID, binding)
	if err != nil {
		return directread.EdgePage{}, err
	}
	cypher, params := directEdgePageCypher(orgID, query)
	rows, err := a.api.query(ctx, key, cypher, params, true)
	if err != nil {
		return directread.EdgePage{}, safeDependencyError("direct edge page", err)
	}
	page := directread.EdgePage{Edges: make([]directread.EdgeCandidate, 0, min(len(rows), query.Limit))}
	for _, row := range rows {
		e, ok := row["r"].(*edge)
		from, fromOK := row["a"].(*node)
		to, toOK := row["b"].(*node)
		if !ok || e == nil || !fromOK || from == nil || !toOK || to == nil {
			return directread.EdgePage{}, fmt.Errorf("direct edge page: row without an edge and two nodes")
		}
		if len(page.Edges) == query.Limit {
			page.More = true
			break
		}
		fromEnd, toEnd := directEdgeEnd(from), directEdgeEnd(to)
		page.Edges = append(page.Edges, directread.EdgeCandidate{
			Key:          directread.EdgeKey{RelationshipID: propStringValue(e.Properties[propRelationshipID])},
			RelationType: propStringValue(e.Properties[propRelationType]),
			Attributes:   copyProperties(e.Properties),
			From:         fromEnd,
			To:           toEnd,
		})
	}
	return page, nil
}

func directEdgeEnd(n *node) directread.EdgeEnd {
	return directread.EdgeEnd{
		Subject: contextfabric.SubjectRef{
			Kind:        contextfabric.SubjectKind(propStringValue(n.Properties[propKind])),
			CanonicalID: propStringValue(n.Properties[propCanonicalID]),
			Label:       propStringValue(n.Properties[propLabel]),
		},
		Attributes: copyProperties(n.Properties),
	}
}

func copyProperties(properties map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(properties))
	for k, v := range properties {
		out[k] = v
	}
	return out
}

// directEdgePageCypher renders the statement and its parameters. Split out
// so the text is testable without a graph.
func directEdgePageCypher(orgID string, query directread.EdgePageQuery) (string, map[string]interface{}) {
	temporal := temporalFilter{active: true, startNs: nsTimestamp(query.ValidAt), endNs: nsTimestamp(query.ValidAt)}
	origins := make([]interface{}, 0, len(query.Origins))
	for _, origin := range query.Origins {
		origins = append(origins, map[string]interface{}{"k": string(origin.Kind), "i": origin.CanonicalID})
	}
	params := temporal.bind(map[string]interface{}{"org": orgID, "origins": origins, "lim": int64(query.Limit + 1)})

	var filters []string
	filters = append(filters, strings.TrimPrefix(temporal.predicate("r"), " AND "),
		strings.TrimPrefix(temporal.predicate("a"), " AND "), strings.TrimPrefix(temporal.predicate("b"), " AND "))
	if len(query.Types) > 0 {
		types := make([]interface{}, 0, len(query.Types))
		for _, t := range query.Types {
			types = append(types, t)
		}
		params["types"] = types
		filters = append(filters, fmt.Sprintf("r.%s IN $types", propRelationType))
	}
	if query.Exclude != nil {
		params["xk"], params["xi"] = string(query.Exclude.Kind), query.Exclude.CanonicalID
		filters = append(filters,
			fmt.Sprintf("NOT (a.%[1]s = $xk AND a.%[2]s = $xi)", propKind, propCanonicalID),
			fmt.Sprintf("NOT (b.%[1]s = $xk AND b.%[2]s = $xi)", propKind, propCanonicalID))
	}
	if query.After != nil {
		params["after"] = query.After.RelationshipID
		filters = append(filters, fmt.Sprintf("r.%s > $after", propRelationshipID))
	}
	where := strings.Join(filters, " AND ")

	originNode := fmt.Sprintf("%s {%s:$org, %s:o.k, %s:o.i}", labelSubject, propOrgID, propKind, propCanonicalID)
	otherNode := fmt.Sprintf("%s {%s:$org}", labelSubject, propOrgID)
	outArm := fmt.Sprintf("UNWIND $origins AS o MATCH (a:%s)-[r:%s]->(b:%s) WHERE %s RETURN r, a, b", originNode, labelRelation, otherNode, where)
	inArm := fmt.Sprintf("UNWIND $origins AS o MATCH (a:%s)-[r:%s]->(b:%s) WHERE %s RETURN r, a, b", otherNode, labelRelation, originNode, where)
	var inner string
	switch query.Direction {
	case directread.EdgeDirectionOut:
		inner = outArm
	case directread.EdgeDirectionIn:
		inner = inArm
	default:
		inner = outArm + " UNION " + inArm
	}
	return fmt.Sprintf("CALL { %s } RETURN r, a, b ORDER BY r.%s ASC LIMIT $lim", inner, propRelationshipID), params
}
