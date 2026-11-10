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
}

// organizationPresence is the remote entitlement client's organization check; nil in local entitlement mode.
func organizationPresence(provider EntitlementProvider) auth.OrganizationPresence {
	knower, ok := provider.(organizationKnower)
	if !ok || storage.IsNil(knower) {
		return nil
	}
	return entitlementOrganizations{knower: knower}
}

type entitlementOrganizations struct{ knower organizationKnower }

func (o entitlementOrganizations) OrganizationKnown(ctx context.Context, orgID string) error {
	err := o.knower.OrganizationKnown(ctx, orgID)
	if errors.Is(err, entitlements.ErrOrganizationUnknown) {
		return fmt.Errorf("%w: %w", auth.ErrOrganizationGone, err)
	}
	return err
}
