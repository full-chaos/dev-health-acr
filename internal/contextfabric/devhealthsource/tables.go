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
// docs/design/context-fabric-projection-worker.md. git_pull_request_reviews
// and ci_pipeline_runs (CHAOS-3753 codex finding C7) are included: PR
// reviews and CI runs are core work-graph signal, and both are already read
// for context packets (source_queries.go's pull_request_reviews.v1 /
// ci_pipeline_runs.v1), so this reuses that same join shape rather than
// inventing a new one. work_items_hierarchy (CHAOS-3779) is not a distinct
// ClickHouse table -- it self-joins work_items on parent_id, a column
// work_item_dependencies never carries, to project the PART_OF edge type.
var entityTables = []entityTable{
	{name: "repos", query: queryRepositories},
	{name: "work_items", query: queryWorkItems},
	{name: "git_pull_requests", query: queryPullRequests},
	{name: "deployments", query: queryDeployments},
	{name: "operational_incidents", query: queryIncidents},
	{name: "work_item_dependencies", query: queryWorkItemDependencies},
	{name: "work_items_hierarchy", query: queryWorkItemHierarchy},
	{name: "work_graph_deployment_incident_edges", query: queryDeploymentIncidentEdges},
	{name: "git_pull_request_reviews", query: queryPullRequestReviews},
	{name: "ci_pipeline_runs", query: queryCIRuns},
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
			ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
		}
		if provider != "" {
			entity.ProviderIDs = map[string]string{provider: id}
		}
		return []candidate{{observedAt: observedAt, sortKey: id, entity: &entity}}, nil
	})
}

// queryWorkItems LEFT JOINs repos (CHAOS-3785; was INNER JOIN): Linear-sourced
// work items carry repo_id = the zero UUID at ingest (Linear issues are not
// tied to a single git repo), which never matches any repos row. An INNER
// JOIN therefore silently dropped ~every row for a Linear-only or
// Linear-dominant organization -- live-verified against org
// 70d529e0-3c06-4597-8480-794fd02328b6: 3282 of 3288 work items carried the
// zero repo_id and never projected. w.org_id = {org_id:String} in the WHERE
// clause is already the organization-scope guard (it does not depend on the
// repos join); the repos join now does one thing only -- resolve optional
// repo attributes (slug, BELONGS_TO_REPOSITORY) when a real repository
// exists. ifNull(r.repo, ”) covers a backend with join_use_nulls enabled;
// this deployment's repos.repo is a non-Nullable String, so an unmatched
// LEFT JOIN already yields ” by default, but every other optional column
// in this file guards the same way.
func queryWorkItems(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	statement := `SELECT w.work_item_id, toString(w.repo_id), ifNull(r.repo, ''), ifNull(w.title, ''), ifNull(w.status, ''), ifNull(w.url, ''), w.updated_at
FROM work_items AS w FINAL LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
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
			EvidenceRefIDs: []string{"acr:v1:work-item:" + workItemID}, ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
		}
		_ = url
		candidates := []candidate{{observedAt: observedAt, sortKey: workItemID, entity: &entity}}
		// repoSlug is '' exactly when the LEFT JOIN found no repos match --
		// there is no real repository entity to point a BELONGS_TO_REPOSITORY
		// edge at, so this row emits an entity candidate only.
		if repoSlug != "" {
			candidates = append(candidates, belongsToRepository(subject, repoSlug, repoID, observedAt, "acr:v1:work-item:"+workItemID, workItemID))
		}
		return candidates, nil
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
			EvidenceRefIDs: []string{"acr:v1:pull-request:" + repoID + ":" + fmt.Sprint(number)}, ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
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
			EvidenceRefIDs: []string{"acr:v1:deployment:" + deploymentID}, ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
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
				Kind: "incident", CanonicalID: canonicalID, Reason: "source_deleted", EffectiveAt: observedAt, SourceVersion: ClickHouseSourceVersion,
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
			EvidenceRefIDs: []string{"acr:v1:incident:" + incidentID}, ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: incidentID, entity: &entity},
			belongsToRepository(subject, repoSlug, repoID, observedAt, "acr:v1:incident:"+incidentID, incidentID),
		}, nil
	})
}

// queryWorkItemDependencies' natural key is (org, source, target,
// relationship_type) -- work_item_dependencies allows more than one row
// for the same (source, target) pair when relationship_type differs, and
// this is not hypothetical: live ClickHouse holds real rows where the
// same (source, target) pair carries BOTH 'blocks' and 'relates_to'
// (CHAOS-3779 codex round-1 finding H2, verified against the running
// database). rowKey, sortKey, and RelationshipID all include
// relationship_type for exactly that reason -- omitting it, as an earlier
// version of this function did, collapses two genuinely different edges
// onto one identity: within a single page both rows share one
// RelationshipID, which ContextFabricProjectionBatch.Validate() rejects
// outright ("relationship IDs must be unique within a batch"), and across
// two pages (or two incremental ticks) with the SAME rowKey, the
// keyset-pagination predicate (sincePredicate's own doc comment, C5) could
// skip one row entirely, or a later write could silently overwrite the
// earlier edge's Type in the graph via relationship_id-keyed MERGE.
// The INNER JOIN to work_items stays INNER (CHAOS-3785 does not touch it):
// it proves source_work_item_id resolves to a real work item in this
// organization, which live data shows is not guaranteed -- some
// work_item_dependencies rows carry a cross-system PR reference
// ("ghpr:owner/repo#N") in source_work_item_id instead of a work item id,
// and those are correctly not this producer's concern. Only the repos join
// (resolving the source work item's optional repo attributes) relaxes to
// LEFT, for the same zero-UUID reason queryWorkItems does.
func queryWorkItemDependencies(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const relationshipTypeExpr = "ifNull(d.relationship_type, 'related_to')"
	const rowKey = "concat(d.source_work_item_id, ':', d.target_work_item_id, ':', " + relationshipTypeExpr + ")"
	statement := `SELECT d.source_work_item_id, d.target_work_item_id, ` + relationshipTypeExpr + `, ifNull(r.repo, ''), d.last_synced
FROM work_item_dependencies AS d FINAL
INNER JOIN work_items AS w FINAL ON w.org_id = d.org_id AND w.work_item_id = d.source_work_item_id
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE d.org_id = {org_id:String}` + sincePredicate(cursor, "d.last_synced", rowKey) + orderBy("d.last_synced", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var sourceID, targetID, relationshipType, repoSlug string
		var observedAt time.Time
		if err := r.Scan(&sourceID, &targetID, &relationshipType, &repoSlug, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		relationshipID := "relationship:work_item_dependency:" + sourceID + ":" + targetID + ":" + relationshipType
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: relationshipID, Type: contractsv1.ContextFabricRelationshipType(strings.ToUpper(relationshipType)),
			From:       contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + sourceID, Label: sourceID},
			To:         contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + targetID, Label: targetID},
			Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization: repoAuthorization(repoSlug), EvidenceRefIDs: []string{"acr:v1:work-item-dependency:" + sourceID + ":" + targetID + ":" + relationshipType},
			ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: sourceID + ":" + targetID + ":" + relationshipType, relationship: &relationship}}, nil
	})
}

// queryWorkItemHierarchy (CHAOS-3779, §19.5.3 PART_OF) projects
// work_items.parent_id -- a distinct column from work_item_dependencies,
// carrying real hierarchy data (verified live: 2082 of 3282 work_items
// rows have a non-empty parent_id, and every one of those resolves to a
// real work_items row in the same organization). The INNER JOIN to a
// second work_items instance for the parent enforces that resolvability at
// query time: a child row whose parent_id names a work item that does not
// exist (a different organization, or a row deleted out from under it) is
// never projected as a dangling PART_OF edge.
//
// c.parent_id != c.work_item_id (CHAOS-3779 codex round-1 finding M3) is a
// SELF-reference filter, not cycle detection. A multi-node cycle (A part_of
// B part_of A) is accepted graph state -- deciding whether a hierarchy may
// legitimately cycle is a graph-shape policy question outside this issue's
// scope (see docs/design/context-fabric-projection-worker.md's "PART_OF
// cycles" note). A SELF-reference is different in kind: it is never a
// legitimate hierarchy (a work item cannot be its own parent) and
// ContextFabricRelationshipProjection.Validate() unconditionally rejects
// any relationship with From == To ("relationship cannot be
// self-referential"). Because ContextFabricProjectionBatch.Validate() is
// all-or-nothing, ONE such row -- a source-system data bug, not a modeling
// choice -- would poison the ENTIRE batch and wedge this organization's
// projection forever, silently, until someone traced the failure back to
// one bad row. Filtering it at the SQL boundary, before it ever becomes a
// candidate, is cheap and permanent; live ClickHouse holds zero such rows
// today (verified), but the filter guards against a future one.
// The INNER JOIN to the parent work_items row stays INNER (CHAOS-3785 does
// not touch it): it is the resolvability guarantee the doc comment above
// describes, unrelated to which table owns the repo attribute. Only the
// repos join relaxes to LEFT, for the same zero-UUID reason queryWorkItems
// does -- a Linear-sourced child work item's repo_id is the zero UUID.
func queryWorkItemHierarchy(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "c.work_item_id"
	statement := `SELECT c.work_item_id, c.parent_id, ifNull(r.repo, ''), c.updated_at
FROM work_items AS c FINAL
INNER JOIN work_items AS p FINAL ON p.org_id = c.org_id AND p.work_item_id = c.parent_id
LEFT JOIN repos AS r FINAL ON r.id = c.repo_id AND r.org_id = c.org_id
WHERE c.org_id = {org_id:String} AND c.parent_id != '' AND c.parent_id != c.work_item_id` + sincePredicate(cursor, "c.updated_at", rowKey) + orderBy("c.updated_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var childID, parentID, repoSlug string
		var observedAt time.Time
		if err := r.Scan(&childID, &parentID, &repoSlug, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		relationshipID := "relationship:work_item_hierarchy:" + childID + ":" + parentID
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: relationshipID, Type: contractsv1.ContextFabricRelationshipPartOf,
			From:       contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + childID, Label: childID},
			To:         contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + parentID, Label: parentID},
			Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization: repoAuthorization(repoSlug), EvidenceRefIDs: []string{"acr:v1:work-item-hierarchy:" + childID + ":" + parentID},
			ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: childID, relationship: &relationship}}, nil
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
			ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: edgeID, relationship: &relationship}}, nil
	})
}

// queryPullRequestReviews and queryCIRuns (CHAOS-3753 codex finding C7,
// corrected for codex round-2 finding K1) reuse the JOIN shape
// internal/contextpacket/source_queries.go already uses for
// pull_request_reviews.v1 / ci_pipeline_runs.v1, with one deliberate
// difference: that existing query never filters git_pull_request_reviews
// or ci_pipeline_runs by their own org_id, but both tables DO carry one in
// production (testdata/fullstack/v1/README.md:96 -- migration
// 027_add_org_id_to_sorting_keys.py added it to six tables, including
// these two, and made it part of each ReplacingMergeTree's dedup ORDER BY
// key). K1: the first version of this code copied that existing query's
// omission verbatim, repeating the exact class of bug W1 already fixed
// elsewhere in this file -- see the design doc's "every join carries
// org_id equality, no exceptions" class rule, and the full join inventory
// there.
func queryPullRequestReviews(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "r.review_id"
	statement := `SELECT r.review_id, toString(r.repo_id), r.number, ifNull(r.state, ''), r.submitted_at, repo.repo
FROM git_pull_request_reviews AS r FINAL
INNER JOIN git_pull_requests AS p FINAL ON r.repo_id = p.repo_id AND r.number = p.number AND r.org_id = p.org_id
INNER JOIN repos AS repo FINAL ON repo.id = r.repo_id AND repo.org_id = r.org_id
WHERE r.org_id = {org_id:String}` + sincePredicate(cursor, "r.submitted_at", rowKey) + orderBy("r.submitted_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var reviewID, repoID, state, repoSlug string
		var number int64
		var observedAt time.Time
		if err := r.Scan(&reviewID, &repoID, &number, &state, &observedAt, &repoSlug); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		canonicalID := "pull_request_review:" + reviewID
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: canonicalID, Label: fmt.Sprintf("PR #%d review", number)}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if state != "" {
			properties["state"] = stringScalar(state)
		}
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{"acr:v1:review:" + reviewID}, ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
		}
		pullRequestID := fmt.Sprintf("pull_request:%s:%d", repoID, number)
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: "relationship:belongs_to_pull_request:" + canonicalID, Type: "BELONGS_TO_PULL_REQUEST",
			From:       subject,
			To:         contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: pullRequestID, Label: pullRequestID},
			Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization: repoAuthorization(repoSlug), EvidenceRefIDs: []string{"acr:v1:review:" + reviewID},
			ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: reviewID, entity: &entity},
			{observedAt: observedAt, sortKey: reviewID, relationship: &relationship},
		}, nil
	})
}

func queryCIRuns(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const timestampExpr = "coalesce(c.finished_at, c.started_at)"
	const rowKey = "c.run_id"
	statement := `SELECT c.run_id, toString(c.repo_id), ifNull(c.branch, ''), ifNull(c.status, ''), repo.repo, ` + timestampExpr + `
FROM ci_pipeline_runs AS c FINAL
INNER JOIN repos AS repo FINAL ON repo.id = c.repo_id AND repo.org_id = c.org_id
WHERE c.org_id = {org_id:String}` + sincePredicate(cursor, timestampExpr, rowKey) + orderBy(timestampExpr, rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var runID, repoID, branch, status, repoSlug string
		var observedAt time.Time
		if err := r.Scan(&runID, &repoID, &branch, &status, &repoSlug, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:" + runID, Label: "CI " + runID}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if status != "" {
			properties["status"] = stringScalar(status)
		}
		if branch != "" {
			properties["branch"] = stringScalar(branch)
		}
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{"acr:v1:ci:" + runID}, ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: runID, entity: &entity},
			belongsToRepository(subject, repoSlug, repoID, observedAt, "acr:v1:ci:"+runID, runID),
		}, nil
	})
}
