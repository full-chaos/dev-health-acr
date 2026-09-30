package contextpacket

import (
	"context"
	"errors"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Source-row reads for Context Fabric evidence refs (CHAOS-6180).
//
// A Context Fabric evidence ref (acr:v1:<type>:<id>) names a Dev Health row.
// The hosted evidence route expands it to that row through these reads. They
// are the ONLY SQL of that expansion: the caller (contextfabric/sourcerow)
// decides the caller's repository grant in Go, between the repository lookup
// and the row read, and never builds a statement.
//
// Every read here takes the same text and the same bindings for every
// caller: no read carries the caller's grant. A restricted caller's refusal
// and an absent row therefore run the same statements with the same
// arguments, and only rows in a repository the caller may read are ever
// fetched.
//
// The row read reuses the packet catalog statement of the kind
// (SourceQueryCatalogV1), filtered to one catalog evidence id, so an
// expanded row has the same label, citation and provenance as the packet
// path gives it. Two kinds have no catalog statement (the catalog is also the
// packet path's full read set, so a statement cannot be added there without
// changing every context packet); their statements are
// SourceRowOnlyQueriesV1.

// SourceRowQueryVersionV1 versions the statements of this file.
const SourceRowQueryVersionV1 = "dev-health-source-rows.v1"

// maxSourceRowRepositories bounds the repositories one row may map to (an
// incident's services, a dependency's source work item). A row that maps to
// more is refused as saturated rather than served from a partial list.
const maxSourceRowRepositories = 64

// SourceRowOnlyQueriesV1 are the row statements of the kinds the packet
// catalog does not read. Each has the catalog's column shape and bindings.
var SourceRowOnlyQueriesV1 = []SourceQuery{
	{"work_item_hierarchy.v1", "work_items", EvidenceScopeRepo, standardColumns + ` SELECT concat('` + contractsv1.ContextFabricEvidenceRefPrefix + string(contractsv1.ContextFabricEvidenceEntityWorkItemHierarchy) + `:', c.work_item_id, ':', c.parent_id) evidence_ref_id, 'dev_health' system, 'work_item_hierarchy' entity_type, concat(c.work_item_id, ':', c.parent_id) entity_id, concat(c.work_item_id, ' part of ', c.parent_id) display_label, '' safe_uri, 'native' provenance, 1.0 confidence, concat('child=', c.work_item_id, ', parent=', c.parent_id) citation, c.updated_at observed_at FROM work_items AS c FINAL INNER JOIN work_items AS p FINAL ON p.org_id = c.org_id AND p.work_item_id = c.parent_id WHERE c.org_id = {org_id:String} AND c.repo_id = {repo_id:UUID} AND c.parent_id != '' AND c.parent_id != c.work_item_id )`},
	{"work_item_teams.v1", "work_items", EvidenceScopeRepo, standardColumns + ` SELECT concat('` + contractsv1.ContextFabricEvidenceRefPrefix + string(contractsv1.ContextFabricEvidenceEntityWorkItemTeam) + `:', a.work_item_id, ':', ifNull(a.team_id, '')) evidence_ref_id, 'dev_health' system, 'work_item_team' entity_type, concat(a.work_item_id, ':', ifNull(a.team_id, '')) entity_id, concat(a.work_item_id, ' owned by team ', ifNull(a.team_id, '')) display_label, '' safe_uri, 'derived' provenance, 1.0 confidence, concat('source=', toString(a.source), ', confidence=', toString(a.confidence)) citation, a.computed_at observed_at FROM work_item_team_attributions AS a FINAL INNER JOIN (SELECT work_item_id, repo_id, org_id FROM work_items FINAL WHERE org_id = {org_id:String}) AS w ON w.work_item_id = a.work_item_id AND w.org_id = a.org_id INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = ifNull(a.team_id, '') WHERE a.org_id = {org_id:String} AND w.repo_id = {repo_id:UUID} AND a.is_primary = 1 AND ifNull(a.team_id, '') != '' )`},
}

// RepositoryByIDQueryV1 looks one repository of the organization up by id.
const RepositoryByIDQueryV1 = `SELECT toString(id), repo FROM repos FINAL WHERE org_id = {org_id:String} AND id = {repo_id:UUID} ORDER BY repo ASC LIMIT 2`

// SourceRowDiscovery names a statement that finds the repositories a row
// maps to, for a kind whose evidence id carries no repository.
type SourceRowDiscovery string

const (
	// SourceRowDiscoveryIncident: the repositories an incident's service
	// maps to (the incidents.v1 join).
	SourceRowDiscoveryIncident SourceRowDiscovery = "incident_repositories"
	// SourceRowDiscoveryDeploymentIncident: the repository of a
	// deployment-incident edge.
	SourceRowDiscoveryDeploymentIncident SourceRowDiscovery = "deployment_incident_repositories"
	// SourceRowDiscoveryDependency: the repository of a dependency's source
	// work item, the dependency named by the catalog's evidence id
	// <source>:<target>:<relation key> (the work_item_dependencies.v1 join).
	SourceRowDiscoveryDependency SourceRowDiscovery = "dependency_repositories"
)

var sourceRowDiscoveryStatements = map[SourceRowDiscovery]string{
	SourceRowDiscoveryIncident:           `SELECT DISTINCT toString(r.id), r.repo FROM operational_incidents AS i FINAL INNER JOIN operational_service_repository_mappings AS m FINAL ON i.org_id = m.org_id AND i.service_id = m.service_id INNER JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id WHERE i.org_id = {org_id:String} AND m.org_id = {org_id:String} AND i.id = {entity_id:String} AND i.is_deleted = 0 AND m.is_active = 1 ORDER BY r.repo ASC LIMIT 65`,
	SourceRowDiscoveryDeploymentIncident: `SELECT DISTINCT toString(r.id), r.repo FROM work_graph_deployment_incident_edges AS e FINAL INNER JOIN repos AS r FINAL ON r.id = e.repo_id AND r.org_id = toString(e.org_id) WHERE toString(e.org_id) = {org_id:String} AND e.edge_id = {entity_id:String} ORDER BY r.repo ASC LIMIT 65`,
	SourceRowDiscoveryDependency:         `SELECT DISTINCT toString(r.id), r.repo FROM work_item_dependencies AS d FINAL INNER JOIN work_items AS w FINAL ON d.source_work_item_id = w.work_item_id INNER JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id WHERE d.org_id = {org_id:String} AND w.org_id = {org_id:String} AND concat(d.source_work_item_id, ':', d.target_work_item_id, ':', ` + dependencyRelationKeySQL + `) = {entity_id:String} ORDER BY r.repo ASC LIMIT 65`,
}

// SourceRowDiscoveryStatement returns the statement of one discovery, and
// false for a name this file does not declare.
func SourceRowDiscoveryStatement(discovery SourceRowDiscovery) (string, bool) {
	statement, ok := sourceRowDiscoveryStatements[discovery]
	return statement, ok
}

// DependencyLocatorQueryV1 maps the graph producer's dependency id
// (<repo>:<source>:<target>:<relationship type>, devhealthsource) inside one
// repository to the catalog's evidence id of the same row
// (acr:v1:work-item-dependency:<source>:<target>:<relation key>). It matches
// the whole key by concatenation, so work item ids that hold ':' never need
// splitting.
var DependencyLocatorQueryV1 = `SELECT DISTINCT concat('` + contractsv1.ContextFabricEvidenceRefPrefix + string(contractsv1.ContextFabricEvidenceEntityWorkItemDependency) + `:', d.source_work_item_id, ':', d.target_work_item_id, ':', ` + dependencyRelationKeySQL + `) FROM work_item_dependencies AS d FINAL INNER JOIN work_items AS w FINAL ON d.source_work_item_id = w.work_item_id WHERE d.org_id = {org_id:String} AND w.org_id = {org_id:String} AND w.repo_id = {repo_id:UUID} AND concat(d.source_work_item_id, ':', d.target_work_item_id, ':', ifNull(d.relationship_type, 'related_to')) = {entity_key:String} ORDER BY 1 ASC LIMIT 3`

// SourceRowRead is one row read: the statement (a catalog or source-row-only
// query id), the catalog evidence id of the row, and an optional task_ref
// push-down for statements that filter on it.
type SourceRowRead struct {
	QueryID string
	Locator string
	TaskRef string
}

// SourceRowQueryIDs lists every statement id ResolveSourceRow accepts.
func SourceRowQueryIDs() []string {
	ids := make([]string, 0, len(SourceQueryCatalogV1)+len(SourceRowOnlyQueriesV1))
	for _, query := range SourceQueryCatalogV1 {
		ids = append(ids, query.ID)
	}
	for _, query := range SourceRowOnlyQueriesV1 {
		ids = append(ids, query.ID)
	}
	return ids
}

func sourceRowQuery(id string) *SourceQuery {
	if query := catalogSourceQuery(id); query != nil {
		return query
	}
	for index := range SourceRowOnlyQueriesV1 {
		if SourceRowOnlyQueriesV1[index].ID == id {
			return &SourceRowOnlyQueriesV1[index]
		}
	}
	return nil
}

// ErrUnknownSourceRowQuery: a read named a statement this package does not
// declare. It is a programming error, never a not-found.
var ErrUnknownSourceRowQuery = errors.New("contextpacket: unknown source-row statement")

// RepositoryByID returns the organization's repository with this id (at
// most one; two rows are returned as they are so the caller can refuse).
func (r *CatalogClickHouseRows) RepositoryByID(ctx context.Context, orgID, repoID string) (_ []contractsv1.ResolvedScope, err error) {
	completeObservation := beginStoreQueryObservation(ctx, r.assemblyObserver(), StoreOperationEvidence)
	defer func() { completeObservation(err) }()
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("%w: no clickhouse client", ErrUnknownSourceRowQuery)
	}
	return r.queryRepositories(ctx, RepositoryByIDQueryV1, []ClickHouseBinding{{Name: "org_id", Value: orgID}, {Name: "repo_id", Value: repoID}})
}

// SourceRowRepositories returns the repositories a row maps to, ordered by
// slug, up to maxSourceRowRepositories+1 (the caller refuses a saturated
// list).
func (r *CatalogClickHouseRows) SourceRowRepositories(ctx context.Context, orgID string, discovery SourceRowDiscovery, entityID string) (_ []contractsv1.ResolvedScope, err error) {
	completeObservation := beginStoreQueryObservation(ctx, r.assemblyObserver(), StoreOperationEvidence)
	defer func() { completeObservation(err) }()
	statement, ok := sourceRowDiscoveryStatements[discovery]
	if r == nil || r.client == nil || !ok {
		return nil, fmt.Errorf("%w: discovery %q", ErrUnknownSourceRowQuery, discovery)
	}
	return r.queryRepositories(ctx, statement, []ClickHouseBinding{{Name: "org_id", Value: orgID}, {Name: "entity_id", Value: entityID}})
}

// MaxSourceRowRepositories is maxSourceRowRepositories, for the caller's
// saturation check.
func MaxSourceRowRepositories() int { return maxSourceRowRepositories }

// DependencyLocators returns the catalog evidence ids of the dependency rows
// of one repository whose graph-producer key is entityKey.
func (r *CatalogClickHouseRows) DependencyLocators(ctx context.Context, orgID, repoID, entityKey string) (_ []string, err error) {
	completeObservation := beginStoreQueryObservation(ctx, r.assemblyObserver(), StoreOperationEvidence)
	defer func() { completeObservation(err) }()
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("%w: no clickhouse client", ErrUnknownSourceRowQuery)
	}
	rows, err := r.client.Query(ctx, DependencyLocatorQueryV1, []ClickHouseBinding{{Name: "org_id", Value: orgID}, {Name: "repo_id", Value: repoID}, {Name: "entity_key", Value: entityKey}})
	if err != nil {
		return nil, fmt.Errorf("resolve dependency locator: %w", err)
	}
	defer rows.Close()
	locators := []string{}
	for rows.Next() {
		var locator string
		if err := rows.Scan(&locator); err != nil {
			return nil, fmt.Errorf("scan dependency locator: %w", err)
		}
		locators = append(locators, locator)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dependency locator: %w", err)
	}
	return locators, nil
}

// ResolveSourceRow reads the rows of one repository whose catalog evidence id
// is read.Locator, up to two (the caller refuses two as ambiguous). Every
// other plan field is at its zero value: no branch, no as-of, no window, so
// the read sees the row as it is now.
func (r *CatalogClickHouseRows) ResolveSourceRow(ctx context.Context, orgID string, scope contractsv1.ResolvedScope, read SourceRowRead) (_ []EvidenceReference, err error) {
	completeObservation := beginStoreQueryObservation(ctx, r.assemblyObserver(), StoreOperationEvidence)
	defer func() { completeObservation(err) }()
	query := sourceRowQuery(read.QueryID)
	if r == nil || r.client == nil || query == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSourceRowQuery, read.QueryID)
	}
	if read.Locator == "" {
		return nil, storage.ErrNotFound
	}
	plan := ReadPlan{OrgID: orgID, RepoID: scope.RepoID, RepoSlug: scope.RepoSlug, TaskRef: read.TaskRef}
	bindings := append(plan.Bindings(), ClickHouseBinding{Name: "evidence_locator", Value: read.Locator})
	statement := `SELECT * FROM (` + query.Statement + `) WHERE evidence_ref_id = {evidence_locator:String} LIMIT 2`
	return r.queryEvidenceReferences(ctx, scope.RepoSlug, query.ID, statement, bindings, 3)
}

func (r *CatalogClickHouseRows) queryRepositories(ctx context.Context, statement string, bindings []ClickHouseBinding) ([]contractsv1.ResolvedScope, error) {
	rows, err := r.client.Query(ctx, statement, bindings)
	if err != nil {
		return nil, fmt.Errorf("resolve source-row repositories: %w", err)
	}
	defer rows.Close()
	result := []contractsv1.ResolvedScope{}
	for rows.Next() {
		var repoID, slug string
		if err := rows.Scan(&repoID, &slug); err != nil {
			return nil, fmt.Errorf("scan source-row repository: %w", err)
		}
		result = append(result, contractsv1.ResolvedScope{RepoID: repoID, RepoSlug: slug, Resolution: contractsv1.ScopeRepoFallback, FallbackReasons: []string{}})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source-row repositories: %w", err)
	}
	return result, nil
}

func (r *CatalogClickHouseRows) assemblyObserver() AssemblyObserver {
	if r == nil {
		return nil
	}
	return r.observer
}
