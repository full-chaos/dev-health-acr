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

// TestLiveChaos7080ProjectReachHonoursValidTime runs the engine-path project
// reach (project_reach.go, substituted inside AnchorMember) on a real
// FalkorDB, for every valid-time shape of the OWNED_BY_TEAM edge and the team
// node. The unit test pins the query text only; this one evaluates it.
//
// It is also a differential check between the two implementations of the
// same ownership reach: for every project, the engine path admits the
// restricted caller exactly when the direct-read path
// (OwnershipReachedRepositories, direct_read_ownership.go) reaches one of the
// caller's granted repositories.
func TestLiveChaos7080ProjectReachHonoursValidTime(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-chaos7080-reach-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	now := time.Now().UTC()
	longAgo, ended, tomorrow := now.Add(-72*time.Hour), now.Add(-2*time.Hour), now.Add(24*time.Hour)
	ref := func(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id}
	}
	teamT, teamU, teamW, teamS := ref(contextfabric.SubjectTeam, "team:T"), ref(contextfabric.SubjectTeam, "team:U"), ref(contextfabric.SubjectTeam, "team:W"), ref(contextfabric.SubjectTeam, "team:S")
	entity := func(subject contextfabric.SubjectRef, scope contextfabric.AuthorizationScope, validTo *time.Time) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + strings.ReplaceAll(subject.CanonicalID, ":", "_")},
			ObservedAt: now, ValidFrom: &longAgo, ValidTo: validTo, SourceVersion: "v1",
		}
	}
	type edgeSpec struct {
		team      contextfabric.SubjectRef
		validFrom time.Time
		validTo   *time.Time
	}
	cases := []struct {
		name  string
		edges []edgeSpec
		want  bool // admitted to a caller granted acme/a only
	}{
		{"current edge to a current team owning acme/a", []edgeSpec{{teamT, longAgo, nil}}, true},
		{"ended edge to a team owning acme/a", []edgeSpec{{teamT, longAgo, &ended}}, false},
		{"current edge to an ended team owning acme/a", []edgeSpec{{teamU, longAgo, nil}}, false},
		{"edge to a team owning acme/a valid only from tomorrow", []edgeSpec{{teamT, tomorrow, nil}}, false},
		{"current edge to acme/c team, ended edge to acme/a team", []edgeSpec{{teamW, longAgo, nil}, {teamT, longAgo, &ended}}, false},
		{"current edges to acme/c team and acme/a team", []edgeSpec{{teamW, longAgo, nil}, {teamT, longAgo, nil}}, true},
		{"current edge to a wildcard team", []edgeSpec{{teamS, longAgo, nil}}, false},
		{"no owning team", nil, false},
	}

	entities := []contextfabric.EntityProjection{
		entity(teamT, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}, nil),
		entity(teamU, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}, &ended),
		entity(teamW, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/c"}}, nil),
		entity(teamS, contextfabric.AuthorizationScope{TeamIDs: []string{"team:S"}}, nil),
	}
	var relationships []contextfabric.RelationshipProjection
	projects := make([]contextfabric.SubjectRef, len(cases))
	for index, tc := range cases {
		id := "project:" + strings.ReplaceAll(strings.ReplaceAll(tc.name, " ", "-"), "/", "-")
		projects[index] = ref(contextfabric.SubjectProject, id)
		entities = append(entities, entity(projects[index], contextfabric.AuthorizationScope{ProjectIDs: []string{id}}, nil))
		for edgeIndex, spec := range tc.edges {
			validFrom := spec.validFrom
			relationships = append(relationships, contextfabric.RelationshipProjection{
				RelationshipID: "rel_" + strings.ReplaceAll(id, ":", "_") + "_" + string(rune('a'+edgeIndex)), Type: "OWNED_BY_TEAM",
				From: projects[index], To: spec.team,
				Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
				Authorization:  contextfabric.AuthorizationScope{ProjectIDs: []string{id}},
				EvidenceRefIDs: []string{"evidence_rel_" + strings.ReplaceAll(id, ":", "_")},
				ObservedAt:     now, ValidFrom: &validFrom, ValidTo: spec.validTo, SourceVersion: "v1",
			})
		}
	}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_chaos7080_reach_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: now,
		Entities: entities, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("entity ApplyProjectionBatch() error = %v", err)
	}
	edges := batch
	edges.BatchID, edges.Cursor, edges.NextCursor = "batch_chaos7080_reach_00000002", "cursor-1", "cursor-2"
	edges.Entities, edges.Relationships = []contextfabric.EntityProjection{}, relationships
	if _, err := adapter.ApplyProjectionBatch(ctx, edges); err != nil {
		t.Fatalf("relationship ApplyProjectionBatch() error = %v", err)
	}

	restricted := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/a"}}
	binding, err := adapter.ResolveInvestigationBinding(ctx, restricted)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	directReach, err := adapter.OwnershipReachedRepositories(ctx, restricted, binding, projects)
	if err != nil {
		t.Fatalf("OwnershipReachedRepositories() error = %v", err)
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			member, err := adapter.AnchorMember(ctx, restricted, contextfabric.RequestedScope{}, binding, contextfabric.SubjectProject, projects[index].CanonicalID)
			if err != nil {
				t.Fatalf("AnchorMember() error = %v", err)
			}
			if !member.Exists || member.Authorized != tc.want {
				t.Fatalf("engine path AnchorMember() = %+v, want Exists=true Authorized=%t", member, tc.want)
			}
			if direct := slices.Contains(directReach[index], "acme/a"); direct != member.Authorized {
				t.Fatalf("the two reach implementations disagree: engine path admitted=%t, direct-read reach %v", member.Authorized, directReach[index])
			}
			unrestricted, err := adapter.AnchorMember(ctx, storage.Principal{OrgID: orgID}, contextfabric.RequestedScope{}, binding, contextfabric.SubjectProject, projects[index].CanonicalID)
			if err != nil || !unrestricted.Authorized {
				t.Fatalf("unrestricted caller AnchorMember() = %+v, %v, want admitted", unrestricted, err)
			}
		})
	}
}
