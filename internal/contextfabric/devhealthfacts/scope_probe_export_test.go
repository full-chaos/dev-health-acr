package devhealthfacts

import (
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

type ScopeProbeFragment struct {
	Name, SQL string
	Bindings  []contextpacket.ClickHouseBinding
}

func ScopeProbeFragmentsForTest(orgID string) []ScopeProbeFragment {
	org := contextpacket.ClickHouseBinding{Name: "org_id", Value: orgID}
	teams := contextpacket.ClickHouseBinding{Name: "team_ids", Value: []string{"A", "B"}}
	ids := contextpacket.ClickHouseBinding{Name: "project_ids", Value: []string{"P", "Q"}}
	keys := contextpacket.ClickHouseBinding{Name: "project_ids", Value: []string{"linear" + projectOriginKeySeparator + "P", "linear" + projectOriginKeySeparator + "Q"}}
	scope := workItemRepositoryAuthorization(storage.Principal{OrgID: orgID}, nil)
	rendered := readers.WorkItemScopeSQL(scope)
	auth := []contextpacket.ClickHouseBinding{org}
	for _, b := range rendered.Bindings {
		auth = append(auth, contextpacket.ClickHouseBinding{Name: b.Name, Value: b.Value})
	}
	count := func(from string) string { return "SELECT count() FROM " + from }
	current := readers.OwnershipValidityPredicate(readers.TimeBound{})
	return []ScopeProbeFragment{
		{"workItemsFinalOrg", "SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String}", []contextpacket.ClickHouseBinding{org}},
		{"workItemsFinalOrgIDs", "SELECT count(), max(length(work_item_id)) FROM work_items FINAL WHERE org_id = {org_id:String}", []contextpacket.ClickHouseBinding{org}},
		{"workItemsPlainOrg", "SELECT count() FROM work_items WHERE org_id = {org_id:String}", []contextpacket.ClickHouseBinding{org}},
		{"teamOwnedProjects", count(teamOwnedProjectsSQL), []contextpacket.ClickHouseBinding{org}},
		{"resolvedProjects", count(resolvedProjectsSQL), []contextpacket.ClickHouseBinding{org}},
		{"teamRepositoryOwnership", count(readers.TeamRepositoryOwnershipSQL(current)), []contextpacket.ClickHouseBinding{org}},
		{"teamOwnedProjectWorkItemIDs", count(teamOwnedProjectWorkItemIDsSQL), []contextpacket.ClickHouseBinding{org, teams}},
		{"teamOwnedProjectWorkItems", count(teamOwnedProjectWorkItemsSQL), []contextpacket.ClickHouseBinding{org, teams}},
		{"teamLinksAmong", count(admittedIssueLinksAmongSQL(teamOwnedProjectWorkItemIDsSQL)), []contextpacket.ClickHouseBinding{org, teams}},
		{"teamAttributedKeys", count(teamAttributedWorkItemKeysSQL), []contextpacket.ClickHouseBinding{org, teams}},
		{"projectWorkItemIDs", count(requestedProjectWorkItemIDsSQL(requestedProjectIDsPredicate)), []contextpacket.ClickHouseBinding{org, ids}},
		{"projectWorkItemKeysIDs", count(requestedProjectWorkItemIDsSQL(requestedProjectKeysPredicate)), []contextpacket.ClickHouseBinding{org, keys}},
		{"libraryJoinOrg", "SELECT count() FROM work_items AS w FINAL " + rendered.JoinSQL + " WHERE w.org_id = {org_id:String}", auth},
		{"libraryJoinScoped", "SELECT count() FROM work_items AS w FINAL " + rendered.JoinSQL + " WHERE w.org_id = {org_id:String} AND w.work_item_id IN " + requestedProjectWorkItemIDsSQL(requestedProjectKeysPredicate), append(append([]contextpacket.ClickHouseBinding{}, auth...), keys)},
		{"libraryRepoJoinOnly", "SELECT count() FROM work_items AS w FINAL LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id WHERE w.org_id = {org_id:String} AND w.work_item_id IN " + requestedProjectWorkItemIDsSQL(requestedProjectKeysPredicate), []contextpacket.ClickHouseBinding{org, keys}},
	}
}
