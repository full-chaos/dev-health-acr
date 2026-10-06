package memory

import (
	"context"
	"errors"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const deviceAckRevokeActor = "device-credential-ack"

func (s *DeviceAuthorizationStore) Redeem(ctx context.Context, hash storage.DeviceCodeHash, input storage.CredentialCreateInput) (contractsv1.ClientCredential, error) {
	return s.redeem(ctx, hash, input, false)
}

func (s *DeviceAuthorizationStore) RedeemAckable(ctx context.Context, hash storage.DeviceCodeHash, input storage.CredentialCreateInput) (contractsv1.ClientCredential, error) {
	return s.redeem(ctx, hash, input, true)
}

func (s *DeviceAuthorizationStore) redeem(ctx context.Context, hash storage.DeviceCodeHash, input storage.CredentialCreateInput, ackable bool) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.deviceLocked(hash)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	now := s.now().UTC()
	replacing := ""
	switch {
	case record.State == storage.DeviceAuthorizationStateApproved:
	case ackable && record.State == storage.DeviceAuthorizationStateRedeemed && record.CredentialUnacknowledged():
		if !record.AckWindowOpen(now) {
			return contractsv1.ClientCredential{}, storage.NewDeviceAuthorizationConflict(record.State, storage.DeviceConflictAckWindowElapsed)
		}
		replacing = record.RedeemedCredentialID
	case record.State == storage.DeviceAuthorizationStateRedeemed:
		return contractsv1.ClientCredential{}, storage.NewDeviceAuthorizationConflict(record.State, storage.RedeemedConflictReason(record))
	default:
		return contractsv1.ClientCredential{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, record.State, 0)
	}
	if !storage.DeviceAuthorizationCredentialMatches(record, input) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidDeviceAuthorization
	}
	input.IssuanceProvenance = storage.CredentialIssuanceProvenanceDeviceAuthorization
	if replacing != "" {
		if err := s.revokeUnackedLocked(ctx, record); err != nil {
			return contractsv1.ClientCredential{}, err
		}
	}
	credential, err := s.credentials.CreateCredential(ctx, input)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	record.State = storage.DeviceAuthorizationStateRedeemed
	record.RedeemedAt = ptrTime(now)
	record.RedeemedCredentialID = credential.CredentialID
	record.AckRequired = ackable
	record.CredentialAckedAt = nil
	s.byDevice[hash] = cloneDeviceAuthorization(record)
	return credential, nil
}

func (s *DeviceAuthorizationStore) revokeUnackedLocked(ctx context.Context, record storage.DeviceAuthorization) error {
	_, err := s.credentials.RevokeCredential(ctx, storage.CredentialRevocationInput{
		OrgID: record.AuthorizedOrgID, CredentialID: record.RedeemedCredentialID,
		ActorID: deviceAckRevokeActor, ActorType: "system", Reason: "device credential not acknowledged",
	})
	if err != nil && !errors.Is(err, storage.ErrConflict) {
		return err
	}
	return nil
}

func (s *DeviceAuthorizationStore) AcknowledgeCredential(ctx context.Context, orgID, credentialID string) (time.Time, error) {
	if err := s.ready(ctx); err != nil {
		return time.Time{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, record := range s.byDevice {
		if record.RedeemedCredentialID != credentialID || record.AuthorizedOrgID != orgID || !record.AckRequired {
			continue
		}
		if record.CredentialAckedAt != nil {
			return *record.CredentialAckedAt, nil
		}
		now := s.now().UTC()
		if record.RedeemedAt == nil || !now.Before(record.RedeemedAt.Add(storage.DeviceCredentialAckWindow)) {
			return time.Time{}, storage.ErrDeviceAuthorizationNotFound
		}
		credential, err := s.credentials.GetByID(ctx, orgID, credentialID)
		if err != nil || credential.RevokedAt != nil {
			return time.Time{}, storage.ErrDeviceAuthorizationNotFound
		}
		record.CredentialAckedAt = ptrTime(now)
		s.byDevice[hash] = cloneDeviceAuthorization(record)
		return now, nil
	}
	return time.Time{}, storage.ErrDeviceAuthorizationNotFound
}

func (s *DeviceAuthorizationStore) RevokeUnacknowledged(ctx context.Context, limit int) (int, error) {
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, storage.ErrInvalidDeviceAuthorization
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	revoked := 0
	for _, record := range s.byDevice {
		if revoked >= limit {
			break
		}
		if !record.CredentialUnacknowledged() || record.RedeemedAt == nil || now.Before(record.RedeemedAt.Add(storage.DeviceCredentialAckWindow)) {
			continue
		}
		credential, err := s.credentials.GetByID(ctx, record.AuthorizedOrgID, record.RedeemedCredentialID)
		if err != nil {
			return revoked, err
		}
		if credential.RevokedAt != nil {
			continue
		}
		if err := s.revokeUnackedLocked(ctx, record); err != nil {
			return revoked, err
		}
		revoked++
	}
	return revoked, nil
}
