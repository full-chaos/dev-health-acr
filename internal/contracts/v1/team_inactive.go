package v1

// TeamInactiveReason is the answer of a read for a team id the caller may read
// whose team row is inactive. The inactive team's rows are never served; the
// answer may name the active team that replaces it.
const TeamInactiveReason = "team_inactive"

// The reasons a team the repository's ownership records name is left out of a
// count of the repository's owners, carried as the reason of a cohort
// exclusion.
const (
	CohortExclusionReasonInactiveTeam   = "inactive_team"
	CohortExclusionReasonEndedAssertion = "ended_assertion"
)
