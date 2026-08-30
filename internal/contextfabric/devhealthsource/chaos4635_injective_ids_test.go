package devhealthsource

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// CHAOS-4635. An OWNED_BY_TEAM project<->team relationship id must be
// INJECTIVE over the facts it identifies. It was not: the id was a colon
// concatenation over id spaces that themselves contain colons, so the
// delimiter was not a delimiter.
//
// Why this is not a theoretical bound. `projects.id` is routinely
// `{org}:gitlab:71133891` and team ids are routinely `gl:full.chaos`. A
// collision needs no exotic input, only two ordinary values whose colons fall
// in different places.
//
// What a collision cost, in two different severities:
//
//   - two ASSERTIONS sharing an id are rejected by the batch validator as a
//     duplicate RelationshipID; a rejected batch never advances a checkpoint,
//     so the organization's projection wedges PERMANENTLY. Loud, and fatal.
//   - an assertion and a TOMBSTONE sharing an id used to pass validation
//     entirely (the two are checked in separate passes) and, because
//     falkorgraph applies tombstones after relationships, the tombstone
//     silently deleted a valid, still-asserted edge. CHAOS-4565 shipped a
//     contract guard that turns that into the first case -- loud instead of
//     silent -- and this change removes the cause underneath it.

// collidingProjectTeamInputs are two DIFFERENT ownership facts whose old
// encoding produced one id. The shapes are the real ones: a project id and a
// team id that both contain colons, with the colons falling differently.
var collidingProjectTeamInputs = []struct {
	name              string
	provider          string
	projectID, teamID string
	source            string
}{
	{"colon rides in the project id", "github", "project:team", "source", "native"},
	{"colon rides in the team id", "github", "project", "team:source", "native"},
}

// TestChaos4635_ProjectTeamRelationshipIDsAreInjective is the red-first case.
//
// It asserts through the PRODUCTION function, so it cannot pass against a
// second copy of the encoding that happens to be right. On the parent commit
// both inputs return `relationship:project_team:github:project:team:source:native`
// and this fails.
func TestChaos4635_ProjectTeamRelationshipIDsAreInjective(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, input := range collidingProjectTeamInputs {
		projectCanonicalID, omitted, err := identity.Derive(identity.KindProject, []string{input.provider, input.projectID}, nil)
		if err != nil || omitted {
			t.Fatalf("%s: could not derive a project canonical id (omitted=%v err=%v) -- the fixture must use representable ids or this proves nothing", input.name, omitted, err)
		}
		id := projectTeamRelationshipID(projectCanonicalID, input.teamID, input.source)
		if previous, clash := seen[id]; clash {
			t.Fatalf("two DIFFERENT ownership facts share one relationship id %q: %q and %q.\nOne group's tombstone then retracts the other group's live edge, and two assertions wedge the organization's projection permanently.", id, previous, input.name)
		}
		seen[id] = input.name
	}
}

// The id must be STABLE for identical inputs, or every rebuild mints new ids
// and nothing ever retracts the old ones.
//
// A digest-based id makes this worth asserting rather than assuming: a
// derivation that accidentally folded in a timestamp, a map iteration order,
// or anything else non-deterministic would still look injective above and
// would still be catastrophically wrong.
func TestChaos4635_TheIDIsStableForIdenticalInputs(t *testing.T) {
	t.Parallel()
	projectCanonicalID, _, err := identity.Derive(identity.KindProject, []string{"github", "PROJ-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := projectTeamRelationshipID(projectCanonicalID, "TEAM-1", "native")
	for i := 0; i < 32; i++ {
		if again := projectTeamRelationshipID(projectCanonicalID, "TEAM-1", "native"); again != first {
			t.Fatalf("id is not deterministic: %q then %q -- a rebuild would mint new ids and strand every edge it replaces", first, again)
		}
	}
}

// Every component the id claims to distinguish must actually change it.
//
// This is the guard against the opposite failure of a collision: a digest that
// silently drops an input is injective-looking and merges facts that must stay
// apart. Two ownership assertions for the same project and team from DIFFERENT
// sources are different assertions, exactly as a work_item_dependency's
// relationship_type distinguishes "blocks" from "relates_to" between one pair.
func TestChaos4635_EveryDistinguishingComponentChangesTheID(t *testing.T) {
	t.Parallel()
	base, _, err := identity.Derive(identity.KindProject, []string{"github", "PROJ-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := identity.Derive(identity.KindProject, []string{"github", "PROJ-2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	crossProvider, _, err := identity.Derive(identity.KindProject, []string{"gitlab", "PROJ-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reference := projectTeamRelationshipID(base, "TEAM-1", "native")
	for _, variant := range []struct {
		name string
		id   string
	}{
		{"a different project", projectTeamRelationshipID(other, "TEAM-1", "native")},
		{"a different team", projectTeamRelationshipID(base, "TEAM-2", "native")},
		{"a different attribution source", projectTeamRelationshipID(base, "TEAM-1", "manual")},
		{"a different provider (carried inside the project canonical id)", projectTeamRelationshipID(crossProvider, "TEAM-1", "native")},
	} {
		if variant.id == reference {
			t.Errorf("%s produced the SAME id -- that component is not in the derivation, so two distinct facts are being merged into one edge", variant.name)
		}
	}
}
