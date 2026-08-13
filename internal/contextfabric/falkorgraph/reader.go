package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// isInternalSubject always reports false: falkorgraph has no anchor/marker
// nodes the way zepgraph did (organizationRoot, projection-watermark
// subject) -- those existed only because Zep's AddFactTriple forced every
// fact to have a source+target node. This adapter's watermark is its own
// reserved-label node (labelWatermark), never a :Subject node, so it can
// never surface as a subject candidate or relationship endpoint in the
// first place; there is nothing here to filter.
func isInternalSubject(contextfabric.SubjectRef) bool { return false }

func (a *Adapter) ResolveSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion) (contextfabric.SubjectResolution, error) {
	key := graphKey(a.config.GraphPrefix, principal.OrgID)
	deps := graphrank.ResolveDeps{
		ExactHint: func(ctx context.Context, subject contextfabric.SubjectRef) (graphrank.CandidateNode, bool, error) {
			cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind, %s:$id}) RETURN n", labelSubject, propOrgID, propKind, propCanonicalID)
			rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": principal.OrgID, "kind": string(subject.Kind), "id": subject.CanonicalID}, true)
			if err != nil {
				return graphrank.CandidateNode{}, false, safeDependencyError("resolve exact subject hint", err)
			}
			if len(rows) == 0 {
				return graphrank.CandidateNode{}, false, nil
			}
			n, ok := rows[0]["n"].(*node)
			if !ok || n == nil {
				return graphrank.CandidateNode{}, false, nil
			}
			return toCandidateNode(n), true, nil
		},
		Search: func(ctx context.Context, term string, limit int) ([]graphrank.CandidateNode, bool, error) {
			return a.hybridSearchNodes(ctx, key, principal.OrgID, term, limit)
		},
		Traverse: func(ctx context.Context, term string, observation graphrank.CandidateNode) (contextfabric.SubjectCandidate, graphrank.ObservationTraversal) {
			return graphrank.TraverseObservationToSubject(ctx, principal, request.RequestedScope, term, observation, isInternalSubject,
				func(ctx context.Context, uuid string) ([]graphrank.CandidateEdge, error) {
					return a.edgesOfNode(ctx, key, principal.OrgID, uuid)
				},
				func(ctx context.Context, uuid string) (graphrank.CandidateNode, bool) {
					n, err := a.nodeByUUID(ctx, key, principal.OrgID, uuid)
					if err != nil || n == nil {
						return graphrank.CandidateNode{}, false
					}
					return toCandidateNode(n), true
				},
			)
		},
		IsInternal: isInternalSubject,
		TraversalDegraded: func(ctx context.Context, orgID string, count int) {
			if a.config.Telemetry != nil {
				a.config.Telemetry.RecordObservationTraversalDegraded(ctx, orgID, count)
			}
		},
	}
	return graphrank.ResolveSubjects(ctx, principal, request, interpreted, deps)
}

func (a *Adapter) DiscoverContext(ctx context.Context, principal storage.Principal, request contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.GraphContext{}, errors.New("authenticated organization is required")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.GraphContext{}, err
	}
	key := graphKey(a.config.GraphPrefix, principal.OrgID)
	scope := request.Request.RequestedScope

	// Codex P2a: collection is bounded by a.config.MaxResults, a generous
	// superset cap -- NEVER by request.Request.Options.MaxRelationshipPaths,
	// the final per-request admission budget. Truncating to the tight
	// per-request limit here, before graphrank.SortEdgesByRelevance and
	// graphrank.AdmitEdges ever see the full candidate set, could let a
	// low-value edge reached early consume the limit while a
	// higher-relevance edge discovered later never gets the chance to
	// compete for it. The one and only truncation to MaxRelationshipPaths
	// happens inside AdmitEdges, after ranking.
	collectLimit := a.config.MaxResults

	// falkorgraph resolves every edge endpoint from a single whole-path
	// query -- one graph per org means there is no second-hop concept the
	// way zepgraph needs one (see reader.go's package-level doc in
	// zepgraph and graphrank.ResolvedEdge's doc comment). Two sources feed
	// the candidate edge set: (1) a bounded hop-walk from the committed
	// origin subjects (native Cypher variable-length path, [*1..2]), and
	// (2) a lexical full-text search over the question text, for the
	// subjectless-cohort case (no committed origin) and for text-relevant
	// items outside the hop radius.
	var resolvedNodes []graphrank.CandidateNode
	var resolvedEdges []graphrank.ResolvedEdge
	seenEdge := make(map[string]bool)
	seenNode := make(map[string]bool)
	// failedLookups counts edges dropped because a genuine backend lookup
	// failed (not because authorization or a legitimate "endpoint no longer
	// exists" filtered them) -- Codex P2c: this is the signal that
	// distinguishes real degradation from ordinary, silent filtering, and it
	// alone drives Coverage.Partial.
	failedLookups := 0

	for _, subject := range request.Resolution.Committed {
		nodes, edges, failed, err := a.hopWalk(ctx, key, principal.OrgID, principal, scope, subject, 2, collectLimit)
		if err != nil {
			return contextfabric.GraphContext{}, err
		}
		failedLookups += failed
		for _, n := range nodes {
			nk := graphrank.SubjectKey(mustSubject(n))
			if !seenNode[nk] {
				seenNode[nk] = true
				resolvedNodes = append(resolvedNodes, n)
			}
		}
		for _, e := range edges {
			if !seenEdge[e.UUID] {
				seenEdge[e.UUID] = true
				resolvedEdges = append(resolvedEdges, e)
			}
		}
	}

	// The search-truncation signal (fulltextSearchNodes' 2nd return value)
	// is deliberately discarded here: it exists to gate SUBJECT-RESOLUTION
	// auto-commit (graphrank.ResolveFromMergedCandidates' searchTruncated,
	// via ResolveSubjects above) against an incomplete candidate set, per
	// Codex's round-3 ruling. DiscoverContext has no analogous auto-commit
	// decision to protect -- this call feeds cohort/edge DISCOVERY, already
	// bounded and already best-effort, not a committed-subject gate.
	textNodes, _, err := a.fulltextSearchNodes(ctx, key, principal.OrgID, request.Request.Question, collectLimit)
	if err != nil {
		return contextfabric.GraphContext{}, err
	}
	// Codex P2a (round 2): the full-text-adjacent edge set is gathered from
	// EVERY matched node before any truncation decision, then ranked and
	// bounded the same way hopWalk's own per-hop collection is (Codex round
	// 2: "full-text node expansion also gathers adjacent edges with no
	// global cap") -- the previous version resolved every adjacent edge
	// from every matched node unconditionally as it was found, so an edge
	// discovered from the last matched node could never be dropped in favor
	// of a better one found earlier, but it also had no bound at all.
	var textCandidates []graphrank.CandidateEdge
	for _, n := range textNodes {
		subject, ok := graphrank.NodeSubject(n)
		if !ok {
			continue
		}
		nk := graphrank.SubjectKey(subject)
		if !seenNode[nk] {
			seenNode[nk] = true
			resolvedNodes = append(resolvedNodes, n)
		}
		textEdges, err := a.edgesOfNode(ctx, key, principal.OrgID, n.UUID)
		if err != nil {
			return contextfabric.GraphContext{}, err
		}
		for _, ce := range textEdges {
			if seenEdge[ce.UUID] {
				continue
			}
			seenEdge[ce.UUID] = true
			textCandidates = append(textCandidates, ce)
		}
	}
	textAdmitted := 0
	for _, ce := range rankCandidateEdges(textCandidates) {
		if collectLimit > 0 && textAdmitted >= collectLimit {
			break
		}
		resolved, resolution := a.resolveEdge(ctx, key, principal.OrgID, principal, scope, ce)
		switch resolution {
		case edgeLookupFailed:
			failedLookups++
			continue
		case edgeFiltered:
			continue
		}
		resolvedEdges = append(resolvedEdges, resolved)
		textAdmitted++
	}

	candidateEdges := make([]graphrank.CandidateEdge, 0, len(resolvedEdges))
	for _, r := range resolvedEdges {
		candidateEdges = append(candidateEdges, graphrank.CandidateEdge{
			UUID: r.UUID, Name: r.Name, Fact: r.Fact, Relevance: r.Relevance, Score: r.Score,
		})
	}
	order := graphrank.SortEdgesByRelevance(candidateEdges)
	orderedResolved := make([]graphrank.ResolvedEdge, 0, len(resolvedEdges))
	byUUID := make(map[string]graphrank.ResolvedEdge, len(resolvedEdges))
	for _, r := range resolvedEdges {
		byUUID[r.UUID] = r
	}
	for _, e := range order {
		orderedResolved = append(orderedResolved, byUUID[e.UUID])
	}

	admission := graphrank.AdmitEdges(principal.OrgID, orderedResolved, request.Request.Options, isInternalSubject)
	cohort := graphrank.DiscoveredCohort(principal, request, resolvedNodes, isInternalSubject)
	factRequirements := admission.FactRequirements
	if cohort != nil {
		factRequirements = graphrank.MergeFactRequirements(factRequirements, contextfabric.FactHealth, contextfabric.FactWorkload)
	}

	// Codex P2c: a failed endpoint lookup is a real, silent loss of material
	// (an edge/path that legitimately exists in the graph but this
	// investigation could not confirm and admit) -- it must never present as
	// clean, complete coverage.
	//
	// CHAOS-3779 codex round-1 H1: an edge whose Type failed the closed
	// relationship-type vocabulary is the same shape of silent loss --
	// AdmitEdges (pure, no I/O) only counts and names what it dropped;
	// this is the one I/O boundary in the call chain, so it is the one
	// place that both marks Coverage.Partial and logs it. The type
	// strings themselves are safe to log (not evidence, not a credential,
	// not org-identifying).
	//
	// Codex round-2 ruling: this emits ONE AGGREGATE WARNING PER
	// DiscoverContext CALL -- bounded, request-scoped, naming every
	// distinct dropped type that call saw -- not a process-lifetime
	// dedup (no sync.Once, no cross-call suppression). A strict
	// once-ever log would HIDE recurring bad data on every call after
	// the first; per-call aggregation stays bounded (never one log line
	// per dropped edge) without ever going silent on a call that has
	// something to report.
	partial := failedLookups > 0 || admission.DroppedUnknownRelationshipTypeCount > 0
	var degradedReasons []string
	if failedLookups > 0 {
		degradedReasons = append(degradedReasons, fmt.Sprintf("endpoint_lookup_failed:%d", failedLookups))
	}
	if admission.DroppedUnknownRelationshipTypeCount > 0 {
		degradedReasons = append(degradedReasons, fmt.Sprintf("unknown_relationship_type:%d", admission.DroppedUnknownRelationshipTypeCount))
		slog.Default().Warn("context_fabric: dropped relationship edge(s) with a type outside the closed vocabulary",
			"count", admission.DroppedUnknownRelationshipTypeCount, "types", admission.DroppedUnknownRelationshipTypeNames)
	}
	return contextfabric.GraphContext{
		Resolution: request.Resolution, Cohort: cohort, Paths: admission.Paths, DriverCandidates: admission.Drivers,
		EvidenceRefIDs: admission.EvidenceRefIDs, FactRequirements: factRequirements,
		Coverage: contextfabric.Coverage{
			Sources:         []contextfabric.SourceObservation{{Source: "context-fabric:graph", State: contextfabric.SourceAvailable, ObservedAt: ptrTime(a.now().UTC())}},
			Partial:         partial,
			DegradedReasons: degradedReasons,
		},
	}, nil
}

func mustSubject(n graphrank.CandidateNode) contextfabric.SubjectRef {
	subject, _ := graphrank.NodeSubject(n)
	return subject
}

func ptrTime[T any](v T) *T { return &v }
