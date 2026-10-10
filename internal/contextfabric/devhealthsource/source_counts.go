package devhealthsource

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// kindCountStatement pairs one projected subject kind with a statement that
// counts the DISTINCT entities its producer can emit. The FROM/JOIN/WHERE
// shape mirrors the producer's, so a row a producer cannot project (no owning
// repository, a soft-deleted incident) is not counted as a graph gap.
// uniqExact, not count(): an incident joined to several repositories is one
// subject.
type kindCountStatement struct {
	kind contractsv1.ContextFabricSubjectKind
	sql  string
}

var clickHouseKindCountStatements = []kindCountStatement{
	{contractsv1.ContextFabricSubjectRepository, `SELECT uniqExact(id) FROM repos FINAL WHERE org_id = {org_id:String}`},
	{contractsv1.ContextFabricSubjectWorkItem, `SELECT uniqExact(w.repo_id, w.work_item_id) FROM work_items AS w FINAL WHERE w.org_id = {org_id:String}`},
	{contractsv1.ContextFabricSubjectPullRequest, `SELECT uniqExact(p.repo_id, p.number) FROM git_pull_requests AS p FINAL
INNER JOIN repos AS r FINAL ON r.id = p.repo_id AND r.org_id = p.org_id
WHERE p.org_id = {org_id:String}`},
	{contractsv1.ContextFabricSubjectDeployment, `SELECT uniqExact(d.repo_id, d.deployment_id) FROM deployments AS d FINAL
INNER JOIN repos AS r FINAL ON r.id = d.repo_id AND r.org_id = d.org_id
WHERE d.org_id = {org_id:String}`},
	{contractsv1.ContextFabricSubjectIncident, `SELECT uniqExact(i.id) FROM operational_incidents AS i FINAL
INNER JOIN operational_service_repository_mappings AS m FINAL ON i.org_id = m.org_id AND i.service_id = m.service_id AND m.is_active = 1
INNER JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
WHERE i.org_id = {org_id:String} AND i.is_deleted = 0`},
	{contractsv1.ContextFabricSubjectPullRequestReview, `SELECT uniqExact(r.repo_id, r.number, r.review_id) FROM git_pull_request_reviews AS r FINAL
INNER JOIN git_pull_requests AS p FINAL ON r.repo_id = p.repo_id AND r.number = p.number AND r.org_id = p.org_id
INNER JOIN repos AS repo FINAL ON repo.id = r.repo_id AND repo.org_id = r.org_id
WHERE r.org_id = {org_id:String}`},
	{contractsv1.ContextFabricSubjectCIRun, `SELECT uniqExact(c.repo_id, c.run_id) FROM ci_pipeline_runs AS c FINAL
INNER JOIN repos AS repo FINAL ON repo.id = c.repo_id AND repo.org_id = c.org_id
WHERE c.org_id = {org_id:String}`},
}

var teamsProjectsKindCountStatements = []kindCountStatement{
	{contractsv1.ContextFabricSubjectTeam, `SELECT uniqExact(id) FROM teams FINAL WHERE org_id = {org_id:String} AND ` + devhealthschema.ActiveTeamPredicate("")},
	{contractsv1.ContextFabricSubjectProject, `SELECT uniqExact(provider, id) FROM projects FINAL WHERE org_id = {org_id:String}`},
}

func countKinds(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, statements []kindCountStatement) (map[contextfabric.SubjectKind]int64, error) {
	if strings.TrimSpace(orgID) == "" {
		return nil, fmt.Errorf("devhealthsource: organization is required")
	}
	counts := make(map[contextfabric.SubjectKind]int64, len(statements))
	for _, st := range statements {
		rows, err := client.Query(ctx, st.sql, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}})
		if err != nil {
			return nil, fmt.Errorf("devhealthsource: count %s: %w", st.kind, err)
		}
		var total uint64
		found := false
		for rows.Next() {
			if err := rows.Scan(&total); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("devhealthsource: scan %s count: %w", st.kind, err)
			}
			found = true
		}
		iterErr := rows.Err()
		_ = rows.Close()
		if iterErr != nil {
			return nil, fmt.Errorf("devhealthsource: count %s: %w", st.kind, iterErr)
		}
		if !found {
			return nil, fmt.Errorf("devhealthsource: count %s returned no row", st.kind)
		}
		counts[st.kind] = int64(total)
	}
	return counts, nil
}

// ProjectionSourceCounts implements contextfabric.ProjectionSourceCounts.
func (s *ClickHouseProjectionSource) ProjectionSourceCounts(ctx context.Context, orgID string) (map[contextfabric.SubjectKind]int64, error) {
	return countKinds(ctx, s.client, orgID, clickHouseKindCountStatements)
}

// ProjectionSourceCounts implements contextfabric.ProjectionSourceCounts. A
// disabled source projects nothing, so it reports no counts.
func (s *TeamsProjectsSource) ProjectionSourceCounts(ctx context.Context, orgID string) (map[contextfabric.SubjectKind]int64, error) {
	if !s.enabled {
		return map[contextfabric.SubjectKind]int64{}, nil
	}
	return countKinds(ctx, s.client, orgID, teamsProjectsKindCountStatements)
}
