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
}
