package devhealthsource

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type entityTable struct {
	name  string
	query func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) (rows []candidate, truncated bool, err error)
}

// devhealthschema:not-a-production-replica this is the PRODUCER REGISTRY -- it pairs each table
// with the query that reads it. It mirrors no column types,
// engines or sort keys, so it cannot drift from production the way a rival
// schema declaration would; devhealthschema remains the only physical source.
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
	// devhealthschema:not-a-production-replica registry TAIL -- the same producer list continues here,
	// past the reach of the marker on the declaration above. Still a
	// table-to-query pairing that mirrors no column type, engine or sort key.
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

// havingSincePredicate is sincePredicate for a query whose pagination
// timestamp is an AGGREGATE (CHAOS-4542): the identical keyset condition,
// emitted as HAVING because a WHERE cannot reference an aggregate.
//
// It DELEGATES rather than restating the condition. The two spellings
// drifting apart would mean a page boundary that skips or replays rows --
// silently, and exactly the class sincePredicate's own doc comment was
// written about. One definition, two placements.
func havingSincePredicate(cursor cursorState, timestampExpr, rowKeyExpr string) string {
	predicate := sincePredicate(cursor, timestampExpr, rowKeyExpr)
	if predicate == "" {
		return ""
	}
	// sincePredicate emits a leading " AND " to extend an existing WHERE;
	// a HAVING clause opens the condition instead.
	return " HAVING " + strings.TrimPrefix(predicate, " AND ")
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
	statement := `SELECT toString(id), repo, ifNull(provider, ''), last_synced, created_at, ifNull(tags, '') FROM repos FINAL
WHERE org_id = {org_id:String}` + sincePredicate(cursor, "last_synced", "id") + orderBy("last_synced", "id")
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var id, slug, provider, rawTags string
		var observedAt, createdAt time.Time
		if err := r.Scan(&id, &slug, &provider, &observedAt, &createdAt, &rawTags); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + id, Label: slug}
		// CHAOS-3833 (embed-text spec §2): the repository template's tags
		// field -- a JSON-rendered string array in production, parsed,
		// sorted and joined at the producer so the same row always yields
		// one canonical value.
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		setStringProperty(properties, "tags", parsedRepoTags(rawTags), 0)
		if len(properties) == 0 {
			properties = nil
		}
		// repos records no deletion column, so a repository's window is
		// open-ended: valid from creation, with no recorded end.
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(slug), EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, id)},
			ObservedAt: observedAt, ValidFrom: requiredTime(createdAt), SourceVersion: ClickHouseSourceVersion,
		}
		if provider != "" {
			entity.ProviderIDs = map[string]string{provider: id}
		}
		// CHAOS-3884 Part A: bare-name and provider-variant aliases, so a
		// bare-name/provider-qualified query surfaces this repository in
		// the clarification pool (retrievalHandles/repositorySearchText
		// already fold entity.Aliases/ProviderAliases into the SAME
		// indexed search text the canonical slug uses -- no falkorgraph
		// search-path change needed for this half; only projection-time
		// data was missing). distinctNonEmpty (teams_projects.go, same
		// package) drops "" entries so an unqualified slug or an unset
		// provider contributes nothing rather than a spurious empty alias.
		if alias := repositoryBareNameAlias(slug); alias != "" {
			entity.Aliases = distinctNonEmpty(alias)
		}
		if alias := repositoryProviderAlias(provider, slug); alias != "" {
			entity.ProviderAliases = distinctNonEmpty(alias)
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
// queryWorkItems mints work_item.v2:<repo_id>:<enc(work_item_id)> (design
// brief D-4/§1.3: the work_item exemption is withdrawn -- repo_id is used
// VERBATIM including the zero-UUID sentinel a Linear-sourced row carries,
// because that value IS the row's own natural-key value, not an absence to
// special-case). The pagination rowKey (D-6) carries the same qualifier.
func queryWorkItems(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "concat(toString(w.repo_id), ':', w.work_item_id)"
	statement := `SELECT w.work_item_id, toString(w.repo_id), ifNull(r.repo, ''), ifNull(w.title, ''), ifNull(w.status, ''), ifNull(w.url, ''), w.updated_at,
       w.created_at, ` + nullableTimestamp("coalesce(w.completed_at, w.closed_at)") + `,
       ifNull(w.type, ''), ifNull(w.native_team_key, ''), ifNull(w.project_name, ''), w.labels
FROM work_items AS w FINAL LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE w.org_id = {org_id:String}` + sincePredicate(cursor, "w.updated_at", rowKey) + orderBy("w.updated_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var workItemID, repoID, repoSlug, title, status, url string
		var itemType, teamKey, projectName string
		var labels []string
		var observedAt, createdAt, endedAt time.Time
		var hasEnded uint8
		if err := r.Scan(&workItemID, &repoID, &repoSlug, &title, &status, &url, &observedAt, &createdAt, &hasEnded, &endedAt,
			&itemType, &teamKey, &projectName, &labels); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		canonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
		if err != nil {
			return nil, err
		}
		rowSortKey := repoID + ":" + workItemID
		if omitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		// A work item is valid from creation until it completed or
		// closed, whichever the source recorded; an open item has no end.
		validFrom, validTo := requiredTime(createdAt), optionalTime(hasEnded, endedAt)
		// Trimmed at the row, not left to item_normalization.go's pass, so a
		// reader of this producer sees the contract's trim rule where the
		// label is minted. The pass is still the authority and still covers
		// every OTHER label site in this package; it simply finds nothing to
		// do here.
		label := strings.TrimSpace(title)
		if label == "" {
			label = workItemID
		}
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: canonicalID, Label: label}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if status != "" {
			properties["status"] = stringScalar(status)
		}
		// CHAOS-3833 (embed-text spec §2): the fields the work_item
		// template composes. status stays a structured property and is
		// deliberately NOT part of the text (it flips constantly ->
		// re-embed churn); description stays unread (0% populated).
		setStringProperty(properties, "type", itemType, 0)
		setStringProperty(properties, "native_team_key", teamKey, 0)
		setStringProperty(properties, "project_name", projectName, 0)
		setStringProperty(properties, "labels", joinedSortedList(labels, 10, 40, ", "), 0)
		evidenceRefID := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, repoID+":"+workItemID)
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: workItemAuthorization(repoID, repoSlug),
			EvidenceRefIDs: []string{evidenceRefID}, ObservedAt: observedAt,
			ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
		}
		// CHAOS-3833 (spec §2, review R2): the ticket-key alias derived
		// from work_item_id by the exact first-colon rule. It enters
		// Aliases -- lexical exact-match gains it too -- and the
		// composition leads the text with it.
		if alias := ticketKeyAlias(workItemID); alias != "" {
			entity.Aliases = []string{alias}
		}
		_ = url
		candidates := []candidate{{observedAt: observedAt, sortKey: rowSortKey, entity: &entity}}
		// repoSlug is '' exactly when the LEFT JOIN found no repos match --
		// there is no real repository entity to point a BELONGS_TO_REPOSITORY
		// edge at, so this row emits an entity candidate only.
		if repoSlug != "" {
			candidates = append(candidates, belongsToRepository(subject, repoSlug, repoID, observedAt, evidenceRefID, rowSortKey, validFrom, validTo))
		}
		return candidates, nil
	})
}

// queryPullRequests scans git_pull_requests.number into a uint32 (CHAOS-3789):
// the column is UInt32 in production, and clickhouse-go's native driver
// rejects scanning a UInt32 column into an int64 destination outright
// ("converting UInt32 to *int64 is unsupported") -- this was never caught by
// a test because the package's fixtures modeled the column as int64, a
// different type than the real backend. The int64 conversion happens right
// after Scan, once the value is safely in Go, so every downstream use
// (canonicalID, label, sort key) is unchanged.
func queryPullRequests(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "concat(toString(p.repo_id), ':', toString(p.number))"
	statement := `SELECT toString(p.repo_id), r.repo, p.number, ifNull(p.title, ''), ifNull(p.state, ''), p.last_synced,
       p.created_at, ` + nullableTimestamp("coalesce(p.merged_at, p.closed_at)") + `,
       ifNull(p.head_branch, ''), ifNull(p.body, '')
FROM git_pull_requests AS p FINAL INNER JOIN repos AS r FINAL ON r.id = p.repo_id AND r.org_id = p.org_id
WHERE p.org_id = {org_id:String}` + sincePredicate(cursor, "p.last_synced", rowKey) + orderBy("p.last_synced", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var repoID, repoSlug, state string
		var rawNumber uint32
		var title, headBranch, body string
		var observedAt, createdAt, endedAt time.Time
		var hasEnded uint8
		if err := r.Scan(&repoID, &repoSlug, &rawNumber, &title, &state, &observedAt, &createdAt, &hasEnded, &endedAt,
			&headBranch, &body); err != nil {
			return nil, err
		}
		number := int64(rawNumber)
		observedAt = observedAt.UTC()
		// A pull request is valid from creation until it merged or
		// closed; an open one has no end.
		validFrom, validTo := requiredTime(createdAt), optionalTime(hasEnded, endedAt)
		canonicalID := fmt.Sprintf("pull_request:%s:%d", repoID, number)
		rowSortKey := fmt.Sprintf("%s:%d", repoID, number)
		label := strings.TrimSpace(title)
		if label == "" {
			label = fmt.Sprintf("PR #%d", number)
		}
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: canonicalID, Label: label}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if state != "" {
			properties["state"] = stringScalar(state)
		}
		// CHAOS-3833 (embed-text spec §2): the pull_request template's
		// fields. head_branch is a free alias carrier (ticket keys inside
		// branch names, e.g. "feat/chaos-1725-..."). Only the BODY HEAD is
		// persisted (first 1,200 runes -- the thesis-bearing part of a PR
		// description, avg 4,485 runes live); whether it joins the
		// composed text at all is the §3 provider-locality body gate's
		// decision at composition time, not this producer's.
		// author_name/author_email stay unread (person PII, spec §3).
		properties["number"] = intScalar(number)
		setStringProperty(properties, "repo", repoSlug, 0)
		setStringProperty(properties, "branch", headBranch, 0)
		setStringProperty(properties, "body", body, 1200)
		evidenceRefID := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, repoID+":"+fmt.Sprint(number))
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{evidenceRefID}, ObservedAt: observedAt,
			ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: rowSortKey, entity: &entity},
			belongsToRepository(subject, repoSlug, repoID, observedAt, evidenceRefID, rowSortKey, validFrom, validTo),
		}, nil
	})
}

// queryDeployments mints deployment.v2:<repo_id>:<enc(deployment_id)>
// (design brief D-3, §1.2), the same cross-repo-collision fix as
// queryCIRuns above, applied to an audit-found defect rather than a
// live-verified collision.
func queryDeployments(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const timestampExpr = "coalesce(d.deployed_at, d.started_at, d.last_synced)"
	const rowKey = "concat(toString(d.repo_id), ':', d.deployment_id)"
	statement := `SELECT toString(d.repo_id), r.repo, d.deployment_id, ifNull(d.status, ''), ifNull(d.environment, ''), ` + timestampExpr + `,
       ` + nullableTimestamp("coalesce(d.started_at, d.deployed_at)") + `, ` + nullableTimestamp("d.finished_at") + `,
       ifNull(d.release_ref, '')
FROM deployments AS d FINAL INNER JOIN repos AS r FINAL ON r.id = d.repo_id AND r.org_id = d.org_id
WHERE d.org_id = {org_id:String}` + sincePredicate(cursor, timestampExpr, rowKey) + orderBy(timestampExpr, rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var repoID, repoSlug, deploymentID, status, environment string
		var releaseRef string
		var observedAt, startedAt, finishedAt time.Time
		var hasStarted, hasFinished uint8
		if err := r.Scan(&repoID, &repoSlug, &deploymentID, &status, &environment, &observedAt, &hasStarted, &startedAt, &hasFinished, &finishedAt, &releaseRef); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		canonicalID, omitted, err := identity.Derive(identity.KindDeployment, []string{repoID, deploymentID}, nil)
		if err != nil {
			return nil, err
		}
		rowSortKey := repoID + ":" + deploymentID
		if omitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		// A deployment is valid while it is running; one still in flight
		// has no recorded end.
		validFrom, validTo := optionalTime(hasStarted, startedAt), optionalTime(hasFinished, finishedAt)
		label := deploymentID
		if environment != "" {
			label = environment + " deployment"
		}
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectDeployment, CanonicalID: canonicalID, Label: label}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if status != "" {
			properties["status"] = stringScalar(status)
		}
		if environment != "" {
			properties["environment"] = stringScalar(environment)
		}
		// CHAOS-3833 (embed-text spec §2): the deployment template's
		// fields. status is 0% populated live -- nothing to add there.
		setStringProperty(properties, "release_ref", releaseRef, 0)
		setStringProperty(properties, "repo", repoSlug, 0)
		evidenceRefID := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityDeployment, repoID+":"+deploymentID)
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{evidenceRefID}, ObservedAt: observedAt,
			ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: rowSortKey, entity: &entity},
			belongsToRepository(subject, repoSlug, repoID, observedAt, evidenceRefID, rowSortKey, validFrom, validTo),
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
       ` + timestampExpr + `, i.is_deleted,
       ` + nullableTimestamp("i.started_at") + `, ` + nullableTimestamp("coalesce(i.resolved_at, i.deleted_at)") + `,
       ifNull(i.description, '')
FROM operational_incidents AS i FINAL
INNER JOIN operational_service_repository_mappings AS m FINAL ON i.org_id = m.org_id AND i.service_id = m.service_id AND m.is_active = 1
INNER JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
WHERE i.org_id = {org_id:String}` + sincePredicate(cursor, timestampExpr, "i.id") + orderBy(timestampExpr, "i.id")
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var incidentID, repoID, repoSlug, title, status, severity, description string
		var observedAt, startedAt, endedAt time.Time
		var isDeleted, hasStarted, hasEnded uint8
		if err := r.Scan(&incidentID, &repoID, &repoSlug, &title, &status, &severity, &observedAt, &isDeleted, &hasStarted, &startedAt, &hasEnded, &endedAt, &description); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		// An incident is valid while it is ongoing: from when it started
		// until it resolved (or was soft-deleted). An unresolved
		// incident has no end.
		validFrom, validTo := optionalTime(hasStarted, startedAt), optionalTime(hasEnded, endedAt)
		canonicalID := "incident:" + incidentID
		if isDeleted != 0 {
			tombstone := contractsv1.ContextFabricProjectionTombstone{
				Kind: "incident", CanonicalID: canonicalID, Reason: "source_deleted", EffectiveAt: observedAt, SourceVersion: ClickHouseSourceVersion,
			}
			return []candidate{{observedAt: observedAt, sortKey: incidentID, tombstone: &tombstone}}, nil
		}
		label := strings.TrimSpace(title)
		if label == "" {
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
		// CHAOS-3833 (embed-text spec §2): only the description HEAD
		// (first 800 runes) is persisted. 0% populated live today, but the
		// column is the natural payload when a real incident provider
		// ships; it is body-class text and follows the §3 provider-
		// locality gate at composition time.
		setStringProperty(properties, "description", description, 800)
		evidenceRefID := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityIncident, incidentID)
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{evidenceRefID}, ObservedAt: observedAt,
			ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: incidentID, entity: &entity},
			belongsToRepository(subject, repoSlug, repoID, observedAt, evidenceRefID, incidentID, validFrom, validTo),
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
// queryWorkItemDependencies mints work_item.v2 endpoint references (design
// brief D-4/§1.3) AND, since S2b, closes §1.5 (P5) for the one endpoint
// that can genuinely fail to resolve: a row whose target_work_item_id does
// not name a real work_items row (see this function's doc comment on
// cross-system PR references) mints a NON-AUTHORITATIVE work_item_ref
// stub subject and a relationship.v2 edge to it, rather than being
// omitted -- an honest "we know this dependency exists, not what it
// resolves to" placeholder, never a dangling reference to a work_item.v2
// node that will never exist.
//
// Healing: whenever this SAME source row's target LATER resolves (a
// later sync sees a real work_items row for target_work_item_id), the
// resolved-edge branch below UNCONDITIONALLY derives what the ref-form
// ids WOULD have been (the same deterministic DeriveWorkItemRef/
// DeriveRelationship calls, from the SAME row data) and emits a
// ProjectionTombstone for both the ref-form edge and the ref-form stub
// node, in the SAME batch as the resolved edge. This is idempotent
// whether or not the ref-form was ever actually minted (applyTombstone's
// edge/node deletes are no-ops against a key that was never written), so
// no cross-row bookkeeping is needed to know WHETHER healing is
// "necessary" -- design brief §1.5.
//
// Cross-row bookkeeping IS needed for one narrow reason: the SAME
// unresolved target can be the To of more than one row within a single
// page (CHAOS-3779's own two-relationship-type case -- "blocks" AND
// "relates_to" between the identical (source, target) pair, or two
// DIFFERENT sources depending on the same target). The node tombstone's
// CanonicalID depends only on the target's raw id, never on source or
// type, so two such rows resolving in the same page would derive the
// IDENTICAL node tombstone twice -- which
// ContextFabricProjectionBatch.Validate() rejects outright ("tombstone
// kind and canonical ID must be unique within a batch"). seenNodeTombstones
// (a plain local map, fresh on every call -- this function is invoked
// once per tick/page, never itself a persistent closure) dedupes exactly
// that, and only that: the EDGE tombstone is always per-row-unique
// already (it is keyed by source+target+type together, the row's own
// identity) and is never deduped.
//
// queryWorkItemHierarchy, by contrast, does NOT need any of this: its
// parent join is INNER (see that function's own doc comment), so an
// unresolved parent can never reach a candidate row in the first place --
// there is no ref-stub case to close there.
func queryWorkItemDependencies(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	seenNodeTombstones := make(map[string]struct{})
	const relationshipTypeExpr = "ifNull(d.relationship_type, 'related_to')"
	const rowKey = "concat(toString(w.repo_id), ':', d.source_work_item_id, ':', d.target_work_item_id, ':', " + relationshipTypeExpr + ")"
	statement := `SELECT d.source_work_item_id, d.target_work_item_id, ` + relationshipTypeExpr + `, toString(w.repo_id), ifNull(r.repo, ''), d.last_synced,
       w.created_at, ` + nullableTimestamp("coalesce(w.completed_at, w.closed_at)") + `,
       ` + nullableTimestamp("t.created_at") + `, ` + nullableTimestamp("coalesce(t.completed_at, t.closed_at)") + `, toString(t.repo_id)
FROM work_item_dependencies AS d FINAL
INNER JOIN work_items AS w FINAL ON w.org_id = d.org_id AND w.work_item_id = d.source_work_item_id
LEFT JOIN work_items AS t FINAL ON t.org_id = d.org_id AND t.work_item_id = d.target_work_item_id
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE d.org_id = {org_id:String}` + sincePredicate(cursor, "d.last_synced", rowKey) + orderBy("d.last_synced", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var sourceID, targetID, relationshipType, repoID, repoSlug, targetRepoID string
		var observedAt, sourceCreatedAt, sourceEndedAt, targetCreatedAt, targetEndedAt time.Time
		var sourceHasEnded, targetHasCreated, targetHasEnded uint8
		if err := r.Scan(&sourceID, &targetID, &relationshipType, &repoID, &repoSlug, &observedAt,
			&sourceCreatedAt, &sourceHasEnded, &sourceEndedAt,
			&targetHasCreated, &targetCreatedAt, &targetHasEnded, &targetEndedAt, &targetRepoID); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		rowSortKey := repoID + ":" + sourceID + ":" + targetID + ":" + relationshipType
		sourceCanonicalID, sourceOmitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, sourceID}, nil)
		if err != nil {
			return nil, err
		}
		if sourceOmitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		sourceValidFrom, sourceValidTo := requiredTime(sourceCreatedAt), optionalTime(sourceHasEnded, sourceEndedAt)
		sourceSubject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: sourceCanonicalID, Label: sourceID}
		evidenceRefID := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItemDependency, repoID+":"+sourceID+":"+targetID+":"+relationshipType)

		// The target join is LEFT: a target_work_item_id is not
		// guaranteed to name a work item at all (see this function's doc
		// comment on cross-system PR references). targetHasCreated is
		// exactly "did the target resolve" -- see this function's top doc
		// comment for the §1.5 work_item_ref stub this branch mints
		// instead of omitting the row.
		if targetHasCreated == 0 {
			refID, refOmitted := identity.DeriveWorkItemRef(targetID, nil)
			if refOmitted {
				return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
			}
			refSubject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItemRef, CanonicalID: refID, Label: targetID}
			// The stub's own window is the SOURCE row's window alone
			// (edgeValidity with nil target bounds reduces to exactly
			// that, laterTime/earlierTime's own nil-is-unbounded
			// convention) -- nothing in this row says anything about
			// the unresolved target's own lifecycle, so asserting more
			// would be a guess this producer's own discipline forbids.
			stubValidFrom, stubValidTo := edgeValidity(sourceValidFrom, sourceValidTo, nil, nil)
			stubEntity := contractsv1.ContextFabricEntityProjection{
				Subject: refSubject,
				// Repo-scoped to the SOURCE row's own repo, not the
				// target's (unknown) -- the stub is reachable only
				// through this source work item's edge, so it can never
				// be visible more broadly than the row that revealed it.
				Authorization:  workItemAuthorization(repoID, repoSlug),
				EvidenceRefIDs: []string{evidenceRefID},
				ObservedAt:     observedAt, ValidFrom: stubValidFrom, ValidTo: stubValidTo, SourceVersion: ClickHouseSourceVersion,
			}
			// CHAOS-4874: translate the source spelling before it becomes a
			// wire type. An inverted spelling (BLOCKED_BY) emits its
			// vocabulary member with the endpoints exchanged, so it converges
			// on the same edge as the equivalent forward row; anything the
			// vocabulary does not know passes through and is quarantined
			// per-item rather than rejecting the whole batch.
			refType, refSwap, refSpelling := dependencyRelationshipType(relationshipType)
			refFrom, refTo := orientDependencyEndpoints(sourceSubject, refSubject, refSwap)
			refRelationshipID := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, refFrom.CanonicalID, refTo.CanonicalID, refSpelling)
			refRelationship := contractsv1.ContextFabricRelationshipProjection{
				RelationshipID: refRelationshipID, Type: refType,
				From: refFrom, To: refTo,
				Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
				Authorization: workItemAuthorization(repoID, repoSlug), EvidenceRefIDs: []string{evidenceRefID},
				ObservedAt: observedAt, ValidFrom: stubValidFrom, ValidTo: stubValidTo, SourceVersion: ClickHouseSourceVersion,
			}
			return []candidate{
				// The stub exists only to be refRelationship's endpoint, so
				// it must not outlive it: if the edge is quarantined the
				// stub is an unreachable orphan node, not a partial success.
				{observedAt: observedAt, sortKey: rowSortKey, entity: &stubEntity, supports: refRelationshipID},
				{observedAt: observedAt, sortKey: rowSortKey, relationship: &refRelationship},
			}, nil
		}

		targetCanonicalID, targetOmitted, err := identity.Derive(identity.KindWorkItem, []string{targetRepoID, targetID}, nil)
		if err != nil {
			return nil, err
		}
		if targetOmitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		// work_item_dependencies carries only last_synced -- no interval
		// of its own -- so the edge's window is its endpoints'
		// intersection (CHAOS-3781 round-1 F5). F5 fixed the earlier
		// source-only version, which asserted the edge was valid while
		// the target did not yet exist, and made the window depend on
		// which endpoint happened to be joined rather than on the data.
		validFrom, validTo := edgeValidity(
			sourceValidFrom, sourceValidTo,
			optionalTime(targetHasCreated, targetCreatedAt), optionalTime(targetHasEnded, targetEndedAt))
		// CHAOS-4874: see the ref-form branch above for why the source
		// spelling is translated here rather than cast straight through.
		edgeType, edgeSwap, edgeSpelling := dependencyRelationshipType(relationshipType)
		targetSubject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: targetCanonicalID, Label: targetID}
		edgeFrom, edgeTo := orientDependencyEndpoints(sourceSubject, targetSubject, edgeSwap)
		relationshipID := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, edgeFrom.CanonicalID, edgeTo.CanonicalID, edgeSpelling)
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: relationshipID, Type: edgeType,
			From:       edgeFrom,
			To:         edgeTo,
			Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization: workItemAuthorization(repoID, repoSlug), EvidenceRefIDs: []string{evidenceRefID},
			ObservedAt: observedAt, ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
		}
		candidates := []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}

		// §1.5 tombstone healing: derive what the ref-form ids WOULD have
		// been from this SAME row's own data and tombstone both,
		// unconditionally -- idempotent no-ops if the ref-form was never
		// actually minted (applyTombstone's edge/node deletes match zero
		// rows against a key that was never written). No cross-row state
		// is needed to know whether healing is "necessary".
		if refID, refOmitted := identity.DeriveWorkItemRef(targetID, nil); !refOmitted {
			// Must mint the SAME id the ref-form branch above would have,
			// including the CHAOS-4874 translation -- a healing tombstone
			// derived under a different spelling or endpoint order would
			// match nothing and silently leave the stale edge behind.
			healFrom, healTo := orientDependencyEndpoints(
				sourceSubject,
				contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItemRef, CanonicalID: refID},
				edgeSwap)
			refRelationshipID := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, healFrom.CanonicalID, healTo.CanonicalID, edgeSpelling)
			edgeTombstone := contractsv1.ContextFabricProjectionTombstone{
				Kind: "relationship", CanonicalID: refRelationshipID,
				Reason:      "work_item target resolved: healing the ref-form work_item_dependency edge",
				EffectiveAt: observedAt, SourceVersion: ClickHouseSourceVersion,
			}
			candidates = append(candidates, candidate{observedAt: observedAt, sortKey: rowSortKey, tombstone: &edgeTombstone})
			// seenNodeTombstones: see this function's own top doc comment
			// -- the node tombstone depends only on refID, so more than
			// one row in this page resolving the SAME target must emit it
			// exactly once, never once per row.
			if _, seen := seenNodeTombstones[refID]; !seen {
				seenNodeTombstones[refID] = struct{}{}
				nodeTombstone := contractsv1.ContextFabricProjectionTombstone{
					Kind: "work_item_ref", CanonicalID: refID,
					Reason:      "work_item target resolved: healing the ref-form stub node if orphaned",
					EffectiveAt: observedAt, SourceVersion: ClickHouseSourceVersion,
				}
				candidates = append(candidates, candidate{observedAt: observedAt, sortKey: rowSortKey, tombstone: &nodeTombstone})
			}
		}
		return candidates, nil
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
	const rowKey = "concat(toString(c.repo_id), ':', c.work_item_id)"
	statement := `SELECT c.work_item_id, c.parent_id, toString(c.repo_id), ifNull(r.repo, ''), c.updated_at,
       c.created_at, ` + nullableTimestamp("coalesce(c.completed_at, c.closed_at)") + `,
       p.created_at, ` + nullableTimestamp("coalesce(p.completed_at, p.closed_at)") + `, toString(p.repo_id)
FROM work_items AS c FINAL
INNER JOIN work_items AS p FINAL ON p.org_id = c.org_id AND p.work_item_id = c.parent_id
LEFT JOIN repos AS r FINAL ON r.id = c.repo_id AND r.org_id = c.org_id
WHERE c.org_id = {org_id:String} AND c.parent_id != '' AND c.parent_id != c.work_item_id` + sincePredicate(cursor, "c.updated_at", rowKey) + orderBy("c.updated_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var childID, parentID, repoID, repoSlug, parentRepoID string
		var observedAt, childCreatedAt, childEndedAt, parentCreatedAt, parentEndedAt time.Time
		var childHasEnded, parentHasEnded uint8
		if err := r.Scan(&childID, &parentID, &repoID, &repoSlug, &observedAt,
			&childCreatedAt, &childHasEnded, &childEndedAt, &parentCreatedAt, &parentHasEnded, &parentEndedAt, &parentRepoID); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		rowSortKey := repoID + ":" + childID
		childCanonicalID, childOmitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, childID}, nil)
		if err != nil {
			return nil, err
		}
		parentCanonicalID, parentOmitted, err := identity.Derive(identity.KindWorkItem, []string{parentRepoID, parentID}, nil)
		if err != nil {
			return nil, err
		}
		if childOmitted || parentOmitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		// Both endpoints are joined here, so the PART_OF edge gets the
		// true intersection of the two work items' windows: a child is
		// part of its parent only while both exist.
		validFrom, validTo := edgeValidity(
			requiredTime(childCreatedAt), optionalTime(childHasEnded, childEndedAt),
			requiredTime(parentCreatedAt), optionalTime(parentHasEnded, parentEndedAt))
		// CHAOS-3898 §1.5 P1-2 fix-forward (codex retroactive review of
		// #151/#152, chris-verified): this producer was left on the OLD
		// endpoint-independent id scheme when S2b converted
		// queryWorkItemDependencies -- RelationshipFamilyWorkItemHierarchy
		// was defined (identity/relationship.go) but never actually
		// consumed anywhere. Unlike the dependency edge, hierarchy's INNER
		// JOIN to the parent guarantees BOTH endpoints are always resolved
		// real work_item.v2 nodes here (no unresolved-ref stub case), so
		// this needs only the id-scheme conversion, no tombstone healing.
		relationshipID := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemHierarchy, childCanonicalID, parentCanonicalID, string(contractsv1.ContextFabricRelationshipPartOf))
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: relationshipID, Type: contractsv1.ContextFabricRelationshipPartOf,
			From:       contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: childCanonicalID, Label: childID},
			To:         contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: parentCanonicalID, Label: parentID},
			Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization: workItemAuthorization(repoID, repoSlug), EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItemHierarchy, repoID+":"+childID+":"+parentID)},
			ObservedAt: observedAt, ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	})
}

// queryDeploymentIncidentEdges derives its validity window from its two
// ENDPOINTS (CHAOS-3781 round-1 F4), because
// work_graph_deployment_incident_edges carries no interval of its own --
// only observed_at and computed_at, which are derivation stamps, not the
// span over which the correlation was true.
//
// The semantic interval IS knowable from the source, so leaving the edge
// unbounded would have been the wrong kind of honest: an unbounded edge is
// admitted at EVERY requested time, so a deployment would have appeared
// correlated with an incident years before either happened. Both endpoint
// tables are joined LEFT, so an edge whose endpoint row cannot be resolved
// still projects -- with the window absent, which is the admit-count-label
// path, and the correct answer when the interval genuinely is unknowable.
//
// CHAOS-3898 D-7: the deployments join used to key on (org_id,
// deployment_id) only. deployment_id is not globally unique -- two
// different repos can both run a deployment_id that collides -- so the
// join could pick up the WRONG repo's deployment row: either a
// duplicate-RelationshipID wedge (two edges resolving to endpoint data
// that implies the same RelationshipID) or, for the org-isolation-safe
// direction, a silently wrong deploy/finish window borrowed from an
// unrelated repo's deployment. The join now also equates d.repo_id =
// e.repo_id, matching e's own already-qualified join to repos (the line
// above). The chaos3898_d7_join_fix_test.go regression test pins the SQL
// text so this predicate cannot be dropped by a future edit without a test
// failing. operational_incidents carries no repo_id in its schema (see
// devhealthschema.EngineFull["operational_incidents"]), so the incidents
// join is unaffected -- this fix is scoped exactly to the join the design
// brief's defect inventory (v4.1 §0, D-7) names.
func queryDeploymentIncidentEdges(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	statement := `SELECT e.edge_id, e.deployment_id, e.incident_id, r.repo, e.observed_at,
       ` + nullableTimestamp("coalesce(d.started_at, d.deployed_at)") + `, ` + nullableTimestamp("d.finished_at") + `,
       ` + nullableTimestamp("i.started_at") + `, ` + nullableTimestamp("coalesce(i.resolved_at, i.deleted_at)") + `, toString(e.repo_id)
FROM work_graph_deployment_incident_edges AS e FINAL
INNER JOIN repos AS r FINAL ON r.id = e.repo_id AND r.org_id = toString(e.org_id)
LEFT JOIN deployments AS d FINAL ON d.org_id = toString(e.org_id) AND d.deployment_id = e.deployment_id AND d.repo_id = e.repo_id
LEFT JOIN operational_incidents AS i FINAL ON i.org_id = toString(e.org_id) AND i.id = e.incident_id
WHERE toString(e.org_id) = {org_id:String} AND e.deployment_id != '' AND e.incident_id NOT IN ('', 'none')` + sincePredicate(cursor, "e.observed_at", "e.edge_id") + orderBy("e.observed_at", "e.edge_id")
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var edgeID, deploymentID, incidentID, repoSlug, repoID string
		var observedAt, deployStartedAt, deployFinishedAt, incidentStartedAt, incidentEndedAt time.Time
		var hasDeployStart, hasDeployEnd, hasIncidentStart, hasIncidentEnd uint8
		if err := r.Scan(&edgeID, &deploymentID, &incidentID, &repoSlug, &observedAt,
			&hasDeployStart, &deployStartedAt, &hasDeployEnd, &deployFinishedAt,
			&hasIncidentStart, &incidentStartedAt, &hasIncidentEnd, &incidentEndedAt, &repoID); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		// The correlation is valid only while BOTH ends are -- the same
		// endpoint-intersection rule every other edge here uses.
		validFrom, validTo := edgeValidity(
			optionalTime(hasDeployStart, deployStartedAt), optionalTime(hasDeployEnd, deployFinishedAt),
			optionalTime(hasIncidentStart, incidentStartedAt), optionalTime(hasIncidentEnd, incidentEndedAt))
		relationshipID := "relationship:deployment_incident:" + edgeID
		// CHAOS-3898: the deployment endpoint now references
		// deployment.v2:<repo_id>:<enc(deployment_id)> (the SAME canonical
		// id queryDeployments mints for this row's own deployment, since
		// e.repo_id = e's already-qualified join to d.repo_id above proves
		// this edge's deployment_id belongs to THIS repo) -- a dangling
		// endpoint reference would otherwise point at a node that no
		// longer exists once deployment's canonical id format changes.
		deploymentCanonicalID, omitted, err := identity.Derive(identity.KindDeployment, []string{repoID, deploymentID}, nil)
		if err != nil {
			return nil, err
		}
		if omitted {
			return []candidate{progressCandidate(observedAt, edgeID)}, nil
		}
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: relationshipID, Type: "CORRELATED_WITH_INCIDENT",
			From:       contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectDeployment, CanonicalID: deploymentCanonicalID, Label: deploymentID},
			To:         contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectIncident, CanonicalID: "incident:" + incidentID, Label: incidentID},
			Derivation: contractsv1.ContextFabricDerivationRuleInferred, EpistemicStatus: contractsv1.ContextFabricEpistemicSourceAsserted,
			Authorization: repoAuthorization(repoSlug), EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityDeploymentIncident, edgeID)},
			ObservedAt: observedAt, ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: edgeID, relationship: &relationship}}, nil
	})
}

// queryPullRequestReviews scans git_pull_request_reviews.number into a
// uint32 (CHAOS-3789), the same UInt32-vs-int64 class as queryPullRequests
// above -- this table carries its own copy of the PR number, and the fix
// found by the schema-parity sweep this issue added.
//
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
	const rowKey = "concat(toString(r.repo_id), ':', toString(r.number), ':', r.review_id)"
	statement := `SELECT r.review_id, toString(r.repo_id), r.number, ifNull(r.state, ''), r.submitted_at, repo.repo,
       p.created_at, ` + nullableTimestamp("coalesce(p.merged_at, p.closed_at)") + `,
       ifNull(p.title, '')
FROM git_pull_request_reviews AS r FINAL
INNER JOIN git_pull_requests AS p FINAL ON r.repo_id = p.repo_id AND r.number = p.number AND r.org_id = p.org_id
INNER JOIN repos AS repo FINAL ON repo.id = r.repo_id AND repo.org_id = r.org_id
WHERE r.org_id = {org_id:String}` + sincePredicate(cursor, "r.submitted_at", rowKey) + orderBy("r.submitted_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var reviewID, repoID, state, repoSlug, pullRequestTitle string
		var rawNumber uint32
		var observedAt, pullRequestCreatedAt, pullRequestEndedAt time.Time
		var pullRequestHasEnded uint8
		if err := r.Scan(&reviewID, &repoID, &rawNumber, &state, &observedAt, &repoSlug, &pullRequestCreatedAt, &pullRequestHasEnded, &pullRequestEndedAt, &pullRequestTitle); err != nil {
			return nil, err
		}
		number := int64(rawNumber)
		observedAt = observedAt.UTC()
		numberStr := fmt.Sprint(number)
		canonicalID, omitted, err := identity.Derive(identity.KindPullRequestReview, []string{repoID, numberStr, reviewID}, nil)
		if err != nil {
			return nil, err
		}
		rowSortKey := repoID + ":" + numberStr + ":" + reviewID
		if omitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		// A review is a point event: it becomes true when it is
		// submitted and is never retracted, so its window is open-ended
		// from submitted_at. observedAt IS submitted_at for this query.
		validFrom, validTo := requiredTime(observedAt), (*time.Time)(nil)
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: canonicalID, Label: fmt.Sprintf("PR #%d review", number)}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if state != "" {
			properties["state"] = stringScalar(state)
		}
		// CHAOS-3833 (embed-text spec §2): the pull_request_review
		// template's fields. The producer already pays for the
		// git_pull_requests join, so the PR title is free -- it is what
		// turns 1,121 "PR #N review" near-clones into distinguishable
		// texts. reviewer stays unread (person PII, spec §3).
		properties["number"] = intScalar(number)
		setStringProperty(properties, "pr_title", pullRequestTitle, 0)
		setStringProperty(properties, "repo", repoSlug, 0)
		evidenceRefID := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityReview, repoID+":"+reviewID)
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{evidenceRefID}, ObservedAt: observedAt,
			ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
		}
		pullRequestID := fmt.Sprintf("pull_request:%s:%d", repoID, number)
		// The BELONGS_TO_PULL_REQUEST edge is valid only while BOTH ends
		// are, so it inherits the pull request's own window as well as
		// the review's -- a review submitted on a pull request that has
		// since merged stops being a live association when the pull
		// request ends.
		edgeValidFrom, edgeValidTo := edgeValidity(validFrom, validTo,
			requiredTime(pullRequestCreatedAt), optionalTime(pullRequestHasEnded, pullRequestEndedAt))
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: "relationship:belongs_to_pull_request:" + canonicalID, Type: "BELONGS_TO_PULL_REQUEST",
			From:       subject,
			To:         contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: pullRequestID, Label: pullRequestID},
			Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization: repoAuthorization(repoSlug), EvidenceRefIDs: []string{evidenceRefID},
			ObservedAt: observedAt, ValidFrom: edgeValidFrom, ValidTo: edgeValidTo, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: rowSortKey, entity: &entity},
			{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship},
		}, nil
	})
}

// queryCIRuns mints ci_pipeline_run.v2:<repo_id>:<enc(run_id)> (design brief
// D-1, §1.2), fixing the cross-repo collision a bare run_id canonical id
// carried: two different repos' CI runs sharing the same raw run_id used to
// collapse onto one canonical id (last-write-wins payload AND
// authorization). The pagination rowKey (D-6) carries the same repo_id
// qualifier for the identical reason, one level down: two different repos'
// rows sharing a run_id also tied at the SQL keyset-pagination tiebreaker,
// which could silently skip one of them at a page boundary.
func queryCIRuns(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const timestampExpr = "coalesce(c.finished_at, c.started_at)"
	const rowKey = "concat(toString(c.repo_id), ':', c.run_id)"
	statement := `SELECT c.run_id, toString(c.repo_id), ifNull(c.branch, ''), ifNull(c.status, ''), repo.repo, ` + timestampExpr + `,
       c.started_at, ` + nullableTimestamp("c.finished_at") + `,
       ifNull(c.pipeline_name, '')
FROM ci_pipeline_runs AS c FINAL
INNER JOIN repos AS repo FINAL ON repo.id = c.repo_id AND repo.org_id = c.org_id
WHERE c.org_id = {org_id:String}` + sincePredicate(cursor, timestampExpr, rowKey) + orderBy(timestampExpr, rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var runID, repoID, branch, status, repoSlug, pipelineName string
		var observedAt, startedAt, finishedAt time.Time
		var hasFinished uint8
		if err := r.Scan(&runID, &repoID, &branch, &status, &repoSlug, &observedAt, &startedAt, &hasFinished, &finishedAt, &pipelineName); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		canonicalID, omitted, err := identity.Derive(identity.KindCIPipelineRun, []string{repoID, runID}, nil)
		if err != nil {
			return nil, err
		}
		rowSortKey := repoID + ":" + runID
		if omitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		// A CI run is valid while it is executing; a run still in
		// progress has no recorded end.
		validFrom, validTo := requiredTime(startedAt), optionalTime(hasFinished, finishedAt)
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: canonicalID, Label: "CI " + runID}
		properties := map[string]contractsv1.ContextFabricScalarValue{}
		if status != "" {
			properties["status"] = stringScalar(status)
		}
		if branch != "" {
			properties["branch"] = stringScalar(branch)
		}
		// CHAOS-3833 (embed-text spec §2): the ci_pipeline_run template's
		// fields -- pipeline_name is populated on 78% of live rows and is
		// the only semantic text a CI run carries.
		setStringProperty(properties, "pipeline_name", pipelineName, 0)
		setStringProperty(properties, "repo", repoSlug, 0)
		evidenceRefID := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityCI, repoID+":"+runID)
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject, Properties: properties, Authorization: repoAuthorization(repoSlug),
			EvidenceRefIDs: []string{evidenceRefID}, ObservedAt: observedAt,
			ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{
			{observedAt: observedAt, sortKey: rowSortKey, entity: &entity},
			belongsToRepository(subject, repoSlug, repoID, observedAt, evidenceRefID, rowSortKey, validFrom, validTo),
		}, nil
	})
}
