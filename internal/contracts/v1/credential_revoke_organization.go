package v1

import (
	"fmt"
	"strings"
)

const (
	CredentialRevokeOrganizationRequestSchema  = "credential_revoke_organization_request.v1"
	CredentialRevokeOrganizationResponseSchema = "credential_revoke_organization_response.v1"

	// The reasons the Auth Control Plane gives for revoking every credential
	// of an organization.
	CredentialRevokeOrganizationReasonDisabled = "organization_disabled"
	CredentialRevokeOrganizationReasonDeleted  = "organization_deleted"

	credentialRevokeOrganizationActorIDMax = 200
)

// CredentialRevokeOrganizationRequest is the body of the organization revoke:
// the admin the Auth Control Plane authorized and why the organization's
// credentials are revoked. The organization is the caller's binding; the body
// never names it.
type CredentialRevokeOrganizationRequest struct {
	SchemaVersion string `json:"schema_version"`
	ActorID       string `json:"actor_id"`
	Reason        string `json:"reason"`
}

func (r CredentialRevokeOrganizationRequest) Validate() error {
	if r.SchemaVersion != CredentialRevokeOrganizationRequestSchema ||
		(r.Reason != CredentialRevokeOrganizationReasonDisabled && r.Reason != CredentialRevokeOrganizationReasonDeleted) ||
		strings.TrimSpace(r.ActorID) != r.ActorID || !stringLengthBetween(r.ActorID, 1, credentialRevokeOrganizationActorIDMax) {
		return fmt.Errorf("credential organization revocation request violates v1 bounds")
	}
	return nil
}

// CredentialRevokeOrganizationResponse answers one organization revoke call.
// Revoked counts the credentials revoked that the caller's binding did not
// mint, CallerRevoked those it did. More is true while credentials the
// caller's binding did not mint remain live; the caller then calls again.
type CredentialRevokeOrganizationResponse struct {
	SchemaVersion string `json:"schema_version"`
	Revoked       int    `json:"revoked"`
	CallerRevoked int    `json:"caller_revoked"`
	More          bool   `json:"more"`
}

func (r CredentialRevokeOrganizationResponse) Validate() error {
	if r.SchemaVersion != CredentialRevokeOrganizationResponseSchema || r.Revoked < 0 || r.CallerRevoked < 0 || (r.More && r.CallerRevoked != 0) {
		return fmt.Errorf("credential organization revocation response violates v1 bounds")
	}
	return nil
}
