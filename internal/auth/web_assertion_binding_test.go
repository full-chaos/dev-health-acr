package auth

import "testing"

// TestValidWebPermissions_rejectsContextAdmin locks that the retired
// context:admin scope is outside the closed web permission vocabulary.
func TestValidWebPermissions_rejectsContextAdmin(t *testing.T) {
	if validWebPermissions([]string{"context:admin"}) {
		t.Fatal("validWebPermissions accepted the retired context:admin permission")
	}
}

func TestValidWebPermissions_stillAcceptsTheOriginalPermissions(t *testing.T) {
	for _, permission := range []string{ScopeContextRead, ScopeEvidenceRead, WebAssertionPermissionCredentialIssue} {
		if !validWebPermissions([]string{permission}) {
			t.Fatalf("validWebPermissions rejected pre-existing permission %q", permission)
		}
	}
}

// TestValidWebPermissions_stillRejectsEpisodeWrite locks that the vocabulary
// stays closed: a
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
	if validWebPermissions([]string{ScopeContextRead, ScopeContextRead}) {
		t.Fatal("validWebPermissions accepted a duplicate permission")
	}
}
