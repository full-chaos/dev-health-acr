package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	TokenPrefix = "fcacr_"

	ScopeContextRead  = "context:read"
	ScopeEvidenceRead = "evidence:read"
	ScopeEpisodeWrite = "episode:write"
)

var (
	ErrInvalidToken        = errors.New("invalid token")
	ErrCredentialExpired   = errors.New("credential expired")
	ErrCredentialRevoked   = errors.New("credential revoked")
	ErrInsufficientScope   = errors.New("insufficient scope")
	ErrRepositoryForbidden = errors.New("repository forbidden")
	ErrRateLimited         = errors.New("authentication rate limited")
	ErrInvalidCredential   = errors.New("invalid credential request")
)

var knownScopes = map[string]struct{}{
	ScopeContextRead:  {},
	ScopeEvidenceRead: {},
	ScopeEpisodeWrite: {},
}

type CreateCredentialRequest struct {
	OrgID            string
	Name             string
	RepositoryScopes []string
	Scopes           []string
	CreatedBy        string
	ExpiresAt        *time.Time
}

type RotateCredentialRequest struct {
	OrgID            string
	CredentialID     string
	Name             string
	RepositoryScopes []string
	Scopes           []string
	CreatedBy        string
	ExpiresAt        *time.Time
	Overlap          time.Duration
}

type IssuedCredential struct {
	Credential contractsv1.ClientCredential `json:"credential"`
	Token      string                       `json:"token"`
}

func normalizeCreateRequest(request CreateCredentialRequest) (CreateCredentialRequest, error) {
	request.OrgID = strings.TrimSpace(request.OrgID)
	request.Name = strings.TrimSpace(request.Name)
	request.CreatedBy = strings.TrimSpace(request.CreatedBy)
	if request.OrgID == "" || request.Name == "" {
		return request, fmt.Errorf("%w: org_id and name are required", ErrInvalidCredential)
	}
	if len(request.Name) > 200 {
		return request, fmt.Errorf("%w: name exceeds 200 characters", ErrInvalidCredential)
	}
	var err error
	request.RepositoryScopes, err = NormalizeRepositoryScopes(request.RepositoryScopes)
	if err != nil {
		return request, err
	}
	request.Scopes, err = normalizeScopes(request.Scopes)
	if err != nil {
		return request, err
	}
	return request, nil
}

func normalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return []string{ScopeContextRead, ScopeEvidenceRead}, nil
	}
	result := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, raw := range scopes {
		scope := strings.TrimSpace(raw)
		if _, ok := knownScopes[scope]; !ok {
			return nil, fmt.Errorf("%w: unknown scope %q", ErrInvalidCredential, scope)
		}
		if _, duplicate := seen[scope]; duplicate {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: at least one scope is required", ErrInvalidCredential)
	}
	return result, nil
}

func HasScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}
