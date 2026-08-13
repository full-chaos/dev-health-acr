package devhealthsource

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
	names := make([]string, len(teamsProjectsTables))
	for i, table := range teamsProjectsTables {
		names[i] = table.name
	}
	return names
}
