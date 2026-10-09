package devhealthschema

// ActiveTeamPredicate renders the active-team predicate for a read of the
// teams table (read with FINAL, so the newest row per team wins). An inactive
// team row is the superseded copy of a carried team and is never a subject.
// alias is the table alias ("" for an unaliased table). It is the one place the
// rule lives.
func ActiveTeamPredicate(alias string) string {
	if alias == "" {
		return "is_active = 1"
	}
	return alias + ".is_active = 1"
}
