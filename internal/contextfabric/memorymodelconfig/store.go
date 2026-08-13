// Package memorymodelconfig is the in-memory test/dev twin of
// contextfabric.OrgModelConfigStore and contextfabric.OrgModelConfigResolver
// (mirrors internal/storage's memory-vs-postgres split,
// internal/storage/AGENTS.md, and internal/contextfabric/memoryinvestigation's
// precedent). It enforces the same org-scoping semantics as
// internal/contextfabric/pgmodelconfig.Store, including masking the
// credential on every read that isn't ResolveOrgModelConfig, so behavior
// cannot silently drift between the two implementations.
package memorymodelconfig

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type entry struct {
	provider      string
	baseURL       string
	model         string
	fallbackModel string
	credential    string
	generation    int64
	createdAt     time.Time
	updatedAt     time.Time
}

// Store is a mutex-protected, map-backed contextfabric.OrgModelConfigStore
// and contextfabric.OrgModelConfigResolver.
type Store struct {
	mu      sync.Mutex
	configs map[string]entry
	now     func() time.Time
	// nextGeneration is a single counter shared across every organization,
	// mirroring pgmodelconfig's table-wide Postgres sequence (Codex round-1
	// finding F3/F4): it must be global, not per-org, so a value already
	// handed out to a since-deleted row is never reissued to a
	// newly-recreated row for the same org_id.
	nextGeneration int64
}

var (
	_ contextfabric.OrgModelConfigStore    = (*Store)(nil)
	_ contextfabric.OrgModelConfigResolver = (*Store)(nil)
)

// NewStore returns an empty Store. now defaults to time.Now when nil.
func NewStore(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{configs: make(map[string]entry), now: now}
}

func (s *Store) UpsertOrgModelConfig(ctx context.Context, principal storage.Principal, request contractsv1.ContextFabricOrgModelConfigWriteRequest) (contractsv1.ContextFabricOrgModelConfig, error) {
	if s == nil {
		return contractsv1.ContextFabricOrgModelConfig{}, errors.New("memorymodelconfig: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return contractsv1.ContextFabricOrgModelConfig{}, err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return contractsv1.ContextFabricOrgModelConfig{}, errors.New("memorymodelconfig: organization is required")
	}
	if err := request.Validate(); err != nil {
		return contractsv1.ContextFabricOrgModelConfig{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	createdAt := now
	if existing, ok := s.configs[orgID]; ok {
		createdAt = existing.createdAt
	}
	s.nextGeneration++
	record := entry{
		provider:      request.Provider,
		baseURL:       request.BaseURL,
		model:         request.Model,
		fallbackModel: request.FallbackModel,
		credential:    request.Credential,
		generation:    s.nextGeneration,
		createdAt:     createdAt,
		updatedAt:     now,
	}
	s.configs[orgID] = record
	return toContract(orgID, record), nil
}

func (s *Store) GetOrgModelConfig(ctx context.Context, principal storage.Principal) (contractsv1.ContextFabricOrgModelConfig, error) {
	if s == nil {
		return contractsv1.ContextFabricOrgModelConfig{}, errors.New("memorymodelconfig: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return contractsv1.ContextFabricOrgModelConfig{}, err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.configs[orgID]
	if orgID == "" || !ok {
		return contractsv1.ContextFabricOrgModelConfig{}, contextfabric.ErrOrgModelConfigNotFound
	}
	return toContract(orgID, record), nil
}

func (s *Store) DeleteOrgModelConfig(ctx context.Context, principal storage.Principal) error {
	if s == nil {
		return errors.New("memorymodelconfig: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return errors.New("memorymodelconfig: organization is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.configs, orgID)
	return nil
}

func (s *Store) ResolveOrgModelConfig(ctx context.Context, orgID string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	if s == nil {
		return contextfabric.ResolvedOrgModelConfig{}, false, errors.New("memorymodelconfig: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.ResolvedOrgModelConfig{}, false, err
	}
	orgID = strings.TrimSpace(orgID)
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.configs[orgID]
	if orgID == "" || !ok {
		return contextfabric.ResolvedOrgModelConfig{}, false, nil
	}
	return contextfabric.ResolvedOrgModelConfig{
		Provider:      record.provider,
		BaseURL:       record.baseURL,
		Model:         record.model,
		FallbackModel: record.fallbackModel,
		Credential:    record.credential,
		Generation:    record.generation,
		UpdatedAt:     record.updatedAt,
	}, true, nil
}

func toContract(orgID string, record entry) contractsv1.ContextFabricOrgModelConfig {
	return contractsv1.ContextFabricOrgModelConfig{
		SchemaVersion:    contractsv1.ContextFabricOrgModelConfigSchema,
		OrgID:            orgID,
		Provider:         record.provider,
		BaseURL:          record.baseURL,
		Model:            record.model,
		FallbackModel:    record.fallbackModel,
		CredentialMasked: contractsv1.MaskContextFabricOrgModelCredential(record.credential),
		CreatedAt:        record.createdAt,
		UpdatedAt:        record.updatedAt,
	}
}
