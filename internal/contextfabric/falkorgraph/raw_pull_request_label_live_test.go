package falkorgraph_test

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A graph holds the state the old writers left: an edge created the pull
// request's node with its canonical id as the label, and no entity write reached
// it. The read serves its number, and a later entity write still wins
// (CHAOS-12468).
func TestAnOldPullRequestStubReadsAsItsNumberOnRealStores(t *testing.T) {
	ctx := chaos7074Ctx("chaos12468")
	adapter := newLiveAdapter(t, ctx)
	const orgID, id = "org-chaos12468", "pull_request:repo-1:5063"
	now := time.Now().UTC()
	from := now.Add(-time.Hour)
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}
	issue := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item.v2:w1", Label: "linear:CHAOS-1"}
	stub := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: id, Label: id}
	entity := func(subject contextfabric.SubjectRef) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: scope, EvidenceRefIDs: []string{"evidence_entity"}, ObservedAt: now, ValidFrom: &from, SourceVersion: "v1",
		}
	}
	batch := func(n string, entities []contextfabric.EntityProjection, relationships []contextfabric.RelationshipProjection) contextfabric.ProjectionBatch {
		return contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_chaos12468_" + n, OrgID: orgID, Source: "live-test",
			SourceVersion: "v1", Cursor: "cursor-" + n, NextCursor: "cursor-next-" + n, GeneratedAt: now,
			Entities: entities, Relationships: relationships,
			Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
		}
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch("1", []contextfabric.EntityProjection{entity(issue)}, []contextfabric.RelationshipProjection{{
		RelationshipID: "rel_link", Type: contextfabric.RelationshipType("LINKS_PULL_REQUEST"), From: issue, To: stub,
		Derivation: contextfabric.DerivationRuleInferred, EpistemicStatus: contextfabric.EpistemicSourceAsserted,
		Authorization: scope, EvidenceRefIDs: []string{"evidence_link"}, ObservedAt: now, ValidFrom: &from, SourceVersion: "v1",
	}})); err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{OrgID: orgID, Subject: "u", CredentialID: "c"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	read := func() directread.EdgeCandidate {
		t.Helper()
		page, err := adapter.DirectEdgePage(ctx, principal, binding, directread.EdgePageQuery{
			Origins: []contextfabric.SubjectRef{issue}, Direction: directread.EdgeDirectionBoth, Limit: 10, ValidAt: time.Now().UTC(), Current: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Edges) != 1 {
			t.Fatalf("edges = %+v, want the one link", page.Edges)
		}
		return page.Edges[0]
	}
	edge := read()
	if got := edge.To.Subject.Label; got != "PR #5063" {
		t.Fatalf("pull request end label = %q, want PR #5063", got)
	}
	fact, _ := edge.Attributes["fact"].(string)
	if fact != "linear:CHAOS-1 links pull request PR #5063" || strings.Contains(fact, id) {
		t.Fatalf("fact = %q", fact)
	}
	titled := stub
	titled.Label = "ci: one canary leg"
	if _, err := adapter.ApplyProjectionBatch(ctx, batch("2", []contextfabric.EntityProjection{entity(titled)}, []contextfabric.RelationshipProjection{})); err != nil {
		t.Fatal(err)
	}
	if got := read().To.Subject.Label; got != titled.Label {
		t.Fatalf("label after the entity write = %q, want the title", got)
	}
}
