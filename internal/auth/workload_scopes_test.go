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
	for _, role := range []string{"read", "ops"} {
		if HasScope(RoleScopes(role), ScopeCredentialLifecycleRead) {
			t.Fatalf("role %s carries the credential listing scope", role)
		}
	}
	if _, ok := knownScopes[ScopeCredentialLifecycleRead]; !ok {
		t.Fatal("the credential listing scope is not a known scope")
	}
	if got := RoleScopes("unknown"); got != nil {
		t.Fatalf("unknown role scopes = %#v, want nil", got)
	}
}
