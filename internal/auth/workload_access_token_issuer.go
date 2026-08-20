package auth

import (
	"context"
	"errors"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type serviceAccessTokenIssuer struct {
	credentials *Service
	now         func() time.Time
}

// NewWorkloadAccessTokenIssuer builds the production AccessTokenIssuer,
// reusing credentials -- the SAME *Service every other credential issuance
// path (self-service create, device flow, rotation) already goes through
// -- so a workload-exchanged token is created, hashed, and stored exactly
// like any other credential.
func NewWorkloadAccessTokenIssuer(credentials *Service, now func() time.Time) (AccessTokenIssuer, error) {
	if credentials == nil {
		return nil, errors.New("credential service is required")
	}
	if now == nil {
		now = time.Now
	}
	return &serviceAccessTokenIssuer{credentials: credentials, now: now}, nil
}

func (s *serviceAccessTokenIssuer) Issue(ctx context.Context, binding WorkloadBinding, scope []string, subjectExpiresAt time.Time) (IssuedCredential, error) {
	now := s.now().UTC()
	expiresAt := now.Add(WorkloadAccessTokenLifetime)
	if subjectExpiresAt.Before(expiresAt) {
		expiresAt = subjectExpiresAt
	}
	if !expiresAt.After(now) {
		// The subject token is already expired, or expires so
		// imminently there is no room left for an access token --
		// TokenReview validation should already have caught an expired
		// subject token, so this is a defensive belt-and-suspenders
		// check, not the primary expiry gate.
		return IssuedCredential{}, ErrSubjectTokenInvalid
	}
	return s.credentials.Create(ctx, CreateCredentialRequest{
		OrgID: binding.OrgID, Name: "workload:" + binding.BindingID, RepositoryScopes: binding.RepositoryScopes,
		Scopes: scope, CreatedBy: binding.BindingID, ExpiresAt: &expiresAt,
		IssuanceProvenance: storage.CredentialIssuanceProvenanceWorkloadExchange, WorkloadBindingID: binding.BindingID,
	})
}
