package credentiallifecycle

import (
	"context"
	"errors"
	"reflect"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var (
	ErrInvalidLifecycle = errors.New("credential lifecycle is not initialized")
	ErrInvalidInput     = errors.New("credential lifecycle input is invalid")
)

type Store interface {
	List(context.Context, string) ([]contractsv1.ClientCredential, error)
	GetByID(context.Context, string, string) (contractsv1.ClientCredential, error)
	FindByTokenHash(context.Context, string) (contractsv1.ClientCredential, error)
	TouchLastUsed(context.Context, string, string, string, time.Time) error
}

type Backend struct {
	Store  Store
	Create func(context.Context, CreateInput) (contractsv1.ClientCredential, error)
	Rotate func(context.Context, RotationInput) (contractsv1.ClientCredential, error)
	Revoke func(context.Context, RevocationInput) (contractsv1.ClientCredential, error)
}

type Lifecycle struct {
	backend Backend
}

func New(backend Backend) (*Lifecycle, error) {
	lifecycle := &Lifecycle{backend: backend}
	if err := lifecycle.Validate(); err != nil {
		return nil, err
	}
	return lifecycle, nil
}

func (l *Lifecycle) Validate() error {
	if l == nil || isNil(l.backend.Store) || l.backend.Create == nil || l.backend.Rotate == nil || l.backend.Revoke == nil {
		return ErrInvalidLifecycle
	}
	return nil
}

func (l *Lifecycle) CreateCredential(ctx context.Context, input CreateInput) (contractsv1.ClientCredential, error) {
	if err := l.operationContext(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	normalized, err := normalizeCreate(input)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	return l.backend.Create(ctx, normalized)
}

func (l *Lifecycle) RotateCredential(ctx context.Context, input RotationInput) (contractsv1.ClientCredential, error) {
	if err := l.operationContext(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	normalized, err := normalizeRotation(input)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	return l.backend.Rotate(ctx, normalized)
}

func (l *Lifecycle) RevokeCredential(ctx context.Context, input RevocationInput) (contractsv1.ClientCredential, error) {
	if err := l.operationContext(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	normalized, err := normalizeRevocation(input)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	return l.backend.Revoke(ctx, normalized)
}

func (l *Lifecycle) List(ctx context.Context, orgID string) ([]contractsv1.ClientCredential, error) {
	if err := l.operationContext(ctx); err != nil {
		return nil, err
	}
	normalized, err := normalizeIdentifier("organization", orgID)
	if err != nil {
		return nil, err
	}
	return l.backend.Store.List(ctx, normalized)
}

func (l *Lifecycle) GetByID(ctx context.Context, orgID, credentialID string) (contractsv1.ClientCredential, error) {
	if err := l.operationContext(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	normalizedOrg, err := normalizeIdentifier("organization", orgID)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	normalizedID, err := normalizeCredentialID(credentialID)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	return l.backend.Store.GetByID(ctx, normalizedOrg, normalizedID)
}

func (l *Lifecycle) FindByTokenHash(ctx context.Context, tokenHash string) (contractsv1.ClientCredential, error) {
	if err := l.operationContext(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	normalized, err := normalizeTokenHash(tokenHash)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	return l.backend.Store.FindByTokenHash(ctx, normalized)
}

func (l *Lifecycle) TouchLastUsed(ctx context.Context, credentialID, ip, userAgent string, usedAt time.Time) error {
	if err := l.operationContext(ctx); err != nil {
		return err
	}
	normalizedID, err := normalizeCredentialID(credentialID)
	if err != nil {
		return err
	}
	if usedAt.IsZero() {
		return ErrInvalidInput
	}
	normalizedIP, normalizedUserAgent, err := normalizeUsage(ip, userAgent)
	if err != nil {
		return err
	}
	return l.backend.Store.TouchLastUsed(ctx, normalizedID, normalizedIP, normalizedUserAgent, usedAt.UTC())
}

func (l *Lifecycle) operationContext(ctx context.Context) error {
	if err := l.Validate(); err != nil {
		return err
	}
	if isNil(ctx) {
		return ErrInvalidInput
	}
	return ctx.Err()
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
