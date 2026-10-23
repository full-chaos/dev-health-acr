package devhealthsource

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// edgePopulation is one relation type measured by ONE statement that returns a
// row per arm: (arm, distinct edges). A relation with no arms has the single
// arm "".
type edgePopulation struct {
	relation contractsv1.ContextFabricRelationshipType
	arms     []string
	sql      string
}

func (p edgePopulation) keys() []contextfabric.EdgeCountKey {
	arms := p.arms
	if len(arms) == 0 {
		arms = []string{""}
	}
	keys := make([]contextfabric.EdgeCountKey, 0, len(arms))
	for _, arm := range arms {
		keys = append(keys, contextfabric.EdgeCountKey{Relation: p.relation, Arm: arm})
	}
	return keys
}

// repositoryEdgeArms are the subject kinds that BELONGS_TO_REPOSITORY leaves.
var repositoryEdgeArms = []string{
	string(contractsv1.ContextFabricSubjectPullRequest),
	string(contractsv1.ContextFabricSubjectWorkItem),
	string(contractsv1.ContextFabricSubjectDeployment),
	string(contractsv1.ContextFabricSubjectCIRun),
	string(contractsv1.ContextFabricSubjectIncident),
}

var linkEdgeArms = []string{contextfabric.EdgeArmLinkNative, contextfabric.EdgeArmLinkExplicitText, contextfabric.EdgeArmLinkHeuristic}

// belongsToRepositoryEdgeCountSQL counts, per source kind, the distinct
// BELONGS_TO_REPOSITORY edges the producers assert. Each entity has one edge
// (the incident one per repository an active mapping names), so the statement of
// each arm is the producer's own join with the same omissions: a work item gets
// an edge only when its repository row exists with a slug; a work item,
// deployment or CI run whose canonical id exceeds the natural-key bound is
// omitted; an incident gets an edge per active mapping to a known repository
// with a slug, and none when deleted.
func belongsToRepositoryEdgeCountSQL() string {
	arm := func(kind contractsv1.ContextFabricSubjectKind, body string) string {
		return "SELECT '" + string(kind) + "' AS arm, " + body
	}
	return strings.Join([]string{
		arm(contractsv1.ContextFabricSubjectPullRequest, `uniqExact(p.repo_id, p.number) AS edge_count FROM git_pull_requests AS p FINAL
INNER JOIN repos AS r FINAL ON r.id = p.repo_id AND r.org_id = p.org_id
WHERE p.org_id = {org_id:String}`),
		arm(contractsv1.ContextFabricSubjectWorkItem, `uniqExact(w.repo_id, w.work_item_id) AS edge_count FROM work_items AS w FINAL
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE w.org_id = {org_id:String} AND ifNull(r.repo, '') != '' AND `+derivedIDFitsSQL(identity.KindWorkItem, "toString(w.repo_id)", "w.work_item_id")),
		arm(contractsv1.ContextFabricSubjectDeployment, `uniqExact(d.repo_id, d.deployment_id) AS edge_count FROM deployments AS d FINAL
INNER JOIN repos AS r FINAL ON r.id = d.repo_id AND r.org_id = d.org_id
WHERE d.org_id = {org_id:String} AND `+derivedIDFitsSQL(identity.KindDeployment, "toString(d.repo_id)", "d.deployment_id")),
		arm(contractsv1.ContextFabricSubjectCIRun, `uniqExact(c.repo_id, c.run_id) AS edge_count FROM ci_pipeline_runs AS c FINAL
INNER JOIN repos AS r FINAL ON r.id = c.repo_id AND r.org_id = c.org_id
WHERE c.org_id = {org_id:String} AND `+derivedIDFitsSQL(identity.KindCIPipelineRun, "toString(c.repo_id)", "c.run_id")),
		arm(contractsv1.ContextFabricSubjectIncident, `uniqExact(i.id, m.repo_id) AS edge_count FROM `+devhealthschema.CurrentOperationalIncidents+` AS i
INNER JOIN `+devhealthschema.CurrentServiceRepositoryMappings+` AS m ON i.org_id = m.org_id AND i.service_id = m.service_id AND m.is_active = 1
INNER JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
WHERE i.org_id = {org_id:String} AND i.is_deleted = 0 AND ifNull(r.repo, '') != ''`),
	}, "\nUNION ALL\n")
}

// deploymentIncidentEdgeCountSQL counts the CORRELATED_WITH_INCIDENT edges of
// queryDeploymentIncidentEdges: its repository join, its row filter and the
// deployment id bound. An edge is (repository, deployment, incident); edge_id
// and source do not enter its identity.
func deploymentIncidentEdgeCountSQL() string {
	return `SELECT '' AS arm, uniqExact(e.repo_id, e.deployment_id, e.incident_id) AS edge_count
FROM work_graph_deployment_incident_edges AS e FINAL
INNER JOIN repos AS r FINAL ON r.id = e.repo_id AND r.org_id = toString(e.org_id)
WHERE toString(e.org_id) = {org_id:String} AND e.deployment_id != '' AND e.incident_id NOT IN ('', 'none')
  AND ` + derivedIDFitsSQL(identity.KindDeployment, "toString(e.repo_id)", "e.deployment_id")
}

// partOfEdgeCountSQL counts the PART_OF edges of queryWorkItemHierarchy over its
// own row source (hierarchyFromSQL): a child gets an edge exactly when the
// producer's retraction switch does not fire (a parent that is not the child, is
// not an empty qualified key, resolves to one candidate if qualified or to a work
// item of the child's own repository if not) and both canonical ids fit the bound.
func partOfEdgeCountSQL() string {
	const parentRef = hierarchyParentRef
	qualified := qualifiedParentSQL(parentRef)
	parentRepo := "if(" + qualified + ", pa.repo, toString(p.repo_id))"
	return `SELECT '' AS arm, uniqExact(toString(c.repo_id), c.work_item_id, ` + parentRepo + `, ` + parentRef + `) AS edge_count
` + hierarchyFromSQL(parentRef) + `
  AND ` + parentRef + ` != '' AND ` + parentRef + ` != c.work_item_id
  AND NOT (` + emptyKeyParentSQL(parentRef) + `)
  AND ifNull(pa.candidates, 0) > 0
  AND NOT (` + qualified + ` AND ifNull(pa.candidates, 0) > 1)
  AND (` + qualified + ` OR ifNull(p.work_item_id, '') != '')
  AND ` + derivedIDFitsSQL(identity.KindWorkItem, "toString(c.repo_id)", "c.work_item_id") + `
  AND ` + derivedIDFitsSQL(identity.KindWorkItem, parentRepo, parentRef)
}

const linkTierSQL = "trimBoth(l.provenance)"

// linksPullRequestEdgeCountSQL counts, per provenance tier, the LINKS_PULL_REQUEST
// edges of queryIssuePullRequestLinks over its own joins. A row of a known tier
// that a native link outranks (the same issuePullRequestLinkOutranked
// expression) projects nothing, so a lower tier beside a native link on the
// issue or on the pull request is not counted. The issue must resolve, must not
// itself be pull-request typed, and the pull request must exist with a
// repository slug.
func linksPullRequestEdgeCountSQL() string {
	return `SELECT ` + linkTierSQL + ` AS arm, uniqExact(toString(w.repo_id), l.work_item_id, toString(l.repo_id), l.pr_number) AS edge_count
FROM work_graph_issue_pr AS l FINAL
LEFT JOIN work_items AS w FINAL ON w.org_id = l.org_id AND w.work_item_id = l.work_item_id
LEFT JOIN git_pull_requests AS p FINAL ON p.org_id = l.org_id AND p.repo_id = l.repo_id AND p.number = l.pr_number
LEFT JOIN repos AS r FINAL ON r.id = l.repo_id AND r.org_id = l.org_id
WHERE l.org_id = {org_id:String} AND ` + linkTierSQL + ` IN ('` + strings.Join(linkEdgeArms, "', '") + `')
  AND ` + issuePullRequestLinkOutranked + ` = 0
  AND ifNull(w.work_item_id, '') != ''
  AND lower(trimBoth(ifNull(w.type, ''))) NOT IN ('` + strings.Join(pullRequestWorkItemTypeNames(), "', '") + `')
  AND ifNull(p.number, 0) > 0 AND trimBoth(ifNull(r.repo, '')) != ''
  AND ` + derivedIDFitsSQL(identity.KindWorkItem, "toString(w.repo_id)", "w.work_item_id") + `
GROUP BY arm`
}

func pullRequestWorkItemTypeNames() []string {
	names := make([]string, 0, len(pullRequestWorkItemTypes))
	for name := range pullRequestWorkItemTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var clickHouseEdgePopulations = []edgePopulation{
	{relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest, sql: `SELECT '' AS arm, * FROM (` + pullRequestReviewCountSQL + `)`},
	{relation: contractsv1.ContextFabricRelationshipBelongsToRepository, arms: repositoryEdgeArms, sql: belongsToRepositoryEdgeCountSQL()},
	{relation: contractsv1.ContextFabricRelationshipCorrelatedWithIncident, sql: deploymentIncidentEdgeCountSQL()},
	{relation: contractsv1.ContextFabricRelationshipPartOf, sql: partOfEdgeCountSQL()},
	{relation: contractsv1.ContextFabricRelationshipLinksPullRequest, arms: linkEdgeArms, sql: linksPullRequestEdgeCountSQL()},
}

// runEdgePopulations runs each population's statement and returns every key it
// names, zero included. An arm the statement returns that the population does
// not name is an error, never a silent extra.
func runEdgePopulations(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, populations []edgePopulation) (map[contextfabric.EdgeCountKey]int64, error) {
	if strings.TrimSpace(orgID) == "" {
		return nil, fmt.Errorf("devhealthsource: organization is required")
	}
	counts := map[contextfabric.EdgeCountKey]int64{}
	for _, population := range populations {
		for _, key := range population.keys() {
			counts[key] = 0
		}
		rows, err := client.Query(ctx, population.sql, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}})
		if err != nil {
			return nil, fmt.Errorf("devhealthsource: count %s edges: %w", population.relation, err)
		}
		for rows.Next() {
			var arm string
			var total uint64
			if err := rows.Scan(&arm, &total); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("devhealthsource: scan %s edge count: %w", population.relation, err)
			}
			key := contextfabric.EdgeCountKey{Relation: population.relation, Arm: arm}
			if _, known := counts[key]; !known {
				_ = rows.Close()
				return nil, fmt.Errorf("devhealthsource: %s edge count returned unknown arm %q", population.relation, arm)
			}
			counts[key] = int64(total)
		}
		iterErr := rows.Err()
		_ = rows.Close()
		if iterErr != nil {
			return nil, fmt.Errorf("devhealthsource: count %s edges: %w", population.relation, iterErr)
		}
	}
	return counts, nil
}
