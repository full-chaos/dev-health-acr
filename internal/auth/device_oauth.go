package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// OAuthConsentAuthority is the consent step of the OAuth login: it starts a
// consent, reports its state, records the signed-in user's decision, and
// issues the credential once approved. /authorize, /authorize/consent, /token
// and the MCP audience check depend only on this interface. DeviceFlowService
// implements it today (the web consent page approves the request behind its
// handle, authenticated by a web assertion); an Auth Control Plane session or
// device implementation can replace it without touching those routes.
type OAuthConsentAuthority interface {
	StartForOAuth(ctx context.Context) (OAuthDeviceAuthorization, error)
	StateForOAuth(ctx context.Context, ref storage.DeviceCodeHash) (OAuthDeviceState, error)
	ApproveForOAuth(ctx context.Context, principal storage.Principal, ref storage.DeviceCodeHash, repositoryScopes, scopes []string) error
	DenyForOAuth(ctx context.Context, principal storage.Principal, ref storage.DeviceCodeHash) error
	RedeemForResource(ctx context.Context, ref storage.DeviceCodeHash, resource string, scopes []string) (IssuedCredential, error)
	// StartDeviceGrant starts a device authorization for RFC 8628's
	// device_authorization endpoint.
	StartDeviceGrant(ctx context.Context) (OAuthDeviceGrantStart, error)
	// PollDeviceGrant reports an RFC 8628 device-code poll: the standard
	// authorization_pending/slow_down/access_denied/expired_token progression,
	// enforcing the poll interval, and an issued credential once approved.
	PollDeviceGrant(ctx context.Context, ref storage.DeviceCodeHash, resource string, scopes []string) (IssuedCredential, error)
}

var _ OAuthConsentAuthority = (*DeviceFlowService)(nil)

// OAuthDeviceAuthorization is a device authorization started on behalf of an
// OAuth authorization-code request. Neither code leaves this package: the raw
// device code and the raw user code are discarded, so the device grant can
// never redeem the record and the typed-code approval page can never approve
// it; only the ForOAuth methods (keyed by the device code hash) can.
type OAuthDeviceAuthorization struct {
	DeviceCodeHash storage.DeviceCodeHash
	ExpiresAt      time.Time
}

const oauthDeviceAuthorizationRedacted = "auth.OAuthDeviceAuthorization{redacted}"

func (OAuthDeviceAuthorization) String() string   { return oauthDeviceAuthorizationRedacted }
func (OAuthDeviceAuthorization) GoString() string { return oauthDeviceAuthorizationRedacted }

// StartForOAuth starts a device authorization without hints. The approval
// rules are the device flow's own.
func (s *DeviceFlowService) StartForOAuth(ctx context.Context) (OAuthDeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return OAuthDeviceAuthorization{}, err
	}
	for range maxDeviceCodeAttempts {
		deviceCode, userCode, err := s.nextCodes()
		if err != nil {
			return OAuthDeviceAuthorization{}, ErrDeviceCodeGeneration
		}
		record, err := s.store.Create(ctx, storage.DeviceAuthorizationCreateInput{
			DeviceCodeHash: storage.HashDeviceCode(deviceCode),
			UserCodeHash:   storage.HashUserCode(userCode),
		})
		if err == nil {
			return OAuthDeviceAuthorization{DeviceCodeHash: record.DeviceCodeHash, ExpiresAt: record.ExpiresAt}, nil
		}
		if !errors.Is(err, storage.ErrDeviceAuthorizationConflict) {
			return OAuthDeviceAuthorization{}, fmt.Errorf("create device authorization: %w", err)
		}
	}
	return OAuthDeviceAuthorization{}, ErrDeviceCodeCollision
}

// OAuthDeviceState is the approval state an OAuth request observes.
type OAuthDeviceState string

const (
	OAuthDeviceStatePending  OAuthDeviceState = "pending"
	OAuthDeviceStateApproved OAuthDeviceState = "approved"
	OAuthDeviceStateDenied   OAuthDeviceState = "denied"
	OAuthDeviceStateExpired  OAuthDeviceState = "expired"
	// OAuthDeviceStateRedeemed means a credential was already issued.
	OAuthDeviceStateRedeemed OAuthDeviceState = "redeemed"
)

// StateForOAuth reads the approval state without polling (no interval and no
// state change), so the browser page can check it as often as it likes.
func (s *DeviceFlowService) StateForOAuth(ctx context.Context, hash storage.DeviceCodeHash) (OAuthDeviceState, error) {
	if err := s.ready(ctx); err != nil {
		return "", err
	}
	record, err := s.store.GetByDeviceCodeHash(ctx, hash)
	if err != nil {
		if errors.Is(err, storage.ErrDeviceAuthorizationNotFound) || errors.Is(err, storage.ErrDeviceAuthorizationExpired) || errors.Is(err, storage.ErrNotFound) {
			return OAuthDeviceStateExpired, nil
		}
		return "", fmt.Errorf("read device authorization: %w", err)
	}
	if !record.State.Terminal() && !record.ExpiresAt.After(s.now().UTC()) {
		return OAuthDeviceStateExpired, nil
	}
	switch record.State {
	case storage.DeviceAuthorizationStatePending:
		return OAuthDeviceStatePending, nil
	case storage.DeviceAuthorizationStateApproved:
		return OAuthDeviceStateApproved, nil
	case storage.DeviceAuthorizationStateDenied:
		return OAuthDeviceStateDenied, nil
	case storage.DeviceAuthorizationStateRedeemed:
		return OAuthDeviceStateRedeemed, nil
	default:
		return OAuthDeviceStateExpired, nil
	}
}

// ApproveForOAuth approves the pending device authorization behind an OAuth
// request for the signed-in web user, with the same org and repository rules
// as the typed-code approval. A record that is no longer pending fails with
// storage.ErrDeviceAuthorizationConflict, so a request is decided at most
// once, with one exception that makes the decision retryable: a record this
// same user already approved, for the same org and the same repositories, is
// accepted again (the approval happened, but attaching the authorization code
// may have failed after it). scopes is the credential scope set the consent
// authorizes (oauthApprovalScopes); it must be a known, non-empty set that
// always holds the default pair.
func (s *DeviceFlowService) ApproveForOAuth(ctx context.Context, principal storage.Principal, hash storage.DeviceCodeHash, repositoryScopes, scopes []string) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	record, err := s.store.GetByDeviceCodeHash(ctx, hash)
	if err != nil {
		return fmt.Errorf("read device authorization for approval: %w", err)
	}
	// An expired record never reaches here: both stores expire it on read
	// (GetByDeviceCodeHash answers ErrDeviceAuthorizationExpired).
	if record.State == storage.DeviceAuthorizationStateApproved {
		if sameOAuthApproval(record, principal, repositoryScopes) && slices.Equal(record.AuthorizedScopes, scopes) {
			return nil
		}
		return storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, record.State, 0)
	}
	_, err = s.approveUserCodeHash(ctx, principal, record.UserCodeHash, repositoryScopes, scopes)
	return err
}

// sameOAuthApproval reports whether an approved record is exactly the
// approval this web principal is asking for again.
func sameOAuthApproval(record storage.DeviceAuthorization, principal storage.Principal, repositoryScopes []string) bool {
	if !validDeviceApprovalPrincipal(principal) {
		return false
	}
	repositories, err := NormalizeRepositoryScopes(repositoryScopes)
	if err != nil {
		return false
	}
	return record.ApprovingAuthenticationMethod == storage.AuthenticationMethodWebAssertion &&
		record.ApprovingSubject == principal.Subject && record.AuthorizedOrgID == principal.OrgID &&
		slices.Equal(record.AuthorizedRepositoryScopes, repositories)
}

// DenyForOAuth denies the pending device authorization behind an OAuth
// request for the signed-in web user; a decided record fails with
// storage.ErrDeviceAuthorizationConflict.
func (s *DeviceFlowService) DenyForOAuth(ctx context.Context, principal storage.Principal, hash storage.DeviceCodeHash) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if !validDeviceApprovalPrincipal(principal) {
		return ErrInvalidDeviceFlow
	}
	record, err := s.store.GetByDeviceCodeHash(ctx, hash)
	if err != nil {
		return fmt.Errorf("read device authorization for denial: %w", err)
	}
	if _, err := s.store.Deny(ctx, record.UserCodeHash); err != nil {
		return fmt.Errorf("deny device authorization: %w", err)
	}
	return nil
}

// ErrOAuthDeviceNotApproved reports that the device authorization behind an
// authorization code is no longer redeemable.
var ErrOAuthDeviceNotApproved = errors.New("device authorization is not approved")

// RedeemForResource issues the credential of an approved device authorization,
// bound to the protected resource the OAuth request named and carrying only
// the scopes it asked for (a non-empty subset of the approved ones).
func (s *DeviceFlowService) RedeemForResource(ctx context.Context, hash storage.DeviceCodeHash, resource string, scopes []string) (IssuedCredential, error) {
	if err := s.ready(ctx); err != nil {
		return IssuedCredential{}, err
	}
	if resource == "" || !storage.ValidOAuthResource(resource) || len(scopes) == 0 {
		return IssuedCredential{}, ErrInvalidDeviceFlow
	}
	record, err := s.store.GetByDeviceCodeHash(ctx, hash)
	if err != nil {
		if errors.Is(err, storage.ErrDeviceAuthorizationNotFound) || errors.Is(err, storage.ErrDeviceAuthorizationExpired) || errors.Is(err, storage.ErrNotFound) {
			return IssuedCredential{}, ErrOAuthDeviceNotApproved
		}
		return IssuedCredential{}, fmt.Errorf("read device authorization: %w", err)
	}
	if record.State != storage.DeviceAuthorizationStateApproved || !record.ExpiresAt.After(s.now().UTC()) {
		return IssuedCredential{}, ErrOAuthDeviceNotApproved
	}
	issued, err := s.redeem(ctx, record, resource, scopes)
	if err != nil {
		var pollError *DevicePollError
		if errors.As(err, &pollError) {
			return IssuedCredential{}, ErrOAuthDeviceNotApproved
		}
		return IssuedCredential{}, err
	}
	return issued, nil
}

// OAuthDeviceGrantStart is a device authorization started for RFC 8628's
// device_authorization endpoint. Unlike StartForOAuth -- whose raw codes are
// discarded so only the OAuth authorization-code flow's handle-based
// session can redeem it -- the raw codes here ARE returned to the caller:
// RFC 8628 requires the client to receive device_code and the user to
// receive user_code directly.
type OAuthDeviceGrantStart struct {
	DeviceCode     string
	UserCode       string
	DeviceCodeHash storage.DeviceCodeHash
	ExpiresAt      time.Time
	Interval       time.Duration
}

const oauthDeviceGrantStartRedacted = "auth.OAuthDeviceGrantStart{redacted}"

func (OAuthDeviceGrantStart) String() string   { return oauthDeviceGrantStartRedacted }
func (OAuthDeviceGrantStart) GoString() string { return oauthDeviceGrantStartRedacted }

// StartDeviceGrant starts a device authorization for RFC 8628's
// device_authorization endpoint. It creates the same kind of
// device_authorizations row StartForOAuth and the legacy Start do, so the
// existing typed-user-code approval page and POST /api/v1/oauth/device_approval
// (device_approval.go) approve it exactly as they approve any other device
// authorization, with no hints (any org, any repository the approving
// principal is entitled to, same as an OAuth authorization-code request).
func (s *DeviceFlowService) StartDeviceGrant(ctx context.Context) (OAuthDeviceGrantStart, error) {
	if err := s.ready(ctx); err != nil {
		return OAuthDeviceGrantStart{}, err
	}
	for range maxDeviceCodeAttempts {
		deviceCode, userCode, err := s.nextCodes()
		if err != nil {
			return OAuthDeviceGrantStart{}, ErrDeviceCodeGeneration
		}
		record, err := s.store.Create(ctx, storage.DeviceAuthorizationCreateInput{
			DeviceCodeHash: storage.HashDeviceCode(deviceCode),
			UserCodeHash:   storage.HashUserCode(userCode),
		})
		if err == nil {
			return OAuthDeviceGrantStart{
				DeviceCode: deviceCode, UserCode: userCode,
				DeviceCodeHash: record.DeviceCodeHash, ExpiresAt: record.ExpiresAt,
				Interval: storage.DeviceAuthorizationPollInterval,
			}, nil
		}
		if !errors.Is(err, storage.ErrDeviceAuthorizationConflict) {
			return OAuthDeviceGrantStart{}, fmt.Errorf("create device authorization: %w", err)
		}
	}
	return OAuthDeviceGrantStart{}, ErrDeviceCodeCollision
}

// PollDeviceGrant reports one RFC 8628 /token poll for a device authorization
// started by StartDeviceGrant. It enforces the store's poll interval like
// the legacy Poll, and issues a credential bound to the given resource and
// scopes like RedeemForResource, distinguishing pending/slow_down/
// denied/expired so /token can answer each with its own RFC 8628 error code
// (RedeemForResource, built for the single-shot authorization-code exchange,
// collapses all of those into one error and never enforces the interval).
func (s *DeviceFlowService) PollDeviceGrant(ctx context.Context, hash storage.DeviceCodeHash, resource string, scopes []string) (IssuedCredential, error) {
	if err := s.ready(ctx); err != nil {
		return IssuedCredential{}, err
	}
	if resource == "" || !storage.ValidOAuthResource(resource) || len(scopes) == 0 {
		return IssuedCredential{}, ErrInvalidDeviceFlow
	}
	record, err := s.store.Poll(ctx, hash)
	if err != nil {
		return IssuedCredential{}, mapDevicePollStoreError(err)
	}
	switch record.State {
	case storage.DeviceAuthorizationStatePending:
		return IssuedCredential{}, newDevicePollError(DevicePollAuthorizationPending, 0)
	case storage.DeviceAuthorizationStateApproved:
		return s.redeem(ctx, record, resource, scopes)
	case storage.DeviceAuthorizationStateDenied:
		return IssuedCredential{}, newDevicePollError(DevicePollAccessDenied, 0)
	case storage.DeviceAuthorizationStateExpired:
		return IssuedCredential{}, newDevicePollError(DevicePollExpiredToken, 0)
	case storage.DeviceAuthorizationStateRedeemed:
		return IssuedCredential{}, newDevicePollError(DevicePollInvalidGrant, 0)
	default:
		return IssuedCredential{}, ErrInvalidDeviceFlow
	}
}
