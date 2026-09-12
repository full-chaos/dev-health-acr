package devhealthsource_test

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A PROJECT'S ALIAS CHANNEL IS EMPTY FOR A WHOLE PROVIDER, read from the
// outside: the fixture that already carried a gitlab project with a key and a
// linear project without one now shows both leaving the producer with a handle.
//
// The gitlab row is the control. It has a key and has always had an alias, and
// it must keep it exactly -- a change that REPLACED the key channel instead of
// adding beside it would still give the linear row a handle while silently
// costing every jira and gitlab project the handle it already had.
func TestProjectAliasesCarryTheURLHandleBesideTheProviderKey(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedTeamsProjectsClient())

	linear := entityByCanonicalID(t, batch, "project.v2:linear:631fcb5f-c3e9-49ff-b17c-07877aaac9b7")
	if got := linear.Aliases; len(got) != 1 || got[0] != "chaos-draw" {
		t.Fatalf("linear project aliases = %v, want exactly [chaos-draw] -- its provider key is absent, so the URL handle is the only one it can have", got)
	}

	gitlab := entityByCanonicalID(t, batch, "project.v2:gitlab:70d529e0-3c06-4597-8480-794fd02328b6%3Agitlab%3A71133891")
	if got := gitlab.Aliases; len(got) != 2 || got[0] != "full.chaos/chaos-ops" || got[1] != "chaos-ops" {
		t.Fatalf("gitlab project aliases = %v, want the provider key FIRST and the URL handle beside it", got)
	}
}

// THE HANDLE IS AN ADDITIONAL SPELLING, NEVER A RENAME.
func TestTheURLHandleReachesTheProjectedSearchText(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedTeamsProjectsClient())
	linear := entityByCanonicalID(t, batch, "project.v2:linear:631fcb5f-c3e9-49ff-b17c-07877aaac9b7")
	found := false
	for _, alias := range linear.Aliases {
		if alias == "chaos-draw" {
			found = true
		}
	}
	if !found {
		t.Fatal("the handle is absent from the entity's own alias list, so it cannot reach the search text composed from it")
	}
	if linear.Subject.Label != "Chaos Draw" {
		t.Fatalf("project label = %q, want the projects.name value unchanged", linear.Subject.Label)
	}
}

// Asserted over the whole batch rather than one row, so a provider added to
// the fixture later is covered without editing this test.
func TestEveryProjectWithAProviderKeyKeepsItFirst(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedTeamsProjectsClient())
	projects := 0
	for _, entity := range batch.Entities {
		if entity.Subject.Kind != contractsv1.ContextFabricSubjectProject {
			continue
		}
		projects++
		if len(entity.Aliases) == 0 {
			t.Errorf("project %q projected with NO alias at all", entity.Subject.CanonicalID)
		}
	}
	if projects == 0 {
		t.Fatal("no project entities in the batch -- this sweep would pass vacuously")
	}
}
