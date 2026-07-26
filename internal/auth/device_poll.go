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
	deviceCode, ok := normalizeDeviceCode(deviceCode)
	if !ok {
		return IssuedCredential{}, newDevicePollError(DevicePollInvalidGrant, 0)
	}
	record, err := s.store.Poll(ctx, storage.HashDeviceCode(deviceCode))
	if err != nil {
		return IssuedCredential{}, mapDevicePollStoreError(err)
	}
	switch record.State {
	case storage.DeviceAuthorizationStatePending:
		return IssuedCredential{}, newDevicePollError(DevicePollAuthorizationPending, 0)
	case storage.DeviceAuthorizationStateApproved:
		return s.redeem(ctx, record)
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

func (s *DeviceFlowService) redeem(ctx context.Context, record storage.DeviceAuthorization) (IssuedCredential, error) {
	expiresAt := s.now().UTC().Add(DeviceCredentialLifetime)
	prepared, err := s.credentials.PrepareCreate(CreateCredentialRequest{
		OrgID:            record.AuthorizedOrgID,
		Name:             deviceAuthorizationCredentialName,
		RepositoryScopes: record.AuthorizedRepositoryScopes,
		Scopes:           []string{ScopeContextRead, ScopeEvidenceRead},
		CreatedBy:        record.ApprovingSubject,
		ExpiresAt:        &expiresAt,
	})
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("prepare device credential: %w", err)
	}
	credential, err := s.store.Redeem(ctx, record.DeviceCodeHash, prepared.StorageInput())
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
