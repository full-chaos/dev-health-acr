package v1

import (
	"strings"
	"testing"
	"time"
)

// CHAOS-4565. A batch must not assert a relationship and tombstone that SAME
// relationship id, and this is the guard that makes the pair unrepresentable
// rather than merely unlikely.
//
// How the pair becomes reachable. devhealthsource's OWNED_BY_TEAM
// relationship id is a colon concatenation of (provider, project id, team id,
// source), and it is NOT injective -- `projects.id` is routinely
// `{org}:gitlab:71133891` and team ids are routinely `gl:full.chaos`, so the
// delimiter is not a delimiter. Two DIFFERENT (project, team) pairs can
// therefore land on one id (CHAOS-4635 carries the root fix).
//
// Why that used to be silent, which is the part worth understanding.
// validateProjectionRelationships and validateProjectionTombstones each
// enforce uniqueness WITHIN their own slice, and neither looks at the other.
// So a cross-kind collision passed Validate() cleanly. falkorgraph then
// applies relationships BEFORE tombstones, so the batch wrote the edge and
// immediately deleted it -- a valid, still-asserted ownership silently
// removed from the graph, with every count, log and receipt in the pipeline
// reporting success.
//
// This guard converts that into a loud, checkpoint-holding rejection. That is
// deliberately NOT a fix for the collision itself: a wedge is a bad outcome
// too, and CHAOS-4635 is what makes the ids injective. It is the difference
// between a failure you can see and one you cannot.
func TestChaos4565_ABatchMayNotAssertAndTombstoneOneRelationship(t *testing.T) {
	t.Parallel()
	// The exact id two different (project, team) pairs collide on:
	//   project "project:team" + team "source", and
	//   project "project"      + team "team:source".
	const collided = "relationship:project_team:github:project:team:source:native"

	batch := crossKindProbeBatch(collided, collided)
	err := batch.Validate()
	if err == nil {
		t.Fatal("a batch that asserts AND tombstones one relationship id was accepted; falkorgraph applies tombstones after relationships, so it writes the edge and immediately deletes it -- a valid ownership silently removed, reported as success")
	}
	if !strings.Contains(err.Error(), "tombstone") || !strings.Contains(err.Error(), "relationship") {
		t.Errorf("rejection message does not name the collision it caught (%q); an operator holding a wedged checkpoint has to be able to tell this apart from an ordinary duplicate", err)
	}

	// NON-VACUITY. The assertion above passes just as well if Validate()
	// rejects this batch for some unrelated reason -- a malformed field, a
	// bound, anything. The SAME batch shape with a non-colliding tombstone id
	// must be ACCEPTED, or this test proves nothing about collisions.
	if err := crossKindProbeBatch(collided, collided+":other").Validate(); err != nil {
		t.Fatalf("the control batch was rejected too (%v) -- then the assertion above is not evidence about collisions, only that this batch shape is invalid", err)
	}
}

// crossKindProbeBatch is one minimal valid batch asserting relationshipID and
// tombstoning tombstoneID. Both ids are parameters so the guard and its
// control differ in exactly one thing.
func crossKindProbeBatch(relationshipID, tombstoneID string) ContextFabricProjectionBatch {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	const version = "devhealthsource.teams_projects.v9"
	return ContextFabricProjectionBatch{
		SchemaVersion: ContextFabricProjectionBatchSchema, BatchID: "batch_crosskind01", OrgID: "org-1",
		Source: "dev_health_teams_projects", SourceVersion: version, GeneratedAt: now,
		Entities: []ContextFabricEntityProjection{},
		Relationships: []ContextFabricRelationshipProjection{{
			RelationshipID: relationshipID, Type: ContextFabricRelationshipOwnedByTeam,
			From:       ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project:p1", Label: "p1"},
			To:         ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:t1", Label: "t1"},
			Derivation: ContextFabricDerivationRuleInferred, EpistemicStatus: ContextFabricEpistemicSourceAsserted,
			Authorization:  ContextFabricAuthorizationScope{ProjectIDs: []string{"p1"}, TeamIDs: []string{"t1"}},
			EvidenceRefIDs: []string{"acr:v1:project-team:github:p1:t1"},
			ObservedAt:     now, SourceVersion: version,
		}},
		Contents: []ContextFabricContentProjection{}, Episodes: []ContextFabricEpisodeProjection{},
		Tombstones: []ContextFabricProjectionTombstone{{
			Kind: "relationship", CanonicalID: tombstoneID,
			Reason: "ownership suppressed: probe", EffectiveAt: now, SourceVersion: version,
		}},
	}
}

// Only tombstones that actually delete a RELATIONSHIP may trip the guard.
//
// applyTombstone routes on Kind: "relationship" and "edge" delete an edge by
// relationship_id; every other kind deletes a NODE, in a different key space
// where an equal string means nothing. A guard that fired on those would
// reject legitimate batches -- and a guard that rejects correct input is worse
// than the hole it closes, because the projection it wedges was healthy.
func TestChaos4565_TheCrossKindGuardOnlyCoversRelationshipTombstones(t *testing.T) {
	t.Parallel()
	const id = "relationship:project_team:github:p1:t1:native"
	for _, kind := range []string{"relationship", "edge", "RELATIONSHIP", "Edge"} {
		batch := crossKindProbeBatch(id, id)
		batch.Tombstones[0].Kind = kind
		if err := batch.Validate(); err == nil {
			t.Errorf("kind %q deletes a relationship by relationship_id but was not covered by the guard", kind)
		}
	}
	// A node tombstone whose canonical id happens to equal a relationship id
	// is not a collision: applyTombstone matches it against subject nodes.
	for _, kind := range []string{"incident", "work_item_ref", "document"} {
		batch := crossKindProbeBatch(id, id)
		batch.Tombstones[0].Kind = kind
		if err := batch.Validate(); err != nil {
			t.Errorf("kind %q deletes a NODE, in a different key space, so an equal string is not a collision -- rejecting it wedges a healthy projection: %v", kind, err)
		}
	}
}
