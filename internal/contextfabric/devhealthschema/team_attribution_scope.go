package devhealthschema

// AttributionScope names who reads work_item_team_attributions. The column
// is_primary holds 1 for the single primary row of a work item, 2 for a
// co-owner row (another active team owning the item's project at the winner's
// rank) and 0 for provenance-only candidates.
type AttributionScope int

const (
	// AttributionScopeOrg is every organization-level reader and every
	// one-team-per-work-unit vote: the primary row only, so an organization
	// total counts an item once.
	AttributionScopeOrg AttributionScope = iota
	// AttributionScopeTeam is a reader that filters by team or gives each
	// team its own row or edge: primary and co-owner rows.
	AttributionScopeTeam
)

// TeamAttributionPredicate renders the is_primary predicate for the given
// table alias ("" for an unaliased table). It is the one place the rule lives.
func TeamAttributionPredicate(alias string, scope AttributionScope) string {
	column := "is_primary"
	if alias != "" {
		column = alias + ".is_primary"
	}
	if scope == AttributionScopeTeam {
		return column + " IN (1, 2)"
	}
	return column + " = 1"
}
