package falkorgraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
//
// Codex P2b/P2d: orgID is a mandatory predicate on every read (ADR 0009:95
// claims this as defense-in-depth even though the graph key already scopes
// the whole database to one organization -- a second, cheap check that
// costs nothing and catches a graphKey derivation bug or a stray
// cross-tenant write before it can ever surface). The result LIMIT is
// applied server-side, in the Cypher itself, rather than by fetching every
// match and breaking client-side after the desired count -- unlike the
// former client-side break, this bounds the actual query cost, not just the
// slice the caller sees. limit is always this adapter's own bounded int
// (clamped to a.config.MaxResults below), never caller text, so inlining it
// as a literal into the query string is safe -- see safeParams' doc for why
// untrusted values never take this path.
func (a *Adapter) fulltextSearchNodes(ctx context.Context, key, orgID, text string, limit int) ([]graphrank.CandidateNode, error) {
	terms := tokenizeForFulltext(text)
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > a.config.MaxResults {
		limit = a.config.MaxResults
	}
	query := strings.Join(terms, "|")
	cypher := fmt.Sprintf(
		"CALL db.idx.fulltext.queryNodes('%s', $query) YIELD node, score "+
			"WHERE node.%s = $org "+
			"RETURN node, score ORDER BY score DESC LIMIT %d",
		labelSubject, propOrgID, limit,
	)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"query": query, "org": orgID}, true)
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

func (a *Adapter) nodeByUUID(ctx context.Context, key, orgID, uuid string) (*node, error) {
	kind, canonicalID := splitSubjectUUID(uuid)
	if kind == "" {
		return nil, nil
	}
	return a.nodeByKindID(ctx, key, orgID, kind, canonicalID)
}

// nodeByKindID looks up one Subject node by its natural key. Codex P2b: the
// org_id predicate is mandatory on every read query, not only the ones that
// happened to already carry it -- this is the standing review rule
// regardless of how strong graph-key tenancy isolation already is.
func (a *Adapter) nodeByKindID(ctx context.Context, key, orgID, kind, canonicalID string) (*node, error) {
	cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind, %s:$id}) RETURN n", labelSubject, propOrgID, propKind, propCanonicalID)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "kind": kind, "id": canonicalID}, true)
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
//
// Codex P2b: org_id is filtered on BOTH node patterns in the UNION (the
// origin subject `n` and the neighbor `other`) -- filtering only the origin
// side would still let a cross-tenant neighbor node leak into the result
// through a stray or corrupted edge.
//
// Codex P2a (round 2): the combined UNION result is wrapped in a `CALL {}`
// subquery so an outer `ORDER BY r.relationship_id ASC` can apply to it --
// verified live that a bare `ORDER BY` placed directly after a top-level
// `UNION` is silently NOT honored by this FalkorDB version (the combined
// rows come back in the union's own internal order regardless), while the
// exact same ORDER BY on a `CALL { <the union> } RETURN ...` wrapper sorts
// correctly. This makes edgesOfNode's own output deterministic by the same
// key graphrank's relevance tie-break uses (ascending relationship UUID) --
// see hopWalk's doc comment for why a real relevance-bearing property does
// not exist for a graph-walked edge in the first place, and why this
// property is the correct proxy for it.
func (a *Adapter) edgesOfNode(ctx context.Context, key, orgID, uuid string) ([]graphrank.CandidateEdge, error) {
	kind, canonicalID := splitSubjectUUID(uuid)
	if kind == "" {
		return nil, nil
	}
	cypher := fmt.Sprintf(
		"CALL { "+
			"MATCH (n:%s {%s:$org, %s:$kind, %s:$id})-[r:%s]->(other:%s {%s:$org}) RETURN r, %s AS srcKind, $id AS srcId, other.%s AS dstKind, other.%s AS dstId "+
			"UNION "+
			"MATCH (other:%s {%s:$org})-[r:%s]->(n:%s {%s:$org, %s:$kind, %s:$id}) RETURN r, other.%s AS srcKind, other.%s AS srcId, %s AS dstKind, $id AS dstId "+
			"} RETURN r, srcKind, srcId, dstKind, dstId ORDER BY r.%s ASC",
		labelSubject, propOrgID, propKind, propCanonicalID, labelRelation, labelSubject, propOrgID, "$kind", propKind, propCanonicalID,
		labelSubject, propOrgID, labelRelation, labelSubject, propOrgID, propKind, propCanonicalID, propKind, propCanonicalID, "$kind",
		propRelationshipID,
	)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "kind": kind, "id": canonicalID}, true)
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

// edgeResolution classifies what resolveEdge did with one candidate edge,
// so a caller can tell a legitimate exclusion (unauthorized, or an endpoint
// that genuinely no longer exists) apart from a degraded one (a lookup that
// actually failed) -- Codex P2c: the two must never be reported identically,
// since collapsing them into one silent "not admitted" produced
// Coverage.Partial=false even when a real backend failure had quietly
// dropped material from the result.
type edgeResolution int

const (
	edgeAdmitted edgeResolution = iota
	edgeFiltered
	edgeLookupFailed
)

// resolveEdge converts a CandidateEdge (as returned by edgesOfNode) into a
// fully-resolved graphrank.ResolvedEdge by decoding its endpoint subjectUUIDs
// back into SubjectRefs via a node lookup for the label -- falkorgraph never
// needs a "second-hop verify" step the way zepgraph does (every lookup here
// is already structurally scoped to principal's own organization graph key),
// so this is a plain fetch, not a trust decision.
//
// Codex P1: authorization is checked here, on the edge's own attributes AND
// on both resolved endpoints' attributes, before the edge is ever handed to
// graphrank.AdmitEdges -- AdmitEdges itself applies no authorization check
// (it only excludes self-loops and internal-bookkeeping endpoints), so an
// unauthorized edge or an edge into/out of an unauthorized subject must
// never reach it. This mirrors zepgraph's DiscoverContext exactly:
// graphrank.AuthorizedAttributes gates the edge, then each endpoint,
// independently.
func (a *Adapter) resolveEdge(ctx context.Context, key, orgID string, principal storage.Principal, scope contextfabric.RequestedScope, ce graphrank.CandidateEdge) (graphrank.ResolvedEdge, edgeResolution) {
	if !graphrank.AuthorizedAttributes(principal, scope, ce.Attributes) {
		return graphrank.ResolvedEdge{}, edgeFiltered
	}
	fromKind, fromID := splitSubjectUUID(ce.SourceNodeUUID)
	toKind, toID := splitSubjectUUID(ce.TargetNodeUUID)
	fromNode, err := a.nodeByKindID(ctx, key, orgID, fromKind, fromID)
	if err != nil {
		return graphrank.ResolvedEdge{}, edgeLookupFailed
	}
	if fromNode == nil {
		return graphrank.ResolvedEdge{}, edgeFiltered
	}
	toNode, err := a.nodeByKindID(ctx, key, orgID, toKind, toID)
	if err != nil {
		return graphrank.ResolvedEdge{}, edgeLookupFailed
	}
	if toNode == nil {
		return graphrank.ResolvedEdge{}, edgeFiltered
	}
	fromCandidate := toCandidateNode(fromNode)
	toCandidate := toCandidateNode(toNode)
	if !graphrank.AuthorizedAttributes(principal, scope, fromCandidate.Attributes) || !graphrank.AuthorizedAttributes(principal, scope, toCandidate.Attributes) {
		return graphrank.ResolvedEdge{}, edgeFiltered
	}
	fromSubject, ok := graphrank.NodeSubject(fromCandidate)
	if !ok {
		return graphrank.ResolvedEdge{}, edgeFiltered
	}
	toSubject, ok := graphrank.NodeSubject(toCandidate)
	if !ok {
		return graphrank.ResolvedEdge{}, edgeFiltered
	}
	return graphrank.ResolvedEdge{
		UUID: ce.UUID, Name: ce.Name, Fact: ce.Fact, From: fromSubject, To: toSubject,
		Relevance: ce.Relevance, Score: ce.Score, Attributes: ce.Attributes,
		CreatedAt: ce.CreatedAt, ValidAt: ce.ValidAt, InvalidAt: ce.InvalidAt, ExpiredAt: ce.ExpiredAt,
	}, edgeAdmitted
}

// rankAndBoundCandidateEdges sorts edges by graphrank's own relevance
// tie-break and caps the set entering resolution to a generous multiple of
// the remaining collection budget.
//
// Codex P2a (round 2): a collection-side truncation decision must always
// operate on a RANKED set, never on whatever order a query happened to
// return -- collecting per-node/per-hop bounded but rank-aware, per the
// review's own alternative to a single fully-global pre-collection sort
// (which is not expressible as one Cypher pass across multiple frontier
// nodes and, per edgesOfNode's doc comment, is not even reliably honored by
// this FalkorDB version across a single UNION without the CALL{} wrapper).
// The multiplier bound below exists only to cap worst-case resolution cost
// (a pathological hub node with thousands of neighbors, almost all of which
// end up authorization-filtered) -- it still operates on the RANKED list,
// so it can only ever trim away the edges that were already going to lose
// the tie-break, never a contender for the surviving budget.
func rankAndBoundCandidateEdges(edges []graphrank.CandidateEdge, collectLimit int) []graphrank.CandidateEdge {
	ranked := graphrank.SortEdgesByRelevance(edges)
	if collectLimit > 0 && len(ranked) > collectLimit*4 {
		ranked = ranked[:collectLimit*4]
	}
	return ranked
}

// hopWalk performs a bounded N-hop traversal from one origin subject,
// returning every neighbor node and resolved edge reached, plus a count of
// edges/nodes dropped due to a genuine lookup failure (Codex P2c -- see
// edgeResolution).
//
// Codex P2a: walk collection is bounded by a generous superset limit (the
// caller passes a.config.MaxResults, not request.Options.MaxRelationshipPaths),
// never by the final per-request path limit -- truncating to
// MaxRelationshipPaths during collection, before graphrank.SortEdgesByRelevance
// and graphrank.AdmitEdges ever see the full candidate set, could silently
// discard a higher-relevance edge in favor of one merely reached first. Final
// truncation to MaxRelationshipPaths happens exactly once, inside AdmitEdges,
// after ranking.
//
// Codex P2a (round 2): that alone was not enough -- collection could still
// exhaust collectLimit itself in arrival order (whichever frontier node was
// processed first, whichever row a query happened to return first) before a
// higher-ranked edge discovered later ever got a chance to compete for the
// budget. Every graph-walked edge ties at ResultConfidence=0 (there is no
// real relevance signal for a hop-walked edge the way a full-text search
// score is one), so graphrank.SortEdgesByRelevance's own deterministic
// tie-break -- ascending relationship UUID -- IS the correct admission
// order, not an approximation of one; this function now collects an ENTIRE
// hop's candidate edges from every frontier node before making ANY
// truncation decision, ranks that full set with the exact same tie-break,
// and only then resolves/admits edges in ranked order up to the remaining
// budget (rankAndBoundCandidateEdges). A candidate that gets
// authorization-filtered or fails to resolve does not consume budget, so a
// lower-ranked-but-admissible edge is never starved by a higher-ranked one
// that turned out to be unauthorized.
func (a *Adapter) hopWalk(ctx context.Context, key, orgID string, principal storage.Principal, scope contextfabric.RequestedScope, origin contextfabric.SubjectRef, maxHops, collectLimit int) ([]graphrank.CandidateNode, []graphrank.ResolvedEdge, int, error) {
	originUUID := subjectUUID(string(origin.Kind), origin.CanonicalID)
	visited := make(map[string]graphrank.CandidateNode)
	var edges []graphrank.ResolvedEdge
	seenEdge := make(map[string]bool)
	failedLookups := 0
	frontier := []string{originUUID}
	for hop := 0; hop < maxHops && len(frontier) > 0 && (collectLimit <= 0 || len(edges) < collectLimit); hop++ {
		var hopCandidates []graphrank.CandidateEdge
		for _, uuid := range frontier {
			candidateEdges, err := a.edgesOfNode(ctx, key, orgID, uuid)
			if err != nil {
				return nil, nil, failedLookups, err
			}
			for _, ce := range candidateEdges {
				if seenEdge[ce.UUID] {
					continue
				}
				seenEdge[ce.UUID] = true
				hopCandidates = append(hopCandidates, ce)
			}
		}
		if len(hopCandidates) == 0 {
			frontier = nil
			continue
		}
		var next []string
		for _, ce := range rankAndBoundCandidateEdges(hopCandidates, collectLimit-len(edges)) {
			if collectLimit > 0 && len(edges) >= collectLimit {
				break
			}
			resolved, resolution := a.resolveEdge(ctx, key, orgID, principal, scope, ce)
			switch resolution {
			case edgeLookupFailed:
				failedLookups++
				continue
			case edgeFiltered:
				continue
			}
			edges = append(edges, resolved)
			for _, neighbor := range []string{ce.SourceNodeUUID, ce.TargetNodeUUID} {
				if neighbor == originUUID {
					continue
				}
				if _, ok := visited[neighbor]; ok {
					continue
				}
				// Codex P2c (round 2): a genuine lookup failure here was
				// previously indistinguishable from a legitimate "this
				// neighbor no longer exists" -- both fell through the same
				// `continue`, so a real backend failure reached only through
				// this bookkeeping fetch (the edge/path it belonged to was
				// already admitted via resolveEdge above) never surfaced as
				// Coverage.Partial. Only err != nil is a failure; n == nil
				// with no error is a legitimate miss.
				n, err := a.nodeByUUID(ctx, key, orgID, neighbor)
				if err != nil {
					failedLookups++
					continue
				}
				if n == nil {
					continue
				}
				candidate := toCandidateNode(n)
				visited[neighbor] = candidate
				next = append(next, neighbor)
			}
		}
		frontier = next
	}
	nodes := make([]graphrank.CandidateNode, 0, len(visited))
	for _, n := range visited {
		nodes = append(nodes, n)
	}
	return nodes, edges, failedLookups, nil
}
