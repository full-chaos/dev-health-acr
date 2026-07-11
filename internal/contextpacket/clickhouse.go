package contextpacket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type ClickHouseRows interface {
	ResolveEvidenceScope(context.Context, ReadPlan) (contractsv1.ResolvedScope, error)
	EvidenceRows(context.Context, ReadPlan) ([]contractsv1.EvidenceRef, []contractsv1.SourceWatermark, []contractsv1.UnavailableSource, error)
}

type EvidenceReference struct {
	RepoSlug string
	Evidence contractsv1.EvidenceRef
	Excerpt  string
}

type EvidenceReferenceRows interface {
	AuthorizedRepositories(context.Context, string, []string) ([]contractsv1.ResolvedScope, error)
	ResolveEvidenceReference(context.Context, string, contractsv1.ResolvedScope, string) ([]EvidenceReference, error)
}

type CatalogClickHouseRows struct {
	resolver *ClickHouseScopeResolver
	executor SourceQueryExecutor
	client   ClickHouseQueryClient
	observer AssemblyObserver
}

func NewCatalogClickHouseRows(client ClickHouseQueryClient) *CatalogClickHouseRows {
	return NewObservedCatalogClickHouseRows(client, nil)
}

func NewObservedCatalogClickHouseRows(client ClickHouseQueryClient, observer AssemblyObserver) *CatalogClickHouseRows {
	return &CatalogClickHouseRows{resolver: NewObservedClickHouseScopeResolver(client, observer), executor: NewClickHouseSourceExecutor(client), client: client, observer: observer}
}

func (r *CatalogClickHouseRows) ResolveEvidenceScope(ctx context.Context, plan ReadPlan) (contractsv1.ResolvedScope, error) {
	return r.resolver.ResolveEvidenceScope(ctx, plan)
}

func (r *CatalogClickHouseRows) EvidenceRows(ctx context.Context, plan ReadPlan) ([]contractsv1.EvidenceRef, []contractsv1.SourceWatermark, []contractsv1.UnavailableSource, error) {
	result, err := ExecuteCatalogObserved(ctx, r.executor, plan, r.observer)
	if err != nil {
		return nil, nil, nil, err
	}
	return result.Evidence, result.Watermarks, result.Unavailable, nil
}

func (r *CatalogClickHouseRows) AuthorizedRepositories(ctx context.Context, orgID string, scopes []string) (_ []contractsv1.ResolvedScope, err error) {
	var observer AssemblyObserver
	if r != nil {
		observer = r.observer
	}
	completeObservation := beginStoreQueryObservation(ctx, observer, StoreOperationEvidence)
	defer func() { completeObservation(err) }()
	if r == nil || r.client == nil {
		return nil, storage.ErrNotFound
	}
	rows, err := r.client.Query(ctx, `SELECT toString(id), repo FROM repos FINAL WHERE org_id = {org_id:String} AND arrayExists(scope -> scope = '*' OR scope = repo OR (endsWith(scope, '/*') AND startsWith(repo, left(scope, length(scope) - 1))), {repository_scopes:Array(String)}) ORDER BY repo ASC LIMIT 65`, []ClickHouseBinding{{Name: "org_id", Value: orgID}, {Name: "repository_scopes", Value: scopes}})
	if err != nil {
		return nil, fmt.Errorf("resolve authorized repositories: %w", err)
	}
	defer rows.Close()
	result := []contractsv1.ResolvedScope{}
	for rows.Next() {
		var repoID, slug string
		if err := rows.Scan(&repoID, &slug); err != nil {
			return nil, fmt.Errorf("scan authorized repository: %w", err)
		}
		result = append(result, contractsv1.ResolvedScope{RepoID: repoID, RepoSlug: slug, Resolution: contractsv1.ScopeRepoFallback, FallbackReasons: []string{}})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate authorized repositories: %w", err)
	}
	if len(result) == 0 || len(result) > maxEvidenceCandidateRepos {
		return nil, storage.ErrNotFound
	}
	return result, nil
}

func (r *CatalogClickHouseRows) ResolveEvidenceReference(ctx context.Context, orgID string, scope contractsv1.ResolvedScope, queryID string) (_ []EvidenceReference, err error) {
	var observer AssemblyObserver
	if r != nil {
		observer = r.observer
	}
	completeObservation := beginStoreQueryObservation(ctx, observer, StoreOperationEvidence)
	defer func() { completeObservation(err) }()
	query := catalogSourceQuery(queryID)
	if r == nil || r.client == nil || query == nil {
		return nil, storage.ErrNotFound
	}
	plan := ReadPlan{OrgID: orgID, RepoID: scope.RepoID, RepoSlug: scope.RepoSlug}
	rows, err := r.client.Query(ctx, `SELECT evidence_ref_id, system, entity_type, entity_id, display_label, safe_uri, provenance, confidence, citation, observed_at FROM (`+query.Statement+`) LIMIT 501`, plan.Bindings())
	if err != nil {
		return nil, fmt.Errorf("resolve evidence locator: %w", err)
	}
	defer rows.Close()
	scanned := 0
	var result []contractsv1.EvidenceRef
	for rows.Next() {
		scanned++
		evidence, scanErr := scanEvidenceRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan evidence locator: %w", scanErr)
		}
		result = append(result, evidence)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evidence locator: %w", err)
	}
	if scanned == 501 {
		return nil, storage.ErrNotFound
	}
	references := make([]EvidenceReference, 0, len(result))
	for _, evidence := range result {
		evidence.SourceVersion = query.ID
		references = append(references, EvidenceReference{RepoSlug: scope.RepoSlug, Evidence: evidence, Excerpt: evidence.Citation})
	}
	return references, nil
}

type ClickHouseEvidenceStore struct {
	rows     ClickHouseRows
	codec    *EvidenceIDCodec
	resolver *EvidenceResolver
}

type EvidenceStoreOptions struct {
	Codec    *EvidenceIDCodec
	Resolver *EvidenceResolver
}

func NewClickHouseEvidenceStore(rows ClickHouseRows) *ClickHouseEvidenceStore {
	return &ClickHouseEvidenceStore{rows: rows}
}

func NewClickHouseEvidenceStoreWithOptions(rows ClickHouseRows, options EvidenceStoreOptions) (*ClickHouseEvidenceStore, error) {
	if rows == nil || options.Codec == nil {
		return nil, ErrInvalidEvidenceID
	}
	if options.Resolver == nil {
		options.Resolver = NewEvidenceResolver(EvidenceResolverOptions{})
	}
	return &ClickHouseEvidenceStore{rows: rows, codec: options.Codec, resolver: options.Resolver}, nil
}
func (s *ClickHouseEvidenceStore) ResolveScope(ctx context.Context, p storage.Principal, r contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	plan, err := BuildReadPlanV1(p, r)
	if err != nil {
		return contractsv1.ResolvedScope{}, err
	}
	if err = ctx.Err(); err != nil {
		return contractsv1.ResolvedScope{}, err
	}
	if s.rows == nil {
		return contractsv1.ResolvedScope{}, fmt.Errorf("contextpacket: clickhouse rows adapter is required")
	}
	scope, err := s.rows.ResolveEvidenceScope(ctx, plan)
	if err != nil {
		return contractsv1.ResolvedScope{}, err
	}
	if scope.Resolution == contractsv1.ScopeUnresolved {
		return scope, nil
	}
	if scope.RepoID == "" || scope.RepoSlug != plan.RepoSlug {
		return contractsv1.ResolvedScope{}, ErrEvidenceScopeMismatch
	}
	return scope, nil
}
func (s *ClickHouseEvidenceStore) ContextForTask(ctx context.Context, p storage.Principal, r contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	if s.rows == nil || s.codec == nil {
		return storage.EvidenceBundle{}, fmt.Errorf("contextpacket: authenticated evidence codec is required")
	}
	plan, err := BuildReadPlanV1(p, r)
	if err != nil {
		return storage.EvidenceBundle{}, err
	}
	scope, err := s.ResolveScope(ctx, p, r)
	if err != nil {
		return storage.EvidenceBundle{}, err
	}
	if scope.Resolution == contractsv1.ScopeUnresolved {
		return storage.EvidenceBundle{ResolvedScope: scope, Unavailable: unavailableCatalog("scope_unresolved"), QueryVersion: QueryVersionV1}, nil
	}
	plan.RepoID = scope.RepoID
	evidence, watermarks, unavailable, err := s.rows.EvidenceRows(ctx, plan)
	if err != nil {
		return storage.EvidenceBundle{}, err
	}
	for index := range evidence {
		handle, encodeErr := s.codec.Encode(p.OrgID, scope.RepoID, evidence[index].SourceVersion, evidence[index].EvidenceRefID)
		if encodeErr != nil {
			return storage.EvidenceBundle{}, fmt.Errorf("encode evidence handle: %w", encodeErr)
		}
		evidence[index].EvidenceRefID = handle
	}
	return storage.EvidenceBundle{ResolvedScope: scope, Evidence: evidence, Watermarks: watermarks, Unavailable: unavailable, QueryVersion: QueryVersionV1}, nil
}

func unavailableCatalog(reason string) []contractsv1.UnavailableSource {
	values := make([]contractsv1.UnavailableSource, 0, len(SourceQueryCatalogV1))
	for _, query := range SourceQueryCatalogV1 {
		values = append(values, contractsv1.UnavailableSource{Source: query.ID, Reason: reason})
	}
	return values
}
func (s *ClickHouseEvidenceStore) ResolveEvidence(ctx context.Context, principal storage.Principal, evidenceRefID string) (contractsv1.ExpandedEvidence, error) {
	if err := ctx.Err(); err != nil {
		return contractsv1.ExpandedEvidence{}, err
	}
	if s.rows == nil || s.codec == nil || strings.TrimSpace(principal.OrgID) == "" || !validOpaqueEvidenceRefID(evidenceRefID) {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	rows, ok := s.rows.(EvidenceReferenceRows)
	if !ok {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	handle, err := s.codec.Parse(evidenceRefID)
	if err != nil {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	candidates, err := rows.AuthorizedRepositories(ctx, principal.OrgID, principal.RepositoryScopes)
	if err != nil {
		if ctx.Err() != nil {
			return contractsv1.ExpandedEvidence{}, ctx.Err()
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return contractsv1.ExpandedEvidence{}, fmt.Errorf("resolve authorized evidence repositories: %w", err)
		}
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	if len(candidates) == 0 || len(candidates) > maxEvidenceCandidateRepos {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	var matched EvidenceReference
	matches := 0
	for _, scope := range candidates {
		if auth.AuthorizeRepository(principal, scope.RepoSlug) != nil {
			continue
		}
		references, lookupErr := rows.ResolveEvidenceReference(ctx, principal.OrgID, scope, handle.QueryID)
		if lookupErr != nil && !errors.Is(lookupErr, storage.ErrNotFound) {
			return contractsv1.ExpandedEvidence{}, fmt.Errorf("resolve evidence reference: %w", lookupErr)
		}
		for _, reference := range references {
			if lookupErr == nil && reference.RepoSlug == scope.RepoSlug && reference.Evidence.SourceVersion == handle.QueryID && s.codec.Matches(handle, principal.OrgID, scope.RepoID, reference.Evidence.EvidenceRefID) {
				matches++
				matched = reference
			}
		}
	}
	if matches != 1 {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	matched.Evidence.EvidenceRefID = evidenceRefID
	return s.resolver.Expand(ctx, EvidenceExpansionInput{Evidence: matched.Evidence, Excerpt: matched.Excerpt})
}
