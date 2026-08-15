package graphrank

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func observationNode(uuid, canonicalID, label string, relevance float64) CandidateNode {
	r := relevance
	return CandidateNode{
		UUID: uuid, Name: label, Relevance: Normalized(r),
		Attributes: map[string]interface{}{
			"canonical_id": canonicalID, "subject_kind": string(contextfabric.SubjectDocument), "label": label,
			"evidence_refs": []string{"evidence_document_1234"},
		},
	}
}

// TestTraverseObservationToSubjectFindsCanonicalParent is the direct port of
// zepgraph's TestResolveSubjectsTraversesObservationNodeToCanonicalSubject,
// calling graphrank.TraverseObservationToSubject directly with injected
// getEdges/getVerifiedNode callbacks instead of going through an adapter's
// I/O. Proves observation-to-entity traversal finds the canonical parent and
// discounts its confidence below a raw match.
func TestTraverseObservationToSubjectFindsCanonicalParent(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	document := observationNode("node-document", "document_1234", "Ask Dev readiness review", 0.95)
	subject := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 0, "*")

	getEdges := func(context.Context, string) ([]CandidateEdge, error) {
		return []CandidateEdge{{
			UUID: "edge-documented-by", Name: "DOCUMENTED_BY", Fact: "Ask Dev is documented by Ask Dev readiness review.",
			SourceNodeUUID: subject.UUID, TargetNodeUUID: document.UUID,
			Attributes: map[string]interface{}{},
		}}, nil
	}
	getVerifiedNode := func(context.Context, string) (CandidateNode, bool) { return subject, true }

	candidate, outcome := TraverseObservationToSubject(context.Background(), principal, scope, "readiness review", document, noInternalSubjects, true, getEdges, getVerifiedNode)
	if outcome != ObservationParentFound {
		t.Fatalf("TraverseObservationToSubject() outcome = %v, want ObservationParentFound", outcome)
	}
	if candidate.Subject.CanonicalID != "project_ask_dev" {
		t.Fatalf("traversed candidate = %#v, want the canonical parent", candidate)
	}
	documentConfidence := ResultConfidence(document.Relevance, document.Score)
	if candidate.Confidence <= 0 || candidate.Confidence >= documentConfidence {
		t.Fatalf("traversed subject confidence = %v, want positive and discounted below the observation's own confidence %v", candidate.Confidence, documentConfidence)
	}
}

// TestTraverseObservationToSubjectIgnoresUnrelatedRelationEdges is the
// direct port of zepgraph's TestResolveSubjectsTraversalIgnoresUnrelatedRelationEdges:
// only DOCUMENTED_BY/HAS_EPISODE attribution edges are followed -- a generic
// MENTIONS edge pointing at the observation must never be treated as
// attribution.
func TestTraverseObservationToSubjectIgnoresUnrelatedRelationEdges(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	document := observationNode("node-document", "document_1234", "Ask Dev readiness review", 0.95)
	unrelated := candidateNode(contextfabric.SubjectProject, "project_unrelated", "Unrelated Project", 0.99, "*")

	getEdges := func(context.Context, string) ([]CandidateEdge, error) {
		return []CandidateEdge{{
			UUID: "edge-mentions", Name: "MENTIONS", Fact: "Unrelated Project mentions Ask Dev readiness review.",
			SourceNodeUUID: unrelated.UUID, TargetNodeUUID: document.UUID,
			Attributes: map[string]interface{}{"authorization_repositories": "*"},
		}}, nil
	}
	getVerifiedNode := func(context.Context, string) (CandidateNode, bool) { return unrelated, true }

	_, outcome := TraverseObservationToSubject(context.Background(), principal, scope, "readiness review", document, noInternalSubjects, true, getEdges, getVerifiedNode)
	if outcome != ObservationNoParent {
		t.Fatalf("TraverseObservationToSubject() outcome = %v, want ObservationNoParent: a MENTIONS edge must never be followed as attribution", outcome)
	}
}

// TestTraverseObservationToSubjectRequiresEdgeAuthorization is the direct
// port of zepgraph's TestResolveSubjectsTraversalRequiresEdgeAuthorization:
// the attribution edge's own authorization narrows what the relationship
// discloses, independent of either endpoint's own visibility -- a source
// node visible on its own must not be traversed to if the edge itself is
// scoped to a repository the principal cannot see.
func TestTraverseObservationToSubjectRequiresEdgeAuthorization(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	scope := contextfabric.RequestedScope{}
	document := observationNode("node-document", "document_1234", "Ask Dev readiness review", 0.95)
	subject := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 0, "*")

	getEdges := func(context.Context, string) ([]CandidateEdge, error) {
		return []CandidateEdge{{
			UUID: "edge-documented-by", Name: "DOCUMENTED_BY", Fact: "Ask Dev is documented by Ask Dev readiness review.",
			SourceNodeUUID: subject.UUID, TargetNodeUUID: document.UUID,
			// The attribution fact itself is scoped to a repository the
			// principal does not have, even though the source node reports
			// an unrestricted "*" scope.
			Attributes: map[string]interface{}{"authorization_repositories": []string{"other/private"}},
		}}, nil
	}
	getVerifiedNode := func(context.Context, string) (CandidateNode, bool) { return subject, true }

	_, outcome := TraverseObservationToSubject(context.Background(), principal, scope, "readiness review", document, noInternalSubjects, true, getEdges, getVerifiedNode)
	if outcome == ObservationParentFound {
		t.Fatal("TraverseObservationToSubject() found a parent through an unauthorized attribution edge")
	}
}

// TestTraverseObservationToSubjectErrorPreventsConfirmedNoParent is the
// direct port of zepgraph's TestResolveSubjectsTraversalErrorPreventsAutoCommitOfObservation
// (Codex round-3 finding P1-1): a backend failure must report
// ObservationTraversalErrored, never collapse to the same ObservationNoParent
// a genuine, confirmed absence of a parent produces -- the caller (graphrank
// ResolveSubjects) fails toward ambiguity only for the former.
//
// graphrank's getVerifiedNode callback contract deliberately unifies
// zepgraph's two second-hop failure modes (a raw GetNode error, and a
// fetched-but-organization-identity-mismatched node) into a single "ok=false
// means uncertain" signal (see TraverseObservationToSubject's doc comment) --
// so both of zepgraph's original subcases ("source GetNode fails" and
// "source identity verification fails") are exercised identically at this
// layer via getVerifiedNode returning false.
func TestTraverseObservationToSubjectErrorPreventsConfirmedNoParent(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	document := observationNode("node-document-erroring", "document_error", "Ask Dev readiness review", 0.9)

	t.Run("getEdges fails", func(t *testing.T) {
		t.Parallel()
		getEdges := func(context.Context, string) ([]CandidateEdge, error) {
			return nil, errors.New("transient backend failure")
		}
		getVerifiedNode := func(context.Context, string) (CandidateNode, bool) { return CandidateNode{}, false }
		_, outcome := TraverseObservationToSubject(context.Background(), principal, scope, "readiness review", document, noInternalSubjects, true, getEdges, getVerifiedNode)
		if outcome != ObservationTraversalErrored {
			t.Fatalf("outcome = %v, want ObservationTraversalErrored", outcome)
		}
	})

	t.Run("source verification is unresolved (GetNode failure or identity mismatch)", func(t *testing.T) {
		t.Parallel()
		getEdges := func(context.Context, string) ([]CandidateEdge, error) {
			return []CandidateEdge{{
				UUID: "edge-documented-by", Name: "DOCUMENTED_BY", SourceNodeUUID: "node-attributed-source", TargetNodeUUID: document.UUID,
				Attributes: map[string]interface{}{},
			}}, nil
		}
		getVerifiedNode := func(context.Context, string) (CandidateNode, bool) { return CandidateNode{}, false }
		_, outcome := TraverseObservationToSubject(context.Background(), principal, scope, "readiness review", document, noInternalSubjects, true, getEdges, getVerifiedNode)
		if outcome != ObservationTraversalErrored {
			t.Fatalf("outcome = %v, want ObservationTraversalErrored", outcome)
		}
	})
}
