package falkorgraph

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// TeamAnchorWalkOutcome is the closed outcome of one read of a committed
// team's repositories or projects over its own ownership edges.
type TeamAnchorWalkOutcome string

const (
	// TeamAnchorWalkMembers: the read served at least one member.
	TeamAnchorWalkMembers TeamAnchorWalkOutcome = "members"
	// TeamAnchorWalkNoMembers: the read finished with no member and nothing
	// hidden from the caller.
	TeamAnchorWalkNoMembers TeamAnchorWalkOutcome = "no_members"
	// TeamAnchorWalkDenied: the read finished with no member served, and
	// members or the team itself were hidden by the caller's authorization.
	TeamAnchorWalkDenied TeamAnchorWalkOutcome = "denied"
	// TeamAnchorWalkReadFailed: the read failed and the call ends.
	TeamAnchorWalkReadFailed TeamAnchorWalkOutcome = "read_failed"
)

// TeamAnchorWalkOutcomeVocabulary returns every declared outcome, in
// declaration order.
func TeamAnchorWalkOutcomeVocabulary() []TeamAnchorWalkOutcome {
	return []TeamAnchorWalkOutcome{TeamAnchorWalkMembers, TeamAnchorWalkNoMembers, TeamAnchorWalkDenied, TeamAnchorWalkReadFailed}
}

// TeamAnchorWalkDecision is one decision line of the team member read: counts
// and closed values only, never a name or an id.
type TeamAnchorWalkDecision struct {
	Outcome    TeamAnchorWalkOutcome
	MemberKind contextfabric.SubjectKind
	// Committed is how many subjects the resolution committed.
	Committed int
	// Members, Denied and Truncated describe a read that finished, summed
	// over the committed teams the question anchors on.
	Members, Denied int
	Truncated       bool
	// Err is the failed read.
	Err error
}

// teamAnchorWalkTally folds the reads of one call into its single decision.
type teamAnchorWalkTally struct {
	ran             bool
	members, denied int
	truncated       bool
}

func (t *teamAnchorWalkTally) add(walk treeWalk) {
	t.ran = true
	t.members += len(walk.nodes)
	t.denied += walk.denied
	t.truncated = t.truncated || walk.truncated
}

func (t teamAnchorWalkTally) decision(memberKind contextfabric.SubjectKind, committed int, err error) TeamAnchorWalkDecision {
	d := TeamAnchorWalkDecision{MemberKind: memberKind, Committed: committed, Members: t.members, Denied: t.denied, Truncated: t.truncated, Err: err}
	switch {
	case err != nil:
		d.Outcome = TeamAnchorWalkReadFailed
	case t.members > 0:
		d.Outcome = TeamAnchorWalkMembers
	case t.denied > 0:
		d.Outcome = TeamAnchorWalkDenied
	default:
		d.Outcome = TeamAnchorWalkNoMembers
	}
	return d
}
