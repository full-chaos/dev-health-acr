package devhealthsource

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type entityTable struct {
	name  string
	query func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) (rows []candidate, truncated bool, err error)
}

// entityTables is the bounded, documented coverage of canonical Dev Health
// data this source projects today. Every table here is one this repository
// already reads for context packets (internal/contextpacket/source_queries.go);
// no new ingest path is introduced. work_graph_edges and
// work_graph_pr_review_outcome_edges are intentionally excluded: their
// source_id/target_id columns don't carry a subject kind, and guessing one
// would risk mis-typed relationships entering the graph -- see
// docs/design/context-fabric-projection-worker.md.
var entityTables = []entityTable{
	{name: "repos", query: queryRepositories},
	{name: "work_items", query: queryWorkItems},
	{name: "git_pull_requests", query: queryPullRequests},
	{name: "deployments", query: queryDeployments},
	{name: "operational_incidents", query: queryIncidents},
	{name: "work_item_dependencies", query: queryWorkItemDependencies},
	{name: "work_graph_deployment_incident_edges", query: queryDeploymentIncidentEdges},
}

// sincePredicate builds the keyset-pagination predicate. rowKeyExpr MUST be
// a SQL expression that, for a given row, evaluates to exactly the same
// string every candidate scanned from that row uses as candidate.sortKey
// (see each query function below) -- CHAOS-3753 codex finding C5: a
// mismatched or ambiguous tiebreaker (the previous version hardcoded the
// bare identifier "id", which -- in every query joining "repos AS r" --
// resolved to repos.id, not the entity's own id, silently paginating on
// the wrong column for every table except repos itself) causes rows to be
// skipped or replayed whenever two rows share the same timestampExpr
// value, which is common (bulk syncs land in the same second).
func sincePredicate(cursor cursorState, timestampExpr, rowKeyExpr string) string {
	if cursor.Since.IsZero() && cursor.After == "" {
		return ""
	}
	return fmt.Sprintf(" AND (%s > {since:DateTime64(6,'UTC')} OR (%s = {since:DateTime64(6,'UTC')} AND toString(%s) > {after:String}))", timestampExpr, timestampExpr, rowKeyExpr)
}

// orderBy is sincePredicate's ORDER BY counterpart: the same
// (timestampExpr, rowKeyExpr) pair, so the rows this query returns are in
// exactly the order the predicate above assumes for the next page. Every
// query below builds its ORDER BY through this helper rather than
// hand-writing a possibly-divergent one.
func orderBy(timestampExpr, rowKeyExpr string) string {
	return fmt.Sprintf(" ORDER BY %s ASC, toString(%s) ASC LIMIT {row_limit:UInt32}", timestampExpr, rowKeyExpr)
}

func rowLimitBindings(orgID string, cursor cursorState, limit int) []contextpacket.ClickHouseBinding {
	since := cursor.Since
	if since.IsZero() {
		since = time.Unix(0, 0).UTC()
	}
	return []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID}, {Name: "since", Value: since}, {Name: "after", Value: cursor.After},
		{Name: "row_limit", Value: uint32(limit) + 1},
	}
}

// fetch runs statement (which must select exactly the columns scan expects,
// in order) and reports whether more than limit rows were available. scan
// may return more than one candidate per row (e.g. an entity plus its
// BELONGS_TO_REPOSITORY relationship); truncation is still decided per row,
// not per candidate, to match the SQL LIMIT. Every candidate scan returns
// for one row MUST share the same sortKey (the row's keyset-pagination
// identity -- see sincePredicate) regardless of how many candidates that
// row produces.
func fetch(ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, bindings []contextpacket.ClickHouseBinding, limit int, scan func(contextpacket.ClickHouseRowScanner) ([]candidate, error)) ([]candidate, bool, error) {
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	rowGroups := make([][]candidate, 0, limit)
	for rows.Next() {
		items, err := scan(rows)
		if err != nil {
			return nil, false, err
		}
		rowGroups = append(rowGroups, items)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(rowGroups) > limit
	if truncated {
		rowGroups = rowGroups[:limit]
	}
	result := make([]candidate, 0, len(rowGroups))
	for _, group := range rowGroups {
		result = append(result, group...)
	}
	return result, truncated, nil
}

func queryRepositories(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	statement := `SELECT toString(id), repo, ifNull(provider, ''), last_synced FROM repos FINAL
WHERE org_id = {org_id:String}` + sincePredicate(cursor, "last_synced", "id") + orderBy("last_synced", "id")
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var id, slug, provider string
		var observedAt time.Time
		if err := r.Scan(&id, &slug, &provider, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + id, Label: slug}
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Authorization: repoAuthorization(slug), EvidenceRefIDs: []string{"acr:v1:repository:" + id},
			ObservedAt: observedAt, SourceVersion: sourceVersion,
		}
		if provider != "" {
			entity.ProviderIDs = map[string]string{provider: id}
		}
		return []candidate{{observedAt: observedAt, sortKey: id, entity: &entity}}, nil
	})
}

func queryWorkItems(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	statement := `SELECT w.work_item_id, toString(w.repo_id), r.repo, ifNull(w.title, ''), ifNull(w.status, ''), ifNull(w.url, ''), w.updated_at
FROM work_items AS w FINAL INNER JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE w.org_id = {org_id:String}` + sincePredicate(cursor, "w.updated_at", "w.work_item_id") + orderBy("w.updated_at", "w.work_item_id")
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var workItemID, repoID, repoSlug, title, status, url string
		var observedAt time.Time
		if err := r.Scan(&workItemID, &repoID, &repoSlug, &title, &status, &url, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		label := title
		if strings.TrimSpace(label) == "" {
			label = workItemID
		}
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + workItemID, Label: label}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if status != "" {
			properties["status"] = stringScalar(status)
		}
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{"acr:v1:work-item:" + workItemID}, ObservedAt: observedAt, SourceVersion: sourceVersion,
		}
		_ = url
		return []candidate{
			{observedAt: observedAt, sortKey: workItemID, entity: &entity},
			belongsToRepository(subject, repoSlug, repoID, observedAt, "acr:v1:work-item:"+workItemID, workItemID),
		}, nil
	})
}

func queryPullRequests(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "concat(toString(p.repo_id), ':', toString(p.number))"
	statement := `SELECT toString(p.repo_id), r.repo, p.number, ifNull(p.title, ''), ifNull(p.state, ''), p.last_synced
FROM git_pull_requests AS p FINAL INNER JOIN repos AS r FINAL ON r.id = p.repo_id AND r.org_id = p.org_id
WHERE p.org_id = {org_id:String}` + sincePredicate(cursor, "p.last_synced", rowKey) + orderBy("p.last_synced", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var repoID, repoSlug, state string
		var number int64
		var title string
		var observedAt time.Time
		if err := r.Scan(&repoID, &repoSlug, &number, &title, &state, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		canonicalID := fmt.Sprintf("pull_request:%s:%d", repoID, number)
		rowSortKey := fmt.Sprintf("%s:%d", repoID, number)
		label := title
		if strings.TrimSpace(label) == "" {
			label = fmt.Sprintf("PR #%d", number)
		}
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: canonicalID, Label: label}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if state != "" {
			properties["state"] = stringScalar(state)
		}
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{"acr:v1:pull-request:" + repoID + ":" + fmt.Sprint(number)}, ObservedAt: observedAt, SourceVersion: sourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: rowSortKey, entity: &entity},
			belongsToRepository(subject, repoSlug, repoID, observedAt, "acr:v1:pull-request:"+repoID+":"+fmt.Sprint(number), rowSortKey),
		}, nil
	})
}

func queryDeployments(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const timestampExpr = "coalesce(d.deployed_at, d.started_at, d.last_synced)"
	statement := `SELECT toString(d.repo_id), r.repo, d.deployment_id, ifNull(d.status, ''), ifNull(d.environment, ''), ` + timestampExpr + `
FROM deployments AS d FINAL INNER JOIN repos AS r FINAL ON r.id = d.repo_id AND r.org_id = d.org_id
WHERE d.org_id = {org_id:String}` + sincePredicate(cursor, timestampExpr, "d.deployment_id") + orderBy(timestampExpr, "d.deployment_id")
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var repoID, repoSlug, deploymentID, status, environment string
		var observedAt time.Time
		if err := r.Scan(&repoID, &repoSlug, &deploymentID, &status, &environment, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		label := deploymentID
		if environment != "" {
			label = environment + " deployment"
		}
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectDeployment, CanonicalID: "deployment:" + deploymentID, Label: label}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if status != "" {
			properties["status"] = stringScalar(status)
		}
		if environment != "" {
			properties["environment"] = stringScalar(environment)
		}
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{"acr:v1:deployment:" + deploymentID}, ObservedAt: observedAt, SourceVersion: sourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: deploymentID, entity: &entity},
			belongsToRepository(subject, repoSlug, repoID, observedAt, "acr:v1:deployment:"+deploymentID, deploymentID),
		}, nil
	})
}

// queryIncidents mirrors the existing incidents.v1 evidence query's join
// through operational_service_repository_mappings (internal/contextpacket/
// source_queries.go) rather than inventing a new repo-scoping path.
// is_deleted is the one confirmed soft-delete signal in this schema and
// becomes a ProjectionTombstone.
func queryIncidents(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const timestampExpr = "coalesce(i.started_at, i.source_event_at, i.observed_at)"
	statement := `SELECT i.id, toString(m.repo_id) AS repo_id, r.repo AS repo_slug, ifNull(i.title, ''),
       ifNull(i.normalized_status, ifNull(i.raw_status, '')), ifNull(i.normalized_severity, ifNull(i.raw_severity, '')),
       ` + timestampExpr + `, i.is_deleted
FROM operational_incidents AS i FINAL
INNER JOIN operational_service_repository_mappings AS m FINAL ON i.org_id = m.org_id AND i.service_id = m.service_id AND m.is_active = 1
INNER JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
WHERE i.org_id = {org_id:String}` + sincePredicate(cursor, timestampExpr, "i.id") + orderBy(timestampExpr, "i.id")
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var incidentID, repoID, repoSlug, title, status, severity string
		var observedAt time.Time
		var isDeleted uint8
		if err := r.Scan(&incidentID, &repoID, &repoSlug, &title, &status, &severity, &observedAt, &isDeleted); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		canonicalID := "incident:" + incidentID
		if isDeleted != 0 {
			tombstone := contractsv1.ContextFabricProjectionTombstone{
				Kind: "incident", CanonicalID: canonicalID, Reason: "source_deleted", EffectiveAt: observedAt, SourceVersion: sourceVersion,
			}
			return []candidate{{observedAt: observedAt, sortKey: incidentID, tombstone: &tombstone}}, nil
		}
		label := title
		if strings.TrimSpace(label) == "" {
			label = incidentID
		}
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectIncident, CanonicalID: canonicalID, Label: label}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if status != "" {
			properties["status"] = stringScalar(status)
		}
		if severity != "" {
			properties["severity"] = stringScalar(severity)
		}
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{"acr:v1:incident:" + incidentID}, ObservedAt: observedAt, SourceVersion: sourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: incidentID, entity: &entity},
			belongsToRepository(subject, repoSlug, repoID, observedAt, "acr:v1:incident:"+incidentID, incidentID),
		}, nil
	})
}

func queryWorkItemDependencies(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "concat(d.source_work_item_id, ':', d.target_work_item_id)"
	statement := `SELECT d.source_work_item_id, d.target_work_item_id, ifNull(d.relationship_type, 'related_to'), r.repo, d.last_synced
FROM work_item_dependencies AS d FINAL
INNER JOIN work_items AS w FINAL ON w.org_id = d.org_id AND w.work_item_id = d.source_work_item_id
INNER JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE d.org_id = {org_id:String}` + sincePredicate(cursor, "d.last_synced", rowKey) + orderBy("d.last_synced", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var sourceID, targetID, relationshipType, repoSlug string
		var observedAt time.Time
		if err := r.Scan(&sourceID, &targetID, &relationshipType, &repoSlug, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		relationshipID := "relationship:work_item_dependency:" + sourceID + ":" + targetID
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: relationshipID, Type: strings.ToUpper(relationshipType),
			From:       contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + sourceID, Label: sourceID},
			To:         contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + targetID, Label: targetID},
			Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization: repoAuthorization(repoSlug), EvidenceRefIDs: []string{"acr:v1:work-item-dependency:" + sourceID + ":" + targetID},
			ObservedAt: observedAt, SourceVersion: sourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: sourceID + ":" + targetID, relationship: &relationship}}, nil
	})
}

func queryDeploymentIncidentEdges(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	statement := `SELECT e.edge_id, e.deployment_id, e.incident_id, r.repo, e.observed_at
FROM work_graph_deployment_incident_edges AS e FINAL
INNER JOIN repos AS r FINAL ON r.id = e.repo_id AND r.org_id = toString(e.org_id)
WHERE toString(e.org_id) = {org_id:String} AND e.deployment_id != '' AND e.incident_id NOT IN ('', 'none')` + sincePredicate(cursor, "e.observed_at", "e.edge_id") + orderBy("e.observed_at", "e.edge_id")
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var edgeID, deploymentID, incidentID, repoSlug string
		var observedAt time.Time
		if err := r.Scan(&edgeID, &deploymentID, &incidentID, &repoSlug, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		relationshipID := "relationship:deployment_incident:" + edgeID
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: relationshipID, Type: "CORRELATED_WITH_INCIDENT",
			From:       contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectDeployment, CanonicalID: "deployment:" + deploymentID, Label: deploymentID},
			To:         contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectIncident, CanonicalID: "incident:" + incidentID, Label: incidentID},
			Derivation: contractsv1.ContextFabricDerivationRuleInferred, EpistemicStatus: contractsv1.ContextFabricEpistemicSourceAsserted,
			Authorization: repoAuthorization(repoSlug), EvidenceRefIDs: []string{"acr:v1:deployment-incident:" + edgeID},
			ObservedAt: observedAt, SourceVersion: sourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: edgeID, relationship: &relationship}}, nil
	})
}
