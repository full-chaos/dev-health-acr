package contextpacket

import (
	"context"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type ClickHouseRows interface {
	ResolveEvidenceScope(context.Context, ReadPlan) (contractsv1.ResolvedScope, error)
	EvidenceRows(context.Context, ReadPlan) ([]contractsv1.EvidenceRef, []contractsv1.SourceWatermark, []contractsv1.UnavailableSource, error)
}

type CatalogClickHouseRows struct {
	resolver *ClickHouseScopeResolver
	executor SourceQueryExecutor
}

func NewCatalogClickHouseRows(client ClickHouseQueryClient) *CatalogClickHouseRows {
	return &CatalogClickHouseRows{resolver: NewClickHouseScopeResolver(client), executor: NewClickHouseSourceExecutor(client)}
}

func (r *CatalogClickHouseRows) ResolveEvidenceScope(ctx context.Context, plan ReadPlan) (contractsv1.ResolvedScope, error) {
	return r.resolver.ResolveEvidenceScope(ctx, plan)
}

func (r *CatalogClickHouseRows) EvidenceRows(ctx context.Context, plan ReadPlan) ([]contractsv1.EvidenceRef, []contractsv1.SourceWatermark, []contractsv1.UnavailableSource, error) {
	result, err := ExecuteCatalog(ctx, r.executor, plan)
	if err != nil {
		return nil, nil, nil, err
	}
	return result.Evidence, result.Watermarks, result.Unavailable, nil
}

type ClickHouseEvidenceStore struct{ rows ClickHouseRows }

func NewClickHouseEvidenceStore(rows ClickHouseRows) *ClickHouseEvidenceStore {
	return &ClickHouseEvidenceStore{rows: rows}
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
	if s.rows == nil {
		return storage.EvidenceBundle{}, fmt.Errorf("contextpacket: clickhouse rows adapter is required")
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
	return storage.EvidenceBundle{ResolvedScope: scope, Evidence: evidence, Watermarks: watermarks, Unavailable: unavailable, QueryVersion: QueryVersionV1}, nil
}

func unavailableCatalog(reason string) []contractsv1.UnavailableSource {
	values := make([]contractsv1.UnavailableSource, 0, len(SourceQueryCatalogV1))
	for _, query := range SourceQueryCatalogV1 {
		values = append(values, contractsv1.UnavailableSource{Source: query.ID, Reason: reason})
	}
	return values
}
func (s *ClickHouseEvidenceStore) ResolveEvidence(context.Context, storage.Principal, string) (contractsv1.ExpandedEvidence, error) {
	return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
}
