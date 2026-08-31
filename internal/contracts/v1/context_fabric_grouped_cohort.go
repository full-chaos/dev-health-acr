package v1

import "fmt"

// The grouped cohort (CHAOS-4636 / intent-engine design §6.2).
//
// A grouped question -- "what are the project statuses for EACH team" -- asks
// for one answer per group, not one flat list. The design's first draft added
// `Groups` as a plain additive field and claimed every existing consumer kept
// reading what it reads today. That was wrong, and it is the most consequential
// correction in the slice: consumers keep RUNNING, but three of them go
// group-blind, which is worse than failing.
//
//   - answerprojection.projectCohort retains the LEADING MaxCohortMembers of
//     the flat list, so it can return every project of team A and none of
//     team B, silently, with the caller unable to tell.
//   - RankCohort scores across the flat list, producing one cross-group
//     ranking for a question that asked for per-group results.
//   - render selection plots by AttentionRank across the flat list, drawing
//     one cross-group bar chart.
//
// And ContextFabricCohort.Complete/Truncated are single booleans over the
// union, so they CANNOT express "team A complete, team B truncated" -- mixed
// group completeness had no representation at all.
//
// So the group carries its OWN completeness, and the cohort-level booleans are
// defined as the conjunction (Complete = every group complete). An old reader
// that ignores Groups then gets a CONSERVATIVE answer rather than a wrong one:
// never Complete: true over a partially-truncated union.

// ContextFabricCohortGroup is one group of a grouped cohort answer -- for a
// "per team" question, one team and the members that belong to it.
//
// Members are named by CANONICAL ID into ContextFabricCohort.Members rather
// than nested inside the group. That is deliberate on two grounds. It keeps
// the flattened Members list authoritative, so a consumer that never learned
// about groups reads exactly one member list rather than two that could
// disagree; and it does not double the serialized size of the very answers
// that are already the ones straining the byte budget -- a grouped cohort is
// the shape that 413s, so paying for each member twice would make the
// grouping the cause of the refusal it exists to avoid.
type ContextFabricCohortGroup struct {
	// Subject is the group entity itself (the team), not one of its
	// members. Its Kind is the plan's declared group kind.
	Subject ContextFabricSubjectRef `json:"subject"`
	// MemberCanonicalIDs names this group's members by
	// ContextFabricCohortMember.Subject.CanonicalID, in the cohort's own
	// member order. Validate proves every id resolves.
	MemberCanonicalIDs []string `json:"member_canonical_ids"`
	// Complete and Truncated are this group's OWN completeness, which is
	// the representation the flat booleans could not express. They are
	// mutually exclusive, exactly as they are on the cohort.
	Complete  bool `json:"complete"`
	Truncated bool `json:"truncated"`
	// Total is this group's member count BEFORE any narrowing, so a caller
	// reading a truncated group still sees the true size. It is never less
	// than len(MemberCanonicalIDs).
	Total int `json:"total"`
}

// Validate enforces the group's own invariants. It is called from
// ContextFabricCohort's validation, so a grouped cohort cannot be persisted or
// served in a state a reader would misread.
func (g ContextFabricCohortGroup) Validate() error {
	if err := g.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if g.Complete && g.Truncated {
		// The published JSON Schema states the same exclusion on the
		// cohort itself; a group that claimed both would be a contract
		// violation a consumer could read either way.
		return fmt.Errorf("cohort group %q is both complete and truncated", g.Subject.CanonicalID)
	}
	if g.Total < len(g.MemberCanonicalIDs) {
		return fmt.Errorf("cohort group %q reports total %d below its %d listed members", g.Subject.CanonicalID, g.Total, len(g.MemberCanonicalIDs))
	}
	if g.Truncated && g.Total <= len(g.MemberCanonicalIDs) {
		return fmt.Errorf("cohort group %q is marked truncated but lists all %d of its members", g.Subject.CanonicalID, g.Total)
	}
	seen := make(map[string]struct{}, len(g.MemberCanonicalIDs))
	for _, id := range g.MemberCanonicalIDs {
		if id == "" {
			return fmt.Errorf("cohort group %q names an empty member canonical id", g.Subject.CanonicalID)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("cohort group %q names member %q twice", g.Subject.CanonicalID, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// ValidateCohortGroups proves the group axis closes over the cohort's own
// member list: every named member exists, and no group is declared twice.
//
// Two things it deliberately does NOT require, each for a reason found in this
// repository's own data rather than argued from tidiness:
//
//   - A member may belong to MORE THAN ONE group. Ownership is a relation,
//     not a function: devhealthfacts/shared.go:337-344 records that
//     team_project_ownership orders by `source`, so a project's native and
//     manual ownership rows can both be current, and every project rollup
//     "must dedupe by team_id". A project genuinely owned by two teams
//     belongs under both, and a validator that forbade it would force the
//     engine either to drop a true ownership or to pick one silently.
//     Member IDENTITY stays unique -- the flattened Members list is still one
//     entry per member, so the item budget charges each member once.
//   - A member may belong to NO group. A grouped answer whose group axis
//     could not place one member must be able to say so by leaving it
//     ungrouped, rather than inventing a group or dropping a member the flat
//     list already committed to. On this data that is a real case: the team
//     association is read off a project fact's own declared rows, and the
//     providers that carry it join on compounding risk, so a member whose
//     facts came back empty has no derivable group.
func ValidateCohortGroups(groups []ContextFabricCohortGroup, members []ContextFabricCohortMember) error {
	if len(groups) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(members))
	for _, member := range members {
		known[member.Subject.CanonicalID] = struct{}{}
	}
	groupKeys := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if err := group.Validate(); err != nil {
			return err
		}
		if _, duplicate := groupKeys[group.Subject.CanonicalID]; duplicate {
			return fmt.Errorf("cohort names group %q twice", group.Subject.CanonicalID)
		}
		groupKeys[group.Subject.CanonicalID] = struct{}{}
		for _, id := range group.MemberCanonicalIDs {
			if _, ok := known[id]; !ok {
				return fmt.Errorf("cohort group %q names member %q, which is not in the cohort's member list", group.Subject.CanonicalID, id)
			}
		}
	}
	return nil
}

// CohortCompletenessFromGroups derives the cohort-level booleans from the
// groups, as the conjunction/disjunction the design specifies: Complete only
// if EVERY group is complete, Truncated if ANY group is.
//
// The two cannot both come out true, because a group may not be both -- so
// the derived pair always satisfies the schema's own mutual exclusion. An old
// reader that ignores Groups reads a conservative summary of the whole union
// rather than a boolean that happened to describe only the first group.
func CohortCompletenessFromGroups(groups []ContextFabricCohortGroup) (complete bool, truncated bool) {
	if len(groups) == 0 {
		return false, false
	}
	complete = true
	for _, group := range groups {
		if !group.Complete {
			complete = false
		}
		if group.Truncated {
			truncated = true
		}
	}
	return complete, truncated
}
