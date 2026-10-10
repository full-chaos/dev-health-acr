package v1

// TeamInactiveReason is the answer of a read for a team id the caller may read
// whose team row is inactive. The inactive team's rows are never served; the
// answer may name the active team that replaces it.
const TeamInactiveReason = "team_inactive"
