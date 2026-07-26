package memory

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (s *DeviceAuthorizationStore) Redeem(ctx context.Context, hash storage.DeviceCodeHash, input storage.CredentialCreateInput) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.deviceLocked(hash)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if record.State != storage.DeviceAuthorizationStateApproved {
		return contractsv1.ClientCredential{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, record.State, 0)
	}
	if !storage.DeviceAuthorizationCredentialMatches(record, input) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidDeviceAuthorization
	}
	input.IssuanceProvenance = storage.CredentialIssuanceProvenanceDeviceAuthorization
	credential, err := s.credentials.CreateCredential(ctx, input)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	now := s.now().UTC()
	record.State = storage.DeviceAuthorizationStateRedeemed
	record.RedeemedAt = ptrTime(now)
	record.RedeemedCredentialID = credential.CredentialID
	s.byDevice[hash] = cloneDeviceAuthorization(record)
	return credential, nil
}
