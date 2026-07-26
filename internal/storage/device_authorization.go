package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
)

const (
	DeviceAuthorizationTTL          = 10 * time.Minute
	DeviceAuthorizationPollInterval = 5 * time.Second
)

type DeviceAuthorizationState string

const (
	DeviceAuthorizationStatePending  DeviceAuthorizationState = "pending"
	DeviceAuthorizationStateApproved DeviceAuthorizationState = "approved"
	DeviceAuthorizationStateDenied   DeviceAuthorizationState = "denied"
	DeviceAuthorizationStateExpired  DeviceAuthorizationState = "expired"
	DeviceAuthorizationStateRedeemed DeviceAuthorizationState = "redeemed"
)

type CredentialIssuanceProvenance = credentiallifecycle.IssuanceProvenance

const CredentialIssuanceProvenanceDeviceAuthorization = credentiallifecycle.IssuanceProvenanceDeviceAuthorization

type DeviceCodeHash struct{ value [sha256.Size]byte }

func HashDeviceCode(code string) DeviceCodeHash {
	return DeviceCodeHash{value: sha256.Sum256([]byte(code))}
}

func ParseDeviceCodeHash(value string) (DeviceCodeHash, error) {
	decoded, err := parseCodeHash(value)
	if err != nil {
		return DeviceCodeHash{}, err
	}
	return DeviceCodeHash{value: decoded}, nil
}

func (h DeviceCodeHash) String() string { return hex.EncodeToString(h.value[:]) }

func (h DeviceCodeHash) IsZero() bool { return h == DeviceCodeHash{} }

type UserCodeHash struct{ value [sha256.Size]byte }

func HashUserCode(code string) UserCodeHash {
	return UserCodeHash{value: sha256.Sum256([]byte(code))}
}

func ParseUserCodeHash(value string) (UserCodeHash, error) {
	decoded, err := parseCodeHash(value)
	if err != nil {
		return UserCodeHash{}, err
	}
	return UserCodeHash{value: decoded}, nil
}

func (h UserCodeHash) String() string { return hex.EncodeToString(h.value[:]) }

func (h UserCodeHash) IsZero() bool { return h == UserCodeHash{} }

func parseCodeHash(value string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		return result, ErrInvalidDeviceAuthorization
	}
	copy(result[:], decoded)
	return result, nil
}

func ParseDeviceAuthorizationState(value string) (DeviceAuthorizationState, error) {
	state := DeviceAuthorizationState(value)
	switch state {
	case DeviceAuthorizationStatePending,
		DeviceAuthorizationStateApproved,
		DeviceAuthorizationStateDenied,
		DeviceAuthorizationStateExpired,
		DeviceAuthorizationStateRedeemed:
		return state, nil
	default:
		return "", ErrInvalidDeviceAuthorization
	}
}

func (s DeviceAuthorizationState) Terminal() bool {
	switch s {
	case DeviceAuthorizationStateDenied, DeviceAuthorizationStateExpired, DeviceAuthorizationStateRedeemed:
		return true
	case DeviceAuthorizationStatePending, DeviceAuthorizationStateApproved:
		return false
	default:
		return false
	}
}

type DeviceAuthorization struct {
	DeviceCodeHash                DeviceCodeHash
	UserCodeHash                  UserCodeHash
	State                         DeviceAuthorizationState
	ExpiresAt                     time.Time
	PollInterval                  time.Duration
	LastPollAt                    *time.Time
	AuthorizedOrgID               string
	AuthorizedRepositoryScopes    []string
	AuthorizedScopes              []string
	ApprovingSubject              string
	ApprovingAuthenticationMethod AuthenticationMethod
	CreatedAt                     time.Time
	ApprovedAt                    *time.Time
	RedeemedAt                    *time.Time
	RedeemedCredentialID          string
	IssuanceProvenance            CredentialIssuanceProvenance
}

type DeviceAuthorizationCreateInput struct {
	DeviceCodeHash DeviceCodeHash
	UserCodeHash   UserCodeHash
}

type DeviceAuthorizationGrant struct {
	OrgID                         string
	RepositoryScopes              []string
	Scopes                        []string
	ApprovingSubject              string
	ApprovingAuthenticationMethod AuthenticationMethod
}

type DeviceAuthorizationStore interface {
	Create(context.Context, DeviceAuthorizationCreateInput) (DeviceAuthorization, error)
	GetByDeviceCodeHash(context.Context, DeviceCodeHash) (DeviceAuthorization, error)
	GetByUserCodeHash(context.Context, UserCodeHash) (DeviceAuthorization, error)
	Poll(context.Context, DeviceCodeHash) (DeviceAuthorization, error)
	Approve(context.Context, UserCodeHash, DeviceAuthorizationGrant) (DeviceAuthorization, error)
	Deny(context.Context, UserCodeHash) (DeviceAuthorization, error)
	Redeem(context.Context, DeviceCodeHash, CredentialCreateInput) (contractsv1.ClientCredential, error)
}

type DeviceAuthorizationErrorKind string

const (
	DeviceAuthorizationErrorNotFound    DeviceAuthorizationErrorKind = "not_found"
	DeviceAuthorizationErrorConflict    DeviceAuthorizationErrorKind = "conflict"
	DeviceAuthorizationErrorExpired     DeviceAuthorizationErrorKind = "expired"
	DeviceAuthorizationErrorPollTooSoon DeviceAuthorizationErrorKind = "poll_too_soon"
)

type DeviceAuthorizationError struct {
	Kind       DeviceAuthorizationErrorKind
	State      DeviceAuthorizationState
	RetryAfter time.Duration
}

func (e *DeviceAuthorizationError) Error() string {
	switch e.Kind {
	case DeviceAuthorizationErrorNotFound:
		return "device authorization not found"
	case DeviceAuthorizationErrorConflict:
		return "device authorization state conflict"
	case DeviceAuthorizationErrorExpired:
		return "device authorization expired"
	case DeviceAuthorizationErrorPollTooSoon:
		return "device authorization polled too soon"
	default:
		return "device authorization failed"
	}
}

func (e *DeviceAuthorizationError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case DeviceAuthorizationErrorNotFound:
		return target == ErrDeviceAuthorizationNotFound
	case DeviceAuthorizationErrorConflict:
		return target == ErrDeviceAuthorizationConflict
	case DeviceAuthorizationErrorExpired:
		return target == ErrDeviceAuthorizationExpired
	case DeviceAuthorizationErrorPollTooSoon:
		return target == ErrDeviceAuthorizationPollTooSoon
	default:
		return false
	}
}

var (
	ErrInvalidDeviceAuthorization     = errors.New("device authorization input is invalid")
	ErrDeviceAuthorizationNotFound    = errors.New("device authorization not found")
	ErrDeviceAuthorizationConflict    = errors.New("device authorization state conflict")
	ErrDeviceAuthorizationExpired     = errors.New("device authorization expired")
	ErrDeviceAuthorizationPollTooSoon = errors.New("device authorization polled too soon")
)

func ValidateDeviceAuthorizationGrant(grant DeviceAuthorizationGrant) error {
	if strings.TrimSpace(grant.OrgID) == "" || strings.TrimSpace(grant.ApprovingSubject) == "" {
		return ErrInvalidDeviceAuthorization
	}
	if grant.ApprovingAuthenticationMethod != AuthenticationMethodWebAssertion || len(grant.RepositoryScopes) == 0 || len(grant.Scopes) == 0 {
		return ErrInvalidDeviceAuthorization
	}
	for _, repository := range grant.RepositoryScopes {
		if repository == "*" || strings.HasSuffix(repository, "/*") || strings.TrimSpace(repository) == "" {
			return ErrInvalidDeviceAuthorization
		}
	}
	for _, scope := range grant.Scopes {
		if scope != "context:read" && scope != "evidence:read" {
			return ErrInvalidDeviceAuthorization
		}
	}
	return nil
}

func DeviceAuthorizationCredentialMatches(record DeviceAuthorization, input CredentialCreateInput) bool {
	return input.OrgID == record.AuthorizedOrgID &&
		input.ActorID == record.ApprovingSubject &&
		slices.Equal(input.RepositoryScopes, record.AuthorizedRepositoryScopes) &&
		slices.Equal(input.Scopes, record.AuthorizedScopes)
}

func NewDeviceAuthorizationError(kind DeviceAuthorizationErrorKind, state DeviceAuthorizationState, retryAfter time.Duration) error {
	if retryAfter < 0 {
		retryAfter = 0
	}
	return &DeviceAuthorizationError{Kind: kind, State: state, RetryAfter: retryAfter}
}
