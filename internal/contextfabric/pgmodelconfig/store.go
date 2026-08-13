// Package pgmodelconfig is the production Postgres store for an
// organization's per-organization BYO LLM configuration (CHAOS-3775). The
// caller owns database construction; this package never parses or logs
// DSNs (repository convention, internal/storage/AGENTS.md).
//
// This package is the only place a decrypted org credential is read back
// out of storage. ResolveOrgModelConfig is the ONLY method that returns
// plaintext, and it exists for internal/contextfabric/modelruntimeresolver
// to build a modelprovider.Config -- never for an API response.
// GetOrgModelConfig and UpsertOrgModelConfig, the methods the HTTP layer
// calls, only ever return contractsv1.ContextFabricOrgModelConfig, whose
// CredentialMasked field is display-only (AC-3775-4).
package pgmodelconfig

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelconfigcrypto"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/jackc/pgx/v5/pgconn"
)

// Store is the production org model config store. It implements
// contextfabric.OrgModelConfigStore and contextfabric.OrgModelConfigResolver.
type Store struct {
	db     *sql.DB
	cipher *modelconfigcrypto.Cipher
	now    func() time.Time
}

var (
	_ contextfabric.OrgModelConfigStore    = (*Store)(nil)
	_ contextfabric.OrgModelConfigResolver = (*Store)(nil)
)

// NewStore builds a Store around a caller-owned *sql.DB and cipher. cipher
// must be non-nil: a deployment that has not configured
// ACR_CONTEXT_FABRIC_CREDENTIAL_ENCRYPTION_KEYS must not construct this
// store at all (composition leaves the per-organization write route
// disabled instead -- see internal/runtime/hosted), rather than accepting
// writes it cannot safely seal.
func NewStore(db *sql.DB, cipher *modelconfigcrypto.Cipher) (*Store, error) {
	if db == nil {
		return nil, errors.New("pgmodelconfig: store requires a database")
	}
	if cipher == nil {
		return nil, errors.New("pgmodelconfig: store requires a credential cipher")
	}
	return &Store{db: db, cipher: cipher, now: time.Now}, nil
}

// UpsertOrgModelConfig replaces the whole configuration for
// principal.OrgID (full replace, not a partial patch -- see
// ContextFabricOrgModelConfigWriteRequest's doc comment). The credential is
// sealed before it ever reaches the INSERT/UPDATE statement.
func (s *Store) UpsertOrgModelConfig(ctx context.Context, principal storage.Principal, request contractsv1.ContextFabricOrgModelConfigWriteRequest) (contractsv1.ContextFabricOrgModelConfig, error) {
	if err := s.ready(); err != nil {
		return contractsv1.ContextFabricOrgModelConfig{}, err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return contractsv1.ContextFabricOrgModelConfig{}, errors.New("pgmodelconfig: organization is required")
	}
	if err := request.Validate(); err != nil {
		return contractsv1.ContextFabricOrgModelConfig{}, fmt.Errorf("pgmodelconfig: invalid configuration: %w", err)
	}
	// AAD binds this ciphertext to orgID (modelconfigcrypto.CredentialAAD):
	// a ciphertext that ever ended up under a different org_id row -- a bad
	// migration, a hand-edited UPDATE, a copy-paste in an admin tool --
	// fails to decrypt rather than silently opening under the wrong
	// organization's identity.
	ciphertext, kid, err := s.cipher.Encrypt(request.Credential, modelconfigcrypto.CredentialAAD(orgID))
	if err != nil {
		return contractsv1.ContextFabricOrgModelConfig{}, fmt.Errorf("pgmodelconfig: seal credential: %w", err)
	}
	now := s.now().UTC()
	// generation is never listed in the INSERT column list, so every
	// execution of this statement (whether it inserts or hits the ON
	// CONFLICT path) picks up a fresh value from its column DEFAULT
	// (nextval on a table-wide sequence, not a per-row counter) --
	// PostgreSQL evaluates column defaults while constructing the
	// candidate row before the conflict is even checked, so EXCLUDED.generation
	// already holds that fresh value on the update path too. This is the
	// Codex round-1 F3/F4 fix: a monotonic cache key that can never repeat,
	// including across a DELETE followed by a fresh INSERT for the same
	// org_id. See the migration's comment for the full reasoning.
	row := s.db.QueryRowContext(ctx, `
INSERT INTO acr.context_fabric_org_model_config
    (org_id, provider, base_url, model, fallback_model, credential_ciphertext, credential_kid, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
ON CONFLICT (org_id) DO UPDATE SET
    provider = EXCLUDED.provider,
    base_url = EXCLUDED.base_url,
    model = EXCLUDED.model,
    fallback_model = EXCLUDED.fallback_model,
    credential_ciphertext = EXCLUDED.credential_ciphertext,
    credential_kid = EXCLUDED.credential_kid,
    generation = EXCLUDED.generation,
    updated_at = EXCLUDED.updated_at
RETURNING created_at, updated_at`,
		orgID, request.Provider, request.BaseURL, request.Model, request.FallbackModel, ciphertext, kid, now)
	var createdAt, updatedAt time.Time
	if err := row.Scan(&createdAt, &updatedAt); err != nil {
		return contractsv1.ContextFabricOrgModelConfig{}, fmt.Errorf("pgmodelconfig: upsert configuration: %w", sanitizeError(err))
	}
	return contractsv1.ContextFabricOrgModelConfig{
		SchemaVersion:    contractsv1.ContextFabricOrgModelConfigSchema,
		OrgID:            orgID,
		Provider:         request.Provider,
		BaseURL:          request.BaseURL,
		Model:            request.Model,
		FallbackModel:    request.FallbackModel,
		CredentialMasked: contractsv1.MaskContextFabricOrgModelCredential(request.Credential),
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
	}, nil
}

// GetOrgModelConfig returns the masked configuration for principal.OrgID,
// or contextfabric.ErrOrgModelConfigNotFound if the organization has none.
func (s *Store) GetOrgModelConfig(ctx context.Context, principal storage.Principal) (contractsv1.ContextFabricOrgModelConfig, error) {
	if err := s.ready(); err != nil {
		return contractsv1.ContextFabricOrgModelConfig{}, err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return contractsv1.ContextFabricOrgModelConfig{}, contextfabric.ErrOrgModelConfigNotFound
	}
	row := s.db.QueryRowContext(ctx, `
SELECT provider, base_url, model, fallback_model, credential_ciphertext, credential_kid, created_at, updated_at
FROM acr.context_fabric_org_model_config
WHERE org_id = $1`, orgID)
	var provider, baseURL, model, fallbackModel, kid string
	var ciphertext []byte
	var createdAt, updatedAt time.Time
	switch err := row.Scan(&provider, &baseURL, &model, &fallbackModel, &ciphertext, &kid, &createdAt, &updatedAt); {
	case errors.Is(err, sql.ErrNoRows):
		return contractsv1.ContextFabricOrgModelConfig{}, contextfabric.ErrOrgModelConfigNotFound
	case err != nil:
		return contractsv1.ContextFabricOrgModelConfig{}, fmt.Errorf("pgmodelconfig: get configuration: %w", sanitizeError(err))
	}
	credential, err := s.cipher.Decrypt(ciphertext, kid, modelconfigcrypto.CredentialAAD(orgID))
	if err != nil {
		// The stored ciphertext itself is unreadable (e.g. its key id was
		// retired, or -- ErrDecryptFailed via AAD mismatch -- this row's
		// org_id no longer matches the identity the ciphertext was sealed
		// under). The masked display value degrades to a fixed
		// placeholder rather than failing the whole read: an operator
		// viewing their configuration should still see the provider/model
		// they set, even though the credential itself must be re-entered.
		return contractsv1.ContextFabricOrgModelConfig{
			SchemaVersion:    contractsv1.ContextFabricOrgModelConfigSchema,
			OrgID:            orgID,
			Provider:         provider,
			BaseURL:          baseURL,
			Model:            model,
			FallbackModel:    fallbackModel,
			CredentialMasked: "unavailable",
			CreatedAt:        createdAt,
			UpdatedAt:        updatedAt,
		}, nil
	}
	return contractsv1.ContextFabricOrgModelConfig{
		SchemaVersion:    contractsv1.ContextFabricOrgModelConfigSchema,
		OrgID:            orgID,
		Provider:         provider,
		BaseURL:          baseURL,
		Model:            model,
		FallbackModel:    fallbackModel,
		CredentialMasked: contractsv1.MaskContextFabricOrgModelCredential(credential),
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
	}, nil
}

// DeleteOrgModelConfig removes principal.OrgID's configuration, so its next
// request falls through to the deployment default (AC-3775-3). It is
// idempotent: deleting an organization with no configuration is not an
// error.
func (s *Store) DeleteOrgModelConfig(ctx context.Context, principal storage.Principal) error {
	if err := s.ready(); err != nil {
		return err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return errors.New("pgmodelconfig: organization is required")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM acr.context_fabric_org_model_config WHERE org_id = $1`, orgID); err != nil {
		return fmt.Errorf("pgmodelconfig: delete configuration: %w", sanitizeError(err))
	}
	return nil
}

// ResolveOrgModelConfig returns the decrypted configuration for orgID, or
// (zero, false, nil) when the organization has none -- the caller falls
// through to the deployment default in that case, per AC-3775-3. A non-nil
// error means the organization DOES have a configuration but it could not
// be read (decryption failure); the caller must treat that as unavailable
// for this organization specifically, never as "no configuration".
func (s *Store) ResolveOrgModelConfig(ctx context.Context, orgID string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	if err := s.ready(); err != nil {
		return contextfabric.ResolvedOrgModelConfig{}, false, err
	}
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return contextfabric.ResolvedOrgModelConfig{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
SELECT provider, base_url, model, fallback_model, credential_ciphertext, credential_kid, generation, updated_at
FROM acr.context_fabric_org_model_config
WHERE org_id = $1`, orgID)
	var config contextfabric.ResolvedOrgModelConfig
	var ciphertext []byte
	var kid string
	switch err := row.Scan(&config.Provider, &config.BaseURL, &config.Model, &config.FallbackModel, &ciphertext, &kid, &config.Generation, &config.UpdatedAt); {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.ResolvedOrgModelConfig{}, false, nil
	case err != nil:
		return contextfabric.ResolvedOrgModelConfig{}, false, fmt.Errorf("pgmodelconfig: resolve configuration: %w", sanitizeError(err))
	}
	credential, err := s.cipher.Decrypt(ciphertext, kid, modelconfigcrypto.CredentialAAD(orgID))
	if err != nil {
		return contextfabric.ResolvedOrgModelConfig{}, true, fmt.Errorf("pgmodelconfig: decrypt credential for organization %s: %w", orgID, err)
	}
	config.Credential = credential
	return config, true, nil
}

func (s *Store) ready() error {
	if s == nil || s.db == nil || s.cipher == nil || s.now == nil {
		return errors.New("pgmodelconfig: store is not configured")
	}
	return nil
}

func sanitizeError(err error) error {
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return storage.ErrConflict
	}
	return storage.ErrUnavailable
}
