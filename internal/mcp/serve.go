package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// Serve bootstraps the sidecar (config, credential, hosted client,
// capability/version/entitlement compatibility) and, on success, runs the
// MCP STDIO server until the client disconnects or ctx is cancelled. On a
// bootstrap failure it writes a single sanitized, secret-free diagnostic
// line to diagnostics (intended to be the process's stderr, never stdout,
// since stdout must carry only MCP JSON-RPC traffic) and returns a non-nil
// error without ever starting the transport.
//
// Cancellation of the caller-supplied ctx (for example signal.NotifyContext
// firing on SIGINT/SIGTERM in cmd/acr-mcp) is an expected shutdown, not a
// fault: both the bootstrap and Run failure branches check
// causedByCallerCancellation and return nil so the process exits cleanly,
// while any other bootstrap or SDK/transport failure is still reported and
// returned as before.
func Serve(ctx context.Context, diagnostics io.Writer, serverVersion string) error {
	boot, err := NewBootstrap(ctx, serverVersion)
	if err != nil {
		if causedByCallerCancellation(ctx, err) {
			return nil
		}
		fmt.Fprintf(diagnostics, "acr-mcp: startup failed: %s\n", err.Error())
		return err
	}
	logger := newDiagnosticsLogger(diagnostics, boot.Config.LogLevel)
	logger.InfoContext(ctx, "acr-mcp starting",
		"version", serverVersion,
		"service", boot.Capabilities.Service,
		"enabled_tools", boot.Capabilities.EnabledTools,
		"agent_context_runtime", boot.Capabilities.Entitlements.AgentContextRuntime,
		"context_read_scope", boot.Capabilities.Permissions.ContextRead,
		"evidence_read_scope", boot.Capabilities.Permissions.EvidenceRead,
	)
	server := NewServer(boot, serverVersion)
	if err := Run(ctx, server); err != nil {
		if causedByCallerCancellation(ctx, err) {
			return nil
		}
		fmt.Fprintf(diagnostics, "acr-mcp: serve exited: %s\n", classify(err).Error())
		return err
	}
	return nil
}

// causedByCallerCancellation reports whether err is exactly the
// cancellation of the ctx the caller supplied to Serve, as opposed to an
// unrelated bootstrap or SDK/transport failure. The go-sdk's
// mcp.Server.Run returns ctx.Err() verbatim on its ctx.Done() branch, so a
// live ctx.Err() that err wraps is sufficient to identify a caller-driven
// shutdown without inspecting err's text; a coincidental context.Canceled
// from some other, still-live context does not match because ctx.Err()
// itself is nil in that case.
func causedByCallerCancellation(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, ctx.Err())
}

// newDiagnosticsLogger builds the structured, level-gated logger Serve
// uses for all post-bootstrap diagnostics (stderr in production).
// ACR_LOG_LEVEL (default info, see internal/sidecar.Config.LogLevel)
// controls verbosity without a code change; it never controls what gets
// redacted -- callers must never pass a bearer token or raw response body
// as a logged field regardless of level.
func newDiagnosticsLogger(diagnostics io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(diagnostics, &slog.HandlerOptions{Level: level}))
}
