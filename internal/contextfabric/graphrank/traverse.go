package graphrank

import (
	"context"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ObservationTraversal classifies the outcome of TraverseObservationToSubject.
// Codex round-3 finding P1-1: a backend error must never collapse to the
// same signal as a confirmed, legitimate absence of a parent -- doing so
// re-enabled the round-2 misattribution bug (a document auto-committing
// itself) whenever the parent lookup merely failed transiently.
type ObservationTraversal int

const (
	// ObservationNoParent means the traversal completed and found no
	// attribution edge (or none survived relation-type/authorization
	// filtering) -- a genuine, confirmed absence of a canonical parent.
	ObservationNoParent ObservationTraversal = iota
	// ObservationParentFound means a canonical parent candidate was found
	// and is returned.
	ObservationParentFound
	// ObservationTraversalErrored means a backend call failed, or a
	// second-hop node could not be fetched or could not be verified as
	// belonging to this organization -- parent status is genuinely
	// unknown, not confirmed absent.
	ObservationTraversalErrored
)

// TraverseObservationToSubject implements observation-to-entity traversal:
// given a document/episode node that a hybrid search matched on its text, it
// walks the node's incoming edges back to the canonical entity that
// document/episode is projected against and proposes that entity as an
// additional subject candidate. This lets a term that only appears inside a
// document body or episode summary still resolve to the subject the
// question is actually about, without requiring the term to match the
// subject's own label, alias, or previous name directly.
//
// getEdges fetches every edge touching observation.UUID (a backend's
// GetNodeEdges equivalent). getVerifiedNode fetches a node by UUID AND
// confirms it genuinely belongs to principal.OrgID before returning ok=true
// -- for a backend where every lookup is already structurally scoped to one
// organization's graph (e.g. falkorgraph's one-graph-per-org), this
// verification is unconditional; for a backend with organization-agnostic
// UUID lookups (zepgraph's GetNode has no per-call graph parameter), the
// caller must re-derive and compare the expected UUID the way
// zepgraph.verifiedNodeSubject does. Either way, ok=false here always means
// "uncertain" (ObservationTraversalErrored), never "confirmed absent" --
// only an edge set with no matching attribution edge at all is a confirmed
// absence.
//
// Ported from zepgraph.traverseObservationToSubject.
func TraverseObservationToSubject(
	ctx context.Context,
	principal storage.Principal,
	scope contextfabric.RequestedScope,
	term string,
	observation CandidateNode,
	isInternal func(contextfabric.SubjectRef) bool,
	getEdges func(context.Context, string) ([]CandidateEdge, error),
	getVerifiedNode func(context.Context, string) (CandidateNode, bool),
) (contextfabric.SubjectCandidate, ObservationTraversal) {
	if strings.TrimSpace(observation.UUID) == "" {
		return contextfabric.SubjectCandidate{}, ObservationNoParent
	}
	edges, err := getEdges(ctx, observation.UUID)
	if err != nil {
		return contextfabric.SubjectCandidate{}, ObservationTraversalErrored
	}
	// uncertain tracks whether any candidate attribution edge was found but
	// could not be resolved to a trusted parent (a second-hop fetch failure
	// or an organization-identity mismatch) -- as opposed to no candidate
	// edge existing at all. Only the latter is a confirmed "no parent"; the
	// former must report ObservationTraversalErrored so the caller fails
	// toward ambiguity rather than treating an unresolved edge as if it
	// never existed.
	uncertain := false
	for _, edge := range edges {
		if edge.TargetNodeUUID != observation.UUID || strings.TrimSpace(edge.SourceNodeUUID) == "" {
			continue
		}
		if !IsObservationAttributionRelation(NormalizeRelation(edge.Name)) {
			continue
		}
		// The attribution edge is its own authorization boundary,
		// independent of either endpoint's own scope: a source node and a
		// document can each be individually unrestricted while the fact
		// "this document belongs to this subject" is itself scoped more
		// narrowly. A clean authorization denial is a definitive answer,
		// not a failure, so it does not set uncertain.
		if !AuthorizedAttributes(principal, scope, edge.Attributes) {
			continue
		}
		source, verified := getVerifiedNode(ctx, edge.SourceNodeUUID)
		if !verified {
			uncertain = true
			continue
		}
		candidate, ok := NodeCandidate(principal, scope, term, source, isInternal)
		if !ok || IsObservationSubjectKind(candidate.Subject.Kind) {
			continue
		}
		// One hop removed from a direct label/alias/text match, so the
		// traversed candidate never outranks a subject the search matched
		// directly.
		candidate.Confidence *= 0.85
		candidate.MatchReasons = []string{"Matched an associated document or episode that references this subject."}
		return candidate, ObservationParentFound
	}
	if uncertain {
		return contextfabric.SubjectCandidate{}, ObservationTraversalErrored
	}
	return contextfabric.SubjectCandidate{}, ObservationNoParent
}
