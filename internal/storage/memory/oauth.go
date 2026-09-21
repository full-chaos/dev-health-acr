package memory

import (
	"context"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// OAuthStore is the in-memory storage.OAuthStore implementation: mutex-
// protected maps, matching every other memory adapter in this package.
type OAuthStore struct {
	mu           sync.Mutex
	now          func() time.Time
	clients      map[string]storage.OAuthClient
	byHandle     map[storage.OAuthSecretHash]storage.OAuthAuthorizationRequest
	byDeviceCode map[storage.DeviceCodeHash]storage.OAuthSecretHash
	byCode       map[storage.OAuthSecretHash]storage.OAuthSecretHash
}

// NewOAuthStore constructs an OAuthStore. now must be non-nil.
func NewOAuthStore(now func() time.Time) *OAuthStore {
	if now == nil {
		now = time.Now
	}
	return &OAuthStore{
		now:          now,
		clients:      make(map[string]storage.OAuthClient),
		byHandle:     make(map[storage.OAuthSecretHash]storage.OAuthAuthorizationRequest),
		byDeviceCode: make(map[storage.DeviceCodeHash]storage.OAuthSecretHash),
		byCode:       make(map[storage.OAuthSecretHash]storage.OAuthSecretHash),
	}
}

func (s *OAuthStore) ready(ctx context.Context) error {
	if s == nil || s.now == nil || s.clients == nil || s.byHandle == nil || s.byDeviceCode == nil || s.byCode == nil || storage.IsNil(ctx) {
		return storage.ErrInvalidOAuthClient
	}
	return ctx.Err()
}

func (s *OAuthStore) RegisterClient(ctx context.Context, client storage.OAuthClient) (storage.OAuthClient, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthClient{}, err
	}
	if err := storage.ValidateOAuthClient(client); err != nil {
		return storage.OAuthClient{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.OAuthClient{}, err
	}
	if _, exists := s.clients[client.ClientID]; exists {
		return storage.OAuthClient{}, storage.ErrConflict
	}
	s.clients[client.ClientID] = cloneOAuthClient(client)
	return cloneOAuthClient(client), nil
}

func (s *OAuthStore) GetClient(ctx context.Context, clientID string) (storage.OAuthClient, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthClient{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client, exists := s.clients[clientID]
	if !exists {
		return storage.OAuthClient{}, storage.ErrNotFound
	}
	return cloneOAuthClient(client), nil
}

func (s *OAuthStore) CreateAuthorizationRequest(ctx context.Context, request storage.OAuthAuthorizationRequest) (storage.OAuthAuthorizationRequest, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	if err := storage.ValidateOAuthAuthorizationRequest(request); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	if _, exists := s.byHandle[request.HandleHash]; exists {
		return storage.OAuthAuthorizationRequest{}, storage.ErrConflict
	}
	if _, exists := s.byDeviceCode[request.DeviceCodeHash]; exists {
		return storage.OAuthAuthorizationRequest{}, storage.ErrConflict
	}
	stored := storage.CloneOAuthAuthorizationRequest(request)
	s.byHandle[request.HandleHash] = stored
	s.byDeviceCode[request.DeviceCodeHash] = request.HandleHash
	return storage.CloneOAuthAuthorizationRequest(stored), nil
}

func (s *OAuthStore) GetAuthorizationRequest(ctx context.Context, handle storage.OAuthSecretHash) (storage.OAuthAuthorizationRequest, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	request, exists := s.byHandle[handle]
	if !exists {
		return storage.OAuthAuthorizationRequest{}, storage.ErrNotFound
	}
	return storage.CloneOAuthAuthorizationRequest(request), nil
}

// IssueAuthorizationCode attaches the code hash to an unexpired request that
// has no code yet. An unknown handle is ErrNotFound; an expired request or a
// request that already has a code is ErrConflict.
func (s *OAuthStore) IssueAuthorizationCode(ctx context.Context, handle, code storage.OAuthSecretHash, codeExpiresAt time.Time) (storage.OAuthAuthorizationRequest, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	if handle.IsZero() || code.IsZero() || codeExpiresAt.IsZero() {
		return storage.OAuthAuthorizationRequest{}, storage.ErrInvalidOAuthAuthorizationRequest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	request, exists := s.byHandle[handle]
	if !exists {
		return storage.OAuthAuthorizationRequest{}, storage.ErrNotFound
	}
	now := s.now().UTC()
	if !request.ExpiresAt.After(now) || request.CodeHash != nil {
		return storage.OAuthAuthorizationRequest{}, storage.ErrConflict
	}
	if _, exists := s.byCode[code]; exists {
		return storage.OAuthAuthorizationRequest{}, storage.ErrConflict
	}
	codeCopy := code
	expiresCopy := codeExpiresAt.UTC()
	request.CodeHash = &codeCopy
	request.CodeExpiresAt = &expiresCopy
	stored := storage.CloneOAuthAuthorizationRequest(request)
	s.byHandle[handle] = stored
	s.byCode[code] = handle
	return storage.CloneOAuthAuthorizationRequest(stored), nil
}

// ConsumeAuthorizationCode atomically marks the code used and returns its
// request. Unknown, expired and already consumed codes are all
// ErrOAuthAuthorizationCodeUnavailable, deliberately indistinguishable.
func (s *OAuthStore) ConsumeAuthorizationCode(ctx context.Context, code storage.OAuthSecretHash) (storage.OAuthAuthorizationRequest, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	handle, exists := s.byCode[code]
	if !exists {
		return storage.OAuthAuthorizationRequest{}, storage.ErrOAuthAuthorizationCodeUnavailable
	}
	request, exists := s.byHandle[handle]
	if !exists || request.CodeHash == nil || *request.CodeHash != code {
		return storage.OAuthAuthorizationRequest{}, storage.ErrOAuthAuthorizationCodeUnavailable
	}
	now := s.now().UTC()
	if request.CodeExpiresAt == nil || !request.CodeExpiresAt.After(now) || request.ConsumedAt != nil {
		return storage.OAuthAuthorizationRequest{}, storage.ErrOAuthAuthorizationCodeUnavailable
	}
	consumedAt := now
	request.ConsumedAt = &consumedAt
	stored := storage.CloneOAuthAuthorizationRequest(request)
	s.byHandle[handle] = stored
	return storage.CloneOAuthAuthorizationRequest(stored), nil
}

func cloneOAuthClient(client storage.OAuthClient) storage.OAuthClient {
	client.RedirectURIs = append([]string(nil), client.RedirectURIs...)
	return client
}
