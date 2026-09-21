package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// OAuthConsentAuthority is the consent step of the OAuth login: it starts a
// consent the user approves out of band, reports its state, and issues the
// credential once approved. /authorize, /token and the MCP audience check
// depend only on this interface. DeviceFlowService implements it today (the
// user approves the user code on the web approval page, authenticated by a
// web assertion); an Auth Control Plane session or device implementation can
// replace it without touching those routes.
type OAuthConsentAuthority interface {
	StartForOAuth(ctx context.Context) (OAuthDeviceAuthorization, error)
	StateForOAuth(ctx context.Context, ref storage.DeviceCodeHash) (OAuthDeviceState, error)
	RedeemForResource(ctx context.Context, ref storage.DeviceCodeHash, resource string) (IssuedCredential, error)
}

var _ OAuthConsentAuthority = (*DeviceFlowService)(nil)

// OAuthDeviceAuthorization is a device authorization started on behalf of an
// OAuth authorization-code request. Only the user code leaves this package:
// the raw device code is discarded, so the device grant can never redeem the
// record and only RedeemForResource (keyed by its hash) can.
type OAuthDeviceAuthorization struct {
	UserCode       string
	DeviceCodeHash storage.DeviceCodeHash
	ExpiresAt      time.Time
}

const oauthDeviceAuthorizationRedacted = "auth.OAuthDeviceAuthorization{redacted}"

func (OAuthDeviceAuthorization) String() string   { return oauthDeviceAuthorizationRedacted }
func (OAuthDeviceAuthorization) GoString() string { return oauthDeviceAuthorizationRedacted }

// StartForOAuth starts a device authorization without hints. The approval
// page and the approval rules are the device flow's own.
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
			return OAuthDeviceAuthorization{UserCode: userCode, DeviceCodeHash: record.DeviceCodeHash, ExpiresAt: record.ExpiresAt}, nil
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

// ErrOAuthDeviceNotApproved reports that the device authorization behind an
// authorization code is no longer redeemable.
var ErrOAuthDeviceNotApproved = errors.New("device authorization is not approved")

// RedeemForResource issues the credential of an approved device authorization,
// bound to the protected resource the OAuth request named.
func (s *DeviceFlowService) RedeemForResource(ctx context.Context, hash storage.DeviceCodeHash, resource string) (IssuedCredential, error) {
	if err := s.ready(ctx); err != nil {
		return IssuedCredential{}, err
	}
	if resource == "" || !storage.ValidOAuthResource(resource) {
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
	issued, err := s.redeem(ctx, record, resource)
	if err != nil {
		var pollError *DevicePollError
		if errors.As(err, &pollError) {
			return IssuedCredential{}, ErrOAuthDeviceNotApproved
		}
		return IssuedCredential{}, err
	}
	return issued, nil
}
