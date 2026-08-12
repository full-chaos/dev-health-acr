package zepgraph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	zep "github.com/getzep/zep-go/v3"
)

// ResolveSubjects and DiscoverContext are thin wrappers over
// internal/contextfabric/graphrank's backend-neutral decision logic
// (CHAOS-3752 extraction): this file owns only the Zep-specific I/O
// (a.api.* calls) and the wire-type conversions (toCandidateNode/Edge,
// normalizeAttributes) that let graphrank operate on plain
// CandidateNode/CandidateEdge shapes instead of *zep.EntityNode/EntityEdge.
// The resolution/ranking/ambiguity/evidence-budget/second-hop-verification
// rules themselves -- each individually hardened by a specific historical
// Codex review finding -- live in graphrank and are shared with every other
// graph backend adapter, so they are proven once, not re-hand-ported per
// backend.

func (a *Adapter) ResolveSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion) (contextfabric.SubjectResolution, error) {
	deps := graphrank.ResolveDeps{
		ExactHint: func(ctx context.Context, subject contextfabric.SubjectRef) (graphrank.CandidateNode, bool, error) {
			node, err := a.api.GetNode(ctx, nodeUUID(principal.OrgID, subject))
			if err != nil {
				if zepStatusCode(err) == 404 {
					return graphrank.CandidateNode{}, false, nil
				}
				return graphrank.CandidateNode{}, false, safeDependencyError("resolve exact subject hint", err)
			}
			return toCandidateNode(node), true, nil
		},
		Search: func(ctx context.Context, term string, limit int) ([]graphrank.CandidateNode, error) {
			results, err := a.search(ctx, principal.OrgID, term, zep.GraphSearchScopeNodes, nil, limit)
			if err != nil {
				return nil, err
			}
			nodes := make([]graphrank.CandidateNode, 0, len(results.Nodes))
			for _, node := range results.Nodes {
				if node != nil {
					nodes = append(nodes, toCandidateNode(node))
				}
			}
			return nodes, nil
		},
		Traverse: func(ctx context.Context, term string, observation graphrank.CandidateNode) (contextfabric.SubjectCandidate, graphrank.ObservationTraversal) {
			return graphrank.TraverseObservationToSubject(ctx, principal, request.RequestedScope, term, observation, isInternalBookkeepingSubject,
				func(ctx context.Context, uuid string) ([]graphrank.CandidateEdge, error) {
					edges, err := a.api.GetNodeEdges(ctx, uuid)
					if err != nil {
						return nil, err
					}
					out := make([]graphrank.CandidateEdge, 0, len(edges))
					for _, edge := range edges {
						if edge != nil {
							out = append(out, toCandidateEdge(edge))
						}
					}
					return out, nil
				},
				func(ctx context.Context, uuid string) (graphrank.CandidateNode, bool) {
					source, err := a.api.GetNode(ctx, uuid)
					if err != nil || source == nil {
						return graphrank.CandidateNode{}, false
					}
					candidate := toCandidateNode(source)
					if _, verified := verifiedNodeSubject(principal.OrgID, uuid, candidate); !verified {
						return graphrank.CandidateNode{}, false
					}
					return candidate, true
				},
			)
		},
		IsInternal: isInternalBookkeepingSubject,
		TraversalDegraded: func(ctx context.Context, orgID string, count int) {
			if a.config.Telemetry != nil {
				a.config.Telemetry.RecordObservationTraversalDegraded(ctx, orgID, count)
			}
		},
	}
	return graphrank.ResolveSubjects(ctx, principal, request, interpreted, deps)
}

// DiscoverContext owns the Zep-shaped part of graph discovery this adapter
// still needs even after the CHAOS-3752 graphrank extraction: Zep's GetNode
// is a UUID-only lookup with no per-call graph/organization parameter (see
// verifiedNodeSubject), so an edge endpoint not already present in the
// first-hop search results has to be fetched separately (a "second hop")
// and then proved to belong to principal.OrgID before it can be trusted.
// That fetch-and-verify machinery, and the second_hop_node_* degraded-reason
// vocabulary it produces, is Zep-specific and deliberately stays here rather
// than in graphrank: a backend where every lookup is already structurally
// scoped to one organization (e.g. falkorgraph's one-graph-per-org) resolves
// every edge endpoint from a single whole-path query and has no second-hop
// concept to fake. Only the backend-neutral admission decision -- sorting,
// the evidence budget, path/driver construction and validation, and the
// subjectless-cohort search -- is shared, via graphrank.SortEdgesByRelevance,
// graphrank.AdmitEdges, and graphrank.DiscoveredCohort.
func (a *Adapter) DiscoverContext(ctx context.Context, principal storage.Principal, request contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.GraphContext{}, errors.New("authenticated organization is required")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.GraphContext{}, err
	}
	originIDs := make([]string, 0, len(request.Resolution.Committed))
	for _, subject := range request.Resolution.Committed {
		originIDs = append(originIDs, nodeUUID(principal.OrgID, subject))
	}
	limit := request.Request.Options.MaxRelationshipPaths
	if limit > a.config.MaxResults {
		limit = a.config.MaxResults
	}
	results, err := a.search(ctx, principal.OrgID, request.Request.Question, zep.GraphSearchScopeAuto, originIDs, limit)
	if err != nil {
		return contextfabric.GraphContext{}, err
	}

	nodes := make(map[string]graphrank.CandidateNode, len(results.Nodes))
	nodesResult := make([]graphrank.CandidateNode, 0, len(results.Nodes))
	for _, node := range results.Nodes {
		if node == nil {
			continue
		}
		candidate := toCandidateNode(node)
		nodesResult = append(nodesResult, candidate)
		if graphrank.AuthorizedAttributes(principal, request.Request.RequestedScope, candidate.Attributes) {
			nodes[candidate.UUID] = candidate
		}
	}
	edgesResult := make([]graphrank.CandidateEdge, 0, len(results.Edges))
	for _, edge := range results.Edges {
		if edge != nil {
			edgesResult = append(edgesResult, toCandidateEdge(edge))
		}
	}
	// N2: admission order must not depend on whatever order the backend
	// happened to return edges in -- sort before any second-hop fetch or
	// admission decision runs.
	edges := graphrank.SortEdgesByRelevance(edgesResult)

	// secondHopDrops counts every second-hop node drop by reason class, so
	// a caller can tell degradation happened instead of an investigation
	// silently looking complete with fewer paths/drivers than the graph
	// actually held.
	secondHopDrops := make(map[string]int)
	fetchSecondHop := func(uuid string) (graphrank.CandidateNode, bool) {
		fetched, _ := a.api.GetNode(ctx, uuid)
		if fetched == nil {
			secondHopDrops["second_hop_node_unavailable"]++
			return graphrank.CandidateNode{}, false
		}
		candidate := toCandidateNode(fetched)
		if !graphrank.AuthorizedAttributes(principal, request.Request.RequestedScope, candidate.Attributes) {
			secondHopDrops["second_hop_node_unauthorized"]++
			return graphrank.CandidateNode{}, false
		}
		return candidate, true
	}

	resolved := make([]graphrank.ResolvedEdge, 0, len(edges))
	for _, edge := range edges {
		if !graphrank.AuthorizedAttributes(principal, request.Request.RequestedScope, edge.Attributes) {
			continue
		}
		// from/to found directly in nodes are first-hop results from
		// Search, which is itself scoped to principal.OrgID -- trusted as
		// belonging there. from/to reached only through the fallback fetch
		// are second-hop and additionally require verifiedNodeSubject
		// before being trusted.
		from, fromFirstHop := nodes[edge.SourceNodeUUID]
		if !fromFirstHop {
			var ok bool
			from, ok = fetchSecondHop(edge.SourceNodeUUID)
			if !ok {
				continue
			}
		}
		to, toFirstHop := nodes[edge.TargetNodeUUID]
		if !toFirstHop {
			var ok bool
			to, ok = fetchSecondHop(edge.TargetNodeUUID)
			if !ok {
				continue
			}
		}
		var fromSubject, toSubject contextfabric.SubjectRef
		var ok bool
		if fromFirstHop {
			fromSubject, ok = graphrank.NodeSubject(from)
		} else {
			fromSubject, ok = verifiedNodeSubject(principal.OrgID, edge.SourceNodeUUID, from)
			if !ok {
				secondHopDrops["second_hop_node_unverified"]++
			}
		}
		if !ok {
			continue
		}
		if toFirstHop {
			toSubject, ok = graphrank.NodeSubject(to)
		} else {
			toSubject, ok = verifiedNodeSubject(principal.OrgID, edge.TargetNodeUUID, to)
			if !ok {
				secondHopDrops["second_hop_node_unverified"]++
			}
		}
		if !ok {
			continue
		}
		resolved = append(resolved, graphrank.ResolvedEdge{
			UUID: edge.UUID, Name: edge.Name, Fact: edge.Fact,
			From: fromSubject, To: toSubject,
			Relevance: edge.Relevance, Score: edge.Score, Attributes: edge.Attributes,
			CreatedAt: edge.CreatedAt, ValidAt: edge.ValidAt, InvalidAt: edge.InvalidAt, ExpiredAt: edge.ExpiredAt,
		})
	}

	admission := graphrank.AdmitEdges(principal.OrgID, resolved, request.Request.Options, isInternalBookkeepingSubject)
	cohort := graphrank.DiscoveredCohort(principal, request, nodesResult, isInternalBookkeepingSubject)
	factRequirements := admission.FactRequirements
	if cohort != nil {
		factRequirements = mergeFactRequirements(factRequirements, contextfabric.FactHealth, contextfabric.FactWorkload)
	}

	degradedReasons := []string{}
	partial := false
	if len(secondHopDrops) > 0 {
		partial = true
		degradedReasons = make([]string, 0, len(secondHopDrops))
		for reason, count := range secondHopDrops {
			degradedReasons = append(degradedReasons, fmt.Sprintf("%s:%d", reason, count))
		}
		sort.Strings(degradedReasons)
	}
	return contextfabric.GraphContext{
		Resolution: request.Resolution, Cohort: cohort, Paths: admission.Paths, DriverCandidates: admission.Drivers,
		EvidenceRefIDs: admission.EvidenceRefIDs, FactRequirements: factRequirements,
		Coverage: contextfabric.Coverage{
			// Source and Watermark land verbatim in the public
			// InvestigationResult.Coverage, so neither may name the backing
			// graph vendor or leak its internal graph identifier: "graph" is
			// the vendor-neutral source name, and Watermark stays empty
			// until a real, non-identifying watermark value exists.
			Sources:         []contextfabric.SourceObservation{{Source: "context-fabric:graph", State: contextfabric.SourceAvailable, ObservedAt: ptr(a.now().UTC())}},
			Partial:         partial,
			DegradedReasons: degradedReasons,
		},
	}, nil
}

// mergeFactRequirements dedups additional fact kinds into an already-sorted
// requirement list, matching the original DiscoverContext behavior of
// merging every driver-derived and cohort-derived FactRequirement into one
// map before a single final sort.
func mergeFactRequirements(existing []contextfabric.FactRequirement, kinds ...contextfabric.FactKind) []contextfabric.FactRequirement {
	seen := make(map[contextfabric.FactKind]bool, len(existing)+len(kinds))
	result := append([]contextfabric.FactRequirement(nil), existing...)
	for _, requirement := range existing {
		seen[requirement.Kind] = true
	}
	for _, kind := range kinds {
		if !seen[kind] {
			seen[kind] = true
			result = append(result, contextfabric.FactRequirement{Kind: kind})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Kind < result[j].Kind })
	return result
}

func (a *Adapter) search(ctx context.Context, orgID, query string, scope zep.GraphSearchScope, origins []string, limit int) (*zep.GraphSearchResults, error) {
	if limit < 1 {
		limit = a.config.MaxResults
	}
	if limit > 50 {
		limit = 50
	}
	reranker := zep.RerankerRrf
	returnRaw := true
	request := &zep.GraphSearchQuery{
		GraphID: ptr(graphID(a.config.GraphPrefix, orgID)), Query: strings.TrimSpace(query), Scope: &scope,
		Limit: &limit, Reranker: &reranker, ReturnRawResults: &returnRaw, BfsOriginNodeUUIDs: append([]string(nil), origins...),
	}
	if request.Query == "" {
		request.Query = "current engineering context"
	}
	results, err := a.api.Search(ctx, request)
	if err != nil {
		return nil, safeDependencyError("search context graph", err)
	}
	if results == nil {
		return &zep.GraphSearchResults{}, nil
	}
	return results, nil
}

// toCandidateNode converts a *zep.EntityNode into graphrank's backend-neutral
// CandidateNode shape, normalizing this adapter's pipe-encoded authorization
// and evidence attribute strings into graphrank's shared attribute-value
// convention (see graphrank's authorize.go doc comment).
func toCandidateNode(node *zep.EntityNode) graphrank.CandidateNode {
	if node == nil {
		return graphrank.CandidateNode{}
	}
	return graphrank.CandidateNode{
		UUID: node.UUID, Name: node.Name, Relevance: node.Relevance, Score: node.Score,
		Attributes: normalizeAttributes(node.Attributes),
	}
}

// toCandidateEdge is toCandidateNode's counterpart for edges.
func toCandidateEdge(edge *zep.EntityEdge) graphrank.CandidateEdge {
	if edge == nil {
		return graphrank.CandidateEdge{}
	}
	return graphrank.CandidateEdge{
		UUID: edge.UUID, Name: edge.Name, Fact: edge.Fact,
		SourceNodeUUID: edge.SourceNodeUUID, TargetNodeUUID: edge.TargetNodeUUID,
		Relevance: edge.Relevance, Score: edge.Score,
		CreatedAt: edge.CreatedAt, ValidAt: edge.ValidAt, InvalidAt: edge.InvalidAt, ExpiredAt: edge.ExpiredAt,
		Attributes: normalizeAttributes(edge.Attributes),
	}
}

// normalizeAttributes converts a raw Zep attributes map into graphrank's
// shared attribute-value convention: each authorization_* key becomes
// either the literal "*" (unrestricted, passed through) or a decoded
// []string (present only when non-wildcard and decodable -- an
// empty/malformed/fail-closed-sentinel value is OMITTED so graphrank denies
// by absence, exactly matching this adapter's original
// scopeContains("", value) == false rule); evidence_refs always becomes a
// []string. Every other key passes through unchanged.
func normalizeAttributes(raw map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(raw)+1)
	for key, value := range raw {
		out[key] = value
	}
	for _, key := range []string{"authorization_repositories", "authorization_projects", "authorization_teams"} {
		encoded := stringAttribute(raw, key)
		switch {
		case encoded == "*":
			out[key] = "*"
		case encoded == "" || encoded == scopeDeniedSentinel:
			delete(out, key)
		default:
			out[key] = decodeScope(encoded)
		}
	}
	out["evidence_refs"] = decodeScope(stringAttribute(raw, "evidence_refs"))
	return out
}

// verifiedNodeSubject is nodeSubject plus organization-identity
// verification for a "second-hop" node: one fetched by a UUID this adapter
// did not itself derive (e.g. edge.SourceNodeUUID/TargetNodeUUID from a
// search result, or GetNodeEdges), as opposed to a UUID this adapter
// computed itself via nodeUUID(orgID, subject) (which is trivially always
// correct for orgID, since the adapter chose it).
//
// GetNode and GetNodeEdges are UUID-only lookups with no per-call
// graph/organization parameter, unlike Search, which is scoped by GraphID.
// That makes a second-hop lookup the one place a node genuinely belonging
// to a different organization's graph -- reached through a compromised or
// misbehaving backend response, not through any fault of Search's own
// GraphID scoping -- could be trusted without this check. Because nodeUUID
// is a keyed SHA-256 digest of organization ID + subject kind + canonical
// ID, only a node whose own reported attributes hash back to the UUID it
// was actually fetched under can pass.
func verifiedNodeSubject(orgID, fetchedUUID string, node graphrank.CandidateNode) (contextfabric.SubjectRef, bool) {
	subject, ok := graphrank.NodeSubject(node)
	if !ok {
		return contextfabric.SubjectRef{}, false
	}
	if nodeUUID(orgID, subject) != fetchedUUID {
		return contextfabric.SubjectRef{}, false
	}
	return subject, true
}

// isInternalBookkeepingSubject reports whether subject is one of the
// adapter's own internal marker nodes (organizationRoot, markerSubject in
// identity.go) rather than a real canonical entity. These nodes exist only
// so projection has an anchor node for organization-scoped triples (the
// "HAS_SUBJECT" root edge, projection watermarks); a caller can never
// usefully mean one of them by name, and surfacing them as a subject
// candidate or a relationship endpoint would leak adapter-internal
// bookkeeping into a public result.
func isInternalBookkeepingSubject(subject contextfabric.SubjectRef) bool {
	// Matched on CanonicalID alone, not gated on the reported Kind also
	// being Organization/Metric. organizationRoot/markerSubject
	// (identity.go) only ever write these reserved canonical_id values
	// paired with those kinds -- but a node's own subject_kind is just
	// another attribute read back off the wire (see nodeSubject), not
	// something this adapter can independently verify. Requiring an exact
	// Kind match here would let a node that reports some OTHER kind (a bug
	// in a differently-configured write path, or a deliberately malformed
	// one) bypass the exclusion while still carrying one of these reserved
	// identifiers, so the identifier itself is treated as reserved
	// regardless of what kind accompanies it. Normalized case-insensitively
	// for the same reason: the write path never legitimately produces
	// anything but the exact lowercase form, but nothing structurally
	// prevents a differently-cased value from reaching this check.
	canonicalID := strings.ToLower(subject.CanonicalID)
	if canonicalID == "organization-root" {
		return true
	}
	if strings.HasPrefix(canonicalID, "projection-watermark:") {
		return true
	}
	return false
}

// resultConfidence is kept as a thin, directly-testable wrapper (see
// adapter_test.go) over graphrank.ResultConfidence.
func resultConfidence(relevance, score *float64) float64 {
	return graphrank.ResultConfidence(relevance, score)
}

// edgeEvidence reads and decodes a Zep edge's pipe-encoded evidence_refs
// attribute. Kept zep-local (unlike graphrank.EvidenceRefs, which reads the
// already-decoded []string form) because adapter_test.go exercises it
// directly against a raw *zep.EntityEdge.
func edgeEvidence(edge *zep.EntityEdge) []string {
	// Zep episode UUIDs are backend-native provenance and are never promoted to
	// canonical Dev Health evidence identifiers. Only ACR-projected evidence
	// references may close a public relationship or driver claim.
	return uniqueSorted(decodeScope(stringAttribute(edge.Attributes, "evidence_refs")))
}

var _ contextfabric.GraphReader = (*Adapter)(nil)
