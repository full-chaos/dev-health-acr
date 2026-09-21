package mcp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProcessConfig is the caller-INDEPENDENT half of a running sidecar: the
// validated configuration (origin, limits, timeouts, resolved build
// identity), the diagnostics logger, and the local workspace federation
// runtime. One value serves every caller in the process, so by contract it
// holds nothing derived from a credential, a principal, or a request --
// no hosted API client, no capability snapshot, and no cache whose entries
// were produced for one caller's question.
//
// Everything that IS caller-derived lives in CallerContext, which reaches a
// tool handler only through the request context.
type ProcessConfig struct {
	// Config is the validated sidecar configuration. It is immutable for
	// the process lifetime; a caller never influences it.
	Config sidecar.Config

	diagnostics *slog.Logger
	local       *localFederationRuntime
}

// NewProcessConfig builds the process half from a validated configuration.
// diagnostics receives the structured JSON log stream (production supplies
// stderr; stdout stays protocol-only on STDIO).
//
// The local federation runtime it constructs reads the PROCESS's own
// workspace, which is a local-sidecar concept: a hosted transport that
// serves many callers leaves it disabled through the local index
// configuration rather than handing one caller another's workspace.
func NewProcessConfig(cfg sidecar.Config, diagnostics io.Writer) *ProcessConfig {
	return &ProcessConfig{
		Config:      cfg,
		diagnostics: newDiagnosticsLogger(diagnostics, cfg.LogLevel),
		local:       newLocalFederationRuntime(sidecar.LoadLocalIndexConfig(), time.Now, sha256Sum),
	}
}

// Diagnostics exposes the process logger so a transport can log on the same
// stream the tool handlers use. It is never nil for a ProcessConfig built
// by NewProcessConfig.
func (p *ProcessConfig) Diagnostics() *slog.Logger {
	if p == nil {
		return nil
	}
	return p.diagnostics
}

// CallerCredential is everything a transport knows about ONE caller at the
// moment it asks for a caller context.
//
// Bearer is always the caller's OWN ACR API token. The MCP workload never
// mints, exchanges, or substitutes a service credential of its own: the
// hosted API authenticates the same token the caller presented, so every
// hosted authorization decision -- including CHAOS-6070's live per-result
// grant check -- is decided against the human caller's live grant rather
// than against a shared process identity.
type CallerCredential struct {
	// Bearer is the caller's ACR API token. Required.
	Bearer string

	// Principal is set only when THIS process already authenticated Bearer
	// through internal/auth (the Authenticator that owns the credential
	// store). A transport that merely forwards the bearer to the hosted API
	// leaves it nil: the hosted API is then the authenticating authority,
	// and no second credential store or token format exists here.
	Principal *storage.Principal
}

// CallerContext is the request-scoped half of a running sidecar: one
// caller's identity, a hosted API client bound to that caller's own bearer,
// the capability snapshot the hosted API returned FOR that bearer, and the
// caches holding data produced for that caller. Two callers in one process
// hold two CallerContext values and share none of it.
//
// Tool handlers obtain it from the request context through
// CallerFromContext, never from a struct field or a package global, so a
// request that arrives without an identity cannot silently borrow one.
type CallerContext struct {
	principal    *storage.Principal
	client       *sidecar.Client
	capabilities contractsv1.Capabilities
	localCache   *localEvidenceCache
	hostedRoutes *hostedRouteCache
}

// Capabilities returns the hosted capability snapshot fetched with THIS
// caller's credential. Tool visibility, budget limits, and entitlement
// gating all read it, so it is per caller and never a process-wide value.
func (c *CallerContext) Capabilities() contractsv1.Capabilities {
	if c == nil {
		return contractsv1.Capabilities{}
	}
	return c.capabilities
}

// Client returns the hosted API client bound to this caller's bearer.
func (c *CallerContext) Client() *sidecar.Client {
	if c == nil {
		return nil
	}
	return c.client
}

// Principal returns the authenticated principal when the hosting process
// resolved one through internal/auth, and false otherwise. Absence is not a
// failure: a transport that forwards the caller's bearer to the hosted API
// leaves authentication to the hosted API and holds no principal itself.
func (c *CallerContext) Principal() (storage.Principal, bool) {
	if c == nil || c.principal == nil {
		return storage.Principal{}, false
	}
	return *c.principal, true
}

// ErrCallerCredentialInvalid reports a caller credential that is missing or
// not shaped like an ACR API token. Resolution fails closed on it rather
// than falling back to any process-resolved credential.
var ErrCallerCredentialInvalid = errors.New("mcp: the caller credential is missing or malformed")

// ErrProcessConfigMissing reports a caller resolution attempted without the
// process half.
var ErrProcessConfigMissing = errors.New("mcp: a process configuration is required to resolve a caller")

// ResolveCaller turns one caller's credential into the request-scoped
// context its tool calls run under: a hosted API client bound to that
// bearer alone, the capability snapshot the hosted API returns for it, and
// that caller's own caches.
//
// It is the seam a hosted transport uses per request. Every failure is
// closed: a malformed credential, an unreachable or refusing hosted API,
// and an incompatible capability handshake all return an error and no
// caller context, so no tool call can proceed on a borrowed identity.
//
// Capabilities are fetched with the caller's own bearer on every call
// rather than read from a process snapshot, which is what makes tool
// visibility, limits, and entitlement reflect the caller and not whoever
// started the process.
func ResolveCaller(ctx context.Context, cfg *ProcessConfig, credential CallerCredential) (*CallerContext, error) {
	if cfg == nil {
		return nil, ErrProcessConfigMissing
	}
	if !auth.IsTokenShapeValid(credential.Bearer) {
		return nil, ErrCallerCredentialInvalid
	}
	client, err := sidecar.NewClient(cfg.Config, callerCredentialSource(credential.Bearer))
	if err != nil {
		return nil, newProbeError(err)
	}
	capabilities, err := client.Capabilities(ctx)
	if err != nil {
		return nil, newProbeError(err)
	}
	if err := checkCompatibility(capabilities, cfg.Config.SidecarVersion, cfg.Config.EnableWriteback); err != nil {
		return nil, err
	}
	return newCallerContext(credential.Principal, client, capabilities), nil
}

// callerCredentialSource pins a sidecar client to one caller's bearer. The
// hosted client resolves its credential per request; binding it to this
// closure is what stops a request from picking up the process-wide
// environment/keyring/file credential LoadCredential would otherwise find.
func callerCredentialSource(bearer string) sidecar.CredentialSource {
	return func() (sidecar.CredentialResult, error) {
		return sidecar.CredentialResult{Token: bearer, Source: "caller"}, nil
	}
}

// newCallerContext assembles a caller with its OWN caches. Both caches hold
// entries produced while answering this caller's questions -- an evidence
// excerpt, the routing decision for an evidence reference -- so neither may
// be shared with another caller or promoted to the process.
func newCallerContext(principal *storage.Principal, client *sidecar.Client, capabilities contractsv1.Capabilities) *CallerContext {
	caller := &CallerContext{
		client:       client,
		capabilities: capabilities,
		localCache:   newLocalEvidenceCache(localEvidenceCacheEntries, localEvidenceCacheTTL, time.Now),
		hostedRoutes: newHostedRouteCache(hostedRouteCacheEntries, hostedRouteCacheTTL, time.Now),
	}
	if principal != nil {
		copied := *principal
		copied.RepositoryScopes = append([]string(nil), principal.RepositoryScopes...)
		copied.Permissions = append([]string(nil), principal.Permissions...)
		copied.ProductEntitlements = append([]string(nil), principal.ProductEntitlements...)
		caller.principal = &copied
	}
	return caller
}

// callerContextKey is a package-private key type, so nothing outside this
// package can place a value the accessor below would accept as an identity.
type callerContextKey struct{}

// ErrCallerNotInContext reports a request context that carries no caller
// identity. Reaching a tool handler without one is a defect in the
// transport wiring, never a request the sidecar may serve on a default
// identity, so the accessor reports it instead of substituting one.
var ErrCallerNotInContext = errors.New("mcp: the request context carries no caller identity")

// ContextWithCaller binds a caller to a request context. A transport calls
// it once per request (per connection on STDIO, where one credential is one
// caller for the process lifetime).
func ContextWithCaller(ctx context.Context, caller *CallerContext) context.Context {
	return context.WithValue(ctx, callerContextKey{}, caller)
}

// CallerFromContext returns the caller bound to ctx, or
// ErrCallerNotInContext. It fails closed: a missing binding, a nil caller,
// and a caller without a hosted client are all refusals, never a fallback
// to a process credential.
func CallerFromContext(ctx context.Context) (*CallerContext, error) {
	caller, ok := ctx.Value(callerContextKey{}).(*CallerContext)
	if !ok || caller == nil || caller.client == nil {
		return nil, ErrCallerNotInContext
	}
	return caller, nil
}

// callerMiddleware binds one caller to every request a server serves. A
// server is built for exactly one caller (see NewServerForCaller), so the
// binding is unconditional: it overwrites rather than defers to whatever
// the transport may have left in the context, which keeps the identity a
// tool handler reads the same identity the tool catalogue was built from.
func callerMiddleware(caller *CallerContext) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			return next(ContextWithCaller(ctx, caller), method, req)
		}
	}
}

// refuseWithoutCaller renders the fail-closed refusal for a tool call that
// reached a handler with no caller identity, and logs it at the point of
// decision so the refusal is diagnosable from the process's own artifacts
// rather than from re-reading this source.
func refuseWithoutCaller(ctx context.Context, cfg *ProcessConfig, tool string) *mcpsdk.CallToolResult {
	if cfg != nil && cfg.diagnostics != nil {
		cfg.diagnostics.ErrorContext(ctx, "mcp tool call refused",
			"tool", tool,
			"failure_class", "caller_identity_absent",
		)
	}
	return toolErrorResult(&classifiedError{category: "auth", message: "this request carries no authenticated caller identity"})
}
