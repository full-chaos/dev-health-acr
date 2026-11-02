package devhealthsource

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
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
	// day (CHAOS-12649) counts the same population per UTC day (days since the
	// Unix epoch) of each entity's latest sync stamp: the value the producer
	// writes as contextfabric.ProjectionSyncedAtProperty. Empty for a kind whose
	// producer does not stamp it.
	day string
}

// stampedKindCount builds the total and the per-day statement of one kind from
// one population: ids is the distinct key, syncedAt the producer's cursor
// column, from the FROM/JOIN/WHERE the producer reads.
func stampedKindCount(kind contractsv1.ContextFabricSubjectKind, ids, syncedAt, from string) kindCountStatement {
	return kindCountStatement{
		kind: kind,
		sql:  "SELECT uniqExact(" + ids + ") " + from,
		day: "SELECT toUInt64(intDiv(toUnixTimestamp(synced_at), 86400)) AS day, count() FROM (SELECT " + ids + ", max(" + syncedAt + ") AS synced_at " + from +
			" GROUP BY " + ids + ") GROUP BY day",
	}
}

// pullRequestReviewCountSQL counts the distinct reviews queryPullRequestReviews
// can project: the producer omits a review whose canonical id exceeds the
// natural-key bound (identity.Derive), so the count keeps the rows whose id fits.
// Each one is one entity and one BELONGS_TO_PULL_REQUEST edge, so the node count
// and the edge count share this statement.
var pullRequestReviewCount = stampedKindCount(contractsv1.ContextFabricSubjectPullRequestReview, "r.repo_id, r.number, r.review_id", "r.last_synced", `FROM git_pull_request_reviews AS r FINAL
INNER JOIN git_pull_requests AS p FINAL ON r.repo_id = p.repo_id AND r.number = p.number AND r.org_id = p.org_id
INNER JOIN repos AS repo FINAL ON repo.id = r.repo_id AND repo.org_id = r.org_id
WHERE r.org_id = {org_id:String} AND `+derivedIDFitsSQL(identity.KindPullRequestReview, "toString(r.repo_id)", "toString(r.number)", "r.review_id"))

var pullRequestReviewCountSQL = pullRequestReviewCount.sql

var clickHouseKindCountStatements = []kindCountStatement{
	stampedKindCount(contractsv1.ContextFabricSubjectRepository, "id", "last_synced", `FROM repos FINAL WHERE org_id = {org_id:String}`),
	stampedKindCount(contractsv1.ContextFabricSubjectWorkItem, "w.repo_id, w.work_item_id", "w.last_synced", `FROM work_items AS w FINAL WHERE w.org_id = {org_id:String} AND `+derivedIDFitsSQL(identity.KindWorkItem, "toString(w.repo_id)", "w.work_item_id")),
	stampedKindCount(contractsv1.ContextFabricSubjectPullRequest, "p.repo_id, p.number", "p.last_synced", `FROM git_pull_requests AS p FINAL
INNER JOIN repos AS r FINAL ON r.id = p.repo_id AND r.org_id = p.org_id
WHERE p.org_id = {org_id:String}`),
	stampedKindCount(contractsv1.ContextFabricSubjectDeployment, "d.repo_id, d.deployment_id", "d.last_synced", `FROM deployments AS d FINAL
INNER JOIN repos AS r FINAL ON r.id = d.repo_id AND r.org_id = d.org_id
WHERE d.org_id = {org_id:String} AND `+derivedIDFitsSQL(identity.KindDeployment, "toString(d.repo_id)", "d.deployment_id")),
	{kind: contractsv1.ContextFabricSubjectIncident, sql: `SELECT uniqExact(i.id) FROM ` + devhealthschema.CurrentOperationalIncidents + ` AS i
INNER JOIN ` + devhealthschema.CurrentServiceRepositoryMappings + ` AS m ON i.org_id = m.org_id AND i.service_id = m.service_id AND m.is_active = 1
INNER JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
WHERE i.org_id = {org_id:String} AND i.is_deleted = 0`},
	pullRequestReviewCount,
	stampedKindCount(contractsv1.ContextFabricSubjectCIRun, "c.repo_id, c.run_id", "c.last_synced", `FROM ci_pipeline_runs AS c FINAL
INNER JOIN repos AS repo FINAL ON repo.id = c.repo_id AND repo.org_id = c.org_id
WHERE c.org_id = {org_id:String} AND `+derivedIDFitsSQL(identity.KindCIPipelineRun, "toString(c.repo_id)", "c.run_id")),
}

var teamsProjectsKindCountStatements = []kindCountStatement{
	{kind: contractsv1.ContextFabricSubjectTeam, sql: `SELECT uniqExact(id) FROM teams FINAL WHERE org_id = {org_id:String} AND ` + devhealthschema.ActiveTeamPredicate("")},
	{kind: contractsv1.ContextFabricSubjectProject, sql: `SELECT uniqExact(provider, id) FROM projects FINAL WHERE org_id = {org_id:String} AND ` + derivedIDFitsSQL(identity.KindProject, "provider", "id")},
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

// ProjectionSourceDayCounts implements contextfabric.ProjectionSourceDayCounts
// (CHAOS-12649): per stamped kind, the rows per UTC day of their latest sync
// stamp. A kind its producer does not stamp is absent.
func (s *ClickHouseProjectionSource) ProjectionSourceDayCounts(ctx context.Context, orgID string) (map[contextfabric.SubjectKind]map[int64]int64, error) {
	return countKindDays(ctx, s.client, orgID, clickHouseKindCountStatements)
}

func countKindDays(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, statements []kindCountStatement) (map[contextfabric.SubjectKind]map[int64]int64, error) {
	if strings.TrimSpace(orgID) == "" {
		return nil, fmt.Errorf("devhealthsource: organization is required")
	}
	counts := map[contextfabric.SubjectKind]map[int64]int64{}
	for _, st := range statements {
		if st.day == "" {
			continue
		}
		rows, err := client.Query(ctx, st.day, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}})
		if err != nil {
			return nil, fmt.Errorf("devhealthsource: count %s per day: %w", st.kind, err)
		}
		days := map[int64]int64{}
		for rows.Next() {
			var day, total uint64
			if err := rows.Scan(&day, &total); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("devhealthsource: scan %s day count: %w", st.kind, err)
			}
			days[int64(day)] = int64(total)
		}
		iterErr := rows.Err()
		_ = rows.Close()
		if iterErr != nil {
			return nil, fmt.Errorf("devhealthsource: count %s per day: %w", st.kind, iterErr)
		}
		counts[st.kind] = days
	}
	return counts, nil
}
