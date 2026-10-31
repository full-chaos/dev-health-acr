package falkorgraph

// White-box (package falkorgraph): reads the raw node property through
// nodeByKindID, as chaos3785_round3_live_test.go does.

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestLiveWorkItemProjectIdentitiesRoundTripThroughFalkorDB is CHAOS-12226's
// live proof of the write semantics on a real FalkorDB:
//
//  1. a stated set lands as a list (sorted, deduplicated);
//  2. a stated EMPTY set lands as a present, empty list (SET n += {k: []}
//     keeps the key) -- "in no project", not "not stated";
//  3. a referenced stub write of the same work item (a relationship
//     endpoint) leaves it untouched;
//  4. an entity write that does not state it removes the key (null), so a
//     stale set can never survive a producer that stopped stating it.
func TestLiveWorkItemProjectIdentitiesRoundTripThroughFalkorDB(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-12226-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, orgID)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	target := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:LIN-12226", Label: "LIN-12226"}
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:LIN-OTHER", Label: "LIN-OTHER"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acr-context-fabric:no-repository"}}
	step := 0
	apply := func(entities []contextfabric.EntityProjection, relationships []contextfabric.RelationshipProjection) {
		t.Helper()
		step++
		batch := contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: fmt.Sprintf("batch_12226_%d", step), OrgID: orgID, Source: "live-test",
			SourceVersion: "v1", Cursor: fmt.Sprintf("cursor-%d", step-1), NextCursor: fmt.Sprintf("cursor-%d", step), GeneratedAt: observed.Add(time.Duration(step) * time.Minute),
			Entities: entities, Relationships: relationships, Contents: []contextfabric.ContentProjection{},
			Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
		}
		if entities == nil {
			batch.Entities = []contextfabric.EntityProjection{}
		}
		if relationships == nil {
			batch.Relationships = []contextfabric.RelationshipProjection{}
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("step %d: ApplyProjectionBatch() error = %v", step, err)
		}
	}
	entity := func(identities *[]string) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: target, Authorization: scope, ProjectIdentities: identities,
			EvidenceRefIDs: []string{"evidence_12226_entity"}, ObservedAt: observed.Add(time.Duration(step+1) * time.Minute), SourceVersion: "v1",
		}
	}
	read := func() (interface{}, bool) {
		t.Helper()
		n, err := adapter.nodeByKindID(ctx, key, orgID, string(target.Kind), target.CanonicalID, temporalFilter{})
		if err != nil {
			t.Fatalf("step %d: nodeByKindID() error = %v", step, err)
		}
		if n == nil {
			t.Fatalf("step %d: the work item node does not exist", step)
		}
		value, present := n.Properties[propAuthzProjectIdentities]
		return value, present
	}
	asStrings := func(value interface{}) []string {
		t.Helper()
		switch list := value.(type) {
		case []string:
			return list
		case []interface{}:
			out := make([]string, 0, len(list))
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					t.Fatalf("step %d: %s holds a non-string %T", step, propAuthzProjectIdentities, item)
				}
				out = append(out, s)
			}
			return out
		default:
			t.Fatalf("step %d: %s = %#v (%T), want a list", step, propAuthzProjectIdentities, value, value)
			return nil
		}
	}

	// 1. A stated set lands as a sorted list.
	apply([]contextfabric.EntityProjection{entity(&[]string{"project.v2:jira:ENG", "project.v2:github:P-1"})}, nil)
	value, present := read()
	if !present || !reflect.DeepEqual(asStrings(value), []string{"project.v2:github:P-1", "project.v2:jira:ENG"}) {
		t.Fatalf("stated set: %s = %#v (present=%v), want [project.v2:github:P-1 project.v2:jira:ENG]", propAuthzProjectIdentities, value, present)
	}

	// 2. A stated empty set is a present, empty list.
	apply([]contextfabric.EntityProjection{entity(&[]string{})}, nil)
	value, present = read()
	if !present {
		t.Fatalf("stated empty set: %s is absent, want a present empty list (in no project, not unstated)", propAuthzProjectIdentities)
	}
	if got := asStrings(value); len(got) != 0 {
		t.Fatalf("stated empty set: %s = %v, want []", propAuthzProjectIdentities, got)
	}

	// 3. A referenced stub write (the work item as a relationship endpoint)
	// leaves the value alone: first restate a set, then reference the node.
	apply([]contextfabric.EntityProjection{entity(&[]string{"project.v2:github:P-1"})}, nil)
	relationship := contextfabric.RelationshipProjection{
		RelationshipID: "relationship_12226_blocks", Type: contextfabric.RelationshipType("BLOCKS"), From: other, To: target,
		Derivation: contextfabric.DerivationMethod("canonical_structured"), EpistemicStatus: contextfabric.EpistemicStatus("source_asserted"), Authorization: scope,
		EvidenceRefIDs: []string{"evidence_12226_edge"}, ObservedAt: observed.Add(time.Hour), SourceVersion: "v1",
	}
	apply(nil, []contextfabric.RelationshipProjection{relationship})
	value, present = read()
	if !present || !reflect.DeepEqual(asStrings(value), []string{"project.v2:github:P-1"}) {
		t.Fatalf("after a stub write: %s = %#v (present=%v), want it untouched [project.v2:github:P-1]", propAuthzProjectIdentities, value, present)
	}

	// 4. An entity write that does not state it removes the key.
	apply([]contextfabric.EntityProjection{entity(nil)}, nil)
	if value, present = read(); present {
		t.Fatalf("not stated: %s = %#v, want the key removed", propAuthzProjectIdentities, value)
	}
}
