package auth

import (
	"errors"
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

func TestResolveRequestedScope_emptyRequestReturnsFullGrant(t *testing.T) {
	binding := WorkloadBinding{Role: "ops"}
	scope, err := ResolveRequestedScope(binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(scope, []string{ScopeContextRead, ScopeEvidenceRead, ScopeEpisodeWrite}) {
		t.Fatalf("scope = %#v", scope)
	}
}

func TestResolveRequestedScope_narrowsToRequestedSubset(t *testing.T) {
	binding := WorkloadBinding{Role: "ops"}
	scope, err := ResolveRequestedScope(binding, []string{ScopeContextRead})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(scope, []string{ScopeContextRead}) {
		t.Fatalf("scope = %#v", scope)
	}
}

func TestResolveRequestedScope_rejectsWideningBeyondTheGrant(t *testing.T) {
	binding := WorkloadBinding{Role: "read"}
	if _, err := ResolveRequestedScope(binding, []string{ScopeEpisodeWrite}); !errors.Is(err, ErrScopeNotGranted) {
		t.Fatalf("error = %v, want ErrScopeNotGranted", err)
	}
}

func TestResolveRequestedScope_rejectsContextAdminEvenForOps(t *testing.T) {
	binding := WorkloadBinding{Role: "ops"}
	if _, err := ResolveRequestedScope(binding, []string{ScopeContextAdmin}); !errors.Is(err, ErrScopeNotGranted) {
		t.Fatalf("error = %v, want ErrScopeNotGranted (context:admin is never implicit in ops)", err)
	}
}

func TestResolveRequestedScope_dedupesRequestedScopes(t *testing.T) {
	binding := WorkloadBinding{Role: "ops"}
	scope, err := ResolveRequestedScope(binding, []string{ScopeContextRead, ScopeContextRead})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(scope, []string{ScopeContextRead}) {
		t.Fatalf("scope = %#v, want deduped", scope)
	}
}

func TestResolveRequestedScope_unknownRoleFails(t *testing.T) {
	if _, err := ResolveRequestedScope(WorkloadBinding{Role: "admin"}, nil); !errors.Is(err, ErrWorkloadBindingNotFound) {
		t.Fatalf("error = %v, want ErrWorkloadBindingNotFound", err)
	}
}
