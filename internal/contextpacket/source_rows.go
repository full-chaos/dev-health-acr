package contextpacket

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
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
// path gives it. The four ".v2" kinds (CHAOS-7252) have no catalog
// statement -- the catalog is also the packet path's read set, and its
// locators live inside ev2 handles clients hold -- so their statements are
// SourceRowOnlyQueriesV2. Each filters on the decoded components of the ref
// (bound as component_1..n) AND on evidence_ref_id = the ref, where
// evidence_ref_id is rendered by evidenceref.SQL: the SQL spelling of the one
// grammar every producer mints through, so the row the read returns is the
// row whose components the ref names, never a row whose unescaped join
// happens to read the same.

// SourceRowQueryVersion versions the statements of this file (v2,
// CHAOS-7252: the ".v2" statements and the work-item discovery).
const SourceRowQueryVersion = "dev-health-source-rows.v2"

// maxSourceRowRepositories bounds the repositories one row may map to (an
// incident's services). A row that maps to more is refused as saturated
// rather than served from a partial list.
const maxSourceRowRepositories = 64

// OrganizationRowQuery is the row statement of an organization-level kind
// (CHAOS-7227: teams and projects, authorized by ownership). It returns the
// catalog's ten columns and then KeyColumns: the row's OWN identity columns,
// from which the caller recomputes the row's canonical subject and requires
// it to equal the subject the gate authorized (#742 r1 P1: the locator
// concatenates provider and id, so a string match alone can reach a row of a
// different subject).
type OrganizationRowQuery struct {
	SourceQuery
	KeyColumns []string
}

// organizationProviderPattern is the colon-free provider grammar a project
// ref splits on (sourcerow providerPattern); a row whose provider breaks it
// is never read as a source row.
const organizationProviderPattern = `^[a-z][a-z0-9_-]*$`

func organizationColumns(keys ...string) string {
	return `SELECT evidence_ref_id, system, entity_type, entity_id, display_label, safe_uri, provenance, toFloat64(confidence) confidence, citation, observed_at, ` + strings.Join(keys, ", ") + ` FROM (`
}

// OrganizationRowQueriesV1 are the organization-level row statements. They
// are not in the packet catalog, whose statements are repository-scoped and
// are the packet path's full read set; they bind the organization only.
var OrganizationRowQueriesV1 = []OrganizationRowQuery{
	{SourceQuery{"teams.v1", "teams", EvidenceScopeRepo, organizationColumns("key_id") + ` SELECT concat('` + contractsv1.ContextFabricEvidenceRefPrefix + string(contractsv1.ContextFabricEvidenceEntityTeam) + `:', t.id) evidence_ref_id, 'dev_health' system, 'team' entity_type, t.id entity_id, if(lengthUTF8(t.name) BETWEEN 1 AND 1000, t.name, concat('team ', t.id)) display_label, '' safe_uri, 'native' provenance, 1.0 confidence, concat('provider=', t.provider, ', active=', toString(t.is_active)) citation, t.updated_at observed_at, t.id key_id FROM teams AS t FINAL WHERE t.org_id = {org_id:String} )`}, []string{"id"}},
	{SourceQuery{"projects.v1", "projects", EvidenceScopeRepo, organizationColumns("key_provider", "key_id") + ` SELECT concat('` + contractsv1.ContextFabricEvidenceRefPrefix + string(contractsv1.ContextFabricEvidenceEntityProject) + `:', p.provider, ':', p.id) evidence_ref_id, 'dev_health' system, 'project' entity_type, concat(p.provider, ':', p.id) entity_id, if(lengthUTF8(p.name) BETWEEN 1 AND 1000, p.name, concat('project ', p.id)) display_label, '' safe_uri, 'native' provenance, 1.0 confidence, concat('state=', toString(p.state), ', active=', toString(p.is_active)) citation, p.updated_at observed_at, p.provider key_provider, p.id key_id FROM projects AS p FINAL WHERE p.org_id = {org_id:String} AND match(p.provider, '` + organizationProviderPattern + `') )`}, []string{"provider", "id"}},
}

// RepositoryByIDQueryV1 looks one repository of the organization up by id.
const RepositoryByIDQueryV1 = `SELECT toString(id), repo FROM repos FINAL WHERE org_id = {org_id:String} AND id = {repo_id:UUID} ORDER BY repo ASC LIMIT 2`

// SourceRowDiscovery names a statement that finds the repositories a row
// maps to, for a kind whose evidence id carries no repository.
type SourceRowDiscovery string

const (
	// SourceRowDiscoveryIncident: the repositories an incident's service
	// maps to NOW (the incidents.v1 join and its mapping validity window,
	// so an expired mapping never takes a place in the bounded list).
	SourceRowDiscoveryIncident SourceRowDiscovery = "incident_repositories"
	// SourceRowDiscoveryWorkItem: every repository a work item id has a
	// work_items row in (the key is (org_id, repo_id, work_item_id), so one
	// id can live in several), with the slug "" for a repository id repos
	// does not hold (the zero UUID of a Linear item). A slug-less repository
	// holds repo-less work items: the resolver decides each one with the
	// scope expander's repo-less rule (organization grant, or a project
	// ownership or native pull-request link path to a granted repository).
	SourceRowDiscoveryWorkItem SourceRowDiscovery = "work_item_repositories"
)

var sourceRowDiscoveryStatements = map[SourceRowDiscovery]string{
	SourceRowDiscoveryWorkItem: `SELECT DISTINCT toString(w.repo_id), ifNull(r.repo, '') FROM work_items AS w FINAL LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id WHERE w.org_id = {org_id:String} AND w.work_item_id = {entity_id:String} ORDER BY 2 ASC, 1 ASC LIMIT 65`,
	SourceRowDiscoveryIncident: `SELECT DISTINCT toString(r.id), r.repo FROM operational_incidents AS i FINAL INNER JOIN operational_service_repository_mappings AS m FINAL ON i.org_id = m.org_id AND i.service_id = m.service_id INNER JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id WHERE i.org_id = {org_id:String} AND m.org_id = {org_id:String} AND i.id = {entity_id:String} AND i.is_deleted = 0 AND m.is_active = 1 AND m.valid_from <= now64(6) AND (m.valid_to IS NULL OR m.valid_to > now64(6)) ORDER BY r.repo ASC LIMIT 65`,
}

// SourceRowDiscoveryStatement returns the statement of one discovery, and
// false for a name this file does not declare.
func SourceRowDiscoveryStatement(discovery SourceRowDiscovery) (string, bool) {
	statement, ok := sourceRowDiscoveryStatements[discovery]
	return statement, ok
}

// SourceRowRead is one row read: the statement id, the evidence id of the
// row, an optional task_ref push-down for catalog statements that filter on
// it, and the decoded ref components a ".v2" statement binds as
// component_1..n.
type SourceRowRead struct {
	QueryID    string
	Locator    string
	TaskRef    string
	Components []string
}

// workItemsSourceFamily is the source family of the work item statements,
// read off the catalog's own dependency statement rather than spelled a
// second time: the ".v2" rows belong to the same family as their catalog
// siblings.
var workItemsSourceFamily = catalogSourceQuery("work_item_dependencies.v1").Source

// SourceRowOnlyQueriesV2 are the row statements of the ".v2" kinds
// (CHAOS-7252). Each has the catalog's column shape and bindings plus
// component_1..n, and returns at most one row: the table key (or, for a
// dependency, the canonical relation) is fully bound.
var SourceRowOnlyQueriesV2 = []SourceQuery{
	// The canonical relation (source, target, relation key) whose source
	// work item has a row in the read repository. Spelling twins (raw
	// relationship_type values sharing one key, CHAOS-7177) are ONE relation:
	// the catalog's representative is served (latest last_synced, then the
	// raw type).
	{"work_item_dependencies.v2", workItemsSourceFamily, EvidenceScopeRepo, standardColumns + ` SELECT ` +
		evidenceref.SQL(contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, "d.source_work_item_id", "d.target_work_item_id", dependencyRelationKeySQL) + ` evidence_ref_id, 'dev_health' system, 'work_item_dependency' entity_type, ` +
		evidenceref.IDSQL(contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, "d.source_work_item_id", "d.target_work_item_id", dependencyRelationKeySQL) + ` entity_id, concat(d.source_work_item_id, ' ', d.relationship_type, ' ', d.target_work_item_id) display_label, '' safe_uri, 'native' provenance, 1.0 confidence, if(lengthUTF8(ifNull(d.relationship_type_raw, '')) BETWEEN 1 AND 2000, ifNull(d.relationship_type_raw, ''), concat('dependency=', d.source_work_item_id, ' -> ', d.target_work_item_id)) citation, d.last_synced observed_at FROM work_item_dependencies AS d FINAL WHERE d.org_id = {org_id:String} AND d.source_work_item_id = {component_1:String} AND d.target_work_item_id = {component_2:String} AND ` + dependencyRelationKeySQL + ` = {component_3:String} AND d.source_work_item_id IN (SELECT work_item_id FROM work_items FINAL WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND work_item_id = {component_1:String}) ORDER BY d.last_synced DESC, d.relationship_type ASC LIMIT 1 )`},
	// The child work_items row (org, repo, work item) naming this parent;
	// the parent must be a work item of the organization (the projector's
	// resolvability rule), checked without a join that fans out when the
	// parent id lives in several repositories.
	{"work_item_hierarchy.v2", workItemsSourceFamily, EvidenceScopeRepo, standardColumns + ` SELECT ` +
		evidenceref.SQL(contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, "c.repo_id", "c.work_item_id", "c.parent_id") + ` evidence_ref_id, 'dev_health' system, 'work_item_hierarchy' entity_type, ` +
		evidenceref.IDSQL(contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, "c.repo_id", "c.work_item_id", "c.parent_id") + ` entity_id, concat(c.work_item_id, ' part of ', c.parent_id) display_label, '' safe_uri, 'native' provenance, 1.0 confidence, concat('child=', c.work_item_id, ', parent=', c.parent_id) citation, c.updated_at observed_at FROM work_items AS c FINAL WHERE c.org_id = {org_id:String} AND c.repo_id = {repo_id:UUID} AND c.work_item_id = {component_2:String} AND c.parent_id = {component_3:String} AND c.parent_id != '' AND c.parent_id != c.work_item_id AND c.parent_id IN (SELECT work_item_id FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id = {component_3:String}) )`},
	// The primary work_item_team_attributions row by its full key; its work
	// item has a row in the read repository and its team is a team of the
	// organization (the projector's joins). Provenance follows the
	// projector's epistemic split (CHAOS-4101): a native_team row is the
	// provider's own assertion, every other source is Ops' inference.
	{"work_item_teams.v2", workItemsSourceFamily, EvidenceScopeRepo, standardColumns + ` SELECT ` +
		evidenceref.SQL(contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, "a.repo_id", "a.work_item_id", "ifNull(a.team_id, '')", "toString(a.source)") + ` evidence_ref_id, 'dev_health' system, 'work_item_team' entity_type, ` +
		evidenceref.IDSQL(contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, "a.repo_id", "a.work_item_id", "ifNull(a.team_id, '')", "toString(a.source)") + ` entity_id, concat(a.work_item_id, ' owned by team ', ifNull(a.team_id, '')) display_label, '' safe_uri, if(toString(a.source) = 'native_team', 'native', 'heuristic') provenance, 1.0 confidence, concat('source=', toString(a.source), ', confidence=', toString(a.confidence)) citation, a.computed_at observed_at FROM work_item_team_attributions AS a FINAL WHERE a.org_id = {org_id:String} AND toString(a.repo_id) = {component_1:String} AND a.work_item_id = {component_2:String} AND ifNull(a.team_id, '') = {component_3:String} AND toString(a.source) = {component_4:String} AND a.is_primary = 1 AND ifNull(a.team_id, '') != '' AND a.work_item_id IN (SELECT work_item_id FROM work_items FINAL WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND work_item_id = {component_2:String}) AND ifNull(a.team_id, '') IN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String} AND id = {component_3:String}) )`},
	// The work_graph_deployment_incident_edges row by its full key (org,
	// deployment, incident, source), in the repository the ref names.
	{"deployment_incident_edges.v2", "work_graph", EvidenceScopeRepo, standardColumns + ` SELECT ` +
		evidenceref.SQL(contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, "assumeNotNull(repo_id)", "deployment_id", "incident_id", "toString(source)") + ` evidence_ref_id, 'dev_health' system, 'deployment_incident_edge' entity_type, ` +
		evidenceref.IDSQL(contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, "assumeNotNull(repo_id)", "deployment_id", "incident_id", "toString(source)") + ` entity_id, concat(deployment_id, ' linked to ', incident_id) display_label, '' safe_uri, ` + deploymentIncidentProvenanceMapping + ` provenance, confidence, if(lengthUTF8(ifNull(evidence, '')) BETWEEN 1 AND 2000, ifNull(evidence, ''), concat('deployment=', deployment_id, ', incident=', incident_id)) citation, observed_at FROM work_graph_deployment_incident_edges FINAL WHERE toString(org_id) = {org_id:String} AND repo_id = {repo_id:UUID} AND deployment_id = {component_2:String} AND incident_id = {component_3:String} AND toString(source) = {component_4:String} AND deployment_id != '' AND incident_id NOT IN ('', 'none') )`},
}

// SourceRowQueryIDs lists every source-row statement id: the packet
// catalog's, the organization rows', then the ".v2" source-row-only ones.
func SourceRowQueryIDs() []string {
	ids := make([]string, 0, len(SourceQueryCatalogV1)+len(OrganizationRowQueriesV1)+len(SourceRowOnlyQueriesV2))
	for _, query := range SourceQueryCatalogV1 {
		ids = append(ids, query.ID)
	}
	for _, query := range OrganizationRowQueriesV1 {
		ids = append(ids, query.ID)
	}
	for _, query := range SourceRowOnlyQueriesV2 {
		ids = append(ids, query.ID)
	}
	return ids
}

func organizationRowQuery(id string) *OrganizationRowQuery {
	for index := range OrganizationRowQueriesV1 {
		if OrganizationRowQueriesV1[index].ID == id {
			return &OrganizationRowQueriesV1[index]
		}
	}
	return nil
}

// CatalogSourceQuery reports whether id is a packet catalog statement (as
// opposed to a source-row-only one), so an expansion names the catalog
// version only when the catalog produced the row.
func CatalogSourceQuery(id string) bool {
	return catalogSourceQuery(id) != nil
}

func sourceRowQuery(id string) *SourceQuery {
	if query := catalogSourceQuery(id); query != nil {
		return query
	}
	for index := range SourceRowOnlyQueriesV2 {
		if SourceRowOnlyQueriesV2[index].ID == id {
			return &SourceRowOnlyQueriesV2[index]
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
	for index, component := range read.Components {
		bindings = append(bindings, ClickHouseBinding{Name: "component_" + strconv.Itoa(index+1), Value: component})
	}
	statement := `SELECT * FROM (` + query.Statement + `) WHERE evidence_ref_id = {evidence_locator:String} LIMIT 2`
	return r.queryEvidenceReferences(ctx, scope.RepoSlug, query.ID, statement, bindings, 3)
}

// OrganizationRowReference is one organization-level row: the evidence and
// the row's own key column values, in the statement's KeyColumns order.
type OrganizationRowReference struct {
	Reference EvidenceReference
	Key       []string
}

// ResolveOrganizationRow reads the organization-level row whose evidence id
// is read.Locator (an OrganizationRowQueriesV1 statement), up to two (the
// caller refuses two as ambiguous), with the row's own key columns. It binds
// the organization and the locator only.
func (r *CatalogClickHouseRows) ResolveOrganizationRow(ctx context.Context, orgID string, read SourceRowRead) (_ []OrganizationRowReference, err error) {
	completeObservation := beginStoreQueryObservation(ctx, r.assemblyObserver(), StoreOperationEvidence)
	defer func() { completeObservation(err) }()
	query := organizationRowQuery(read.QueryID)
	if r == nil || r.client == nil || query == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSourceRowQuery, read.QueryID)
	}
	if read.Locator == "" {
		return nil, storage.ErrNotFound
	}
	bindings := []ClickHouseBinding{{Name: "org_id", Value: orgID}, {Name: "evidence_locator", Value: read.Locator}}
	statement := `SELECT * FROM (` + query.Statement + `) WHERE evidence_ref_id = {evidence_locator:String} LIMIT 2`
	rows, err := r.client.Query(ctx, statement, bindings)
	if err != nil {
		return nil, fmt.Errorf("resolve organization row: %w", err)
	}
	defer rows.Close()
	references := []OrganizationRowReference{}
	for rows.Next() {
		var id, system, entityType, entityID, label, safeURI, provenance, citation string
		var confidence float64
		var observedAt time.Time
		key := make([]string, len(query.KeyColumns))
		destinations := []any{&id, &system, &entityType, &entityID, &label, &safeURI, &provenance, &confidence, &citation, &observedAt}
		for index := range key {
			destinations = append(destinations, &key[index])
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("scan organization row: %w", err)
		}
		evidence := contractsv1.EvidenceRef{
			SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: id, SourceVersion: query.ID,
			Source:     contractsv1.EvidenceSource{System: system, EntityType: entityType, EntityID: entityID, DisplayLabel: label, SafeURI: safeURI},
			Provenance: provenance, Confidence: confidence, Citation: citation, ObservedAt: observedAt.UTC(), Availability: contractsv1.EvidenceAvailable,
		}
		references = append(references, OrganizationRowReference{Reference: EvidenceReference{Evidence: evidence, Excerpt: citation}, Key: key})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate organization row: %w", err)
	}
	return references, nil
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
