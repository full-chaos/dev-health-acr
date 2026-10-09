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

// InactiveTeamPredicate renders the predicate for an inactive row of the teams
// table (read with FINAL).
func InactiveTeamPredicate(alias string) string {
	if alias == "" {
		return "is_active = 0"
	}
	return alias + ".is_active = 0"
}

// ActiveTeamScopePredicate renders the predicate for a read of a daily table
// keyed by team_id: the row's team is not an inactive team of the organization.
// A carried team id leaves recomputed days under both the superseded and the
// current id; this drops the superseded copy. A row whose team has no catalog
// row, and an unattributed row (team_id empty or NULL), stay.
func ActiveTeamScopePredicate(column string) string {
	return "ifNull(" + column + ", '') NOT IN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String} AND " + InactiveTeamPredicate("") + ")"
}
