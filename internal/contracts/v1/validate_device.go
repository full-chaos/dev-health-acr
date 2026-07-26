package v1

import (
	"fmt"
	"strings"
	"time"
)

const deviceCredentialLifetime = 30 * 24 * time.Hour

func (r DeviceAuthorizationRequest) Validate() error {
	if r.SchemaVersion != DeviceAuthorizationRequestSchema {
		return fmt.Errorf("device authorization request violates v1 bounds")
	}
	return validateDeviceAuthorizationHints(r.OrganizationIDHint, r.RepositoryHints)
}

func (r DeviceAuthorizationResponse) Validate() error {
	if r.SchemaVersion != DeviceAuthorizationResponseSchema || !stringLengthBetween(r.DeviceCode, 32, 256) || !validDeviceUserCode(r.UserCode) || r.VerificationURI == "" || !optionalURI(r.VerificationURI, 2048) || r.ExpiresIn != 600 || r.Interval != 5 {
		return fmt.Errorf("device authorization response violates v1 bounds")
	}
	return nil
}

func (r DeviceTokenRequest) Validate() error {
	if r.SchemaVersion != DeviceTokenRequestSchema || r.GrantType != DeviceCodeGrantType || !stringLengthBetween(r.DeviceCode, 32, 256) {
		return fmt.Errorf("device token request violates v1 bounds")
	}
	return nil
}

func (r DeviceTokenResponse) Validate() error {
	if r.SchemaVersion != DeviceTokenResponseSchema || !stringLengthBetween(r.AccessToken, 1, 512) || r.TokenType != "Bearer" || r.ExpiresIn != int(deviceCredentialLifetime.Seconds()) {
		return fmt.Errorf("device token response violates v1 bounds")
	}
	return validateDeviceIssuedCredential(r.Credential)
}

func (r DeviceApprovalRequest) Validate() error {
	if r.SchemaVersion != DeviceApprovalRequestSchema || !validDeviceUserCode(r.UserCode) {
		return fmt.Errorf("device approval request violates v1 bounds")
	}
	return validateBoundedRepositoryScopes(r.RepositoryScopes)
}

func (r DeviceApprovalResponse) Validate() error {
	if r.SchemaVersion != DeviceApprovalResponseSchema || r.Status != "approved" {
		return fmt.Errorf("device approval response violates v1 bounds")
	}
	return nil
}

func (r DeviceApprovalPreviewRequest) Validate() error {
	if r.SchemaVersion != DeviceApprovalPreviewRequestSchema || !validDeviceUserCode(r.UserCode) {
		return fmt.Errorf("device approval preview request violates v1 bounds")
	}
	return nil
}

func (r DeviceApprovalPreviewResponse) Validate() error {
	if r.SchemaVersion != DeviceApprovalPreviewResponseSchema {
		return fmt.Errorf("device approval preview response violates v1 bounds")
	}
	return validateDeviceAuthorizationHints(r.OrganizationIDHint, r.RepositoryHints)
}

func (r CredentialRotateRequest) Validate() error {
	if r.SchemaVersion != CredentialRotateRequestSchema {
		return fmt.Errorf("credential rotation request violates v1 bounds")
	}
	return nil
}

func (r CredentialRotateResponse) Validate() error {
	if r.SchemaVersion != CredentialRotateResponseSchema || !stringLengthBetween(r.AccessToken, 1, 512) || r.Receipt.SourceCredentialID == r.Receipt.ReplacementCredentialID || !stringLengthBetween(r.Receipt.SourceCredentialID, 8, 256) || !stringLengthBetween(r.Receipt.ReplacementCredentialID, 8, 256) || r.Receipt.RollbackUntil.IsZero() {
		return fmt.Errorf("credential rotation response violates v1 bounds")
	}
	return validateCredentialMetadata(r.Credential)
}

func (r CredentialRevokeRequest) Validate() error {
	if r.SchemaVersion != CredentialRevokeRequestSchema {
		return fmt.Errorf("credential revocation request violates v1 bounds")
	}
	return nil
}

func (r CredentialRevokeResponse) Validate() error {
	if r.SchemaVersion != CredentialRevokeResponseSchema || r.Credential.RevokedAt == nil {
		return fmt.Errorf("credential revocation response violates v1 bounds")
	}
	return validateCredentialMetadata(r.Credential)
}

func (r OAuthDeviceErrorResponse) Validate() error {
	if r.SchemaVersion != OAuthDeviceErrorSchema || !validOAuthDeviceError(r.Error) {
		return fmt.Errorf("OAuth device error violates v1 bounds")
	}
	return nil
}

func validateDeviceIssuedCredential(credential ClientCredential) error {
	if err := validateCredentialMetadata(credential); err != nil {
		return err
	}
	if credential.ExpiresAt == nil || credential.ExpiresAt.Sub(credential.CreatedAt) != deviceCredentialLifetime || credential.RevokedAt != nil || credential.LastUsedAt != nil || len(credential.Scopes) != 2 || credential.Scopes[0] != "context:read" || credential.Scopes[1] != "evidence:read" {
		return fmt.Errorf("device credential does not satisfy fixed issuance policy")
	}
	return validateBoundedRepositoryScopes(credential.RepositoryScopes)
}

func validateCredentialMetadata(credential ClientCredential) error {
	if credential.SchemaVersion != ClientCredentialSchema || !stringLengthBetween(credential.CredentialID, 8, 256) || !stringLengthBetween(credential.Name, 1, 200) || !clientCredentialTokenPrefixPattern.MatchString(credential.TokenPrefix) || !stringLengthBetween(credential.OrgID, 1, 128) || credential.CreatedAt.IsZero() || !validCredentialScopes(credential.Scopes) || !validCredentialRepositoryScopes(credential.RepositoryScopes) {
		return fmt.Errorf("credential metadata violates v1 bounds")
	}
	return nil
}

func validCredentialRepositoryScopes(repositories []string) bool {
	if repositories == nil || len(repositories) > 100 || !uniqueStrings(repositories) {
		return false
	}
	for _, repository := range repositories {
		if !stringLengthBetween(repository, 0, 512) {
			return false
		}
	}
	return true
}

func validateBoundedRepositoryScopes(repositories []string) error {
	if repositories == nil {
		return fmt.Errorf("repository scopes are required")
	}
	if len(repositories) == 0 || len(repositories) > 100 || !uniqueStrings(repositories) {
		return fmt.Errorf("repository scopes violate v1 bounds")
	}
	for _, repository := range repositories {
		if !stringLengthBetween(repository, 1, 512) || !repositorySlugPattern.MatchString(repository) || strings.ContainsRune(repository, '*') {
			return fmt.Errorf("repository scopes violate v1 bounds")
		}
	}
	return nil
}

func validateDeviceAuthorizationHints(organizationIDHint string, repositoryHints []string) error {
	if organizationIDHint != "" && !stringLengthBetween(organizationIDHint, 1, 128) {
		return fmt.Errorf("device authorization hints violate v1 bounds")
	}
	if repositoryHints == nil {
		return nil
	}
	return validateBoundedRepositoryScopes(repositoryHints)
}

func validDeviceUserCode(value string) bool {
	if len(value) != 8 {
		return false
	}
	for _, character := range value {
		if !((character >= 'A' && character <= 'H') || (character >= 'J' && character <= 'N') || (character >= 'P' && character <= 'Z') || (character >= '2' && character <= '9')) {
			return false
		}
	}
	return true
}

func validCredentialScopes(scopes []string) bool {
	if len(scopes) == 0 || !uniqueStrings(scopes) {
		return false
	}
	for _, scope := range scopes {
		switch scope {
		case "context:read", "evidence:read", "episode:write":
		default:
			return false
		}
	}
	return true
}

func validOAuthDeviceError(value OAuthDeviceErrorCode) bool {
	switch value {
	case OAuthDeviceErrorAuthorizationPending, OAuthDeviceErrorSlowDown, OAuthDeviceErrorAccessDenied, OAuthDeviceErrorExpiredToken, OAuthDeviceErrorInvalidGrant:
		return true
	default:
		return false
	}
}
