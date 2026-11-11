package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type organizationKnower interface {
	OrganizationKnown(ctx context.Context, orgID string) error
	OrganizationStateNow(ctx context.Context, orgID string) (entitlements.OrganizationState, error)
}

// organizationPresence is the remote entitlement client's organization check; nil in local entitlement mode.
func organizationPresence(provider EntitlementProvider) auth.OrganizationPresence {
	knower, ok := provider.(organizationKnower)
	if !ok || storage.IsNil(knower) {
		return nil
	}
	return entitlementOrganizations{knower: knower}
}

// OrganizationPresenceFor is organizationPresence for the runtime wiring: the
// workload grant resolver reads it for a live_orgs binding. Nil in local
// entitlement mode.
func OrganizationPresenceFor(provider EntitlementProvider) auth.OrganizationPresence {
	return organizationPresence(provider)
}

type entitlementOrganizations struct{ knower organizationKnower }

func (o entitlementOrganizations) OrganizationKnown(ctx context.Context, orgID string) error {
	err := o.knower.OrganizationKnown(ctx, orgID)
	switch {
	case errors.Is(err, entitlements.ErrOrganizationUnknown):
		return fmt.Errorf("%w: %w", auth.ErrOrganizationGone, err)
	case errors.Is(err, entitlements.ErrOrganizationDisabled):
		return fmt.Errorf("%w: %w", auth.ErrOrganizationDisabled, err)
	}
	return err
}

func (o entitlementOrganizations) OrganizationKnownNow(ctx context.Context, orgID string) error {
	state, err := o.knower.OrganizationStateNow(ctx, orgID)
	switch {
	case err != nil:
		return err
	case state == entitlements.OrganizationUnknown:
		return auth.ErrOrganizationGone
	case state == entitlements.OrganizationDisabled:
		return auth.ErrOrganizationDisabled
	}
	return nil
}
