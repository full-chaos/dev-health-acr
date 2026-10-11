package contextfabric

import "context"

type platformModelWithheldKey struct{}

// WithPlatformModelWithheld marks a turn that may not run on the deployment's
// own model. The caller decides it before the engine runs; the engine reads it
// for the calls that always run on the deployment model, such as offer
// phrasing.
func WithPlatformModelWithheld(ctx context.Context) context.Context {
	return context.WithValue(ctx, platformModelWithheldKey{}, true)
}

// PlatformModelWithheld reports whether WithPlatformModelWithheld marked ctx.
func PlatformModelWithheld(ctx context.Context) bool {
	withheld, _ := ctx.Value(platformModelWithheldKey{}).(bool)
	return withheld
}
