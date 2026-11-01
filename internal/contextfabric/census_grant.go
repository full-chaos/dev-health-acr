package contextfabric

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type censusGrantKey struct{}

// WithCensusGrant binds the repository grant every census read with ctx must
// apply inside its own statements (CHAOS-12452). It is set once, on the
// context a round or a redemption reads with, so a census cannot be missed by
// one of its calls. A list that cannot be applied stays bound: the census
// refuses it and never falls back to the organization.
func WithCensusGrant(ctx context.Context, grant []string) context.Context {
	return context.WithValue(ctx, censusGrantKey{}, append([]string(nil), grant...))
}

// CensusGrantFrom returns the list WithCensusGrant bound and whether one is
// bound. Bound with no usable list still reads as bound.
func CensusGrantFrom(ctx context.Context) ([]string, bool) {
	grant, bound := ctx.Value(censusGrantKey{}).([]string)
	return grant, bound
}

// ScopeCensusToCaller binds the grant a census of this caller must apply. A
// restricted caller's census reads inside its repository grant. A project-only
// caller's grant is not a repository list, so the reserved value is bound and
// every census refuses it. An unrestricted or universal caller sees every
// repository: nothing is bound and its statements do not change.
func ScopeCensusToCaller(ctx context.Context, principal storage.Principal) context.Context {
	switch auth.ClassifyGrant(principal) {
	case auth.GrantRestricted:
		return WithCensusGrant(ctx, principal.RepositoryScopes)
	case auth.GrantProjectOnly:
		return WithCensusGrant(ctx, []string{contractsv1.ProjectOnlyRepositoryScope})
	default:
		return ctx
	}
}
