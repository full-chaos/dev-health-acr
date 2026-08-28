package auth

// RoleScopes maps a WorkloadBinding's role to its fixed scope set (design
// brief: "read = context:read + evidence:read; ops = read +
// episode:write; context:admin stays an explicit overlay, never implicit
// in ops"). An unrecognized role returns nil -- callers building an
// authverify.WorkloadBinding treat a nil/empty GrantedScopes as
// authverify.ErrWorkloadBindingNotFound rather than granting nothing
// silently (see authverify.ResolveRequestedScope).
//
// This is ACR's own role-to-scope policy and deliberately stays in acr
// rather than moving to the shared authverify library: query-api will
// define its own scope vocabulary and role mapping.
func RoleScopes(role string) []string {
	switch role {
	case "read":
		return []string{ScopeContextRead, ScopeEvidenceRead}
	case "ops":
		return []string{ScopeContextRead, ScopeEvidenceRead, ScopeEpisodeWrite}
	default:
		return nil
	}
}
