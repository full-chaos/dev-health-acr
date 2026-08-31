package devhealthsource

import (
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// EntityTableNamesForTest exposes entityTables' table names (tables.go) to
// devhealthsource_test -- CHAOS-3789 codex round-1 F2: the schema-parity
// test derives its table inventory from this instead of a hand-duplicated
// list, so a producer added to entityTables without a matching parity-test
// seed row and expectation fails loudly instead of going silently
// unasserted.
func EntityTableNamesForTest() []string {
	names := make([]string, len(entityTables))
	for i, table := range entityTables {
		names[i] = table.name
	}
	return names
}

// TeamsProjectsTableNamesForTest exposes teamsProjectsTables' table names
// (teams_projects.go) for the same CHAOS-3789 F2 reason
// EntityTableNamesForTest exists: the schema-parity sweep derives its table
// inventory from the producer list itself, so a producer added without a
// matching seed row and expectation fails loudly instead of going silently
// unasserted.
func TeamsProjectsTableNamesForTest() []string {
	tables := teamsProjectsTables(nil, nil, nil)
	names := make([]string, len(tables))
	for i, table := range tables {
		names[i] = table.name
	}
	return names
}

// NoTeamOwnershipSentinelForTest exposes noTeamOwnershipSentinel
// (teams_projects.go, CHAOS-4390) to devhealthsource_test, so a test can
// assert against the SAME literal production actually emits rather than a
// second hand-copied string that could silently drift from it.
func NoTeamOwnershipSentinelForTest() string {
	return noTeamOwnershipSentinel
}

// ProjectAuthorizationScopeForTest exposes queryProjects' reserved-namespace
// decision directly (CHAOS-3802 codex round-1 F4), so a test can prove the
// PRODUCER refuses a colliding project id without routing through
// ContextFabricEntityProjection.Validate(). Testing it end-to-end cannot
// distinguish the two: the contract rejects the same row either way, which is
// exactly how an earlier producer-side guard here went unverifiable and was
// removed. Both layers are wanted -- the producer fails fast and
// attributably, the contract is the unforgettable backstop -- so the producer
// half needs its own reachable seam.
func ProjectAuthorizationScopeForTest(projectID string) error {
	_, err := projectAuthorizationScope(projectID)
	return err
}

// EdgeValidityForTest exposes edgeValidity (validity.go) directly to
// devhealthsource_test -- CHAOS-3825. The end-to-end tests prove the
// degenerate-window collapse through two of the four call sites, but the
// invariant edgeValidity actually owns ("never return a valid_to before
// the valid_from") is a property of the FUNCTION, not of any one caller,
// and the remaining callers reach it with combinations no fixture
// exercises (nil starts, nil ends, touching bounds). Asserting it through
// a seam keeps the guard covered when a call site is added or a query is
// rewritten.
func EdgeValidityForTest(fromValidFrom, fromValidTo, toValidFrom, toValidTo *time.Time) (*time.Time, *time.Time) {
	return edgeValidity(fromValidFrom, fromValidTo, toValidFrom, toValidTo)
}

// ProjectTeamRelationshipIDForTest exposes the OWNED_BY_TEAM project<->team
// edge id to devhealthsource_test, derived exactly the way the producer
// derives it (CHAOS-4635).
//
// It takes the RAW source values a fixture actually seeds -- provider,
// projects.id, teams.id, the attribution source -- and runs them through the
// same identity.Derive + projectTeamRelationshipID pair queryProjectTeams
// uses, so a test expectation cannot become a second spelling of the
// encoding. That mattered immediately: the ids these tests used to hard-code
// were a raw colon join, and after the digest change every literal would have
// had to be re-copied by hand from a failing test's output -- which is how a
// fixture stops describing the producer and starts describing whatever it
// last printed.
//
// It is also why the conversion could not be mechanical. A literal like
// `relationship:project_team:github:70d529e0-...:gitlab:71133891:gl:full.chaos:native`
// cannot be split back into its components without already knowing where the
// boundaries are -- the exact ambiguity CHAOS-4635 exists to remove. Every
// call site was therefore rewritten from the fixture's OWN seeded values.
//
// Fails loudly rather than returning a wrong expectation: a project id this
// producer cannot represent yields no edge at all, so a test asserting on one
// is asserting about something that never existed.
func ProjectTeamRelationshipIDForTest(t interface{ Fatalf(string, ...any) }, provider, projectID, teamID, source string) string {
	projectCanonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
	if err != nil {
		t.Fatalf("derive project canonical id for (%q, %q): %v", provider, projectID, err)
		return ""
	}
	if omitted {
		t.Fatalf("project (%q, %q) is not representable, so the producer emits no edge for it -- this expectation is about an edge that cannot exist", provider, projectID)
		return ""
	}
	return projectTeamRelationshipID(projectCanonicalID, teamID, source)
}

// RowKeySQLForTest exposes rowKeySQL so the SQL/Go byte-agreement test builds
// the SAME expression the producers page on, rather than a second copy of it
// that could agree with Go while production disagrees.
func RowKeySQLForTest(columns ...string) string { return rowKeySQL(columns...) }

// WorkItemTeamRelationshipIDForTest and ProjectMembershipRelationshipIDForTest
// are the seams for the other two edge families (CHAOS-4635), for the same
// reason ProjectTeamRelationshipIDForTest exists: an expectation must run the
// producer's own derivation, never a second copy of it.
//
// Both take the RAW values a fixture seeds and derive the endpoint canonical
// ids exactly as the producers do, so a fixture change and an expectation
// cannot drift apart.
func WorkItemTeamRelationshipIDForTest(t interface{ Fatalf(string, ...any) }, repoID, workItemID, teamID string) string {
	workItemCanonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil {
		t.Fatalf("derive work item (%q, %q): %v", repoID, workItemID, err)
		return ""
	}
	if omitted {
		t.Fatalf("work item (%q, %q) is not representable, so no edge exists to assert on", repoID, workItemID)
		return ""
	}
	return workItemTeamRelationshipID(workItemCanonicalID, teamID)
}

// ProjectMembershipRelationshipIDForTest takes the SUBJECT canonical id
// directly rather than re-deriving it: the pull-request arm mints its own
// legacy `pull_request:<repo>:<number>` id (see querySubjectProjectMemberships'
// doc comment on why identity.Derive is deliberately not used there), so a
// single derive-by-kind helper here would quietly disagree with the producer
// for half its call sites.
func ProjectMembershipRelationshipIDForTest(t interface{ Fatalf(string, ...any) }, subjectCanonicalID, provider, projectID, intervalSuffix string) string {
	projectCanonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
	if err != nil {
		t.Fatalf("derive project (%q, %q): %v", provider, projectID, err)
		return ""
	}
	if omitted {
		t.Fatalf("project (%q, %q) is not representable, so no edge exists to assert on", provider, projectID)
		return ""
	}
	return projectMembershipRelationshipID(subjectCanonicalID, projectCanonicalID, intervalSuffix)
}

// WorkItemSubjectCanonicalIDForTest and PullRequestSubjectCanonicalIDForTest
// mirror the two subject-id shapes querySubjectProjectMemberships mints, so a
// test naming a membership edge builds its FROM endpoint the same way.
func WorkItemSubjectCanonicalIDForTest(t interface{ Fatalf(string, ...any) }, repoID, workItemID string) string {
	id, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive work item (%q, %q): omitted=%v err=%v", repoID, workItemID, omitted, err)
		return ""
	}
	return id
}

func PullRequestSubjectCanonicalIDForTest(repoID string, number int) string {
	return fmt.Sprintf("pull_request:%s:%d", repoID, number)
}
