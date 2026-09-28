package falkorgraph_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveChaos7071OwnershipReachedRepositories runs the ownership-reach
// Cypher on a real FalkorDB (the unit test's fake computes rows itself and
// cannot catch a wrong comparison). Valid time is read at the adapter clock
// on BOTH the OWNED_BY_TEAM edge and the team node:
//
//   - team T (acme/a), current            -> reach acme/a
//   - team U (acme/b), node ended          -> no reach
//   - team W (acme/c), current             -> reach acme/c
//   - team S, wildcard repositories        -> no reach ("*" is not reach)
//   - project Active: current edge to T    -> acme/a
//   - project Ended:  ended edge to W      -> no reach
//   - project Stale:  current edge to U    -> no reach (team node ended)
//   - project Mixed:  current edge to T, ended edge to W -> acme/a only
//   - project Future: edge to W valid only from tomorrow -> no reach
func TestLiveChaos7071OwnershipReachedRepositories(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-chaos7071-reach-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	now := time.Now().UTC()
	longAgo, ended, tomorrow := now.Add(-72*time.Hour), now.Add(-2*time.Hour), now.Add(24*time.Hour)
	ref := func(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id}
	}
	teamT, teamU, teamW, teamS := ref(contextfabric.SubjectTeam, "team:T"), ref(contextfabric.SubjectTeam, "team:U"), ref(contextfabric.SubjectTeam, "team:W"), ref(contextfabric.SubjectTeam, "team:S")
	active, endedP, stale, mixed, future := ref(contextfabric.SubjectProject, "project:active"), ref(contextfabric.SubjectProject, "project:ended"), ref(contextfabric.SubjectProject, "project:stale"), ref(contextfabric.SubjectProject, "project:mixed"), ref(contextfabric.SubjectProject, "project:future")

	entity := func(subject contextfabric.SubjectRef, scope contextfabric.AuthorizationScope, validTo *time.Time) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + strings.ReplaceAll(subject.CanonicalID, ":", "_")},
			ObservedAt: now, ValidFrom: &longAgo, ValidTo: validTo, SourceVersion: "v1",
		}
	}
	project := func(subject contextfabric.SubjectRef) contextfabric.EntityProjection {
		return entity(subject, contextfabric.AuthorizationScope{ProjectIDs: []string{subject.CanonicalID}}, nil)
	}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_chaos7071_reach_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: now,
		Entities: []contextfabric.EntityProjection{
			entity(teamT, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}, nil),
			entity(teamU, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/b"}}, &ended),
			entity(teamW, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/c"}}, nil),
			entity(teamS, contextfabric.AuthorizationScope{TeamIDs: []string{"team:S"}}, nil),
			project(active), project(endedP), project(stale), project(mixed), project(future),
		},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("entity ApplyProjectionBatch() error = %v", err)
	}
	edge := func(id string, from, to contextfabric.SubjectRef, validFrom time.Time, validTo *time.Time) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: "OWNED_BY_TEAM", From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: contextfabric.AuthorizationScope{ProjectIDs: []string{from.CanonicalID}}, EvidenceRefIDs: []string{"evidence_" + id},
			ObservedAt: now, ValidFrom: &validFrom, ValidTo: validTo, SourceVersion: "v1",
		}
	}
	edges := batch
	edges.BatchID, edges.Cursor, edges.NextCursor = "batch_chaos7071_reach_00000002", "cursor-1", "cursor-2"
	edges.Entities = []contextfabric.EntityProjection{}
	edges.Relationships = []contextfabric.RelationshipProjection{
		edge("rel_active_t", active, teamT, longAgo, nil),
		edge("rel_ended_w", endedP, teamW, longAgo, &ended),
		edge("rel_stale_u", stale, teamU, longAgo, nil),
		edge("rel_mixed_t", mixed, teamT, longAgo, nil),
		edge("rel_mixed_w", mixed, teamW, longAgo, &ended),
		edge("rel_future_w", future, teamW, tomorrow, nil),
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, edges); err != nil {
		t.Fatalf("relationship ApplyProjectionBatch() error = %v", err)
	}

	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/a"}}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	subjects := []contextfabric.SubjectRef{teamT, teamU, teamW, teamS, active, endedP, stale, mixed, future}
	want := [][]string{{"acme/a"}, nil, {"acme/c"}, nil, {"acme/a"}, nil, nil, {"acme/a"}, nil}
	got, err := adapter.OwnershipReachedRepositories(ctx, principal, binding, subjects)
	if err != nil {
		t.Fatalf("OwnershipReachedRepositories() error = %v", err)
	}
	if len(got) != len(subjects) {
		t.Fatalf("OwnershipReachedRepositories() returned %d rows for %d subjects", len(got), len(subjects))
	}
	for index, subject := range subjects {
		reached := slices.Clone(got[index])
		slices.Sort(reached)
		if len(reached) == 0 && len(want[index]) == 0 {
			continue
		}
		if !slices.Equal(reached, want[index]) {
			t.Errorf("%s: reach %v, want %v", subject.CanonicalID, reached, want[index])
		}
	}
}
