// Package orgrevokerows is the one expectation the memory and Postgres cells
// of the organization revoke share for the credential_revoked audit rows of a
// browser family, so the two stores cannot drift apart again.
package orgrevokerows

// NamedBrowserRow is the credential_revoked row of the browser credential an
// organization revoke names: the call's admin actor and organization reason,
// plus the family fields of the family it revokes.
func NamedBrowserRow(reason, familyID string) (actorType string, metadata map[string]any) {
	return "admin", map[string]any{"reason": reason, "family_id": familyID, "family_revoke_reason": "org_deprovisioned"}
}

// SiblingBrowserRow is the credential_revoked row of a sibling the family
// cascade revokes with it: the cascade's fixed user actor and the family
// fields only.
func SiblingBrowserRow(familyID string) (actorType string, metadata map[string]any) {
	return "user", map[string]any{"family_id": familyID, "family_revoke_reason": "org_deprovisioned"}
}
