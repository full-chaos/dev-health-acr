package directread

import (
	"encoding/base64"

	"github.com/google/uuid"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// NamedPrincipal is the principal an internal call names for principal
// (CHAOS-13945): the ops principal wire id of the approving user of a
// user-class credential (browser, OAuth user, native device), the same user
// the per-call access read re-checks. Every other caller names none: a web
// assertion, an org bearer, an operator, a workload or a legacy credential
// carries no user the ops decision point could judge.
func NamedPrincipal(principal storage.Principal) string {
	if principal.AuthenticationMethod != storage.AuthenticationMethodCredential {
		return ""
	}
	switch principal.ClientClass {
	case storage.CredentialClientClassBrowser, storage.CredentialClientClassOAuthUser, storage.CredentialClientClassNativeDevice:
	default:
		return ""
	}
	id, err := uuid.Parse(principal.ApprovingUserID)
	if err != nil || id == uuid.Nil || id.String() != principal.ApprovingUserID {
		return ""
	}
	return "prn_" + base64.RawURLEncoding.EncodeToString(id[:])
}

// wirePrincipalID reports whether value is a principal wire id: prn_ and the
// unpadded base64url of 16 bytes that are not all zero, in canonical form.
func wirePrincipalID(value string) bool {
	if len(value) != len("prn_")+22 || value[:4] != "prn_" {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value[4:])
	if err != nil || len(raw) != 16 {
		return false
	}
	var id uuid.UUID
	copy(id[:], raw)
	return id != uuid.Nil
}
