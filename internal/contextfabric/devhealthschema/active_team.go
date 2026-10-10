package devhealthschema

import (
	"context"
	"log/slog"
)

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

// OmissionSite names a reader that left inactive teams out. It is a closed
// enumeration, never request data, and logs as its name.
type OmissionSite int

const (
	OmittedListSubjectsByKind OmissionSite = iota + 1
	OmittedFindSubjectsByExactName
	OmittedExactNameCensus
	OmittedCohortKindCensus
	OmittedFulltextSearch
	OmittedVectorSearch
	OmittedStoredSubjects
	OmittedIdentityUniverse
	OmittedProjectReadiness
	OmittedExactHint
)

var omissionSiteNames = map[OmissionSite]string{
	OmittedListSubjectsByKind:      "list_subjects_by_kind",
	OmittedFindSubjectsByExactName: "find_subjects_by_exact_name",
	OmittedExactNameCensus:         "exact_name_census",
	OmittedCohortKindCensus:        "cohort_kind_census",
	OmittedFulltextSearch:          "fulltext_search",
	OmittedVectorSearch:            "vector_search",
	OmittedStoredSubjects:          "stored_subjects",
	OmittedIdentityUniverse:        "identity_universe",
	OmittedProjectReadiness:        "project_readiness",
	OmittedExactHint:               "exact_hint",
}

// MarshalText makes a handler print the site by name.
func (s OmissionSite) MarshalText() ([]byte, error) {
	if name, ok := omissionSiteNames[s]; ok {
		return []byte(name), nil
	}
	return []byte("unknown"), nil
}

// NoteInactiveTeamsOmitted records, at debug level, that rows of inactive teams
// were left out of a read, so a changed candidate list or aggregate can be
// traced to the active-team rule. site names the reader; a zero count logs
// nothing.
func NoteInactiveTeamsOmitted(ctx context.Context, site OmissionSite, count int) {
	if count <= 0 {
		return
	}
	slog.Default().DebugContext(ctx, "inactive team rows omitted from a read", slog.Any("site", site), slog.Int("omitted", count))
}
