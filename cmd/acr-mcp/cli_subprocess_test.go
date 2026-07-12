package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestACRMCPProcess(t *testing.T) {
	if os.Getenv("ACR_MCP_CLI_PROCESS") != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"acr-mcp"}, os.Args[index+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing command separator")
}

func runACRMCPCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=TestACRMCPProcess", "--"}, args...)...)
	command.Env = append(os.Environ(), "ACR_MCP_CLI_PROCESS=1")
	output, err := command.CombinedOutput()
	if err == nil {
		return 0, string(output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run command: %v\n%s", err, output)
	}
	return exitErr.ExitCode(), string(output)
}

func TestDoctorSubprocessAcceptsDocumentedForms(t *testing.T) {
	t.Setenv("ACR_API_URL", "")
	t.Setenv("ACR_API_TOKEN", "")

	code, output := runACRMCPCLI(t, "doctor")
	if code != 0 {
		t.Fatalf("doctor without args failed: code=%d output=%s", code, output)
	}

	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(71))

	code, output = runACRMCPCLI(t, "doctor", "--offline")
	if code != 0 {
		t.Fatalf("doctor --offline failed: code=%d output=%s", code, output)
	}

	bundlePath := filepath.Join(physicalTempDir(t), "doctor-bundle.tar")
	code, output = runACRMCPCLI(t, "doctor", "--bundle", bundlePath)
	if code != 0 {
		t.Fatalf("doctor --bundle failed: code=%d output=%s", code, output)
	}
	if _, err := os.Stat(bundlePath); err != nil {
		t.Fatalf("doctor --bundle did not write bundle: %v\n%s", err, output)
	}
	liveBundlePath := filepath.Join(physicalTempDir(t), "doctor-live-bundle.tar")
	code, output = runACRMCPCLI(t, "doctor", "--bundle", liveBundlePath, "--live")
	if code != 0 {
		t.Fatalf("doctor --bundle --live failed: code=%d output=%s", code, output)
	}
}

func TestDoctorSubprocessRejectsInvalidFormsWithSanitizedUsage(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(72))

	testCases := []struct {
		name   string
		args   []string
		needle string
	}{
		{name: "unknown_flag", args: []string{"doctor", "--bogus"}, needle: "--bogus"},
		{name: "duplicate_offline", args: []string{"doctor", "--offline", "--offline"}, needle: "--offline --offline"},
		{name: "mixed_offline_bundle", args: []string{"doctor", "--offline", "--bundle", filepath.Join(physicalTempDir(t), "mixed.tar")}, needle: "mixed.tar"},
		{name: "duplicate_bundle", args: []string{"doctor", "--bundle", filepath.Join(physicalTempDir(t), "first.tar"), "--bundle", filepath.Join(physicalTempDir(t), "second.tar")}, needle: "first.tar"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			code, output := runACRMCPCLI(t, tc.args...)
			if code != 2 {
				t.Fatalf("doctor command exited %d, want 2\n%s", code, output)
			}
			if !strings.Contains(output, "Usage: acr-mcp doctor") {
				t.Fatalf("doctor usage missing from output:\n%s", output)
			}
			if strings.Contains(output, tc.needle) {
				t.Fatalf("doctor output leaked %q:\n%s", tc.needle, output)
			}
		})
	}
}

func TestDiagnosticsSubprocessRejectsInvalidFormsWithSanitizedUsage(t *testing.T) {
	testCases := []struct {
		name   string
		args   []string
		needle string
	}{
		{name: "unknown_flag", args: []string{"diagnostics", "--bogus"}, needle: "--bogus"},
		{name: "duplicate_output_same_form", args: []string{"diagnostics", "--output", filepath.Join(physicalTempDir(t), "first.tar"), "--output", filepath.Join(physicalTempDir(t), "second.tar")}, needle: "first.tar"},
		{name: "duplicate_output_mixed_form", args: []string{"diagnostics", "--output=" + filepath.Join(physicalTempDir(t), "one.tar"), "--output", filepath.Join(physicalTempDir(t), "two.tar")}, needle: "one.tar"},
		{name: "duplicate_live", args: []string{"diagnostics", "--output", filepath.Join(physicalTempDir(t), "live.tar"), "--live", "--live"}, needle: "live.tar"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			code, output := runACRMCPCLI(t, tc.args...)
			if code != 2 {
				t.Fatalf("diagnostics command exited %d, want 2\n%s", code, output)
			}
			if !strings.Contains(output, "Usage: acr-mcp diagnostics") {
				t.Fatalf("diagnostics usage missing from output:\n%s", output)
			}
			if strings.Contains(output, tc.needle) {
				t.Fatalf("diagnostics output leaked %q:\n%s", tc.needle, output)
			}
		})
	}
}

// TestVersionAliasesSubprocessPrintFullBuildIdentity is the CHAOS-2926
// release-workflow regression lock at the real subprocess-dispatch level:
// .github/workflows/release.yml's smoke jobs run the compiled binary and
// assert `version`'s stdout equals exactly "$VERSION commit=$COMMIT
// built=$DATE". An unreleased local build keeps the dev-identity defaults,
// so every alias must print "dev commit=unknown built=unknown" here --
// the ldflags-injected-identity case is covered by
// internal/releasebuild's compiled-binary parity test.
func TestVersionAliasesSubprocessPrintFullBuildIdentity(t *testing.T) {
	for _, alias := range []string{"version", "--version", "-version"} {
		t.Run(alias, func(t *testing.T) {
			code, output := runACRMCPCLI(t, alias)
			if code != 0 {
				t.Fatalf("%s exited %d, want 0\n%s", alias, code, output)
			}
			if got, want := strings.TrimSpace(output), "dev commit=unknown built=unknown"; got != want {
				t.Fatalf("%s output = %q, want %q", alias, got, want)
			}
		})
	}
}

func TestSubcommandHelpPrintsUsageAndExitsZero(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		needle string
	}{
		{name: "doctor", args: []string{"doctor", "--help"}, needle: "Usage: acr-mcp doctor"},
		{name: "diagnostics", args: []string{"diagnostics", "--help"}, needle: "Usage: acr-mcp diagnostics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, output := runACRMCPCLI(t, tc.args...)
			if code != 0 {
				t.Fatalf("help command exited %d, want 0\n%s", code, output)
			}
			if !strings.Contains(output, tc.needle) {
				t.Fatalf("usage missing from output:\n%s", output)
			}
		})
	}
}

func TestRootCommandsRejectTrailingArgs(t *testing.T) {
	testCases := []struct {
		name   string
		args   []string
		needle string
	}{
		{name: "help", args: []string{"help", "extra"}, needle: rootUsageLine},
		{name: "version", args: []string{"version", "extra"}, needle: rootUsageLine},
		{name: "metadata", args: []string{"metadata", "extra"}, needle: rootUsageLine},
		{name: "serve", args: []string{"serve", "extra"}, needle: rootUsageLine},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			code, output := runACRMCPCLI(t, tc.args...)
			if code != 2 {
				t.Fatalf("root command exited %d, want 2\n%s", code, output)
			}
			if !strings.Contains(output, tc.needle) {
				t.Fatalf("usage missing from output:\n%s", output)
			}
		})
	}
}

func TestRootCommandNoArgsPreservesMetadata(t *testing.T) {
	t.Setenv("ACR_API_URL", "")
	t.Setenv("ACR_API_TOKEN", "")

	code, output := runACRMCPCLI(t)
	if code != 0 {
		t.Fatalf("root no-arg command exited %d, want 0\n%s", code, output)
	}
	if !strings.Contains(output, `"service":"dev-health-acr-mcp"`) || !strings.Contains(output, `"commit":`) || !strings.Contains(output, `"build_date":`) {
		t.Fatalf("metadata output missing identity fields:\n%s", output)
	}
}
