package devhealthsource

import "time"

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
	tables := teamsProjectsTables(nil)
	names := make([]string, len(tables))
	for i, table := range tables {
		names[i] = table.name
	}
	return names
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
