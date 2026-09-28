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
	// OAuthDeviceGrants lets Poll refuse redeeming a device code that
	// belongs to an RFC 8628 device grant (CHAOS-6233, POST
	// /device_authorization): such a code must be redeemed only through
	// OAuthService.ExchangeDeviceCode, which binds the credential to the
	// grant's resource and requested scope; this legacy path binds neither.
	// REQUIRED -- NewDeviceFlowService refuses construction when this is
	// nil, so a caller cannot silently end up with the guard skipped by
	// forgetting to wire it. A deployment with no OAuth login configured at
	// all passes the explicit NoOAuthDeviceGrants{} value instead of
	// leaving this unset, so "OAuth is genuinely off" and "OAuth is on but
	// someone forgot to wire the lookup" can never be the same nil.
	OAuthDeviceGrants OAuthDeviceGrantLookup
}

type DeviceFlowService struct {
	store             storage.DeviceAuthorizationStore
	credentials       *Service
	now               func() time.Time
	random            io.Reader
	randomMu          sync.Mutex
	oauthDeviceGrants OAuthDeviceGrantLookup
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
	// RequestedScopes (CHAOS-7106) are the scopes the device grant asked for,
	// in canonical order: what the RFC 8628 /device_authorization request
	// named (stored on the grant row), or the default pair when the record has
	// no grant row (the legacy acr-mcp login flow has no scope parameter).
	RequestedScopes []string
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
	// storage.IsNil, not a bare != nil: a caller passing a typed nil
	// pointer that satisfies the interface (e.g. a nil *memory.OAuthStore)
	// must be refused exactly like an unset field -- an interface holding a
	// typed nil is itself non-nil and would slip past a bare check, defeating
	// this guard through the exact shape it exists to catch.
	if storage.IsNil(options.OAuthDeviceGrants) {
		return nil, fmt.Errorf("%w: OAuthDeviceGrants is required (pass NoOAuthDeviceGrants{} when OAuth login is not configured)", ErrInvalidDeviceFlow)
	}
	return &DeviceFlowService{
		store: store, credentials: credentials, now: options.Now, random: options.Random,
		oauthDeviceGrants: options.OAuthDeviceGrants,
	}, nil
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
	principalRepositories, err := normalizedPrincipalRepositories(request.Principal)
	if err != nil {
		return DeviceApprovalPreview{}, ErrInvalidDeviceFlow
	}
	if record.OrganizationIDHint != "" && record.OrganizationIDHint != request.Principal.OrgID {
		return DeviceApprovalPreview{}, ErrInvalidDeviceFlow
	}
	repositoryHints := intersectRepositoryHints(record.RepositoryHints, principalRepositories)
	if len(record.RepositoryHints) > 0 && len(repositoryHints) == 0 {
		return DeviceApprovalPreview{}, ErrInvalidDeviceFlow
	}
	requestedScopes, err := s.requestedScopes(ctx, record.DeviceCodeHash)
	if err != nil {
		return DeviceApprovalPreview{}, err
	}
	return DeviceApprovalPreview{
		OrganizationIDHint: record.OrganizationIDHint,
		RepositoryHints:    repositoryHints,
		RequestedScopes:    requestedScopes,
	}, nil
}

// requestedScopes returns the scopes a device record's grant asked for. A
// record with no grant row (storage.ErrNotFound) is a legacy device
// authorization, whose credential is the default pair. Any other lookup
// failure fails the preview rather than showing a guess: the page must not
// tell a user less than what they are approving.
func (s *DeviceFlowService) requestedScopes(ctx context.Context, hash storage.DeviceCodeHash) ([]string, error) {
	grant, err := s.oauthDeviceGrants.GetDeviceGrant(ctx, hash)
	if errors.Is(err, storage.ErrNotFound) {
		return slices.Clone(oauthDefaultApprovalScopes), nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up device grant scopes: %w", err)
	}
	scopes := strings.Fields(grant.Scope)
	if len(scopes) == 0 {
		return slices.Clone(oauthDefaultApprovalScopes), nil
	}
	return scopes, nil
}

func (s *DeviceFlowService) Approve(ctx context.Context, request DeviceApprovalRequest) (storage.DeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	userCode, ok := normalizeUserCode(request.UserCode)
	if !ok {
		return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
	}
	return s.approveUserCodeHash(ctx, request.Principal, storage.HashUserCode(userCode), request.RepositoryScopes, deviceApprovalScopes)
}

// approveUserCodeHash approves the pending device authorization behind a user
// code hash for a web-assertion principal. The device approval page (by the
// typed user code) and the OAuth consent page (by the request's handle) both
// approve through it, so both apply the same org and repository rules.
func (s *DeviceFlowService) approveUserCodeHash(ctx context.Context, principal storage.Principal, userCodeHash storage.UserCodeHash, repositoryScopes, scopes []string) (storage.DeviceAuthorization, error) {
	if !validDeviceApprovalPrincipal(principal) || !validApprovalScopes(scopes) {
		return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
	}
	repositories, err := NormalizeRepositoryScopes(repositoryScopes)
	if err != nil {
		return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
	}
	principalRepositories, err := normalizedPrincipalRepositories(principal)
	if err != nil {
		return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
	}
	record, err := s.store.Preview(ctx, userCodeHash)
	if err != nil {
		return storage.DeviceAuthorization{}, fmt.Errorf("preview device authorization for approval: %w", err)
	}
	if record.OrganizationIDHint != "" && record.OrganizationIDHint != principal.OrgID {
		return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
	}
	organizationWide := slices.Equal(repositories, []string{"*"})
	if organizationWide {
		if !slices.Equal(principalRepositories, []string{"*"}) {
			return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
		}
	} else if hasRepositoryWildcard(repositories) || !repositoriesWithinGrant(principalRepositories, repositories) {
		return storage.DeviceAuthorization{}, ErrInvalidDeviceFlow
	}
	record, err = s.store.Approve(ctx, userCodeHash, storage.DeviceAuthorizationGrant{
		OrgID:                         principal.OrgID,
		RepositoryScopes:              repositories,
		Scopes:                        slices.Clone(scopes),
		ApprovingSubject:              principal.Subject,
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

func (DeviceAuthorizationStart) String() string { return deviceAuthorizationStartRedacted }

func (DeviceAuthorizationStart) GoString() string { return deviceAuthorizationStartRedacted }

func (DeviceAuthorizationStart) LogValue() slog.Value {
	return slog.StringValue(deviceAuthorizationStartRedacted)
}

// validApprovalScopes: an approval authorizes the default pair, optionally
// plus ScopeDataRead (CHAOS-7071), in that order. Anything else is a caller
// defect, refused rather than stored.
func validApprovalScopes(scopes []string) bool {
	return slices.Equal(scopes, oauthDefaultApprovalScopes) ||
		slices.Equal(scopes, append(slices.Clone(oauthDefaultApprovalScopes), ScopeDataRead))
}
