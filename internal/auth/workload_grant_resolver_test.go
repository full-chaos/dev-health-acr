package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/authverify"
)

type fakeWorkloadBindingStore struct {
	binding storage.WorkloadBinding
	err     error
}

func (f *fakeWorkloadBindingStore) Lookup(context.Context, storage.WorkloadBindingKey) (storage.WorkloadBinding, error) {
	return f.binding, f.err
}

func TestNewGrantResolver_disabledBindingReadsAsNotFound(t *testing.T) {
	disabledAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeWorkloadBindingStore{binding: storage.WorkloadBinding{BindingID: "wlb_1", DisabledAt: &disabledAt}}
	resolver, err := NewGrantResolver(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), authverify.SubjectIdentity{}); !errors.Is(err, authverify.ErrWorkloadBindingNotFound) {
		t.Fatalf("error = %v, want ErrWorkloadBindingNotFound for a disabled binding", err)
	}
}

func TestNewGrantResolver_missingBindingIsNotFound(t *testing.T) {
	store := &fakeWorkloadBindingStore{err: storage.ErrNotFound}
	resolver, err := NewGrantResolver(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), authverify.SubjectIdentity{}); !errors.Is(err, authverify.ErrWorkloadBindingNotFound) {
		t.Fatalf("error = %v, want ErrWorkloadBindingNotFound", err)
	}
}

func TestNewGrantResolver_resolvesRoleToGrantedScopes(t *testing.T) {
	store := &fakeWorkloadBindingStore{binding: storage.WorkloadBinding{BindingID: "wlb_2", OrgID: "org1", Role: "ops", RepositoryScopes: []string{"*"}}}
	resolver, err := NewGrantResolver(store)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := resolver.Resolve(context.Background(), authverify.SubjectIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if binding.BindingID != "wlb_2" || binding.OrgID != "org1" {
		t.Fatalf("binding = %#v", binding)
	}
	want := RoleScopes("ops")
	if len(binding.GrantedScopes) != len(want) {
		t.Fatalf("granted scopes = %#v, want %#v", binding.GrantedScopes, want)
	}
	for i, scope := range want {
		if binding.GrantedScopes[i] != scope {
			t.Fatalf("granted scopes = %#v, want %#v", binding.GrantedScopes, want)
		}
	}
}
