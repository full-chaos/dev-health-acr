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

// The closed notes a direct read serves about a team the caller may read and
// the read admitted. RequestNoteKindTeam is the request-note kind that carries
// them on read_facts; read_relationships serves the same values in its notes.
const (
	RequestNoteKindTeam = "team"
	// TeamRemovedNote: the provider proved the team gone at its source; its
	// edges and facts are kept and may be out of date.
	TeamRemovedNote = "team_removed"
	// TeamStatusUnavailableNote: the removed-team lookup failed, so an
	// admitted team was not checked. It says nothing about the team.
	TeamStatusUnavailableNote = "team_status_unavailable"
)
