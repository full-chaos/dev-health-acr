package contextpacket

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
// path gives it.

// SourceRowQueryVersionV1 versions the statements of this file.
const SourceRowQueryVersionV1 = "dev-health-source-rows.v1"

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
)

var sourceRowDiscoveryStatements = map[SourceRowDiscovery]string{
	SourceRowDiscoveryIncident: `SELECT DISTINCT toString(r.id), r.repo FROM operational_incidents AS i FINAL INNER JOIN operational_service_repository_mappings AS m FINAL ON i.org_id = m.org_id AND i.service_id = m.service_id INNER JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id WHERE i.org_id = {org_id:String} AND m.org_id = {org_id:String} AND i.id = {entity_id:String} AND i.is_deleted = 0 AND m.is_active = 1 AND m.valid_from <= now64(6) AND (m.valid_to IS NULL OR m.valid_to > now64(6)) ORDER BY r.repo ASC LIMIT 65`,
}

// SourceRowDiscoveryStatement returns the statement of one discovery, and
// false for a name this file does not declare.
func SourceRowDiscoveryStatement(discovery SourceRowDiscovery) (string, bool) {
	statement, ok := sourceRowDiscoveryStatements[discovery]
	return statement, ok
}

// SourceRowRead is one row read: the catalog statement id, the catalog
// evidence id of the row, and an optional task_ref push-down for statements
// that filter on it.
type SourceRowRead struct {
	QueryID string
	Locator string
	TaskRef string
}

// SourceRowQueryIDs lists every statement id ResolveSourceRow accepts.
func SourceRowQueryIDs() []string {
	ids := make([]string, 0, len(SourceQueryCatalogV1)+len(OrganizationRowQueriesV1))
	for _, query := range SourceQueryCatalogV1 {
		ids = append(ids, query.ID)
	}
	for _, query := range OrganizationRowQueriesV1 {
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

func sourceRowQuery(id string) *SourceQuery {
	return catalogSourceQuery(id)
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
