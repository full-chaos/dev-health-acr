package v1

import (
	"fmt"
	"strings"
)

const (
	CredentialRevokeOnBehalfRequestSchema = "credential_revoke_on_behalf_request.v1"

	// The actor types the Auth Control Plane names for an on-behalf revoke.
	CredentialRevokeOnBehalfActorUser  = "user"
	CredentialRevokeOnBehalfActorAdmin = "admin"

	credentialRevokeOnBehalfActorIDMax = 200
)

// CredentialRevokeOnBehalfRequest is the body of the on-behalf revoke: the
// actor the Auth Control Plane authorized, as user (the credential's approving
// user) or admin (an organization admin), and that actor's id. The credential
// is named by the path and the organization by the caller's binding; the body
// names neither.
type CredentialRevokeOnBehalfRequest struct {
	SchemaVersion string `json:"schema_version"`
	ActorType     string `json:"actor_type"`
	ActorID       string `json:"actor_id"`
}

func (r CredentialRevokeOnBehalfRequest) Validate() error {
	if r.SchemaVersion != CredentialRevokeOnBehalfRequestSchema ||
		(r.ActorType != CredentialRevokeOnBehalfActorUser && r.ActorType != CredentialRevokeOnBehalfActorAdmin) ||
		strings.TrimSpace(r.ActorID) != r.ActorID || !stringLengthBetween(r.ActorID, 1, credentialRevokeOnBehalfActorIDMax) {
		return fmt.Errorf("credential on-behalf revocation request violates v1 bounds")
	}
	return nil
}
