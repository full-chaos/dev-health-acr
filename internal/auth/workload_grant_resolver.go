package auth

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/authverify"
)

type storageGrantResolver struct {
	store storage.WorkloadBindingStore
	orgs  OrganizationPresence
}

// NewGrantResolver adapts a storage.WorkloadBindingStore into an
// authverify.GrantResolver. orgs answers whether a selected organization
// exists for a live_orgs binding (migration 0072); nil (local entitlement mode)
// leaves every live_orgs row unusable, so such an exchange is refused.
func NewGrantResolver(store storage.WorkloadBindingStore, orgs OrganizationPresence) (authverify.GrantResolver, error) {
	if storage.IsNil(store) {
		return nil, errors.New("workload binding store is required")
	}
	if storage.IsNil(orgs) {
		orgs = nil
	}
	return &storageGrantResolver{store: store, orgs: orgs}, nil
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
	var liveOrgs *storage.WorkloadBinding
	for _, candidate := range bindings {
		if candidate.DisabledAt != nil {
			continue
		}
		enabled = append(enabled, candidate)
		if candidate.OrgScope == storage.WorkloadOrgScopeLiveOrgs && liveOrgs == nil {
			row := candidate
			liveOrgs = &row
		}
	}
	if len(enabled) == 0 {
		return authverify.WorkloadBinding{}, authverify.ErrWorkloadBindingNotFound
	}
	selected, hasSelector := WorkloadOrgSelectorFrom(ctx)
	if !hasSelector {
		// A live_orgs row names no organization: only a selector can bind it.
		if len(enabled) > 1 || liveOrgs != nil {
			return authverify.WorkloadBinding{}, ErrWorkloadOrgSelectorRequired
		}
		return r.orgGrant(ctx, enabled[0])
	}
	// A row bound to the selected organization wins over a live_orgs row.
	for _, candidate := range enabled {
		if candidate.OrgScope != storage.WorkloadOrgScopeLiveOrgs && candidate.OrgID == selected {
			return r.orgGrant(ctx, candidate)
		}
	}
	if liveOrgs == nil {
		return authverify.WorkloadBinding{}, ErrWorkloadOrgNotBound
	}
	return r.liveOrgsGrant(ctx, *liveOrgs, selected)
}

// orgGrant is grantFor for a row bound to one organization. A row of any role
// but revoke is bound only when Dev Health, read fresh, says the organization
// is live: a gone or disabled organization is not bound, and an unreadable
// answer is ErrWorkloadOrgCheckUnavailable. A revoke row reaches its
// organization in every state with no read, so its credentials can always be
// revoked. Local entitlement mode (no organization check) reads nothing.
func (r *storageGrantResolver) orgGrant(ctx context.Context, row storage.WorkloadBinding) (authverify.WorkloadBinding, error) {
	if row.Role == "revoke" || r.orgs == nil {
		return grantFor(row), nil
	}
	switch err := r.orgs.OrganizationKnownNow(ctx, row.OrgID); {
	case errors.Is(err, ErrOrganizationGone), errors.Is(err, ErrOrganizationDisabled):
		return authverify.WorkloadBinding{}, ErrWorkloadOrgNotBound
	case err != nil:
		return authverify.WorkloadBinding{}, errors.Join(ErrWorkloadOrgCheckUnavailable, err)
	}
	return grantFor(row), nil
}

// liveOrgsGrant binds a live_orgs row to the selected organization when Dev
// Health, read fresh (never from a cache), says the organization exists. A
// gone or unknown organization is not bound, a disabled one is (the grant only
// revokes); an unreadable answer is
// ErrWorkloadOrgCheckUnavailable, so no credential is issued while the
// organization state is unknown. The grant is fixed here, whatever the row
// holds (migration 0072 also refuses any other role, and any repository scope,
// on such a row): the revoke scope, and liveOrgsRepositoryScopes.
func (r *storageGrantResolver) liveOrgsGrant(ctx context.Context, row storage.WorkloadBinding, selected string) (authverify.WorkloadBinding, error) {
	if row.Role != "revoke" || r.orgs == nil {
		return authverify.WorkloadBinding{}, ErrWorkloadOrgNotBound
	}
	switch err := r.orgs.OrganizationKnownNow(ctx, selected); {
	case errors.Is(err, ErrOrganizationGone):
		return authverify.WorkloadBinding{}, ErrWorkloadOrgNotBound
	case errors.Is(err, ErrOrganizationDisabled):
		// Bound: the revoke grant is how a disabled organization's credentials are revoked.
	case err != nil:
		return authverify.WorkloadBinding{}, errors.Join(ErrWorkloadOrgCheckUnavailable, err)
	}
	return authverify.WorkloadBinding{
		BindingID: row.BindingID, OrgID: selected, GrantedScopes: RoleScopes("revoke"),
		RepositoryScopes: append([]string(nil), liveOrgsRepositoryScopes...),
	}, nil
}

// liveOrgsRepositoryScopes is the repository scope of a live_orgs credential.
// Every credential needs one (NormalizeRepositoryScopes refuses none); with the
// revoke scope alone it reaches no repository data, and the on-behalf revoke
// reads only the credential id and the exchanged organization
// (RevokeOnBehalf). It is the same value the per-org revoke rows hold.
var liveOrgsRepositoryScopes = []string{"*"}

// grantFor maps an organization row to its grant. Role -> scope policy is
// ACR's own (see RoleScopes's doc comment); the shared
// authverify.WorkloadBinding carries the resolved scopes directly, never the
// role string itself.
func grantFor(binding storage.WorkloadBinding) authverify.WorkloadBinding {
	return authverify.WorkloadBinding{
		BindingID: binding.BindingID, OrgID: binding.OrgID, GrantedScopes: RoleScopes(binding.Role),
		RepositoryScopes: append([]string(nil), binding.RepositoryScopes...),
	}
}
