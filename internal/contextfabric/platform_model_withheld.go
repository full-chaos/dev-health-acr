package contextfabric

import (
	"context"
	"errors"
)

type platformModelWithheldKey struct{}

type platformModelWithheldValue struct {
	basis string
}

// ErrPlatformModelWithheld marks a model call that fell through to the
// deployment model for a turn that may not run on it. Callers treat it as
// "no model call was made", not as a provider failure.
var ErrPlatformModelWithheld = errors.New("context fabric platform model withheld")

// WithPlatformModelWithheld marks a turn that may not run on the deployment's
// own model. The caller decides it before the engine runs; the model runtime
// resolver reads it at the one place a request falls through to the
// deployment model.
func WithPlatformModelWithheld(ctx context.Context) context.Context {
	return WithPlatformModelWithheldBasis(ctx, "")
}

// WithPlatformModelWithheldBasis is WithPlatformModelWithheld carrying the
// gate's decision basis, so the call that is withheld can log why.
func WithPlatformModelWithheldBasis(ctx context.Context, basis string) context.Context {
	return context.WithValue(ctx, platformModelWithheldKey{}, platformModelWithheldValue{basis: basis})
}

// PlatformModelWithheld reports whether WithPlatformModelWithheld marked ctx.
func PlatformModelWithheld(ctx context.Context) bool {
	_, ok := ctx.Value(platformModelWithheldKey{}).(platformModelWithheldValue)
	return ok
}

// PlatformModelWithheldBasis returns the basis the turn was withheld on, or "".
func PlatformModelWithheldBasis(ctx context.Context) string {
	value, _ := ctx.Value(platformModelWithheldKey{}).(platformModelWithheldValue)
	return value.basis
}

type deploymentModelBoundKey struct{}

// WithDeploymentModelBound marks a turn the gate admitted on the deployment
// model: the resolver serves every call of it from the deployment model and
// never reads the organization's model configuration, so a configuration that
// appears mid-turn cannot move the turn to another model.
func WithDeploymentModelBound(ctx context.Context) context.Context {
	return context.WithValue(ctx, deploymentModelBoundKey{}, true)
}

// DeploymentModelBound reports whether WithDeploymentModelBound marked ctx.
func DeploymentModelBound(ctx context.Context) bool {
	bound, _ := ctx.Value(deploymentModelBoundKey{}).(bool)
	return bound
}
