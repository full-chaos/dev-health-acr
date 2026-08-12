package falkorgraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// fulltextSearchNodes runs a lexical full-text search over Subject nodes'
// search_text property, returning matches as CandidateNode with a real
// relevance score (verified live: RediSearch scores vary meaningfully, not
// a boolean match). Space in query is AND by RediSearch default (verified:
// docs/design/context-fabric-falkordb-adapter.md §6.1) -- a multi-word
// question passed as-is would almost always match nothing, so terms are
// joined with "|" (OR) instead. Field names in a full-text query must never
// come from caller text (an unrecognized @field: silently returns empty,
// no error) -- this function never emits one.
func (a *Adapter) fulltextSearchNodes(ctx context.Context, key, text string, limit int) ([]graphrank.CandidateNode, error) {
	terms := tokenizeForFulltext(text)
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = a.config.MaxResults
	}
	query := strings.Join(terms, "|")
	cypher := fmt.Sprintf("CALL db.idx.fulltext.queryNodes('%s', $query) YIELD node, score RETURN node, score", labelSubject)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"query": query}, true)
	if err != nil {
		return nil, safeDependencyError("search context graph", err)
	}
	results := make([]graphrank.CandidateNode, 0, len(rows))
	for _, row := range rows {
		n, ok := row["node"].(*node)
		if !ok || n == nil {
			continue
		}
		candidate := toCandidateNode(n)
		if score, ok := row["score"].(float64); ok {
			candidate.Score = &score
		}
		results = append(results, candidate)
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results, nil
}

// tokenizeForFulltext splits free text into RediSearch-safe search terms.
// Punctuation that RediSearch's query syntax treats specially (the OR "|",
// fuzzy "%", field-scope "@", quotes) is stripped from each term rather than
// escaped, since a caller-typed question is untrusted input and this
// function's only job is producing a query that means "any of these words",
// never anything structurally richer.
func tokenizeForFulltext(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case '|', '%', '@', '"', '\'', '*', '-', '(', ')', ':':
			return true
		}
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			terms = append(terms, field)
		}
	}
	return terms
}

// subjectUUID is the stable, portable identifier this adapter uses in place
// of a backend-internal ID for anything graphrank needs to key on
// (ReceiptID/PathID/DriverID derivation, dedup maps): kind and canonical_id
// joined with the same NUL separator subjectKey-shaped helpers use
// elsewhere in this repository. Deliberately NOT FalkorDB's own internal
// node ID (*node.ID, a uint64): that ID's value depends on insertion
// history, not on subject identity, so two different environments (or a
// replay after a rebuild) would derive different ReceiptIDs for the exact
// same canonical subject.
func subjectUUID(kind, canonicalID string) string {
	return kind + "\x00" + canonicalID
}

func splitSubjectUUID(uuid string) (kind, canonicalID string) {
	parts := strings.SplitN(uuid, "\x00", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// toCandidateNode converts a decoded FalkorDB node into graphrank's
// CandidateNode. Attributes pass through unchanged: this adapter already
// writes authorization_*/evidence_refs in graphrank's shared attribute-value
// convention at projection time (projection.go's authorizationValue),
// unlike zepgraph, which must translate its own pipe-encoded wire format on
// every read.
func toCandidateNode(n *node) graphrank.CandidateNode {
	if n == nil {
		return graphrank.CandidateNode{}
	}
	kind := propStringValue(n.Properties[propKind])
	canonicalID := propStringValue(n.Properties[propCanonicalID])
	return graphrank.CandidateNode{
		UUID: subjectUUID(kind, canonicalID), Name: propStringValue(n.Properties[propLabel]),
		Attributes: n.Properties,
	}
}

func (a *Adapter) nodeByUUID(ctx context.Context, key, uuid string) (*node, error) {
	kind, canonicalID := splitSubjectUUID(uuid)
	if kind == "" {
		return nil, nil
	}
	return a.nodeByKindID(ctx, key, kind, canonicalID)
}

func (a *Adapter) nodeByKindID(ctx context.Context, key, kind, canonicalID string) (*node, error) {
	cypher := fmt.Sprintf("MATCH (n:%s {%s:$kind, %s:$id}) RETURN n", labelSubject, propKind, propCanonicalID)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"kind": kind, "id": canonicalID}, true)
	if err != nil {
		return nil, safeDependencyError("get node", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	n, _ := rows[0]["n"].(*node)
	return n, nil
}

// edgesOfNode returns every edge touching the subject identified by uuid, as
// graphrank.CandidateEdge (SourceNodeUUID/TargetNodeUUID in this adapter's
// subjectUUID form, Name set to the semantic relation type read back from
// the relation_type property -- the Cypher relationship type itself is
// always the generic labelRelation, so it carries no semantic information on
// its own). Used by ResolveSubjects' observation-to-entity traversal.
func (a *Adapter) edgesOfNode(ctx context.Context, key, uuid string) ([]graphrank.CandidateEdge, error) {
	kind, canonicalID := splitSubjectUUID(uuid)
	if kind == "" {
		return nil, nil
	}
	cypher := fmt.Sprintf(
		"MATCH (n:%s {%s:$kind, %s:$id})-[r:%s]->(other:%s) RETURN r, %s AS srcKind, $id AS srcId, other.%s AS dstKind, other.%s AS dstId "+
			"UNION "+
			"MATCH (other:%s)-[r:%s]->(n:%s {%s:$kind, %s:$id}) RETURN r, other.%s AS srcKind, other.%s AS srcId, %s AS dstKind, $id AS dstId",
		labelSubject, propKind, propCanonicalID, labelRelation, labelSubject, "$kind", propKind, propCanonicalID,
		labelSubject, labelRelation, labelSubject, propKind, propCanonicalID, propKind, propCanonicalID, "$kind",
	)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"kind": kind, "id": canonicalID}, true)
	if err != nil {
		return nil, safeDependencyError("get node edges", err)
	}
	edges := make([]graphrank.CandidateEdge, 0, len(rows))
	for _, row := range rows {
		e, ok := row["r"].(*edge)
		if !ok || e == nil {
			continue
		}
		srcKind, srcID := propStringValue(row["srcKind"]), propStringValue(row["srcId"])
		dstKind, dstID := propStringValue(row["dstKind"]), propStringValue(row["dstId"])
		edges = append(edges, toCandidateEdge(e, srcKind, srcID, dstKind, dstID))
	}
	return edges, nil
}

func toCandidateEdge(e *edge, srcKind, srcID, dstKind, dstID string) graphrank.CandidateEdge {
	relationType := propStringValue(e.Properties[propRelationType])
	fact := propStringValue(e.Properties["fact"])
	attrs := make(map[string]interface{}, len(e.Properties)+1)
	for k, v := range e.Properties {
		attrs[k] = v
	}
	return graphrank.CandidateEdge{
		UUID: propStringValue(e.Properties[propRelationshipID]), Name: relationType, Fact: fact,
		SourceNodeUUID: subjectUUID(srcKind, srcID), TargetNodeUUID: subjectUUID(dstKind, dstID),
		Attributes: attrs,
		CreatedAt:  propStringValue(e.Properties[propObservedAt]),
		ValidAt:    optionalString(e.Properties[propValidFrom]),
		InvalidAt:  optionalString(e.Properties[propValidTo]),
	}
}

func optionalString(value interface{}) *string {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil
	}
	return &s
}

// resolveEdge converts a CandidateEdge (as returned by edgesOfNode) into a
// fully-resolved graphrank.ResolvedEdge by decoding its endpoint subjectUUIDs
// back into SubjectRefs via a node lookup for the label -- falkorgraph never
// needs a "second-hop verify" step the way zepgraph does (every lookup here
// is already structurally scoped to principal's own organization graph key),
// so this is a plain fetch, not a trust decision.
func (a *Adapter) resolveEdge(ctx context.Context, key string, ce graphrank.CandidateEdge) (graphrank.ResolvedEdge, bool) {
	fromKind, fromID := splitSubjectUUID(ce.SourceNodeUUID)
	toKind, toID := splitSubjectUUID(ce.TargetNodeUUID)
	fromNode, err := a.nodeByKindID(ctx, key, fromKind, fromID)
	if err != nil || fromNode == nil {
		return graphrank.ResolvedEdge{}, false
	}
	toNode, err := a.nodeByKindID(ctx, key, toKind, toID)
	if err != nil || toNode == nil {
		return graphrank.ResolvedEdge{}, false
	}
	fromSubject, ok := graphrank.NodeSubject(toCandidateNode(fromNode))
	if !ok {
		return graphrank.ResolvedEdge{}, false
	}
	toSubject, ok := graphrank.NodeSubject(toCandidateNode(toNode))
	if !ok {
		return graphrank.ResolvedEdge{}, false
	}
	return graphrank.ResolvedEdge{
		UUID: ce.UUID, Name: ce.Name, Fact: ce.Fact, From: fromSubject, To: toSubject,
		Relevance: ce.Relevance, Score: ce.Score, Attributes: ce.Attributes,
		CreatedAt: ce.CreatedAt, ValidAt: ce.ValidAt, InvalidAt: ce.InvalidAt, ExpiredAt: ce.ExpiredAt,
	}, true
}

// hopWalk performs a bounded N-hop traversal from one origin subject,
// returning every neighbor node and resolved edge reached. Implemented as
// iterative edgesOfNode calls (1-hop, then 1-hop from each new neighbor)
// rather than a single native Cypher [*1..2] variable-length path query --
// simpler to decode correctly against the pinned client's compact-protocol
// path parsing, at the cost of N extra round trips for an N-neighbor node.
// Revisit as a single [*1..2] query if that cost matters in practice; the
// adapter's external behavior does not depend on which query shape produces
// it.
func (a *Adapter) hopWalk(ctx context.Context, key string, origin contextfabric.SubjectRef, maxHops, limit int) ([]graphrank.CandidateNode, []graphrank.ResolvedEdge, error) {
	originUUID := subjectUUID(string(origin.Kind), origin.CanonicalID)
	visited := make(map[string]graphrank.CandidateNode)
	var edges []graphrank.ResolvedEdge
	seenEdge := make(map[string]bool)
	frontier := []string{originUUID}
	for hop := 0; hop < maxHops && len(frontier) > 0 && (limit <= 0 || len(edges) < limit); hop++ {
		var next []string
		for _, uuid := range frontier {
			candidateEdges, err := a.edgesOfNode(ctx, key, uuid)
			if err != nil {
				return nil, nil, err
			}
			for _, ce := range candidateEdges {
				if seenEdge[ce.UUID] {
					continue
				}
				resolved, ok := a.resolveEdge(ctx, key, ce)
				if !ok {
					continue
				}
				seenEdge[ce.UUID] = true
				edges = append(edges, resolved)
				for _, neighbor := range []string{ce.SourceNodeUUID, ce.TargetNodeUUID} {
					if neighbor == originUUID {
						continue
					}
					if _, ok := visited[neighbor]; ok {
						continue
					}
					n, err := a.nodeByUUID(ctx, key, neighbor)
					if err != nil || n == nil {
						continue
					}
					candidate := toCandidateNode(n)
					visited[neighbor] = candidate
					next = append(next, neighbor)
				}
				if limit > 0 && len(edges) >= limit {
					break
				}
			}
			if limit > 0 && len(edges) >= limit {
				break
			}
		}
		frontier = next
	}
	nodes := make([]graphrank.CandidateNode, 0, len(visited))
	for _, n := range visited {
		nodes = append(nodes, n)
	}
	return nodes, edges, nil
}
