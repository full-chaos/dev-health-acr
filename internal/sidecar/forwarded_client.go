package sidecar

import (
	"context"
	"net/netip"
)

type forwardedClientKey struct{}

// ContextWithForwardedClient records the resolved address of the end caller
// on ctx. Every hosted API call made with that context states it as its
// X-Forwarded-For value, so the hosted API's per-address gate keys on the
// caller and not on this process. A value that is not an IP literal is
// dropped: nothing free-form reaches the wire.
func ContextWithForwardedClient(ctx context.Context, address string) context.Context {
	parsed, err := netip.ParseAddr(address)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, forwardedClientKey{}, parsed.Unmap().String())
}

// ForwardedClientFromContext returns the address ContextWithForwardedClient
// recorded.
func ForwardedClientFromContext(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(forwardedClientKey{}).(string)
	return value, ok && value != ""
}
