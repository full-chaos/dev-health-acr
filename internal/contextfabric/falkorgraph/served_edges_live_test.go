package falkorgraph

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveTheDirectReadOfServedEdgesAgreesWithTheWalk (CHAOS-12537) is the
// real-store differential: on a real FalkorDB, the walk (DiscoverContext) and
// the direct read of a stored answer's served edges (ServedEdgeEvidence) see the
// same graph. Where the walk refuses an edge for an authorization reason (the
// edge gate, an endpoint gate) the direct read refuses it too, and the edge the
// gates admit is proven by both. The walk reads every edge here, so no budget
// cut is in play and the two must agree exactly; the budget-cut difference is
// named in TestTheDirectReadAgreesWithTheWalkOnEveryGate.
func TestLiveTheDirectReadOfServedEdgesAgreesWithTheWalk(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-12537-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	observed := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	created := observed.Add(-48 * time.Hour)
	owned := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/owned"}}
	private := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/private"}}
	entity := func(subject contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Properties: map[string]contextfabric.ScalarValue{}, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + subject.CanonicalID},
			ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1",
		}
	}
	relationship := func(id string, from, to contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: contractsv1.ContextFabricRelationshipBlocks, From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1",
		}
	}
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-owned", Label: "acme/owned"}
	workItem := func(id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:" + id, Label: id}
	}
	admitted, edgeDenied, endDenied := workItem("admitted"), workItem("edge-denied"), workItem("end-denied")
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_12537_0001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{
			entity(origin, owned), entity(admitted, owned), entity(edgeDenied, owned), entity(endDenied, private),
		},
		Relationships: []contextfabric.RelationshipProjection{
			relationship("rel_12537_admitted", origin, admitted, owned),
			relationship("rel_12537_edge_denied", origin, edgeDenied, private),
			relationship("rel_12537_end_denied", origin, endDenied, owned),
		},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/owned"}}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12537",
			Question: "what blocks acme/owned?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 50, MaxCohortMembers: 250, MaxRelationshipPaths: 250,
				MaxDrivers: 50, MaxEvidenceRefs: 500, MaxSerializedBytes: 1 << 20,
			},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{origin.Label},
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{origin}, Candidates: []contextfabric.SubjectCandidate{{
			ReceiptID: "receipt_subject", Subject: origin, State: contextfabric.ResolutionCommitted,
			MatchedTerms: []string{origin.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
		}}},
		Binding: binding,
	}
	walked, err := adapter.DiscoverContext(ctx, principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	served := []contextfabric.RelationshipEdge{
		{From: origin, To: admitted, Type: contractsv1.ContextFabricRelationshipBlocks},
		{From: origin, To: edgeDenied, Type: contractsv1.ContextFabricRelationshipBlocks},
		{From: origin, To: endDenied, Type: contractsv1.ContextFabricRelationshipBlocks},
	}
	proof, err := adapter.ServedEdgeEvidence(ctx, principal, request, served)
	if err != nil || proof.Cut {
		t.Fatalf("ServedEdgeEvidence() = cut %t, error %v", proof.Cut, err)
	}
	refs := []string{"evidence_rel_12537_admitted", "evidence_rel_12537_edge_denied", "evidence_rel_12537_end_denied"}
	for _, ref := range refs {
		byWalk, byRead := slices.Contains(walked.EvidenceRefIDs, ref), slices.Contains(proof.Context.EvidenceRefIDs, ref)
		if byWalk != byRead {
			t.Fatalf("%s: the walk proves %t, the direct read proves %t; they must agree on a real store", ref, byWalk, byRead)
		}
	}
	if !slices.Contains(proof.Context.EvidenceRefIDs, refs[0]) {
		t.Fatalf("the admitted edge is not proven: direct %v, walk %v; this case tests nothing", proof.Context.EvidenceRefIDs, walked.EvidenceRefIDs)
	}
	if slices.Contains(proof.Context.EvidenceRefIDs, refs[1]) || slices.Contains(proof.Context.EvidenceRefIDs, refs[2]) {
		t.Fatalf("the direct read proved an edge the gates refuse: %v", proof.Context.EvidenceRefIDs)
	}
}
