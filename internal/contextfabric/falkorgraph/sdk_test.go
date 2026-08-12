package falkorgraph

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNewSDKAPIIsPinnedAndConstructible is the falkorgraph twin of zepgraph's
// same-named test. falkorgraph had SDKModule/SDKVersion constants and a
// newSDKAPI constructor with no test proving they are pinned/constructible.
//
// Unlike zep-go's client (an HTTP client wrapper with no required I/O at
// construction either), falkordb-go's FalkorDBNew constructs a go-redis
// client and pool -- verified I/O-free by reading newSDKAPI's body
// (client.go): falkordb.FalkorDBNew(options) builds a *redis.Client, which
// go-redis itself only dials lazily on the first command, never inside the
// constructor. So a syntactically valid config constructs successfully with
// no live network needed, exactly like zepgraph's newSDKAPI.
func TestNewSDKAPIIsPinnedAndConstructible(t *testing.T) {
	t.Parallel()
	client, err := newSDKAPI(Config{
		Addr: "127.0.0.1:6379", GraphPrefix: "acr-cf", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: 10, PoolSize: 1, AllowInsecure: true,
	})
	if err != nil || client == nil {
		t.Fatalf("newSDKAPI() client = %#v err = %v", client, err)
	}
}

// TestSDKModuleAndVersionMatchThePinnedGoModDependency proves SDKModule/
// SDKVersion (config.go) are not just documentation strings that can drift
// from the actual pinned dependency -- they must match go.mod's require
// line. runtime/debug.ReadBuildInfo() was tried first and rejected: in this
// repository's test environment it reports zero populated Deps entries for
// a `go test` binary (verified directly), so it cannot be relied on here.
// Reading go.mod's own text is more direct anyway and needs no build-info
// cooperation from the toolchain.
func TestSDKModuleAndVersionMatchThePinnedGoModDependency(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed to report this test file's path")
	}
	// This file lives at internal/contextfabric/falkorgraph/sdk_test.go;
	// go.mod is three directories up, at the repository root.
	goModPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "go.mod")
	contents, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("read %s: %v", goModPath, err)
	}
	wantLine := SDKModule + " " + SDKVersion
	found := false
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == wantLine {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("go.mod has no %q require line matching SDKModule=%q SDKVersion=%q -- pin drifted, update the SDKVersion constant", wantLine, SDKModule, SDKVersion)
	}
}
