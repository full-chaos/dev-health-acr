package auth

import (
	"context"
	"errors"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// minWorkloadAccessTokenLifetime is the shortest lifetime this issuer will
// mint. Without a floor, a subject token that is merely seconds from its
// own expiry would still pass the bare ">now" check below, producing a
// credential row the client-side cache (internal/sidecar's own
// workloadRefreshMargin) immediately treats as unusable and discards --
// a wasted issuance and a wasted purge-eligible row for no benefit. Set
// above that client margin so a token this issuer actually returns is
// never one the client would reject on arrival.
const minWorkloadAccessTokenLifetime = time.Minute

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
	if expiresAt.Sub(now) < minWorkloadAccessTokenLifetime {
		// The subject token is already expired, or expires too
		// imminently to be worth issuing against -- TokenReview
		// validation should already have caught an outright-expired
		// subject token, so this is mainly the near-expiry edge (see
		// minWorkloadAccessTokenLifetime's own doc comment).
		return IssuedCredential{}, ErrSubjectTokenInvalid
	}
	return s.credentials.Create(ctx, CreateCredentialRequest{
		OrgID: binding.OrgID, Name: "workload:" + binding.BindingID, RepositoryScopes: binding.RepositoryScopes,
		Scopes: scope, CreatedBy: binding.BindingID, ExpiresAt: &expiresAt,
		IssuanceProvenance: storage.CredentialIssuanceProvenanceWorkloadExchange, WorkloadBindingID: binding.BindingID,
	})
}
