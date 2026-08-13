package auth

import "testing"

// TestValidWebPermissions_acceptsContextAdmin is the Codex round-1 F5
// probe, permanently locked: a web assertion carrying context:admin (the
// product UI reading its own organization's masked BYO LLM configuration,
// GET /api/v1/context-fabric/model-config) must be accepted. Before this
// fix, context:admin was outside the closed permission vocabulary, which
// made the webAssertionAuth path that route's OpenAPI document already
// advertised dead: every such request would have been rejected here,
// before ever reaching the route's own scope check.
func TestValidWebPermissions_acceptsContextAdmin(t *testing.T) {
	if !validWebPermissions([]string{ScopeContextAdmin}) {
		t.Fatal("validWebPermissions rejected context:admin")
	}
}

func TestValidWebPermissions_stillAcceptsTheOriginalPermissions(t *testing.T) {
	for _, permission := range []string{ScopeContextRead, ScopeEvidenceRead, WebAssertionPermissionCredentialIssue} {
		if !validWebPermissions([]string{permission}) {
			t.Fatalf("validWebPermissions rejected pre-existing permission %q", permission)
		}
	}
}

// TestValidWebPermissions_stillRejectsEpisodeWrite locks that widening the
// vocabulary for context:admin did not accidentally widen it further: a
// web assertion still cannot authenticate an episode write (bearer-only,
// unrelated to this fix -- see TestWebAssertion_cannotAuthenticateEpisodeWrite
// for the route-level proof).
func TestValidWebPermissions_stillRejectsEpisodeWrite(t *testing.T) {
	if validWebPermissions([]string{ScopeEpisodeWrite}) {
		t.Fatal("validWebPermissions accepted episode:write")
	}
}

func TestValidWebPermissions_rejectsUnknownPermission(t *testing.T) {
	if validWebPermissions([]string{"not-a-real-permission"}) {
		t.Fatal("validWebPermissions accepted an unknown permission")
	}
}

func TestValidWebPermissions_rejectsDuplicatePermissions(t *testing.T) {
	if validWebPermissions([]string{ScopeContextAdmin, ScopeContextAdmin}) {
		t.Fatal("validWebPermissions accepted a duplicate permission")
	}
}
