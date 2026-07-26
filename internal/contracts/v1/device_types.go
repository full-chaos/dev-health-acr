package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	DeviceAuthorizationRequestSchema    = "device_authorization_request.v1"
	DeviceAuthorizationResponseSchema   = "device_authorization_response.v1"
	DeviceTokenRequestSchema            = "device_token_request.v1"
	DeviceTokenResponseSchema           = "device_token_response.v1"
	DeviceApprovalRequestSchema         = "device_approval_request.v1"
	DeviceApprovalResponseSchema        = "device_approval_response.v1"
	DeviceApprovalPreviewRequestSchema  = "device_approval_preview_request.v1"
	DeviceApprovalPreviewResponseSchema = "device_approval_preview_response.v1"
	CredentialRotateRequestSchema       = "credential_rotate_request.v1"
	CredentialRotateResponseSchema      = "credential_rotate_response.v1"
	CredentialRevokeRequestSchema       = "credential_revoke_request.v1"
	CredentialRevokeResponseSchema      = "credential_revoke_response.v1"
	OAuthDeviceErrorSchema              = "oauth_device_error.v1"
	DeviceCodeGrantType                 = "urn:ietf:params:oauth:grant-type:device_code"
)

type DeviceAuthorizationRequest struct {
	SchemaVersion      string    `json:"schema_version"`
	OrganizationIDHint *string   `json:"organization_id_hint,omitempty"`
	RepositoryHints    *[]string `json:"repository_hints,omitempty"`

	organizationIDHintPresent bool
	repositoryHintsPresent    bool
}

func (r *DeviceAuthorizationRequest) UnmarshalJSON(data []byte) error {
	type wire struct {
		SchemaVersion      string    `json:"schema_version"`
		OrganizationIDHint *string   `json:"organization_id_hint"`
		RepositoryHints    *[]string `json:"repository_hints"`
	}
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*r = DeviceAuthorizationRequest{
		SchemaVersion:             decoded.SchemaVersion,
		OrganizationIDHint:        decoded.OrganizationIDHint,
		RepositoryHints:           decoded.RepositoryHints,
		organizationIDHintPresent: fields["organization_id_hint"] != nil,
		repositoryHintsPresent:    fields["repository_hints"] != nil,
	}
	return nil
}

type DeviceAuthorizationResponse struct {
	SchemaVersion   string `json:"schema_version"`
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type DeviceTokenRequest struct {
	SchemaVersion string `json:"schema_version"`
	GrantType     string `json:"grant_type"`
	DeviceCode    string `json:"device_code"`
}

type DeviceTokenResponse struct {
	SchemaVersion string           `json:"schema_version"`
	AccessToken   string           `json:"access_token"`
	TokenType     string           `json:"token_type"`
	ExpiresIn     int              `json:"expires_in"`
	Credential    ClientCredential `json:"credential"`
}

type DeviceApprovalRequest struct {
	SchemaVersion    string   `json:"schema_version"`
	UserCode         string   `json:"user_code"`
	RepositoryScopes []string `json:"repository_scopes"`
}

func (r *DeviceApprovalRequest) UnmarshalJSON(data []byte) error {
	type wire struct {
		SchemaVersion    string   `json:"schema_version"`
		UserCode         string   `json:"user_code"`
		RepositoryScopes []string `json:"repository_scopes"`
	}
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if decoded.SchemaVersion == DeviceApprovalPreviewRequestSchema && fields["repository_scopes"] != nil {
		return fmt.Errorf("device approval preview request forbids repository_scopes")
	}
	*r = DeviceApprovalRequest{SchemaVersion: decoded.SchemaVersion, UserCode: decoded.UserCode, RepositoryScopes: decoded.RepositoryScopes}
	return nil
}

type DeviceApprovalResponse struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
}

type DeviceApprovalPreviewRequest struct {
	SchemaVersion string `json:"schema_version"`
	UserCode      string `json:"user_code"`
}

type DeviceApprovalPreviewResponse struct {
	SchemaVersion      string   `json:"schema_version"`
	OrganizationIDHint string   `json:"organization_id_hint,omitempty"`
	RepositoryHints    []string `json:"repository_hints,omitempty"`
}

type CredentialRotateRequest struct {
	SchemaVersion string `json:"schema_version"`
}

type CredentialRotationReceipt struct {
	SourceCredentialID      string    `json:"source_credential_id"`
	ReplacementCredentialID string    `json:"replacement_credential_id"`
	RollbackUntil           time.Time `json:"rollback_until"`
}

type CredentialRotateResponse struct {
	SchemaVersion string                    `json:"schema_version"`
	AccessToken   string                    `json:"access_token"`
	Credential    ClientCredential          `json:"credential"`
	Receipt       CredentialRotationReceipt `json:"receipt"`
}

type CredentialRevokeRequest struct {
	SchemaVersion string `json:"schema_version"`
}

type CredentialRevokeResponse struct {
	SchemaVersion string           `json:"schema_version"`
	Credential    ClientCredential `json:"credential"`
}

type OAuthDeviceErrorCode string

const (
	OAuthDeviceErrorAuthorizationPending OAuthDeviceErrorCode = "authorization_pending"
	OAuthDeviceErrorSlowDown             OAuthDeviceErrorCode = "slow_down"
	OAuthDeviceErrorAccessDenied         OAuthDeviceErrorCode = "access_denied"
	OAuthDeviceErrorExpiredToken         OAuthDeviceErrorCode = "expired_token"
	OAuthDeviceErrorInvalidGrant         OAuthDeviceErrorCode = "invalid_grant"
)

type OAuthDeviceErrorResponse struct {
	SchemaVersion string               `json:"schema_version"`
	Error         OAuthDeviceErrorCode `json:"error"`
}
