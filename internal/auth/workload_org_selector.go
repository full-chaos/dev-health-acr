package auth

import (
	"context"
	"errors"
)

var (
	// ErrWorkloadOrgSelectorRequired: the identity is bound in more than one
	// organization and the request named none.
	ErrWorkloadOrgSelectorRequired = errors.New("workload identity is bound in several organizations; an organization selector is required")
	// ErrWorkloadOrgNotBound: the selector names an organization the identity
	// has no enabled binding for. A bound-but-disabled organization reads the
	// same.
	ErrWorkloadOrgNotBound = errors.New("workload identity has no enabled binding for the selected organization")
	// ErrWorkloadOrgCheckUnavailable: the identity's live_orgs binding needs
	// Dev Health's answer that the selected organization exists, and the
	// answer could not be read. No credential is issued; the caller may retry.
	ErrWorkloadOrgCheckUnavailable = errors.New("the selected organization's existence could not be checked")
)

type workloadOrgSelectorKey struct{}

// WithWorkloadOrgSelector carries the validated organization selector of a
// token-exchange request to the grant resolver; authverify's Exchange has no
// parameter for it.
func WithWorkloadOrgSelector(ctx context.Context, orgID string) context.Context {
	return context.WithValue(ctx, workloadOrgSelectorKey{}, orgID)
}

// WorkloadOrgSelectorFrom returns the selector set by WithWorkloadOrgSelector.
func WorkloadOrgSelectorFrom(ctx context.Context) (string, bool) {
	orgID, ok := ctx.Value(workloadOrgSelectorKey{}).(string)
	return orgID, ok
}
