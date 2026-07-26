package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	DeviceCredentialLifetime          = 30 * 24 * time.Hour
	deviceAuthorizationStartRedacted  = "auth.DeviceAuthorizationStart{redacted}"
	deviceAuthorizationCredentialName = "device login"
)

var (
	ErrInvalidDeviceFlow    = errors.New("invalid device flow request")
	ErrDeviceCodeCollision  = errors.New("device authorization code collision")
	ErrDeviceCodeGeneration = errors.New("device authorization code generation failed")
)

type DeviceFlowOptions struct {
	Now    func() time.Time
	Random io.Reader
}

type DeviceFlowService struct {
	store       storage.DeviceAuthorizationStore
	credentials *Service
	now         func() time.Time
	random      io.Reader
	randomMu    sync.Mutex
}

type DeviceAuthorizationStart struct {
	DeviceCode string
	UserCode   string
	ExpiresIn  time.Duration
	Interval   time.Duration
}

type DeviceAuthorizationHints struct {
	OrganizationIDHint string
	RepositoryHints    []string
}

type DeviceApprovalRequest struct {
	Principal        storage.Principal
	UserCode         string
	RepositoryScopes []string
}

type DeviceApprovalPreviewRequest struct {
	Principal storage.Principal
	UserCode  string
}

type DeviceApprovalPreview struct {
	OrganizationIDHint string
	RepositoryHints    []string
}

type DeviceDenialRequest struct {
	Principal storage.Principal
	UserCode  string
}

func NewDeviceFlowService(store storage.DeviceAuthorizationStore, credentials *Service, options DeviceFlowOptions) (*DeviceFlowService, error) {
	if storage.IsNil(store) || credentials == nil {
		return nil, ErrInvalidDeviceFlow
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if storage.IsNil(options.Random) {
		options.Random = rand.Reader
	}
	return &DeviceFlowService{store: store, credentials: credentials, now: options.Now, random: options.Random}, nil
}

func (s *DeviceFlowService) Start(ctx context.Context, hints DeviceAuthorizationHints) (DeviceAuthorizationStart, error) {
	if err := s.ready(ctx); err != nil {
		return DeviceAuthorizationStart{}, err
	}
	normalizedHints, err := normalizeDeviceAuthorizationHints(hints)
	if err != nil {
		return DeviceAuthorizationStart{}, ErrInvalidDeviceFlow
	}
	for range maxDeviceCodeAttempts {
		deviceCode, userCode, err := s.nextCodes()
		if err != nil {
			return DeviceAuthorizationStart{}, ErrDeviceCodeGeneration
		}
		_, err = s.store.Create(ctx, storage.DeviceAuthorizationCreateInput{
			DeviceCodeHash:     storage.HashDeviceCode(deviceCode),
			UserCodeHash:       storage.HashUserCode(userCode),
			OrganizationIDHint: normalizedHints.OrganizationIDHint,
			RepositoryHints:    normalizedHints.RepositoryHints,
		})
		if err == nil {
			return DeviceAuthorizationStart{
				DeviceCode: deviceCode,
				UserCode:   userCode,
				ExpiresIn:  storage.DeviceAuthorizationTTL,
				Interval:   storage.DeviceAuthorizationPollInterval,
			}, nil
		}
		if !errors.Is(err, storage.ErrDeviceAuthorizationConflict) {
			return DeviceAuthorizationStart{}, fmt.Errorf("create device authorization: %w", err)
		}
	}
	return DeviceAuthorizationStart{}, ErrDeviceCodeCollision
}

func (s *DeviceFlowService) Preview(ctx context.Context, request DeviceApprovalPreviewRequest) (DeviceApprovalPreview, error) {
	if err := s.ready(ctx); err != nil {
		return DeviceApprovalPreview{}, err
	}
	userCode, ok := normalizeUserCode(request.UserCode)
	if !ok || !validDeviceApprovalPrincipal(request.Principal) {
		return DeviceApprovalPreview{}, ErrInvalidDeviceFlow
	}
	record, err := s.store.Preview(ctx, storage.HashUserCode(userCode))
	if err != nil {
		return DeviceApprovalPreview{}, fmt.Errorf("preview device authorization: %w", err)
	}
	return DeviceApprovalPreview{
		OrganizationIDHint: record.OrganizationIDHint,
		RepositoryHints:    append([]string(nil), record.RepositoryHints...),
	}, nil
}

func (s *DeviceFlowService) Approve(ctx context.Context, request DeviceApprovalRequest) (storage.DeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	userCode, ok := normalizeUserCode(request.UserCode)
	if !ok || !validDeviceApprovalPrincipal(request.Principal) {
		return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
	}
	repositories, err := NormalizeRepositoryScopes(request.RepositoryScopes)
	if err != nil || hasRepositoryWildcard(repositories) {
		return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
	}
	principalRepositories, err := NormalizeRepositoryScopes(request.Principal.RepositoryScopes)
	if err != nil || !slices.Equal(principalRepositories, request.Principal.RepositoryScopes) ||
		!repositoriesWithinGrant(principalRepositories, repositories) {
		return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
	}
	record, err := s.store.Approve(ctx, storage.HashUserCode(userCode), storage.DeviceAuthorizationGrant{
		OrgID:                         request.Principal.OrgID,
		RepositoryScopes:              repositories,
		Scopes:                        []string{ScopeContextRead, ScopeEvidenceRead},
		ApprovingSubject:              request.Principal.Subject,
		ApprovingAuthenticationMethod: storage.AuthenticationMethodWebAssertion,
	})
	if err != nil {
		return storage.DeviceAuthorization{}, fmt.Errorf("approve device authorization: %w", err)
	}
	return record, nil
}

func (s *DeviceFlowService) Deny(ctx context.Context, request DeviceDenialRequest) (storage.DeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	userCode, ok := normalizeUserCode(request.UserCode)
	if !ok || !validDeviceApprovalPrincipal(request.Principal) {
		return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
	}
	record, err := s.store.Deny(ctx, storage.HashUserCode(userCode))
	if err != nil {
		return storage.DeviceAuthorization{}, fmt.Errorf("deny device authorization: %w", err)
	}
	return record, nil
}

func (s *DeviceFlowService) nextCodes() (string, string, error) {
	s.randomMu.Lock()
	defer s.randomMu.Unlock()
	return generateDeviceCodes(s.random)
}

func (s *DeviceFlowService) ready(ctx context.Context) error {
	if s == nil || storage.IsNil(s.store) || s.credentials == nil || s.now == nil || storage.IsNil(s.random) || storage.IsNil(ctx) {
		return ErrInvalidDeviceFlow
	}
	return ctx.Err()
}

func validDeviceApprovalPrincipal(principal storage.Principal) bool {
	return principal.AuthenticationMethod == storage.AuthenticationMethodWebAssertion &&
		strings.TrimSpace(principal.Subject) != "" && strings.TrimSpace(principal.OrgID) != "" &&
		principal.CredentialID == "" && len(principal.Permissions) == 1 &&
		principal.Permissions[0] == WebAssertionPermissionCredentialIssue
}

func hasRepositoryWildcard(repositories []string) bool {
	for _, repository := range repositories {
		if repository == "*" || strings.HasSuffix(repository, "/*") {
			return true
		}
	}
	return false
}

func repositoriesWithinGrant(grant, selected []string) bool {
	for _, repository := range selected {
		if !RepositoryAllowed(grant, repository) {
			return false
		}
	}
	return true
}

func normalizeDeviceAuthorizationHints(hints DeviceAuthorizationHints) (DeviceAuthorizationHints, error) {
	if strings.TrimSpace(hints.OrganizationIDHint) != hints.OrganizationIDHint || len(hints.OrganizationIDHint) > 128 {
		return DeviceAuthorizationHints{}, ErrInvalidDeviceFlow
	}
	if hints.RepositoryHints == nil {
		return hints, nil
	}
	repositories, err := NormalizeRepositoryScopes(hints.RepositoryHints)
	if err != nil || hasRepositoryWildcard(repositories) {
		return DeviceAuthorizationHints{}, ErrInvalidDeviceFlow
	}
	return DeviceAuthorizationHints{OrganizationIDHint: hints.OrganizationIDHint, RepositoryHints: repositories}, nil
}

func (DeviceAuthorizationStart) String() string { return deviceAuthorizationStartRedacted }

func (DeviceAuthorizationStart) GoString() string { return deviceAuthorizationStartRedacted }

func (DeviceAuthorizationStart) LogValue() slog.Value {
	return slog.StringValue(deviceAuthorizationStartRedacted)
}
