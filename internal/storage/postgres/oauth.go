package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// OAuthStoreOptions mirrors DeviceAuthorizationStoreOptions's injectable
// clock convention.
type OAuthStoreOptions struct {
	Now func() time.Time
}

// OAuthStore persists dynamic OAuth clients and authorization requests in
// PostgreSQL (acr.oauth_clients, acr.oauth_authorization_requests --
// migration 0040).
type OAuthStore struct {
	DB  *sql.DB
	now func() time.Time
	// afterClientLock and afterDeviceAuthorizationLock are test seams, nil in
	// production: purgeLockedThenRechecked calls the one for the table it is
	// purging between locking its candidate rows and re-checking them, so a test
	// can commit a request inside exactly that window.
	afterClientLock              func()
	afterDeviceAuthorizationLock func()
	// purgeLockTimeout overrides oauthPurgeLockTimeout when non-zero (tests).
	purgeLockTimeout time.Duration
}

func NewOAuthStore(db *sql.DB) (*OAuthStore, error) {
	return NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: time.Now})
}

func NewOAuthStoreWithOptions(db *sql.DB, options OAuthStoreOptions) (*OAuthStore, error) {
	if db == nil || options.Now == nil {
		return nil, storage.ErrInvalidOAuthClient
	}
	return &OAuthStore{DB: db, now: options.Now}, nil
}

func (s *OAuthStore) ready(ctx context.Context) error {
	if s == nil || s.DB == nil || s.now == nil || storage.IsNil(ctx) {
		return storage.ErrInvalidOAuthClient
	}
	return ctx.Err()
}

const oauthClientColumns = `client_id, client_name, redirect_uris, created_at`

const oauthAuthorizationRequestColumns = `
	handle_hash, device_code_hash, client_id, client_kind, redirect_uri,
	code_challenge, resource, scope, state, created_at, expires_at,
	code_hash, code_expires_at, consumed_at, bound_org_id, bound_subject`

// createOAuthAuthorizationRequestSQL inserts a request only while its dynamic
// client's row exists (locked FOR KEY SHARE, which conflicts with the purge's
// FOR UPDATE); a metadata-document request has no client row to check.
const createOAuthAuthorizationRequestSQL = `
WITH client AS (
    SELECT 1 FROM acr.oauth_clients WHERE client_id = $3::text AND $4::text = 'dynamic' FOR KEY SHARE
)
INSERT INTO acr.oauth_authorization_requests (` + oauthAuthorizationRequestColumns + `)
SELECT $1::text, $2::text, $3::text, $4::text, $5::text, $6::text, $7::text, $8::text, $9::text,
       $10::timestamptz, $11::timestamptz, NULL, NULL, NULL, NULL, NULL
WHERE $4::text <> 'dynamic' OR EXISTS (SELECT 1 FROM client)`

// createOAuthDeviceGrantSQL is createOAuthAuthorizationRequestSQL's twin for
// device grants.
const createOAuthDeviceGrantSQL = `
WITH client AS (
    SELECT 1 FROM acr.oauth_clients WHERE client_id = $2::text AND $3::text = 'dynamic' FOR KEY SHARE
)
INSERT INTO acr.oauth_device_grants (` + oauthDeviceGrantColumns + `)
SELECT $1::text, $2::text, $3::text, $4::text, $5::text, $6::timestamptz, $7::timestamptz
WHERE $3::text <> 'dynamic' OR EXISTS (SELECT 1 FROM client)`

const oauthDeviceGrantColumns = `device_code_hash, client_id, client_kind, resource, scope, created_at, expires_at`

func (s *OAuthStore) RegisterClient(ctx context.Context, client storage.OAuthClient) (storage.OAuthClient, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthClient{}, err
	}
	if err := storage.ValidateOAuthClient(client); err != nil {
		return storage.OAuthClient{}, err
	}
	// A device-only client (CHAOS-6233: grant_types=[device_code], no
	// redirect_uris) legitimately has none -- normalized to [] (never nil)
	// here so the returned client matches what's actually stored, same as
	// device_authorization.go's Create does for RepositoryHints.
	if client.RedirectURIs == nil {
		client.RedirectURIs = []string{}
	}
	redirectURIs, err := marshalJSONStringArray(client.RedirectURIs)
	if err != nil {
		return storage.OAuthClient{}, fmt.Errorf("encode oauth client redirect uris: %w", err)
	}
	_, err = s.DB.ExecContext(ctx, `
INSERT INTO acr.oauth_clients (`+oauthClientColumns+`)
VALUES ($1, $2, $3::jsonb, $4)`,
		client.ClientID, client.ClientName, string(redirectURIs), client.CreatedAt,
	)
	if err != nil {
		return storage.OAuthClient{}, fmt.Errorf("register oauth client: %w", sanitizeDatabaseError(err))
	}
	return client, nil
}

func (s *OAuthStore) GetClient(ctx context.Context, clientID string) (storage.OAuthClient, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthClient{}, err
	}
	row := s.DB.QueryRowContext(ctx, `SELECT `+oauthClientColumns+`
FROM acr.oauth_clients WHERE client_id = $1`, clientID)
	client, err := scanOAuthClient(row)
	if err != nil {
		return storage.OAuthClient{}, mapNotFound("get oauth client", err)
	}
	return client, nil
}

func (s *OAuthStore) CreateAuthorizationRequest(ctx context.Context, request storage.OAuthAuthorizationRequest) (storage.OAuthAuthorizationRequest, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	if err := storage.ValidateOAuthAuthorizationRequest(request); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	// The client row is locked FOR KEY SHARE inside the same statement that
	// inserts the request: a concurrent idle-client purge either skips the
	// locked row (SKIP LOCKED) or, having locked it first, makes this SELECT
	// return nothing once it commits, so a stored request can never point at a
	// purged client. A metadata-document client has no row and inserts as-is.
	result, err := s.DB.ExecContext(ctx, createOAuthAuthorizationRequestSQL,
		request.HandleHash.String(), request.DeviceCodeHash.String(), request.ClientID, request.ClientKind,
		request.RedirectURI, request.CodeChallenge, request.Resource, request.Scope, request.State,
		request.CreatedAt, request.ExpiresAt,
	)
	if err != nil {
		sanitized := sanitizeDatabaseError(err)
		if errors.Is(sanitized, storage.ErrConflict) {
			return storage.OAuthAuthorizationRequest{}, sanitized
		}
		return storage.OAuthAuthorizationRequest{}, fmt.Errorf("create oauth authorization request: %w", sanitized)
	}
	if inserted, rowsErr := result.RowsAffected(); rowsErr != nil || inserted != 1 {
		if rowsErr != nil {
			return storage.OAuthAuthorizationRequest{}, fmt.Errorf("create oauth authorization request rows affected: %w", sanitizeDatabaseError(rowsErr))
		}
		return storage.OAuthAuthorizationRequest{}, storage.ErrOAuthClientGone
	}
	return storage.CloneOAuthAuthorizationRequest(request), nil
}

func (s *OAuthStore) GetAuthorizationRequest(ctx context.Context, handle storage.OAuthSecretHash) (storage.OAuthAuthorizationRequest, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	row := s.DB.QueryRowContext(ctx, `SELECT `+oauthAuthorizationRequestColumns+`
FROM acr.oauth_authorization_requests WHERE handle_hash = $1`, handle.String())
	request, err := scanOAuthAuthorizationRequest(row)
	if err != nil {
		return storage.OAuthAuthorizationRequest{}, mapNotFound("get oauth authorization request", err)
	}
	return request, nil
}

// BindAuthorizationRequestUser binds the request to (orgID, subject) in one
// atomic UPDATE: the first caller wins and the same user may repeat it. A
// zero-row result is an unknown handle (ErrNotFound) or another user's
// request (ErrConflict).
func (s *OAuthStore) BindAuthorizationRequestUser(ctx context.Context, handle storage.OAuthSecretHash, orgID, subject string) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if handle.IsZero() || orgID == "" || subject == "" {
		return storage.ErrInvalidOAuthAuthorizationRequest
	}
	result, err := s.DB.ExecContext(ctx, `
UPDATE acr.oauth_authorization_requests
SET bound_org_id = $2, bound_subject = $3
WHERE handle_hash = $1
  AND (bound_subject IS NULL OR (bound_org_id = $2 AND bound_subject = $3))`,
		handle.String(), orgID, subject,
	)
	if err != nil {
		return fmt.Errorf("bind oauth authorization request user: %w", sanitizeDatabaseError(err))
	}
	if affected, err := result.RowsAffected(); err == nil && affected > 0 {
		return nil
	}
	if _, getErr := s.GetAuthorizationRequest(ctx, handle); getErr != nil {
		return getErr
	}
	return storage.ErrConflict
}

// IssueAuthorizationCode attaches the code hash to an unexpired request that
// has no code yet, in one atomic UPDATE. A zero-row result means either the
// handle is unknown or the request is in some other state (expired or
// already issued a code); a follow-up SELECT distinguishes the two only in
// that case.
func (s *OAuthStore) IssueAuthorizationCode(ctx context.Context, handle, code storage.OAuthSecretHash, codeExpiresAt time.Time) (storage.OAuthAuthorizationRequest, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	if handle.IsZero() || code.IsZero() || codeExpiresAt.IsZero() {
		return storage.OAuthAuthorizationRequest{}, storage.ErrInvalidOAuthAuthorizationRequest
	}
	now := s.now().UTC()
	row := s.DB.QueryRowContext(ctx, `
UPDATE acr.oauth_authorization_requests
SET code_hash = $2, code_expires_at = $3
WHERE handle_hash = $1
  AND code_hash IS NULL
  AND expires_at > $4
RETURNING `+oauthAuthorizationRequestColumns,
		handle.String(), code.String(), codeExpiresAt.UTC(), now,
	)
	request, err := scanOAuthAuthorizationRequest(row)
	if err == nil {
		return request, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		sanitized := sanitizeDatabaseError(err)
		if errors.Is(sanitized, storage.ErrConflict) {
			return storage.OAuthAuthorizationRequest{}, sanitized
		}
		return storage.OAuthAuthorizationRequest{}, fmt.Errorf("issue oauth authorization code: %w", sanitized)
	}
	// Zero rows: distinguish unknown handle (ErrNotFound) from any other
	// state -- expired, or a code already issued (ErrConflict).
	if _, getErr := s.GetAuthorizationRequest(ctx, handle); getErr != nil {
		return storage.OAuthAuthorizationRequest{}, getErr
	}
	return storage.OAuthAuthorizationRequest{}, storage.ErrConflict
}

// ConsumeAuthorizationCode atomically marks the code used and returns its
// request, in one atomic UPDATE. Unknown, expired, and already consumed
// codes are all ErrOAuthAuthorizationCodeUnavailable -- deliberately
// indistinguishable, so no follow-up query is needed here (unlike
// IssueAuthorizationCode, which must tell ErrNotFound from ErrConflict).
func (s *OAuthStore) ConsumeAuthorizationCode(ctx context.Context, code storage.OAuthSecretHash) (storage.OAuthAuthorizationRequest, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	if code.IsZero() {
		return storage.OAuthAuthorizationRequest{}, storage.ErrOAuthAuthorizationCodeUnavailable
	}
	now := s.now().UTC()
	row := s.DB.QueryRowContext(ctx, `
UPDATE acr.oauth_authorization_requests
SET consumed_at = $2
WHERE code_hash = $1
  AND consumed_at IS NULL
  AND code_expires_at > $2
RETURNING `+oauthAuthorizationRequestColumns,
		code.String(), now,
	)
	request, err := scanOAuthAuthorizationRequest(row)
	if err == nil {
		return request, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return storage.OAuthAuthorizationRequest{}, storage.ErrOAuthAuthorizationCodeUnavailable
	}
	return storage.OAuthAuthorizationRequest{}, fmt.Errorf("consume oauth authorization code: %w", sanitizeDatabaseError(err))
}

func (s *OAuthStore) CreateDeviceGrant(ctx context.Context, grant storage.OAuthDeviceGrant) (storage.OAuthDeviceGrant, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthDeviceGrant{}, err
	}
	if err := storage.ValidateOAuthDeviceGrant(grant); err != nil {
		return storage.OAuthDeviceGrant{}, err
	}
	result, err := s.DB.ExecContext(ctx, createOAuthDeviceGrantSQL,
		grant.DeviceCodeHash.String(), grant.ClientID, grant.ClientKind, grant.Resource, grant.Scope,
		grant.CreatedAt, grant.ExpiresAt,
	)
	if err != nil {
		sanitized := sanitizeDatabaseError(err)
		if errors.Is(sanitized, storage.ErrConflict) {
			return storage.OAuthDeviceGrant{}, sanitized
		}
		return storage.OAuthDeviceGrant{}, fmt.Errorf("create oauth device grant: %w", sanitized)
	}
	if inserted, rowsErr := result.RowsAffected(); rowsErr != nil || inserted != 1 {
		if rowsErr != nil {
			return storage.OAuthDeviceGrant{}, fmt.Errorf("create oauth device grant rows affected: %w", sanitizeDatabaseError(rowsErr))
		}
		return storage.OAuthDeviceGrant{}, storage.ErrOAuthClientGone
	}
	return grant, nil
}

func (s *OAuthStore) GetDeviceGrant(ctx context.Context, hash storage.DeviceCodeHash) (storage.OAuthDeviceGrant, error) {
	if err := s.ready(ctx); err != nil {
		return storage.OAuthDeviceGrant{}, err
	}
	row := s.DB.QueryRowContext(ctx, `SELECT `+oauthDeviceGrantColumns+`
FROM acr.oauth_device_grants WHERE device_code_hash = $1`, hash.String())
	grant, err := scanOAuthDeviceGrant(row)
	if err != nil {
		return storage.OAuthDeviceGrant{}, mapNotFound("get oauth device grant", err)
	}
	return grant, nil
}

func scanOAuthDeviceGrant(row scanner) (storage.OAuthDeviceGrant, error) {
	var (
		deviceCodeHash string
		grant          storage.OAuthDeviceGrant
	)
	err := row.Scan(&deviceCodeHash, &grant.ClientID, &grant.ClientKind, &grant.Resource, &grant.Scope, &grant.CreatedAt, &grant.ExpiresAt)
	if err != nil {
		return storage.OAuthDeviceGrant{}, err
	}
	parsedDeviceCode, err := storage.ParseDeviceCodeHash(deviceCodeHash)
	if err != nil {
		return storage.OAuthDeviceGrant{}, fmt.Errorf("decode oauth device grant device code: %w", err)
	}
	grant.DeviceCodeHash = parsedDeviceCode
	return grant, nil
}

func scanOAuthClient(row scanner) (storage.OAuthClient, error) {
	var client storage.OAuthClient
	var redirectURIs []byte
	if err := row.Scan(&client.ClientID, &client.ClientName, &redirectURIs, &client.CreatedAt); err != nil {
		return storage.OAuthClient{}, err
	}
	if err := json.Unmarshal(redirectURIs, &client.RedirectURIs); err != nil {
		return storage.OAuthClient{}, fmt.Errorf("decode oauth client redirect uris: %w", err)
	}
	return client, nil
}

func scanOAuthAuthorizationRequest(row scanner) (storage.OAuthAuthorizationRequest, error) {
	var (
		handleHash, deviceCodeHash string
		request                    storage.OAuthAuthorizationRequest
		codeHash                   sql.NullString
		codeExpiresAt              sql.NullTime
		consumedAt                 sql.NullTime
		boundOrgID, boundSubject   sql.NullString
	)
	err := row.Scan(
		&handleHash, &deviceCodeHash, &request.ClientID, &request.ClientKind, &request.RedirectURI,
		&request.CodeChallenge, &request.Resource, &request.Scope, &request.State,
		&request.CreatedAt, &request.ExpiresAt, &codeHash, &codeExpiresAt, &consumedAt, &boundOrgID, &boundSubject,
	)
	if err != nil {
		return storage.OAuthAuthorizationRequest{}, err
	}
	parsedHandle, err := storage.ParseOAuthSecretHash(handleHash)
	if err != nil {
		return storage.OAuthAuthorizationRequest{}, fmt.Errorf("decode oauth authorization request handle: %w", err)
	}
	request.HandleHash = parsedHandle
	parsedDeviceCode, err := storage.ParseDeviceCodeHash(deviceCodeHash)
	if err != nil {
		return storage.OAuthAuthorizationRequest{}, fmt.Errorf("decode oauth authorization request device code: %w", err)
	}
	request.DeviceCodeHash = parsedDeviceCode
	request.BoundOrgID, request.BoundSubject = boundOrgID.String, boundSubject.String
	if codeHash.Valid {
		parsedCode, err := storage.ParseOAuthSecretHash(codeHash.String)
		if err != nil {
			return storage.OAuthAuthorizationRequest{}, fmt.Errorf("decode oauth authorization code hash: %w", err)
		}
		request.CodeHash = &parsedCode
	}
	if codeExpiresAt.Valid {
		expires := codeExpiresAt.Time
		request.CodeExpiresAt = &expires
	}
	if consumedAt.Valid {
		consumed := consumedAt.Time
		request.ConsumedAt = &consumed
	}
	return request, nil
}
