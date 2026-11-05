package auth

import (
	"reflect"
	"testing"
)

func TestRoleScopes(t *testing.T) {
	if got := RoleScopes("read"); !reflect.DeepEqual(got, []string{ScopeContextRead, ScopeEvidenceRead}) {
		t.Fatalf("read role scopes = %#v", got)
	}
	if got := RoleScopes("ops"); !reflect.DeepEqual(got, []string{ScopeContextRead, ScopeEvidenceRead, ScopeEpisodeWrite}) {
		t.Fatalf("ops role scopes = %#v", got)
	}
	if got := RoleScopes("reconcile"); !reflect.DeepEqual(got, []string{ScopeCredentialLifecycleRead}) {
		t.Fatalf("reconcile role scopes = %#v, want only the listing scope", got)
	}
	if got := RoleScopes("revoke"); !reflect.DeepEqual(got, []string{ScopeCredentialLifecycleRevoke}) {
		t.Fatalf("revoke role scopes = %#v, want only the revoke scope", got)
	}
	for _, role := range []string{"read", "ops", "revoke"} {
		if HasScope(RoleScopes(role), ScopeCredentialLifecycleRead) {
			t.Fatalf("role %s carries the credential listing scope", role)
		}
	}
	for _, role := range []string{"read", "ops", "reconcile"} {
		if HasScope(RoleScopes(role), ScopeCredentialLifecycleRevoke) {
			t.Fatalf("role %s carries the credential revoke scope", role)
		}
	}
	for _, scope := range []string{ScopeCredentialLifecycleRead, ScopeCredentialLifecycleRevoke} {
		if _, ok := knownScopes[scope]; !ok {
			t.Fatalf("%s is not a known scope", scope)
		}
	}
	if got := RoleScopes("unknown"); got != nil {
		t.Fatalf("unknown role scopes = %#v, want nil", got)
	}
}
