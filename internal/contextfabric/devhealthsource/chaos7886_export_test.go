package devhealthsource

// NonMembershipTableNamesForTest is every table on the teams and projects
// cursor except the membership producer, read from the registry.
func NonMembershipTableNamesForTest() []string {
	var names []string
	for _, table := range teamsProjectsTables(nil, nil, nil, nil) {
		if table.name != membershipTable {
			names = append(names, table.name)
		}
	}
	return names
}
