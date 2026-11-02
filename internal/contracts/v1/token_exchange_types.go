package v1

import (
	"errors"
	"fmt"
	"strings"
)

// RFC 8693 (OAuth 2.0 Token Exchange) constants for the token-exchange
// grant on the existing POST /api/v1/oauth/token endpoint (CHAOS-4013).
// The pre-existing device-code grant on that same endpoint is JSON-encoded
// (DeviceTokenRequest/DeviceTokenResponse) and untouched; this grant is
// form-encoded (application/x-www-form-urlencoded), dispatched by request
// Content-Type.
const (
	TokenExchangeGrantType           = "urn:ietf:params:oauth:grant-type:token-exchange"
	TokenExchangeSubjectTokenTypeJWT = "urn:ietf:params:oauth:token-type:jwt"
	TokenExchangeAccessTokenType     = "urn:ietf:params:oauth:token-type:access_token"

	TokenExchangeResponseSchema   = "token_exchange_response.v1"
	OAuthTokenExchangeErrorSchema = "oauth_token_exchange_error.v1"
)

// TokenExchangeRequest is the decoded form body of an RFC 8693 token
// exchange request. It is built from url.Values by the handler, not
// json.Unmarshal -- there is no JSON wire representation of this request.
type TokenExchangeRequest struct {
	GrantType          string
	SubjectToken       string
	SubjectTokenType   string
	RequestedTokenType string
	Scope              string
	// Resources holds every value of the RFC 8693 section 2.1 resource
	// parameter, in request order. It carries the organization selector,
	// urn:acr:org:<uuid>. No value means the key was absent: no selector.
	// A key that is present but empty is a value, and malformed.
	Resources []string
}

// TokenExchangeResourceOrgPrefix starts the Resource form that selects an
// organization.
const TokenExchangeResourceOrgPrefix = "urn:acr:org:"

// OrgSelector returns the organization named by Resource. present is false
// when no selector was sent.
func (r TokenExchangeRequest) OrgSelector() (orgID string, present bool, err error) {
	if len(r.Resources) == 0 {
		return "", false, nil
	}
	if len(r.Resources) > 1 {
		return "", false, errors.New("resource must be sent at most once")
	}
	orgID, ok := strings.CutPrefix(r.Resources[0], TokenExchangeResourceOrgPrefix)
	if !ok || !canonicalLowerUUID(orgID) {
		return "", false, fmt.Errorf("resource must be %s<lowercase uuid>", TokenExchangeResourceOrgPrefix)
	}
	return orgID, true, nil
}

func canonicalLowerUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c < '0' || c > '9') && (c < 'a' || c > 'f'):
			return false
		}
	}
	return true
}

// Validate checks the RFC 8693 structural shape only. It does not (and
// cannot) validate the subject_token itself -- that is
// authverify.SubjectTokenValidator's job (Kubernetes TokenReview) -- nor
// does it resolve scope narrowing, which happens against the resolved
// authverify.WorkloadBinding after the subject token is validated.
func (r TokenExchangeRequest) Validate() error {
	if r.GrantType != TokenExchangeGrantType {
		return fmt.Errorf("grant_type must be %q", TokenExchangeGrantType)
	}
	if strings.TrimSpace(r.SubjectToken) == "" {
		return errors.New("subject_token is required")
	}
	if r.SubjectTokenType != TokenExchangeSubjectTokenTypeJWT {
		return fmt.Errorf("subject_token_type must be %q", TokenExchangeSubjectTokenTypeJWT)
	}
	if r.RequestedTokenType != "" && r.RequestedTokenType != TokenExchangeAccessTokenType {
		return fmt.Errorf("requested_token_type must be %q when set", TokenExchangeAccessTokenType)
	}
	if _, _, err := r.OrgSelector(); err != nil {
		return err
	}
	return nil
}

// ScopeList splits the RFC 8693 space-delimited scope parameter. An empty
// Scope returns nil (no narrowing requested), matching the RFC's own
// "omitted scope means no narrowing" convention.
func (r TokenExchangeRequest) ScopeList() []string {
	trimmed := strings.TrimSpace(r.Scope)
	if trimmed == "" {
		return nil
	}
	return strings.Fields(trimmed)
}

// TokenExchangeResponse is the RFC 8693 section 2.2.1 JSON response body.
type TokenExchangeResponse struct {
	SchemaVersion   string `json:"schema_version"`
	AccessToken     string `json:"access_token"`
	IssuedTokenType string `json:"issued_token_type"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int    `json:"expires_in"`
	Scope           string `json:"scope,omitempty"`
}

// OAuthTokenExchangeErrorCode is an RFC 6749 section 5.2 / RFC 8693 section
// 2.2.2 token endpoint error code.
type OAuthTokenExchangeErrorCode string

const (
	TokenExchangeErrorInvalidRequest       OAuthTokenExchangeErrorCode = "invalid_request"
	TokenExchangeErrorInvalidGrant         OAuthTokenExchangeErrorCode = "invalid_grant"
	TokenExchangeErrorInvalidScope         OAuthTokenExchangeErrorCode = "invalid_scope"
	TokenExchangeErrorInvalidTarget        OAuthTokenExchangeErrorCode = "invalid_target"
	TokenExchangeErrorUnauthorizedClient   OAuthTokenExchangeErrorCode = "unauthorized_client"
	TokenExchangeErrorUnsupportedGrantType OAuthTokenExchangeErrorCode = "unsupported_grant_type"
)

// OAuthTokenExchangeErrorResponse mirrors OAuthDeviceErrorResponse's shape
// (schema_version + error) for consistency with the sibling grant on the
// same endpoint.
type OAuthTokenExchangeErrorResponse struct {
	SchemaVersion string                      `json:"schema_version"`
	Error         OAuthTokenExchangeErrorCode `json:"error"`
}
