package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// writeManyUntrackedFiles adds count untracked files to dir, one more than
// sidecar.DefaultMaxChangedFiles by default, so DiscoverWorkspace's bounded
// changed-file listing is forced to truncate.
func writeManyUntrackedFiles(t *testing.T, dir string, count int) {
	t.Helper()
	for i := range count {
		name := "untracked-" + strconv.Itoa(i) + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestResolveScopeFailsClosedWhenChangedFilesAreTruncated is the
// CHAOS-2908 rereview regression lock: when the caller explicitly opted
// into changed-file discovery and the local Git workspace's true
// changed-file count exceeds sidecar's bounded discovery limit,
// resolveScope must not silently return the truncated prefix as scope.Files
// -- indistinguishable from a complete list to any caller -- but must fail
// closed with the typed, sanitized ErrChangedFilesTruncated instead. No MCP
// contract field represents "this file list is incomplete", so failing
// closed (rather than inventing a speculative new field) is the only
// non-misleading option.
func TestResolveScopeFailsClosedWhenChangedFilesAreTruncated(t *testing.T) {
	dir := initTempGitRepo(t, "acme/discovered-repo")
	writeManyUntrackedFiles(t, dir, sidecar.DefaultMaxChangedFiles+1)

	req := contractsv1.MCPContextForTaskRequest{
		Goal:  "goal",
		Scope: &contractsv1.MCPRequestedScope{IncludeChangedFiles: boolPtr(true)},
	}
	_, scope, err := resolveScope(context.Background(), nil, req)
	if !errors.Is(err, ErrChangedFilesTruncated) {
		t.Fatalf("expected ErrChangedFilesTruncated, got: %v", err)
	}
	if len(scope.Files) != 0 {
		t.Fatalf("expected no partial file list on truncation, got: %#v", scope.Files)
	}
}

// TestResolveScopeToleratesUntruncatedChangedFilesBelowBound is the
// non-regression companion: a changed-file count at or below the bound
// must continue to populate scope.Files exactly as before this fix.
func TestResolveScopeToleratesUntruncatedChangedFilesBelowBound(t *testing.T) {
	dir := initTempGitRepo(t, "acme/discovered-repo")
	writeUntrackedFile(t, dir)

	req := contractsv1.MCPContextForTaskRequest{
		Goal:  "goal",
		Scope: &contractsv1.MCPRequestedScope{IncludeChangedFiles: boolPtr(true)},
	}
	_, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Files) != 1 || scope.Files[0] != "untracked.txt" {
		t.Fatalf("expected the single discovered changed file, got: %#v", scope.Files)
	}
}
