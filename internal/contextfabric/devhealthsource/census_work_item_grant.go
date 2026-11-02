package devhealthsource

import (
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// censusWorkItemMaxRowsToRead bounds a work-item census under a grant. The
// library relation joins the link and ownership relations and ClickHouse
// counts every one of them. Same figure as workItemReaderMaxRowsToRead in
// devhealthfacts, whose derivation is there; this package cannot import it.
const censusWorkItemMaxRowsToRead = uint64(3_000_000)

// withWorkItemGrantFilter ANDs a restricted caller's grant onto a work-item
// census. An id is in when every row carrying it passes one of two library arms,
// narrowed to the handle's candidates:
//
//   - direct_repo: a row with a real repository, by that repository (CHAOS-12561).
//     GitHub and GitLab issues sit on their own repository's row, and the node gate
//     admits them by it, so a census without this arm said a readable issue was
//     absent.
//   - pr_link: a repository-less row, through a native link to a pull request of a
//     granted repository (CHAOS-12456 ruling (a)).
//
// Never the project path: a project a granted repository's team owns is not the
// relation. The pair is the one graphrank.AuthorizedThroughLink and the node gate
// read.
//
// False ends the read with no statement, as for any grant that cannot apply.
func withWorkItemGrantFilter(orgID string, predicate CensusPredicate, grant []string, handleValue string, handleBound bool) (CensusPredicate, bool) {
	// A census with no handle has no candidates to narrow to, and its count
	// inside the link arm would miss items the node gate admits.
	if !handleBound {
		return predicate, false
	}
	handle, err := workItemTicketKeyPredicate(handleValue)
	if err != nil {
		return predicate, false
	}
	return withWorkItemGrantAmong(orgID, predicate, grant, handle)
}

// withWorkItemGrantAmong ANDs the grant onto predicate over the candidates handle
// names: the same two arms for a key's census and for a key prefix's read.
func withWorkItemGrantAmong(orgID string, predicate CensusPredicate, grant []string, handle CensusPredicate) (CensusPredicate, bool) {
	// First, always: past this line the library would read an empty list or a
	// "*" as the organization-wide grant.
	if !grantApplies(grant) {
		return predicate, false
	}
	selectors := auth.WorkItemRepositorySelectorSet(grant, true)
	candidates := make([]readers.Binding, 0, len(handle.Bindings))
	for _, binding := range handle.Bindings {
		candidates = append(candidates, readers.Binding{Name: binding.Name, Value: binding.Value})
	}
	candidateSQL := "SELECT w.work_item_id FROM work_items AS w WHERE w.org_id = {org_id:String} AND " + handle.SQL
	rendered := readers.WorkItemScopeSQLAmong(
		readers.AuthorizationScope{RepositorySelectors: &readers.RepositorySelectorScope{Granted: selectors}},
		readers.WorkItemCandidateSubquery(candidateSQL, candidates...),
	)
	direct, link := "", ""
	for _, path := range rendered.Provenance {
		switch path.Path {
		case readers.WorkItemAuthorizationDirectRepository:
			direct = path.Expr
		case readers.WorkItemAuthorizationPullRequestLink:
			link = path.Expr
		}
	}
	if direct == "" || link == "" {
		return predicate, false
	}
	arm := "(" + direct + ") OR (" + link + ")"
	predicate.SQL = "(" + predicate.SQL + ") AND w.work_item_id IN (SELECT w.work_item_id FROM work_items AS w FINAL " + rendered.JoinSQL +
		" WHERE w.org_id = {org_id:String} AND w.work_item_id IN (" + candidateSQL + ") GROUP BY w.work_item_id HAVING min(toUInt8(" + arm + ")) = 1)"
	bindings := append([]contextpacket.ClickHouseBinding(nil), predicate.Bindings...)
	bindings = append(bindings, contextpacket.ClickHouseBinding{Name: "org_id", Value: orgID})
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		seen[binding.Name] = struct{}{}
	}
	for _, binding := range rendered.Bindings {
		if _, duplicate := seen[binding.Name]; duplicate {
			continue
		}
		seen[binding.Name] = struct{}{}
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: binding.Name, Value: binding.Value})
	}
	predicate.Bindings = bindings
	predicate.MaxRowsToRead = censusWorkItemMaxRowsToRead
	return predicate, true
}
