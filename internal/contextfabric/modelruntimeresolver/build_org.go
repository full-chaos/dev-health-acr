package modelruntimeresolver

import "context"

type buildOrgKey struct{}

// withBuildOrgID hands the organization a Build is constructing for to the
// Build itself: ResolvedOrgModelConfig carries no organization id, and the
// Build signature is shared with every caller.
func withBuildOrgID(ctx context.Context, orgID string) context.Context {
	return context.WithValue(ctx, buildOrgKey{}, orgID)
}

func buildOrgID(ctx context.Context) string {
	orgID, _ := ctx.Value(buildOrgKey{}).(string)
	return orgID
}
