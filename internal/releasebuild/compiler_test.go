package releasebuild

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGoCompiler_injects_identity_visible_from_acr_api(t *testing.T) {
	// Given
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	output := filepath.Join(t.TempDir(), "acr-api")
	identity := testIdentity()
	compiler := GoCompiler{}

	// When
	err = compiler.Compile(context.Background(), CompileRequest{
		SourceDir:  root,
		OutputPath: output,
		Target:     Target{Product: "acr-api", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		Identity:   identity,
		BuildFlags: "-trimpath -buildvcs=false -mod=readonly",
	})
	if err != nil {
		t.Fatalf("compile acr-api: %v", err)
	}

	// Then
	command := exec.Command(output, "version")
	response, err := command.Output()
	if err != nil {
		t.Fatalf("run version: %v", err)
	}
	for _, want := range []string{identity.Version, identity.Commit, identity.Date} {
		if !strings.Contains(string(response), want) {
			t.Errorf("version output %q does not contain %q", response, want)
		}
	}
}

// TestReleaseBinaries_version_command_matches_release_workflow_smoke_expectation
// is the CHAOS-2926 parity lock for .github/workflows/release.yml's
// smoke-linux/smoke-macos/smoke-windows jobs: each compiles the real
// released binary with ldflags-injected identity and asserts
// `test "$(binary version)" = "$VERSION commit=$COMMIT built=$DATE"` --
// an exact match, not a substring. Both acr-api and acr-mcp ship that
// exact smoke assertion, so both must be proven here; nothing previously
// exercised acr-mcp's compiled `version` output at all.
func TestReleaseBinaries_version_command_matches_release_workflow_smoke_expectation(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	identity := testIdentity()
	want := fmt.Sprintf("%s commit=%s built=%s", identity.Version, identity.Commit, identity.Date)

	for _, product := range []string{"acr-api", "acr-mcp"} {
		t.Run(product, func(t *testing.T) {
			// Given
			output := filepath.Join(t.TempDir(), product)
			compiler := GoCompiler{}

			// When
			if err := compiler.Compile(context.Background(), CompileRequest{
				SourceDir:  root,
				OutputPath: output,
				Target:     Target{Product: product, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
				Identity:   identity,
				BuildFlags: "-trimpath -buildvcs=false -mod=readonly",
			}); err != nil {
				t.Fatalf("compile %s: %v", product, err)
			}
			response, err := exec.Command(output, "version").Output()
			if err != nil {
				t.Fatalf("run %s version: %v", product, err)
			}

			// Then
			if got := strings.TrimSpace(string(response)); got != want {
				t.Fatalf("%s version output = %q, want %q", product, got, want)
			}
		})
	}
}

func TestGoCompiler_crossCompilesMCPFromDarwinToLinux(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin-specific cross-compilation regression")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	// When
	err = (GoCompiler{}).Compile(context.Background(), CompileRequest{
		SourceDir:  root,
		OutputPath: filepath.Join(t.TempDir(), "acr-mcp-linux"),
		Target:     Target{Product: "acr-mcp", GOOS: "linux", GOARCH: runtime.GOARCH},
		Identity:   testIdentity(),
		BuildFlags: reproducibleBuildFlags,
	})

	// Then
	if err != nil {
		t.Fatalf("cross-compile acr-mcp for Linux: %v", err)
	}
}
