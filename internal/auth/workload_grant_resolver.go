package auth

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type storageGrantResolver struct {
	store storage.WorkloadBindingStore
}

// NewGrantResolver adapts a storage.WorkloadBindingStore into a
// GrantResolver.
func NewGrantResolver(store storage.WorkloadBindingStore) (GrantResolver, error) {
	if storage.IsNil(store) {
		return nil, errors.New("workload binding store is required")
	}
	return &storageGrantResolver{store: store}, nil
}

func (r *storageGrantResolver) Resolve(ctx context.Context, identity SubjectIdentity) (WorkloadBinding, error) {
	binding, err := r.store.Lookup(ctx, storage.WorkloadBindingKey{
		TrustDomain: identity.TrustDomain, Namespace: identity.Namespace,
		ServiceAccountName: identity.ServiceAccountName, ServiceAccountUID: identity.ServiceAccountUID,
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return WorkloadBinding{}, ErrWorkloadBindingNotFound
		}
		return WorkloadBinding{}, err
	}
	// A disabled binding must read identically to "not found" to the
	// caller (design brief: "Revocation: disable binding (stops
	// issuance)") -- disabling must not leak the binding's continued
	// existence to a still-presenting workload.
	if binding.DisabledAt != nil {
		return WorkloadBinding{}, ErrWorkloadBindingNotFound
	}
	return WorkloadBinding{
		BindingID: binding.BindingID, OrgID: binding.OrgID, Role: binding.Role,
		RepositoryScopes: append([]string(nil), binding.RepositoryScopes...),
	}, nil
}
