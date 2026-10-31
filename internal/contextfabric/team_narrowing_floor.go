package contextfabric

// TeamNarrowingFloor is the closed log vocabulary of whether a requested team
// narrowing floored the general graph read (CHAOS-12327; the
// contextfabric.team_narrowing_general_read line, log-only, no wire value).
type TeamNarrowingFloor string

const (
	// TeamNarrowingFloorNone: every ownership the narrowing needed was
	// present; every exclusion was scope.
	TeamNarrowingFloorNone TeamNarrowingFloor = "none"
	// TeamNarrowingFloorUndecided: an ownership read the narrowing needed to
	// decide a subject the rest of the request admits was absent, not current,
	// or cut, so the cohort carries the population floor.
	TeamNarrowingFloorUndecided TeamNarrowingFloor = "team_narrowing_undecided"
)

// TeamNarrowingFloorVocabulary is the closed vocabulary of TeamNarrowingFloor,
// in declaration order.
func TeamNarrowingFloorVocabulary() []string {
	return []string{string(TeamNarrowingFloorNone), string(TeamNarrowingFloorUndecided)}
}
