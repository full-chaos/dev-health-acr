package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const deviceAuthorizationColumns = `device_code_hash, user_code_hash, state,
       authorized_org_id, authorized_repository_scopes, authorized_scopes,
       approving_subject, approving_authentication_method, created_at, expires_at,
       poll_interval_seconds, last_poll_at, approved_at, redeemed_at,
       redeemed_credential_id, issuance_provenance`

func scanDeviceAuthorization(row scanner) (storage.DeviceAuthorization, error) {
	var (
		deviceHashText, userHashText, stateText, provenanceText string
		orgID, subject, authenticationMethod, credentialID      sql.NullString
		repositoryJSON, scopeJSON                               []byte
		createdAt, expiresAt                                    time.Time
		intervalSeconds                                         int64
		lastPollAt, approvedAt, redeemedAt                      sql.NullTime
	)
	if err := row.Scan(
		&deviceHashText, &userHashText, &stateText,
		&orgID, &repositoryJSON, &scopeJSON,
		&subject, &authenticationMethod, &createdAt, &expiresAt,
		&intervalSeconds, &lastPollAt, &approvedAt, &redeemedAt,
		&credentialID, &provenanceText,
	); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	deviceHash, err := storage.ParseDeviceCodeHash(deviceHashText)
	if err != nil {
		return storage.DeviceAuthorization{}, fmt.Errorf("parse stored device code hash: %w", err)
	}
	userHash, err := storage.ParseUserCodeHash(userHashText)
	if err != nil {
		return storage.DeviceAuthorization{}, fmt.Errorf("parse stored user code hash: %w", err)
	}
	state, err := storage.ParseDeviceAuthorizationState(stateText)
	if err != nil {
		return storage.DeviceAuthorization{}, fmt.Errorf("parse stored device authorization state: %w", err)
	}
	var repositories, scopes []string
	if err := json.Unmarshal(repositoryJSON, &repositories); err != nil {
		return storage.DeviceAuthorization{}, fmt.Errorf("decode stored authorized repositories: %w", err)
	}
	if err := json.Unmarshal(scopeJSON, &scopes); err != nil {
		return storage.DeviceAuthorization{}, fmt.Errorf("decode stored authorized scopes: %w", err)
	}
	provenance := storage.CredentialIssuanceProvenance(provenanceText)
	if provenance != storage.CredentialIssuanceProvenanceDeviceAuthorization {
		return storage.DeviceAuthorization{}, storage.ErrInvalidDeviceAuthorization
	}
	record := storage.DeviceAuthorization{
		DeviceCodeHash:                deviceHash,
		UserCodeHash:                  userHash,
		State:                         state,
		AuthorizedOrgID:               orgID.String,
		AuthorizedRepositoryScopes:    repositories,
		AuthorizedScopes:              scopes,
		ApprovingSubject:              subject.String,
		ApprovingAuthenticationMethod: storage.AuthenticationMethod(authenticationMethod.String),
		CreatedAt:                     createdAt.UTC(),
		ExpiresAt:                     expiresAt.UTC(),
		PollInterval:                  time.Duration(intervalSeconds) * time.Second,
		RedeemedCredentialID:          credentialID.String,
		IssuanceProvenance:            provenance,
	}
	if lastPollAt.Valid {
		record.LastPollAt = cloneTime(&lastPollAt.Time)
	}
	if approvedAt.Valid {
		record.ApprovedAt = cloneTime(&approvedAt.Time)
	}
	if redeemedAt.Valid {
		record.RedeemedAt = cloneTime(&redeemedAt.Time)
	}
	return record, nil
}

func mapDeviceAuthorizationLookup(operation string, record storage.DeviceAuthorization, err error) (storage.DeviceAuthorization, error) {
	if err == nil {
		if record.State == storage.DeviceAuthorizationStateExpired {
			return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorExpired, record.State, 0)
		}
		return record, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return storage.DeviceAuthorization{}, storage.ErrDeviceAuthorizationNotFound
	}
	return storage.DeviceAuthorization{}, fmt.Errorf("%s: %w", operation, sanitizeDatabaseError(err))
}
