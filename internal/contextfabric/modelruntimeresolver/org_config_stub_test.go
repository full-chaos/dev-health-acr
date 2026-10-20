package modelruntimeresolver_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-go/llm"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type orgConfigInput struct {
	Provider      string
	BaseURL       string
	Model         string
	FallbackModel string
	Credential    string
}

// orgConfigStub is a map-backed contextfabric.OrgModelConfigResolver. Its
// generation counter is table-wide, so a value handed out to a deleted row is
// never reissued to a recreated row of the same organization.
type orgConfigStub struct {
	mu         sync.Mutex
	configs    map[string]contextfabric.ResolvedOrgModelConfig
	now        func() time.Time
	generation int64
}

var _ contextfabric.OrgModelConfigResolver = (*orgConfigStub)(nil)

func newOrgConfigStub(now func() time.Time) *orgConfigStub {
	if now == nil {
		now = time.Now
	}
	return &orgConfigStub{configs: map[string]contextfabric.ResolvedOrgModelConfig{}, now: now}
}

func (s *orgConfigStub) UpsertOrgModelConfig(_ context.Context, principal storage.Principal, in orgConfigInput) (int64, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return 0, errors.New("orgConfigStub: organization is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.configs[orgID] = contextfabric.ResolvedOrgModelConfig{
		Provider:      in.Provider,
		BaseURL:       in.BaseURL,
		Model:         in.Model,
		FallbackModel: in.FallbackModel,
		Credential:    llm.NewSecret(in.Credential),
		Generation:    s.generation,
		UpdatedAt:     s.now().UTC(),
	}
	return s.generation, nil
}

func (s *orgConfigStub) ResolveOrgModelConfig(ctx context.Context, orgID string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	if err := ctx.Err(); err != nil {
		return contextfabric.ResolvedOrgModelConfig{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved, ok := s.configs[strings.TrimSpace(orgID)]
	return resolved, ok, nil
}
