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

// The two remaining relationship ids, same defect, same red-first shape.
//
// Their old encodings collided on values this schema actually holds:
// workItemID is routinely `linear:CHAOS-3802` and teamID `gl:full.chaos`, so
// `repoID:workItemID:teamID` could be re-split at a different boundary.
func TestChaos4635_WorkItemTeamRelationshipIDsAreInjective(t *testing.T) {
	t.Parallel()
	derive := func(repoID, workItemID string) string {
		canonical, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
		if err != nil || omitted {
			t.Fatalf("derive work item (%q, %q): omitted=%v err=%v", repoID, workItemID, omitted, err)
		}
		return canonical
	}
	// The historically-colliding split: "repo" + "linear:CHAOS" + "gl:x"
	// against "repo" + "linear" + "CHAOS:gl:x".
	a := workItemTeamRelationshipID(derive("repo", "linear:CHAOS"), "gl:x")
	b := workItemTeamRelationshipID(derive("repo", "linear"), "CHAOS:gl:x")
	if a == b {
		t.Fatalf("two different work_item<->team facts share one id %q", a)
	}
	// Every component still distinguishes.
	base := workItemTeamRelationshipID(derive("repo", "WI-1"), "TEAM-1")
	for _, variant := range []struct{ name, id string }{
		{"a different repo", workItemTeamRelationshipID(derive("repo-2", "WI-1"), "TEAM-1")},
		{"a different work item", workItemTeamRelationshipID(derive("repo", "WI-2"), "TEAM-1")},
		{"a different team", workItemTeamRelationshipID(derive("repo", "WI-1"), "TEAM-2")},
	} {
		if variant.id == base {
			t.Errorf("%s produced the SAME id -- that component is not in the derivation", variant.name)
		}
	}
}

// projectMembership carries CHAOS-4109's per-interval discriminator, and the
// interval is the reason this one needs its own case: it used to be APPENDED
// to the id as one more unescaped colon-joined component, so an interval
// boundary could be re-read as part of the project id.
func TestChaos4635_ProjectMembershipRelationshipIDsAreInjective(t *testing.T) {
	t.Parallel()
	const subject = "work_item.v2:abc123"
	const project = "project.v2:def456"

	// Two DIFFERENT intervals for one (subject, project) pair must stay
	// distinct -- that is the whole point of CHAOS-4109's suffix.
	first := projectMembershipRelationshipID(subject, project, ":2026-08-13T19:00:00Z:evt-1")
	second := projectMembershipRelationshipID(subject, project, ":2026-08-13T19:00:00Z:evt-2")
	if first == second {
		t.Fatal("two transition intervals for one (subject, project) pair share an id -- Validate rejects the duplicate and wedges the organization")
	}
	// A work_item_column row carries NO interval and must keep exactly one id
	// per pair, unchanged in grain by this refactor.
	bare := projectMembershipRelationshipID(subject, project, "")
	if bare == first {
		t.Fatal("an interval-less row collided with an interval-bearing one")
	}
	if again := projectMembershipRelationshipID(subject, project, ""); again != bare {
		t.Fatal("interval-less ids are not stable")
	}
	// The arms are separated by the SUBJECT canonical id, which carries its
	// kind -- that is why they share one family.
	if projectMembershipRelationshipID("pull_request:repo:7", project, "") == projectMembershipRelationshipID("work_item.v2:repo:7", project, "") {
		t.Fatal("a pull request and a work item collided -- the merged family relies on the subject canonical id carrying its kind")
	}
}

// THE CURSOR KEY, which is a different failure from a colliding id and a worse
// one: a colliding id is REJECTED (loudly, as a duplicate), while a colliding
// cursor key silently DROPS a row at a page boundary and never revisits it.
//
// The keyset predicate is a strict `>`. Two rows sharing a watermark and a key
// are ordered arbitrarily; the page cuts between them; the next page's
// predicate excludes the survivor because its key is not greater. The edge is
// simply gone until some later watermark change or a rebuild.
func TestChaos4635_CursorKeysAreInjective(t *testing.T) {
	t.Parallel()
	// The provider case, which the old key missed entirely by omitting a
	// component its own GROUP BY includes. Cross-provider equal project ids
	// are a documented property of this data model, so this needs no colons
	// at all -- it is the more reachable of the two.
	if identity.JoinSegments("github", "P", "T", "native") == identity.JoinSegments("gitlab", "P", "T", "native") {
		t.Error("two groups differing only by provider share a cursor key; the GROUP BY separates them, so the tiebreaker must")
	}
	// The colon case.
	if identity.JoinSegments("github", "project:team", "source", "native") == identity.JoinSegments("github", "project", "team:source", "native") {
		t.Error("two ownership rows share a cursor key through a re-split colon")
	}
	// The escape must not be defeatable by a value that already looks escaped.
	if identity.JoinSegments("a%3Ab", "x") == identity.JoinSegments("a", "b", "x") {
		t.Error("a %3A preimage decoded back into a separator -- the escape is not injective")
	}
	// The other two producers' keys, same shape.
	if identity.JoinSegments("repo", "linear:CHAOS") == identity.JoinSegments("repo:linear", "CHAOS") {
		t.Error("work_item_team cursor keys collide")
	}
	if identity.JoinSegments("work_item", "repo", "linear:CHAOS", "github", "p1", "e1") ==
		identity.JoinSegments("work_item", "repo", "linear", "CHAOS:github", "p1", "e1") {
		t.Error("project membership cursor keys collide")
	}
}
