package storage

// WithFamilyRevokeDetails adds a revoked browser family's fields to the audit
// metadata of the credential a family revoke names. It keeps every detail
// the call already put there (an organization revoke's reason), so a call
// that carries details and a family reason records both; a call that carries
// none (refresh reuse, sign-out) gets the family fields only, as before.
func WithFamilyRevokeDetails(metadata map[string]any, familyID, familyReason string) map[string]any {
	out := make(map[string]any, len(metadata)+2)
	for key, value := range metadata {
		out[key] = value
	}
	out["family_id"] = familyID
	out["family_revoke_reason"] = familyReason
	return out
}
