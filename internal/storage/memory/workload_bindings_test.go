package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkloadBindingStore_lookupMissReturnsErrNotFound(t *testing.T) {
	store := NewWorkloadBindingStore()
	_, err := store.Lookup(context.Background(), storage.WorkloadBindingKey{TrustDomain: "cluster.local", Namespace: "ns", ServiceAccountName: "sa", ServiceAccountUID: "uid"})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestWorkloadBindingStore_lookupReturnsAnExactlySeededBinding(t *testing.T) {
	store := NewWorkloadBindingStore()
	key := storage.WorkloadBindingKey{TrustDomain: "cluster.local", Namespace: "panel-ns", ServiceAccountName: "panel-read", ServiceAccountUID: "sa-uid-1"}
	seeded := storage.WorkloadBinding{BindingID: "wlb_1", OrgID: "org_1", Role: "read", RepositoryScopes: []string{"*"}}
	store.Put(key, seeded)
	got, err := store.Lookup(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if got.BindingID != "wlb_1" || got.OrgID != "org_1" || got.Role != "read" {
		t.Fatalf("binding = %#v", got)
	}
}

func TestWorkloadBindingStore_lookupIsExactOnEveryFieldOfTheKey(t *testing.T) {
	store := NewWorkloadBindingStore()
	key := storage.WorkloadBindingKey{TrustDomain: "cluster.local", Namespace: "panel-ns", ServiceAccountName: "panel-read", ServiceAccountUID: "sa-uid-1"}
	store.Put(key, storage.WorkloadBinding{BindingID: "wlb_1", OrgID: "org_1", Role: "read"})
	mutations := []storage.WorkloadBindingKey{
		{TrustDomain: "other.cluster", Namespace: key.Namespace, ServiceAccountName: key.ServiceAccountName, ServiceAccountUID: key.ServiceAccountUID},
		{TrustDomain: key.TrustDomain, Namespace: "other-ns", ServiceAccountName: key.ServiceAccountName, ServiceAccountUID: key.ServiceAccountUID},
		{TrustDomain: key.TrustDomain, Namespace: key.Namespace, ServiceAccountName: "other-sa", ServiceAccountUID: key.ServiceAccountUID},
		{TrustDomain: key.TrustDomain, Namespace: key.Namespace, ServiceAccountName: key.ServiceAccountName, ServiceAccountUID: "other-uid"},
	}
	for _, mutated := range mutations {
		if _, err := store.Lookup(context.Background(), mutated); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("lookup(%#v) error = %v, want ErrNotFound -- a stale ServiceAccount UID must not match a rotated one", mutated, err)
		}
	}
}

func TestWorkloadBindingStore_lookupHonorsCancelledContext(t *testing.T) {
	store := NewWorkloadBindingStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Lookup(ctx, storage.WorkloadBindingKey{}); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}

func TestWorkloadBindingStore_putReplacesAnExistingRow(t *testing.T) {
	store := NewWorkloadBindingStore()
	key := storage.WorkloadBindingKey{TrustDomain: "cluster.local", Namespace: "ns", ServiceAccountName: "sa", ServiceAccountUID: "uid"}
	store.Put(key, storage.WorkloadBinding{BindingID: "wlb_1", Role: "read"})
	disabledAt := time.Now()
	store.Put(key, storage.WorkloadBinding{BindingID: "wlb_1", Role: "read", DisabledAt: &disabledAt})
	got, err := store.Lookup(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisabledAt == nil {
		t.Fatal("expected the replaced row's DisabledAt to be visible")
	}
}
