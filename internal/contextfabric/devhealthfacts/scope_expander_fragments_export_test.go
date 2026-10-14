package devhealthfacts

import "github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"

// HistoricalFragmentsForTest renders the pieces of the scoped historical
// statements as statements of their own, each counting the rows its fragment
// returns, so a test can read how many rows each fragment reads.
func HistoricalFragmentsForTest() map[string]string {
	candidates := requestedProjectHistoryWorkItemIDsSQL(requestedProjectIDsPredicate)
	return map[string]string{
		"candidates": `SELECT count() AS probe_candidates FROM ` + candidates,
		"narrowing":  `SELECT count() AS probe_narrowing FROM project_membership_transitions WHERE org_id = {org_id:String} AND subject_kind = 'work_item'` + transitionSubjectsAmongSQL(candidates),
		"relation":   `SELECT count() AS probe_relation FROM ` + devhealthschema.DedupedMembershipTransitionsFiltered(false, transitionSubjectsAmongSQL(candidates)),
		"touches":    `SELECT count() AS probe_touches FROM ` + membershipTouchesAsOfAmongSQL(candidates),
	}
}
