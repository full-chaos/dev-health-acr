package mcp

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// assertErrorCategory drives result through the same shape every other
// handler-level error-matrix test in this package asserts against: a tool
// error (never a protocol error) whose rendered category text is present.
func assertErrorCategory(t *testing.T, result *mcpsdk.CallToolResult, err error, wantCategory string) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected a tool error, not a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError, got success: %#v", result.Content)
	}
	text, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok || !strings.Contains(text.Text, wantCategory) {
		t.Fatalf("expected category %q in result, got: %#v", wantCategory, result.Content)
	}
}

// TestHandleContextForTaskMapsMalformedTwoHundredResponseToUnavailable is
// the CHAOS-2908 approval-remediation regression lock: a hosted 2xx
// response whose body is not valid JSON (decodeExact's failure inside
// sidecar.Client.call, api_client_transport.go) returns sidecar.ErrMalformedResponse
// bare -- never wrapped in *sidecar.APIError -- and must classify as
// "unavailable" through the full handler path, not fall into the generic
// "internal" catch-all.
func TestHandleContextForTaskMapsMalformedTwoHundredResponseToUnavailable(t *testing.T) {
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{not valid json"))
	}
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{
		"goal":       "investigate flaky checkout tests",
		"repository": map[string]any{"slug": "acme/widgets"},
		"scope":      map[string]any{"branch": "main"},
	})
	result, err := handleContextForTask(context.Background(), boot, req)
	assertErrorCategory(t, result, err, "unavailable")
}

// TestHandleContextForTaskMapsOversizedRequestToValidation locks the fix
// for sidecar.ErrRequestTooLarge, which only ever originates bare (the
// size check in sidecar.Client.call runs before any HTTP call is
// attempted, so it can never be wrapped in *sidecar.APIError): a locally
// oversized outgoing request is a caller-input problem and must classify
// as "validation", not "internal". An explicit repository, branch, and
// commit SHA short-circuit resolveScope's own discovery so this test
// isolates the request-size failure from local Git workspace discovery
// entirely; the hosted context-packets endpoint must never be reached.
func TestHandleContextForTaskMapsOversizedRequestToValidation(t *testing.T) {
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the hosted context-packets endpoint must not be called for a locally oversized request")
	}
	boot := newFixtureBootstrap(t, fx)

	files := make([]string, 200)
	for i := range files {
		files[i] = fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 2040))
	}
	req := callToolRequest(t, map[string]any{
		"goal":       "investigate flaky checkout tests",
		"repository": map[string]any{"slug": "acme/widgets"},
		"scope":      map[string]any{"branch": "main", "commit_sha": strings.Repeat("a", 40), "files": files},
	})
	result, err := handleContextForTask(context.Background(), boot, req)
	assertErrorCategory(t, result, err, "validation")
}

// TestHandleContextForTaskMapsRepositoryScopeMismatchToValidation locks
// ErrRepositoryScopeMismatch's classification through the full handler
// path: an explicit repository that does not match the locally
// discovered Git workspace, combined with explicit changed-file
// discovery, must surface as "validation", not "internal".
func TestHandleContextForTaskMapsRepositoryScopeMismatchToValidation(t *testing.T) {
	initTempGitRepo(t, "acme/discovered-repo")

	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the hosted context-packets endpoint must not be called for a mismatched repository scope")
	}
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{
		"goal":       "investigate flaky checkout tests",
		"repository": map[string]any{"slug": "explicit/repo"},
		"scope":      map[string]any{"include_changed_files": true},
	})
	result, err := handleContextForTask(context.Background(), boot, req)
	assertErrorCategory(t, result, err, "validation")
}

// TestHandleContextForTaskMapsTruncatedChangedFilesToValidation locks
// ErrChangedFilesTruncated's classification through the full handler
// path: a local changed-file count exceeding the bounded discovery limit,
// with changed-file discovery explicitly requested, must surface as
// "validation", not "internal".
func TestHandleContextForTaskMapsTruncatedChangedFilesToValidation(t *testing.T) {
	dir := initTempGitRepo(t, "acme/discovered-repo")
	writeManyUntrackedFiles(t, dir, sidecar.DefaultMaxChangedFiles+1)

	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the hosted context-packets endpoint must not be called when the changed-file list is truncated")
	}
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{
		"goal":  "goal-only, discovered repo with a truncated changed-file list",
		"scope": map[string]any{"include_changed_files": true},
	})
	result, err := handleContextForTask(context.Background(), boot, req)
	assertErrorCategory(t, result, err, "validation")
}

// TestHandleContextForTaskMapsCancelledDiscoveryToCancelled is the
// CHAOS-2908 approval-remediation regression lock: a goal-only request
// (no explicit repository, so resolveScope's fast path never applies)
// whose caller context is already cancelled by the time local Git
// workspace discovery runs must surface as "cancelled" -- not be silently
// swallowed as "nothing discoverable" and then rejected as a goal-only
// validation failure by handleContextForTask's own empty-repository
// check.
func TestHandleContextForTaskMapsCancelledDiscoveryToCancelled(t *testing.T) {
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the hosted context-packets endpoint must not be called when workspace discovery is cancelled")
	}
	boot := newFixtureBootstrap(t, fx)

	dir := t.TempDir()
	original, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	t.Cleanup(func() { chdir(t, original) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := callToolRequest(t, map[string]any{
		"goal": "goal-only, cancelled mid-discovery",
	})
	result, callErr := handleContextForTask(ctx, boot, req)
	assertErrorCategory(t, result, callErr, "cancelled")
}

// TestHandleContextForTaskMapsDeadlineDiscoveryToTimeout is the deadline
// analogue of TestHandleContextForTaskMapsCancelledDiscoveryToCancelled:
// an already-expired deadline must surface as "timeout" through the same
// goal-only discovery path, distinctly from cancellation.
func TestHandleContextForTaskMapsDeadlineDiscoveryToTimeout(t *testing.T) {
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the hosted context-packets endpoint must not be called when workspace discovery times out")
	}
	boot := newFixtureBootstrap(t, fx)

	dir := t.TempDir()
	original, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	t.Cleanup(func() { chdir(t, original) })

	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()

	req := callToolRequest(t, map[string]any{
		"goal": "goal-only, expired deadline mid-discovery",
	})
	result, callErr := handleContextForTask(ctx, boot, req)
	assertErrorCategory(t, result, callErr, "timeout")
}
