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
	if got := RoleScopes("unknown"); got != nil {
		t.Fatalf("unknown role scopes = %#v, want nil", got)
	}
	// context:admin must never appear implicitly in ops -- design brief.
	for _, scope := range RoleScopes("ops") {
		if scope == ScopeContextAdmin {
			t.Fatal("ops role must never implicitly grant context:admin")
		}
	}
}
