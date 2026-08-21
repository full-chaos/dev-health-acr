package auth

import (
	"context"
	"errors"
	"time"
)

// WorkloadTokenExchangeResult is the successful outcome of an RFC 8693
// exchange: the minted opaque ACR access token, its remaining lifetime in
// seconds, and the (possibly narrowed) granted scope.
type WorkloadTokenExchangeResult struct {
	AccessToken string
	ExpiresIn   int
	Scope       []string
}

// WorkloadTokenExchangeService composes the three CHAOS-4013 seams
// (SubjectTokenValidator, GrantResolver, AccessTokenIssuer) into the one
// operation the HTTP handler needs. It is the concrete type wired into
// api.RuntimeDependencies.WorkloadTokenExchange.
type WorkloadTokenExchangeService struct {
	validator SubjectTokenValidator
	resolver  GrantResolver
	issuer    AccessTokenIssuer
}

func NewWorkloadTokenExchangeService(validator SubjectTokenValidator, resolver GrantResolver, issuer AccessTokenIssuer) (*WorkloadTokenExchangeService, error) {
	if validator == nil || resolver == nil || issuer == nil {
		return nil, errors.New("workload token exchange requires a validator, resolver, and issuer")
	}
	return &WorkloadTokenExchangeService{validator: validator, resolver: resolver, issuer: issuer}, nil
}

// Exchange runs the full CHAOS-4013 flow: validate the subject token
// (TokenReview), resolve its declarative binding, narrow scope, and issue
// an access token capped at the subject token's own expiry. Every error
// this returns is one of ErrSubjectTokenInvalid, ErrWorkloadBindingNotFound,
// or ErrScopeNotGranted (mapped to specific RFC 8693 wire error codes by
// the caller), or an opaque infrastructure error (mapped to a generic
// upstream_unavailable by the caller) -- see
// internal/api/token_exchange_routes.go.
func (s *WorkloadTokenExchangeService) Exchange(ctx context.Context, subjectToken string, requestedScope []string) (WorkloadTokenExchangeResult, error) {
	identity, err := s.validator.Validate(ctx, subjectToken)
	if err != nil {
		return WorkloadTokenExchangeResult{}, err
	}
	binding, err := s.resolver.Resolve(ctx, identity)
	if err != nil {
		return WorkloadTokenExchangeResult{}, err
	}
	scope, err := ResolveRequestedScope(binding, requestedScope)
	if err != nil {
		return WorkloadTokenExchangeResult{}, err
	}
	issued, err := s.issuer.Issue(ctx, binding, scope, identity.ExpiresAt)
	if err != nil {
		return WorkloadTokenExchangeResult{}, err
	}
	expiresIn := 0
	if issued.Credential.ExpiresAt != nil {
		if remaining := time.Until(*issued.Credential.ExpiresAt); remaining > 0 {
			expiresIn = int(remaining / time.Second)
		}
	}
	return WorkloadTokenExchangeResult{AccessToken: issued.Token, ExpiresIn: expiresIn, Scope: scope}, nil
}
