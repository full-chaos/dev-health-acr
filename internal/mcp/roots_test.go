package mcp

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectedServerSession returns a live *mcpsdk.ServerSession backed by an
// in-memory transport, with the client having declared roots support and
// added the given file:// roots before connecting, so
// resolveMCPFileRoots's capability check and ListRoots round trip both
// exercise real SDK behavior instead of a hand-built fixture.
func connectedServerSession(t *testing.T, uris ...string) (*mcpsdk.ServerSession, func()) {
	t.Helper()
	ctx := context.Background()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	for _, uri := range uris {
		client.AddRoots(&mcpsdk.Root{URI: uri})
	}

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	t1, t2 := mcpsdk.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return serverSession, func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	}
}

func TestResolveMCPFileRootsReturnsClientRoots(t *testing.T) {
	session, closeFn := connectedServerSession(t, "file:///tmp/repo-a", "file:///tmp/repo-b")
	defer closeFn()

	roots, err := resolveMCPFileRoots(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 || roots[0] != "/tmp/repo-a" || roots[1] != "/tmp/repo-b" {
		t.Fatalf("unexpected roots: %#v", roots)
	}
}

func TestResolveMCPFileRootsReturnsNilWhenClientHasNoRootsCapability(t *testing.T) {
	// A client with zero AddRoots calls still declares the roots
	// capability (empty), matching the SDK's default behavior; this
	// asserts the empty-list case is handled without error, not that no
	// capability is declared (see TestResolveMCPFileRootsReturnsNilForNilSession
	// for the "no session at all" case).
	session, closeFn := connectedServerSession(t)
	defer closeFn()

	roots, err := resolveMCPFileRoots(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 0 {
		t.Fatalf("expected no roots, got: %#v", roots)
	}
}

func TestResolveMCPFileRootsReturnsNilForNilSession(t *testing.T) {
	roots, err := resolveMCPFileRoots(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if roots != nil {
		t.Fatalf("expected nil for a nil session, got: %#v", roots)
	}
}

func TestFileURIToPathRejectsNonFileScheme(t *testing.T) {
	if got := fileURIToPath("https://example.com/repo"); got != "" {
		t.Fatalf("expected empty path for a non-file URI, got: %q", got)
	}
}

func TestFileURIToPathDecodesPercentEncoding(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		want string
	}{
		{"percent-encoded space", "file:///tmp/repo%20name", "/tmp/repo name"},
		{"percent-encoded multi-byte UTF-8", "file:///tmp/caf%C3%A9", "/tmp/caf\u00e9"},
		{"already-plain path needs no decoding", "file:///tmp/repo", "/tmp/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fileURIToPath(tc.uri); got != tc.want {
				t.Fatalf("fileURIToPath(%q) = %q, want %q", tc.uri, got, tc.want)
			}
		})
	}
}

func TestFileURIToPathAuthorityValidation(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		want string
	}{
		{"empty authority is accepted", "file:///tmp/repo", "/tmp/repo"},
		{"localhost authority is accepted", "file://localhost/tmp/repo", "/tmp/repo"},
		{"localhost authority is accepted case-insensitively", "file://LOCALHOST/tmp/repo", "/tmp/repo"},
		{"a non-local authority is rejected", "file://attacker.example/etc", ""},
		{"an IP-literal authority is rejected", "file://127.0.0.1/tmp/repo", ""},
		{"userinfo in the authority is rejected even with a localhost host", "file://user@localhost/tmp/repo", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fileURIToPath(tc.uri); got != tc.want {
				t.Fatalf("fileURIToPath(%q) = %q, want %q", tc.uri, got, tc.want)
			}
		})
	}
}

func TestFileURIToPathRejectsMalformedOrNonFileURIs(t *testing.T) {
	cases := []struct {
		name string
		uri  string
	}{
		{"non-file scheme", "https://example.com/repo"},
		{"scheme-less string", "not-a-uri"},
		{"invalid percent-encoding", "file:///tmp/%zz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fileURIToPath(tc.uri); got != "" {
				t.Fatalf("fileURIToPath(%q) = %q, want empty", tc.uri, got)
			}
		})
	}
}

// TestResolveMCPFileRootsPreservesDuplicateRoots locks that two distinct
// client-supplied root URIs which decode to the same filesystem path pass
// through as two separate entries rather than being silently collapsed:
// deduplication here would let a client's root list mask how many
// distinct roots it actually supplied, undermining the raw-count bound
// below. The two URIs must differ as strings ("file:///..." vs.
// "file://localhost/...") because the SDK's own client-side root store
// replaces same-URI entries (see mcpsdk.Client.AddRoots), which would
// otherwise collapse a literal same-URI duplicate before ListRoots ever
// runs -- this test isolates resolveMCPFileRoots's own behavior instead.
func TestResolveMCPFileRootsPreservesDuplicateRoots(t *testing.T) {
	session, closeFn := connectedServerSession(t, "file:///tmp/repo-a", "file://localhost/tmp/repo-a")
	defer closeFn()

	roots, err := resolveMCPFileRoots(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 || roots[0] != "/tmp/repo-a" || roots[1] != "/tmp/repo-a" {
		t.Fatalf("expected two distinct URIs resolving to the same path to pass through unchanged, got: %#v", roots)
	}
}

// TestResolveMCPFileRootsAcceptsExactlyMaxRawRoots locks the boundary: a
// raw ListRoots response of exactly sidecar.MaxMCPFileRoots entries (every
// one valid) passes through in full, unbounded by any smaller,
// off-by-one-prone internal cap.
func TestResolveMCPFileRootsAcceptsExactlyMaxRawRoots(t *testing.T) {
	uris := make([]string, sidecar.MaxMCPFileRoots)
	for i := range uris {
		uris[i] = "file:///tmp/repo-" + strconv.Itoa(i)
	}
	session, closeFn := connectedServerSession(t, uris...)
	defer closeFn()

	roots, err := resolveMCPFileRoots(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != len(uris) {
		t.Fatalf("expected all %d roots at the exact boundary, got %d", len(uris), len(roots))
	}
}

// TestResolveMCPFileRootsRejectsRawRootsOverflow is the CHAOS-2908
// rereview regression lock: resolveMCPFileRoots itself must reject a raw
// ListRoots response of more than sidecar.MaxMCPFileRoots entries with the
// typed sidecar.ErrTooManyWorkspaceRoots, rather than passing every one of
// them through for a downstream caller to bound. Bounding only after
// resolveMCPFileRoots returns is exactly the gap
// TestResolveMCPFileRootsRejectsRawOverflowEvenWhenAllInvalid proves is
// unsafe: an all-invalid raw response of the same size would filter down
// to zero and never reach a downstream count check at all.
func TestResolveMCPFileRootsRejectsRawRootsOverflow(t *testing.T) {
	uris := make([]string, sidecar.MaxMCPFileRoots+1)
	for i := range uris {
		uris[i] = "file:///tmp/repo-" + strconv.Itoa(i)
	}
	session, closeFn := connectedServerSession(t, uris...)
	defer closeFn()

	roots, err := resolveMCPFileRoots(context.Background(), session)
	if !errors.Is(err, sidecar.ErrTooManyWorkspaceRoots) {
		t.Fatalf("expected ErrTooManyWorkspaceRoots, got %v (roots=%#v)", err, roots)
	}
}

// TestResolveMCPFileRootsRejectsRawOverflowEvenWhenAllInvalid is the core
// CHAOS-2908 finding: a raw ListRoots response of more than
// sidecar.MaxMCPFileRoots entries, every single one malformed (a non-file
// scheme that fileURIToPath filters to ""), must still be rejected with
// the typed overflow error. Filtering invalid entries before bounding the
// raw count -- the prior behavior -- let a client-supplied flood of
// malformed roots filter down to an empty, "successful" result, silently
// evading the overflow check entirely while still paying the cost of
// parsing every one of them.
func TestResolveMCPFileRootsRejectsRawOverflowEvenWhenAllInvalid(t *testing.T) {
	uris := make([]string, sidecar.MaxMCPFileRoots+1)
	for i := range uris {
		uris[i] = "https://example.com/repo-" + strconv.Itoa(i)
	}
	session, closeFn := connectedServerSession(t, uris...)
	defer closeFn()

	roots, err := resolveMCPFileRoots(context.Background(), session)
	if !errors.Is(err, sidecar.ErrTooManyWorkspaceRoots) {
		t.Fatalf("expected ErrTooManyWorkspaceRoots even for all-malformed roots, got err=%v roots=%#v", err, roots)
	}
}

// TestResolveMCPFileRootsPropagatesCancellation is the CHAOS-2908
// approval-remediation regression lock: resolveMCPFileRoots must not
// collapse a caller-context cancellation from session.ListRoots into the
// generic "no roots available" (nil, nil) fallback -- that fallback exists
// for a client that genuinely has nothing to offer (no capability, empty
// list, a real ListRoots business failure), never for the caller's own
// context already being done. An already-cancelled context is used (rather
// than racing a live cancel against the in-memory transport's round trip)
// because the SDK's own call() helper (transport.go) checks ctx.Err() != nil
// deterministically after Await returns, independent of whether the peer
// happened to reply in time.
func TestResolveMCPFileRootsPropagatesCancellation(t *testing.T) {
	session, closeFn := connectedServerSession(t, "file:///tmp/repo-a")
	defer closeFn()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	roots, err := resolveMCPFileRoots(ctx, session)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled to propagate, got %v (roots=%#v)", err, roots)
	}
}
