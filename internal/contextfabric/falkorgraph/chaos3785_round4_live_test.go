package falkorgraph

// White-box (package falkorgraph, not falkorgraph_test): reads a raw edge's
// properties via a.api.query directly, which adapter_live_invariants_test.go's
// black-box suite cannot reach. Same reasoning as chaos3785_round3_live_test.go.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// edgeByRelationshipID reads one edge's raw properties by its
// relationship_id -- there is no existing exported/package helper for this
// (edgesOfNode looks up by ENDPOINT, not by relationship_id), so this test
// file provides its own minimal one rather than growing queries.go for a
// test-only need.
func (a *Adapter) edgeByRelationshipID(ctx context.Context, key, relationshipID string) (*edge, error) {
	rows, err := a.api.query(ctx, key, fmt.Sprintf("MATCH ()-[r:%s {%s:$rid}]-() RETURN r LIMIT 1", labelRelation, propRelationshipID),
		map[string]interface{}{"rid": relationshipID}, true)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	e, _ := rows[0]["r"].(*edge)
	return e, nil
}

// TestLiveReprojectedRelationshipClearsAStaleValidityWindow is CHAOS-3785
// codex round-4 finding R4-1: the same class R3-1 fixed for a node's
// canonical write applied equally to the edge write itself -- relationship
// valid_from/valid_to were only ADDED when non-nil, so the SAME
// relationship_id re-projected on a later tick with no validity window this
// time could not clear a window an earlier projection of that same edge had
// set. Read-side confirmation this is live-connected, not cosmetic:
// queries.go's toCandidateEdge feeds valid_from/valid_to straight into
// graphrank.CandidateEdge.ValidAt/InvalidAt.
func TestLiveReprojectedRelationshipClearsAStaleValidityWindow(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-r41-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, orgID)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	from := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_r41_from", Label: "R4-1 from"}
	to := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_r41_to", Label: "R4-1 to"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}
	relationshipID := "relationship_r41_00000001"
	validFrom, validTo := observed, observed.Add(time.Hour)

	// First projection of relationshipID carries a real validity window.
	windowedBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_r41_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: relationshipID, Type: "BLOCKS", From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_r41_1"},
			ObservedAt: observed, ValidFrom: &validFrom, ValidTo: &validTo, SourceVersion: "v1",
		}},
		Entities: []contextfabric.EntityProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, windowedBatch); err != nil {
		t.Fatalf("windowed ApplyProjectionBatch() error = %v", err)
	}

	before, err := adapter.edgeByRelationshipID(ctx, key, relationshipID)
	if err != nil {
		t.Fatalf("edgeByRelationshipID() before re-projection error = %v", err)
	}
	if before == nil {
		t.Fatal("expected the edge to exist after the windowed projection")
	}
	if _, ok := before.Properties[propValidFrom]; !ok {
		t.Fatalf("expected the windowed projection to seed %s, properties = %+v", propValidFrom, before.Properties)
	}

	// The SAME relationship_id is re-projected on a LATER tick with NO
	// validity window this time.
	unwindowedBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_r41_2", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed.Add(2 * time.Hour),
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: relationshipID, Type: "BLOCKS", From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_r41_2"},
			ObservedAt: observed.Add(2 * time.Hour), SourceVersion: "v1",
		}},
		Entities: []contextfabric.EntityProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, unwindowedBatch); err != nil {
		t.Fatalf("unwindowed ApplyProjectionBatch() error = %v", err)
	}

	after, err := adapter.edgeByRelationshipID(ctx, key, relationshipID)
	if err != nil {
		t.Fatalf("edgeByRelationshipID() after re-projection error = %v", err)
	}
	if after == nil {
		t.Fatal("expected the edge to still exist after the unwindowed re-projection")
	}
	for _, propKey := range []string{propValidFrom, propValidFromNs, propValidTo, propValidToNs} {
		if value, ok := after.Properties[propKey]; ok {
			t.Fatalf("expected the unwindowed re-projection to clear stale %s, still present as %v: %+v", propKey, value, after.Properties)
		}
	}
}

// TestLiveRelationshipStubCreationDoesNotSwapEndpointLabels is CHAOS-3785
// codex round-4 finding R4-2's proof-gap close: the AST call-site pinning
// test (chaos3785_round3_fake_test.go) only counts WHICH builder each
// enclosing method calls, not whether projectRelationship's two
// referencedSubjectStubMergeCypher("a", ..., "fromAttrs") /
// ("b", ..., "toAttrs") calls wire the right attrs map to the right alias.
// Every OTHER field in fromAttrs/toAttrs is identical (both derive from the
// SAME relationship.Authorization/EvidenceRefIDs/etc, not per-endpoint), so
// a swap between them is observable in exactly one place: which endpoint's
// node gets seeded with which Label on ON CREATE. Every existing round-trip
// test (TestLivePartOfEdgeRoundTripsFromProjectionThroughDiscoverContext,
// TestLiveDiscoverContextEnforcesAuthorizationOnPathsAndAttributionEdges)
// ALSO entity-projects both endpoints in the same batch, and entities
// process after relationships (ApplyProjectionBatch's own ordering) -- the
// OWNED entity write's unconditional overwrite masks a stub-seeded label
// swap before any of those tests ever reads it back. This test uses a
// relationship-ONLY batch (neither endpoint gets its own entity write) so
// the ON-CREATE-seeded label is the only thing that could be there.
func TestLiveRelationshipStubCreationDoesNotSwapEndpointLabels(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-r42-label-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, orgID)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	from := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_r42_from", Label: "R4-2 From Label"}
	to := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_r42_to", Label: "R4-2 To Label"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}

	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_r42_label_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "relationship_r42_label_1", Type: "BLOCKS", From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_r42_label"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Entities: []contextfabric.EntityProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	fromNode, err := adapter.nodeByKindID(ctx, key, orgID, string(from.Kind), from.CanonicalID)
	if err != nil {
		t.Fatalf("nodeByKindID(from) error = %v", err)
	}
	if fromNode == nil {
		t.Fatal("expected the From endpoint's stub node to exist")
	}
	if got := fromNode.Properties[propLabel]; got != from.Label {
		t.Fatalf("From endpoint's ON-CREATE-seeded label = %v, want %q (fromAttrs/toAttrs swapped?)", got, from.Label)
	}

	toNode, err := adapter.nodeByKindID(ctx, key, orgID, string(to.Kind), to.CanonicalID)
	if err != nil {
		t.Fatalf("nodeByKindID(to) error = %v", err)
	}
	if toNode == nil {
		t.Fatal("expected the To endpoint's stub node to exist")
	}
	if got := toNode.Properties[propLabel]; got != to.Label {
		t.Fatalf("To endpoint's ON-CREATE-seeded label = %v, want %q (fromAttrs/toAttrs swapped?)", got, to.Label)
	}
}

// TestLiveContentReferencedWriteNeverOverwritesTheAttachedSubjectsOwnLabelOrTemporalFields
// is R4-2's proof-gap close for projectContent's "a" (content.Subject,
// REFERENCED) call site's metadata/temporal claim specifically -- distinct
// from TestLiveContentProjectionNeverDowngradesTheAttachedSubjectsOwnAuthorization
// (round-2 R2-1), which proves the AUTHORIZATION half but reuses the SAME
// Label for both the canonical entity and content.Subject, so a label (or
// temporal-field) clobber on ON MATCH would be invisible to it -- the
// values would already match by construction. This test deliberately gives
// content.Subject a DIFFERENT label AND plants a real validity window on
// the canonical entity (devhealthsource itself never sets one, but the
// contract allows it, and this proves the general ON-MATCH-touches-nothing
// mechanism, not a devhealthsource-specific shape) to make either kind of
// clobber observable.
func TestLiveContentReferencedWriteNeverOverwritesTheAttachedSubjectsOwnLabelOrTemporalFields(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-r42-content-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, orgID)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	canonicalLabel := "Canonical Entity Title"
	target := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_r42_content_target", Label: canonicalLabel}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}
	canonicalFrom, canonicalTo := observed, observed.Add(3*time.Hour)

	entityBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_r42_content_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: target, Authorization: scope, EvidenceRefIDs: []string{"evidence_r42_content_target"},
			ObservedAt: observed, ValidFrom: &canonicalFrom, ValidTo: &canonicalTo, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, entityBatch); err != nil {
		t.Fatalf("entity ApplyProjectionBatch() error = %v", err)
	}

	// content.Subject deliberately carries a DIFFERENT label than target's
	// real one, and content itself carries no validity window (Contents
	// don't have ValidFrom/ValidTo) -- an ON MATCH clobber would either
	// replace the label with this bare-ID-shaped one, or (per R3-1's OWNED
	// null-clearing logic, if wrongly applied to a REFERENCED write) erase
	// target's real window.
	contentSubjectRef := contextfabric.SubjectRef{Kind: target.Kind, CanonicalID: target.CanonicalID, Label: "work_r42_content_target"}
	contentBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_r42_content_2", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed.Add(time.Hour),
		Entities: []contextfabric.EntityProjection{},
		Contents: []contextfabric.ContentProjection{{
			ContentID: "content_r42_00000001", Subject: contentSubjectRef, Title: "Attached note", Body: "body",
			ContentDigest: "digest_r42_00000001", Authorization: scope, EvidenceRefIDs: []string{"evidence_r42_content_note"},
			ObservedAt: observed.Add(time.Hour), SourceVersion: "v1", Untrusted: true,
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, contentBatch); err != nil {
		t.Fatalf("content ApplyProjectionBatch() error = %v", err)
	}

	after, err := adapter.nodeByKindID(ctx, key, orgID, string(target.Kind), target.CanonicalID)
	if err != nil {
		t.Fatalf("nodeByKindID() after content write error = %v", err)
	}
	if after == nil {
		t.Fatal("expected target's node to still exist after the content write")
	}
	if got := after.Properties[propLabel]; got != canonicalLabel {
		t.Fatalf("target's label after a content write = %v, want unchanged %q", got, canonicalLabel)
	}
	if _, ok := after.Properties[propValidFrom]; !ok {
		t.Fatalf("target's %s was cleared by a REFERENCED content write -- only an OWNED write may clear it: %+v", propValidFrom, after.Properties)
	}
}
