package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolveMCPFileRoots queries the connected client's MCP roots when the
// client declared roots support during initialize, converting each
// file://-scheme root URI to a plain filesystem path via fileURIToPath. It
// never invokes ListRoots against a client that has not declared roots
// support. A ListRoots failure caused by the caller's own context being
// canceled or its deadline expiring is propagated as-is, so the distinct
// cancelled/timeout classify() categories are never collapsed into a
// silent "no roots available" miss; any other ListRoots error (including
// one from a client that lied about support) is still treated as "no
// roots available" rather than a hard failure, since workspace discovery
// always has a cwd fallback.
//
// The raw root count -- before any filtering or URI decoding -- is bounded
// by sidecar.MaxMCPFileRoots and rejected with the typed
// sidecar.ErrTooManyWorkspaceRoots overflow error before a single entry is
// parsed. This bound must apply to the raw count, not the post-filter
// valid count: a client that floods the roots response with malformed
// entries (wrong scheme, unparseable URI) would otherwise have every one
// of them silently filtered out here, letting the resulting small or empty
// valid list slip under sidecar.DiscoverWorkspace's own downstream bound
// entirely -- evading the overflow rejection while still paying the cost
// of parsing an unbounded raw list. Every valid, in-bound entry still
// passes through unfiltered for duplicates: deduplicating here would let a
// client's root list mask how many distinct roots it actually supplied.
func resolveMCPFileRoots(ctx context.Context, session *mcpsdk.ServerSession) ([]string, error) {
	if session == nil {
		return nil, nil
	}
	params := session.InitializeParams()
	if params == nil || params.Capabilities == nil || params.Capabilities.RootsV2 == nil {
		return nil, nil
	}

	result, err := session.ListRoots(ctx, nil)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, nil
	}
	if result == nil {
		return nil, nil
	}
	if len(result.Roots) > sidecar.MaxMCPFileRoots {
		return nil, fmt.Errorf("%w: client supplied %d roots, max %d", sidecar.ErrTooManyWorkspaceRoots, len(result.Roots), sidecar.MaxMCPFileRoots)
	}

	roots := make([]string, 0, len(result.Roots))
	for _, root := range result.Roots {
		if root == nil {
			continue
		}
		path := fileURIToPath(root.URI)
		if path == "" {
			continue
		}
		roots = append(roots, path)
	}
	return roots, nil
}

// fileURIToPath converts a "file"-scheme root URI to a plain filesystem
// path per RFC 8089 (the "file" URI scheme): the authority component must
// be empty or exactly "localhost" (compared case-insensitively, since
// hostnames are case-insensitive per RFC 3986) -- any other host names a
// remote or otherwise untrusted authority and is rejected outright, rather
// than silently discarded the way a bare "file://" prefix strip would: a
// root like "file://attacker.example/etc" must not resolve to the bare
// path "/etc" as if it were local. A URI carrying userinfo
// ("file://user@host/...") is rejected for the same reason: file URIs
// never legitimately carry credentials.
//
// The path component is decoded by url.Parse's own RFC 3986
// percent-decoding, so percent-encoded spaces ("%20") and multi-byte UTF-8
// sequences (e.g. "%C3%A9") round-trip to their literal characters instead
// of leaking raw percent-escapes into the filesystem path. Non-file
// schemes and unparseable/malformed URIs (invalid percent-encoding, and so
// on) are rejected (returns ""), matching DiscoverOptions.MCPFileRoots's
// own defensive scheme rejection.
func fileURIToPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.User != nil {
		return ""
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return ""
	}
	return u.Path
}
