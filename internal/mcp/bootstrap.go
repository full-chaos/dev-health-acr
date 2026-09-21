package mcp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

// Cache bounds shared by the STDIO boot path and by every caller a hosted
// transport resolves, so one caller's cache can never be sized differently
// from another's.
const (
	localEvidenceCacheEntries = 1024
	localEvidenceCacheTTL     = 30 * time.Minute
	hostedRouteCacheEntries   = 1024
	hostedRouteCacheTTL       = 30 * time.Minute
)

// Bootstrap is the STDIO composition root: it resolves ONE operator
// credential and ONE capability snapshot at process start, which on STDIO
// is exactly right because the process serves exactly one caller for its
// lifetime.
//
// It is a bundle of the two halves the tools actually run on -- a
// ProcessConfig and a CallerContext -- and split() hands those out.
// Nothing downstream of split() reads a Bootstrap, so a hosted transport
// that resolves a caller per request (ResolveCaller) reuses the same tool
// handlers without a boot-time identity existing at all.
type Bootstrap struct {
	diagnostics  *slog.Logger
	Config       sidecar.Config
	Client       *sidecar.Client
	Capabilities contractsv1.Capabilities
	local        *localFederationRuntime
	localCache   *localEvidenceCache
	hostedRoutes *hostedRouteCache
}

// split returns the caller-independent and caller-scoped halves of this
// boot bundle. diagnostics is the stream tool execution logs to.
//
// The caches are memoised on the Bootstrap rather than allocated per call
// so that every server built from one Bootstrap serves the same single
// STDIO caller with the same cache -- the behaviour the process had when
// both caches hung off Bootstrap directly.
func (b *Bootstrap) split(diagnostics io.Writer) (*ProcessConfig, *CallerContext) {
	if b.localCache == nil {
		b.localCache = newLocalEvidenceCache(localEvidenceCacheEntries, localEvidenceCacheTTL, b.clock())
	}
	if b.hostedRoutes == nil {
		b.hostedRoutes = newHostedRouteCache(hostedRouteCacheEntries, hostedRouteCacheTTL, b.clock())
	}
	cfg := &ProcessConfig{
		Config:      b.Config,
		diagnostics: newDiagnosticsLogger(diagnostics, b.Config.LogLevel),
		local:       b.local,
	}
	caller := &CallerContext{
		client:       b.Client,
		capabilities: b.Capabilities,
		localCache:   b.localCache,
		hostedRoutes: b.hostedRoutes,
	}
	return cfg, caller
}

// clock is the time source the boot caller's caches expire against. It
// follows the local federation runtime when one is configured so a test
// that pins federation time pins cache expiry with it.
func (b *Bootstrap) clock() func() time.Time {
	if b.local != nil && b.local.clock != nil {
		return b.local.clock
	}
	return time.Now
}

// sha256Sum is the production content hash for local evidence identity.
func sha256Sum(payload []byte) [32]byte { return sha256.Sum256(payload) }

// CapabilityProbe contains a validated hosted capabilities response before
// compatibility enforcement. Diagnostics use it to distinguish connectivity
// from a reachable but incompatible hosted configuration.
type CapabilityProbe struct {
	Config       sidecar.Config
	Client       *sidecar.Client
	Capabilities contractsv1.Capabilities
}

type probeError struct {
	cause error
	safe  *classifiedError
}

func (e *probeError) Error() string { return e.safe.Error() }

func (e *probeError) Unwrap() error { return e.cause }

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
	return NewBootstrapWithIdentity(ctx, legacyIdentity(serverVersion))
}

// NewBootstrapWithIdentity uses the full ldflags-injected release identity.
// It is the production boundary; NewBootstrap remains for tests and callers
// that only have the legacy version string.
func NewBootstrapWithIdentity(ctx context.Context, identity version.Info) (*Bootstrap, error) {
	probe, err := ProbeCapabilities(ctx, identity)
	if err != nil {
		return nil, err
	}
	if err := probe.CheckCompatibility(); err != nil {
		return nil, err
	}
	return &Bootstrap{
		Config:       probe.Config,
		Client:       probe.Client,
		Capabilities: probe.Capabilities,
		local:        newLocalFederationRuntime(sidecar.LoadLocalIndexConfig(), time.Now, sha256Sum),
		localCache:   newLocalEvidenceCache(localEvidenceCacheEntries, localEvidenceCacheTTL, time.Now),
		hostedRoutes: newHostedRouteCache(hostedRouteCacheEntries, hostedRouteCacheTTL, time.Now),
	}, nil
}

// ProbeCapabilities resolves the compiled identity, constructs the hardened
// client, and fetches a validated capabilities response without deciding
// whether the response is compatible with the local sidecar.
func ProbeCapabilities(ctx context.Context, identity version.Info) (*CapabilityProbe, error) {
	cfg, err := sidecar.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("configuration: %s", sidecar.DescribeConfigError(err))
	}
	cfg.ClientVersion = effectiveSidecarVersion(cfg.ClientVersion, identity)
	cfg.SidecarVersion = effectiveSidecarVersion(cfg.SidecarVersion, identity)

	client, err := sidecar.NewClient(cfg, nil)
	if err != nil {
		return nil, newProbeError(err)
	}

	caps, err := client.Capabilities(ctx)
	if err != nil {
		return nil, newProbeError(err)
	}

	return &CapabilityProbe{Config: cfg, Client: client, Capabilities: caps}, nil
}

func newProbeError(err error) error {
	return &probeError{cause: err, safe: classify(err)}
}

// VersionMismatchMinimum reports remediation only for a validated hosted HTTP
// 426 version_mismatch response. Other HTTP and transport failures remain
// unavailable because they do not prove a compatible capabilities boundary.
func VersionMismatchMinimum(err error) (string, bool) {
	var apiErr *sidecar.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "version_mismatch" || apiErr.HTTPStatus != 426 || apiErr.MinimumClientVersion == "" {
		return "", false
	}
	return apiErr.MinimumClientVersion, true
}

func (p *CapabilityProbe) CheckCompatibility() error {
	return checkCompatibility(p.Capabilities, p.Config.SidecarVersion, p.Config.EnableWriteback)
}

func legacyIdentity(serverVersion string) version.Info {
	if version.IsCanonical(serverVersion) {
		return version.Info{Version: serverVersion, Commit: "0123456789abcdef0123456789abcdef01234567", Date: "1970-01-01T00:00:00Z"}
	}
	return version.Info{Version: serverVersion}
}
