package contextfabric

// TeamNarrowingReadFloor is the closed log vocabulary of whether a requested team
// narrowing floored the general graph read (CHAOS-12327; the
// contextfabric.team_narrowing_general_read line, log-only, no wire value).
type TeamNarrowingReadFloor string

const (
	// TeamNarrowingReadFloorNone: every ownership the narrowing needed was
	// present; every exclusion was scope.
	TeamNarrowingReadFloorNone TeamNarrowingReadFloor = "none"
	// TeamNarrowingReadFloorUndecided: an ownership read the narrowing needed to
	// decide a subject the rest of the request admits was absent, not current,
	// or cut, so the cohort carries the population floor.
	TeamNarrowingReadFloorUndecided TeamNarrowingReadFloor = "team_narrowing_undecided"
)

// TeamNarrowingReadFloorVocabulary is the closed vocabulary of TeamNarrowingReadFloor,
// in declaration order.
func TeamNarrowingReadFloorVocabulary() []string {
	return []string{string(TeamNarrowingReadFloorNone), string(TeamNarrowingReadFloorUndecided)}
}
