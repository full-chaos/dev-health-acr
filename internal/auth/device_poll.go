package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type DevicePollErrorKind string

const (
	DevicePollAuthorizationPending DevicePollErrorKind = "authorization_pending"
	DevicePollSlowDown             DevicePollErrorKind = "slow_down"
	DevicePollAccessDenied         DevicePollErrorKind = "access_denied"
	DevicePollExpiredToken         DevicePollErrorKind = "expired_token"
	DevicePollInvalidGrant         DevicePollErrorKind = "invalid_grant"
)

var (
	ErrDeviceAuthorizationPending = errors.New("device authorization pending")
	ErrDeviceSlowDown             = errors.New("device authorization polling slowed down")
	ErrDeviceAccessDenied         = errors.New("device authorization access denied")
	ErrDeviceExpired              = errors.New("device authorization expired")
	ErrDeviceInvalidGrant         = errors.New("device authorization grant is invalid")
)

// OAuthDeviceGrantLookup is the narrow capability Poll needs to refuse
// redeeming a device code that belongs to an RFC 8628 device grant (CHAOS-6233)
// through this legacy path. Satisfied by storage.OAuthStore.
type OAuthDeviceGrantLookup interface {
	GetDeviceGrant(ctx context.Context, hash storage.DeviceCodeHash) (storage.OAuthDeviceGrant, error)
}

// NoOAuthDeviceGrants is the explicit OAuthDeviceGrantLookup a caller passes
// to NewDeviceFlowService when OAuth login is not configured in this
// deployment at all -- GetDeviceGrant always reports storage.ErrNotFound, so
// Poll's guard falls through to the ordinary legacy poll path unchanged.
// Deliberately not the zero value of an unexported type and not nil: a
// caller has to name this type to opt out, so "OAuth is genuinely off" can
// never be produced by simply forgetting to wire the real lookup.
type NoOAuthDeviceGrants struct{}

func (NoOAuthDeviceGrants) GetDeviceGrant(context.Context, storage.DeviceCodeHash) (storage.OAuthDeviceGrant, error) {
	return storage.OAuthDeviceGrant{}, storage.ErrNotFound
}

// ErrOAuthDeviceGrantConflict marks a Poll refusal for a device code that
// belongs to an RFC 8628 device grant: the underlying error is still a
// *DevicePollError with the ordinary invalid_grant wire shape, but a caller
// that wants OAuth-login telemetry for this SPECIFIC refusal
// (internal/api/device_routes.go) can detect it with errors.Is.
var ErrOAuthDeviceGrantConflict = errors.New("device code belongs to an oauth device grant")

// ErrOAuthDeviceGrantLookupUnavailable marks a Poll refusal caused by a
// failed attempt to confirm whether a device code belongs to an RFC 8628
// device grant (a storage/dependency failure, not a definite answer) --
// Poll fails CLOSED on this uncertainty rather than proceeding to redeem.
var ErrOAuthDeviceGrantLookupUnavailable = errors.New("oauth device grant lookup unavailable")

type DevicePollError struct {
	Kind       DevicePollErrorKind
	RetryAfter time.Duration
}

func (e *DevicePollError) Error() string {
	if e == nil {
		return string(DevicePollInvalidGrant)
	}
	return string(e.Kind)
}

func (e *DevicePollError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case DevicePollAuthorizationPending:
		return target == ErrDeviceAuthorizationPending
	case DevicePollSlowDown:
		return target == ErrDeviceSlowDown
	case DevicePollAccessDenied:
		return target == ErrDeviceAccessDenied
	case DevicePollExpiredToken:
		return target == ErrDeviceExpired
	case DevicePollInvalidGrant:
		return target == ErrDeviceInvalidGrant
	default:
		return false
	}
}

func (s *DeviceFlowService) Poll(ctx context.Context, deviceCode string) (IssuedCredential, error) {
	if err := s.ready(ctx); err != nil {
		return IssuedCredential{}, err
	}
	deviceCode, ok := NormalizeDeviceCode(deviceCode)
	if !ok {
		return IssuedCredential{}, newDevicePollError(DevicePollInvalidGrant, 0)
	}
	hash := storage.HashDeviceCode(deviceCode)
	record, err := s.store.Poll(ctx, hash)
	if err != nil {
		return IssuedCredential{}, mapDevicePollStoreError(err)
	}
	// A device code started by RFC 8628's POST /device_authorization
	// (CHAOS-6233) must be redeemed only through OAuthService.ExchangeDeviceCode,
	// which binds the credential to the grant's resource and requested scope;
	// this legacy path binds neither. Checked here, on the ALREADY-NORMALIZED,
	// ALREADY-RESOLVED device code -- the one place this poll and the
	// OAuth-aware one necessarily agree on what "the same device code" means,
	// closing the class of defect a separate pre-check (hashing or
	// normalizing even slightly differently from this method) keeps
	// reopening.
	// s.oauthDeviceGrants is guaranteed non-nil (NewDeviceFlowService refuses
	// construction otherwise) -- there is no "skip the guard" branch here by
	// construction; a deployment with no OAuth login configured wires the
	// explicit NoOAuthDeviceGrants{} value, which always misses below.
	if _, grantErr := s.oauthDeviceGrants.GetDeviceGrant(ctx, hash); grantErr == nil {
		return IssuedCredential{}, fmt.Errorf("%w: %w", ErrOAuthDeviceGrantConflict, newDevicePollError(DevicePollInvalidGrant, 0))
	} else if !errors.Is(grantErr, storage.ErrNotFound) {
		// Fail CLOSED: an unconfirmed lookup is an unconfirmed conflict,
		// never "safe to proceed" -- redeeming on this uncertainty is
		// exactly the defect this guard exists to close.
		return IssuedCredential{}, fmt.Errorf("%w: %w", ErrOAuthDeviceGrantLookupUnavailable, grantErr)
	}
	switch record.State {
	case storage.DeviceAuthorizationStatePending:
		return IssuedCredential{}, newDevicePollError(DevicePollAuthorizationPending, 0)
	case storage.DeviceAuthorizationStateApproved:
	case storage.DeviceAuthorizationStateDenied:
		return IssuedCredential{}, newDevicePollError(DevicePollAccessDenied, 0)
	case storage.DeviceAuthorizationStateExpired:
		return IssuedCredential{}, newDevicePollError(DevicePollExpiredToken, 0)
	case storage.DeviceAuthorizationStateRedeemed:
		// The response carrying the credential may have been lost: while the
		// client has not acknowledged it, the store decides (inside its own
		// transaction and clock) whether this retry replaces it.
		if !record.CredentialUnacknowledged() {
			return IssuedCredential{}, newDevicePollError(DevicePollInvalidGrant, 0)
		}
	default:
		return IssuedCredential{}, ErrInvalidDeviceFlow
	}
	return s.redeem(ctx, record, "", nil, true)
}

func (s *DeviceFlowService) redeem(ctx context.Context, record storage.DeviceAuthorization, resource string, scopes []string, ackable bool) (IssuedCredential, error) {
	if scopes == nil {
		scopes = []string{ScopeContextRead, ScopeEvidenceRead}
	}
	expiresAt := s.now().UTC().Add(DeviceCredentialLifetime)
	prepared, err := s.credentials.PrepareCreate(CreateCredentialRequest{
		OrgID:            record.AuthorizedOrgID,
		Name:             deviceAuthorizationCredentialName,
		RepositoryScopes: record.AuthorizedRepositoryScopes,
		Scopes:           scopes,
		CreatedBy:        record.ApprovingSubject,
		ExpiresAt:        &expiresAt,
		Resource:         resource,
	})
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("prepare device credential: %w", err)
	}
	storeRedeem := s.store.Redeem
	if ackable {
		storeRedeem = s.store.RedeemAckable
	}
	credential, err := storeRedeem(ctx, record.DeviceCodeHash, prepared.StorageInput())
	if err != nil {
		return IssuedCredential{}, mapDeviceRedemptionStoreError(err)
	}
	issued, err := prepared.Complete(credential)
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("complete device credential: %w", err)
	}
	return issued, nil
}

func mapDevicePollStoreError(err error) error {
	var stateErr *storage.DeviceAuthorizationError
	if errors.As(err, &stateErr) {
		switch stateErr.Kind {
		case storage.DeviceAuthorizationErrorExpired:
			return newDevicePollError(DevicePollExpiredToken, 0)
		case storage.DeviceAuthorizationErrorPollTooSoon:
			return newDevicePollError(DevicePollSlowDown, stateErr.RetryAfter)
		case storage.DeviceAuthorizationErrorNotFound:
			return newDevicePollError(DevicePollInvalidGrant, 0)
		case storage.DeviceAuthorizationErrorConflict:
			return pollErrorForState(stateErr.State)
		}
	}
	if errors.Is(err, storage.ErrDeviceAuthorizationNotFound) {
		return newDevicePollError(DevicePollInvalidGrant, 0)
	}
	return fmt.Errorf("poll device authorization: %w", err)
}

func mapDeviceRedemptionStoreError(err error) error {
	if errors.Is(err, storage.ErrDeviceAuthorizationNotFound) || errors.Is(err, storage.ErrDeviceAuthorizationConflict) {
		return newDevicePollError(DevicePollInvalidGrant, 0)
	}
	if errors.Is(err, storage.ErrDeviceAuthorizationExpired) {
		return newDevicePollError(DevicePollExpiredToken, 0)
	}
	return fmt.Errorf("redeem device authorization: %w", err)
}

func pollErrorForState(state storage.DeviceAuthorizationState) error {
	switch state {
	case storage.DeviceAuthorizationStatePending:
		return newDevicePollError(DevicePollAuthorizationPending, 0)
	case storage.DeviceAuthorizationStateDenied:
		return newDevicePollError(DevicePollAccessDenied, 0)
	case storage.DeviceAuthorizationStateExpired:
		return newDevicePollError(DevicePollExpiredToken, 0)
	case storage.DeviceAuthorizationStateApproved, storage.DeviceAuthorizationStateRedeemed:
		return newDevicePollError(DevicePollInvalidGrant, 0)
	default:
		return newDevicePollError(DevicePollInvalidGrant, 0)
	}
}

func newDevicePollError(kind DevicePollErrorKind, retryAfter time.Duration) error {
	if retryAfter < 0 {
		retryAfter = 0
	}
	return &DevicePollError{Kind: kind, RetryAfter: retryAfter}
}

// ErrDeviceCredentialAckRejected is returned when an acknowledgement does not
// name the caller's own credential or no unacknowledged redemption exists.
var ErrDeviceCredentialAckRejected = errors.New("device credential acknowledgement rejected")

// AcknowledgeCredential records that the caller stored the credential it
// authenticated with. The caller can acknowledge only its own credential: the
// bearer proves possession, and the id in the body must match it.
func (s *DeviceFlowService) AcknowledgeCredential(ctx context.Context, principal storage.Principal, credentialID string) (time.Time, error) {
	if err := s.ready(ctx); err != nil {
		return time.Time{}, err
	}
	if principal.AuthenticationMethod != storage.AuthenticationMethodCredential || principal.CredentialID == "" || principal.CredentialID != credentialID {
		return time.Time{}, ErrDeviceCredentialAckRejected
	}
	ackedAt, err := s.store.AcknowledgeCredential(ctx, principal.OrgID, credentialID)
	if errors.Is(err, storage.ErrDeviceAuthorizationNotFound) {
		return time.Time{}, ErrDeviceCredentialAckRejected
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("acknowledge device credential: %w", err)
	}
	return ackedAt, nil
}
