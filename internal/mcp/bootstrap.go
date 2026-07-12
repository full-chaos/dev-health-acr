package mcp

import (
	"context"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// Bootstrap holds everything a running MCP tool call needs: the validated
// sidecar configuration, a hardened hosted API client, and the hosted
// capability descriptor confirmed compatible with this sidecar at startup.
type Bootstrap struct {
	Config       sidecar.Config
	Client       *sidecar.Client
	Capabilities contractsv1.Capabilities
}

// NewBootstrap loads and validates configuration, resolves a credential
// through the default precedence, constructs the hosted API client, fetches
// the hosted capability descriptor, and enforces service/version/schema/
// tool/entitlement compatibility before any tool call is accepted.
// serverVersion is the actual running binary's compiled-in version (see
// effectiveSidecarVersion): it is authoritative over an unset/default
// ACR_SIDECAR_VERSION *and* ACR_SIDECAR_CLIENT_VERSION so both the
// minimum-sidecar-version compatibility gate and the X-ACR-Client-Version
// header a real hosted API enforces server-side (see
// internal/api/read_capabilities.go's clientVersionCompatible check) fail
// closed on a real stale release binary, or succeed by default on a real
// current one, even when nobody configured either env var. This resolution
// happens before sidecar.NewClient is constructed: cfg.ClientVersion
// defaults to the "dev" sentinel, which api_client_transport.go sends
// verbatim as X-ACR-Client-Version on every request including the very
// first Capabilities() call below -- a real hosted API rejects that
// unparseable "dev" value with 426 Upgrade Required before this function's
// own compatibility check ever runs, so the compiled binary version must
// be authoritative before the client is built, not only when comparing
// against the fetched capabilities afterward.
// Every returned error's Error() text is safe to print verbatim to
// stderr: it never contains a bearer token, a raw hosted response body,
// or a filesystem path.
func NewBootstrap(ctx context.Context, serverVersion string) (*Bootstrap, error) {
	cfg, err := sidecar.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("configuration: %s", sidecar.DescribeConfigError(err))
	}
	cfg.ClientVersion = effectiveSidecarVersion(cfg.ClientVersion, serverVersion)
	cfg.SidecarVersion = effectiveSidecarVersion(cfg.SidecarVersion, serverVersion)

	client, err := sidecar.NewClient(cfg, nil)
	if err != nil {
		return nil, classify(err)
	}

	caps, err := client.Capabilities(ctx)
	if err != nil {
		return nil, classify(err)
	}

	if err := checkCompatibility(caps, cfg.SidecarVersion); err != nil {
		return nil, err
	}

	return &Bootstrap{Config: cfg, Client: client, Capabilities: caps}, nil
}
