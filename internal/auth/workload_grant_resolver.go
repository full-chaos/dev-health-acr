package auth

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/authverify"
)

type storageGrantResolver struct {
	store storage.WorkloadBindingStore
}

// NewGrantResolver adapts a storage.WorkloadBindingStore into an
// authverify.GrantResolver.
func NewGrantResolver(store storage.WorkloadBindingStore) (authverify.GrantResolver, error) {
	if storage.IsNil(store) {
		return nil, errors.New("workload binding store is required")
	}
	return &storageGrantResolver{store: store}, nil
}

func (r *storageGrantResolver) Resolve(ctx context.Context, identity authverify.SubjectIdentity) (authverify.WorkloadBinding, error) {
	bindings, err := r.store.Lookup(ctx, storage.WorkloadBindingKey{
		TrustDomain: identity.TrustDomain, Namespace: identity.Namespace,
		ServiceAccountName: identity.ServiceAccountName, ServiceAccountUID: identity.ServiceAccountUID,
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return authverify.WorkloadBinding{}, authverify.ErrWorkloadBindingNotFound
		}
		return authverify.WorkloadBinding{}, err
	}
	// A disabled binding must read identically to "not found" to the
	// caller (design brief: "Revocation: disable binding (stops
	// issuance)") -- disabling must not leak the binding's continued
	// existence to a still-presenting workload. Disabled rows therefore
	// leave the candidate set before anything is counted or selected.
	enabled := make([]storage.WorkloadBinding, 0, len(bindings))
	for _, candidate := range bindings {
		if candidate.DisabledAt == nil {
			enabled = append(enabled, candidate)
		}
	}
	if len(enabled) == 0 {
		return authverify.WorkloadBinding{}, authverify.ErrWorkloadBindingNotFound
	}
	var binding storage.WorkloadBinding
	if selected, ok := WorkloadOrgSelectorFrom(ctx); ok {
		found := false
		for _, candidate := range enabled {
			if candidate.OrgID == selected {
				binding, found = candidate, true
				break
			}
		}
		if !found {
			return authverify.WorkloadBinding{}, ErrWorkloadOrgNotBound
		}
	} else {
		if len(enabled) > 1 {
			return authverify.WorkloadBinding{}, ErrWorkloadOrgSelectorRequired
		}
		binding = enabled[0]
	}
	// Role -> scope policy is ACR's own (see RoleScopes's doc comment);
	// the shared authverify.WorkloadBinding carries the resolved scopes
	// directly, never the role string itself.
	return authverify.WorkloadBinding{
		BindingID: binding.BindingID, OrgID: binding.OrgID, GrantedScopes: RoleScopes(binding.Role),
		RepositoryScopes: append([]string(nil), binding.RepositoryScopes...),
	}, nil
}
