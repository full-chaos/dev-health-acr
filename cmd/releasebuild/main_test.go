package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/releasebuild"
)

func TestRunner_build_writes_manifest_path_when_inputs_are_clean(t *testing.T) {
	// Given
	output := t.TempDir()
	stdout := &bytes.Buffer{}
	runner := runner{
		compiler: releasebuild.CompilerFunc(func(_ context.Context, request releasebuild.CompileRequest) error {
			return os.WriteFile(request.OutputPath, []byte(request.Identity.Version), 0o755)
		}),
		gitStatus: func(context.Context, string) error { return nil },
	}
	args := []string{"build", "--root", releaseSourceRoot(t), "--out", output, "--version", "1.2.3", "--commit", "0123456789abcdef0123456789abcdef01234567", "--date", "2026-07-12T13:14:15Z"}

	// When
	err := runner.run(context.Background(), args, stdout)

	// Then
	if err != nil {
		t.Fatalf("run build: %v", err)
	}
	want := "releasebuild build: " + filepath.Join(output, "release-manifest.json") + "\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunner_rejects_missing_or_dirty_build_inputs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		gitStatus func(context.Context, string) error
	}{
		{name: "missing identity", args: []string{"build", "--out", t.TempDir()}},
		{name: "dirty checkout", args: []string{"build", "--root", t.TempDir(), "--out", t.TempDir(), "--version", "1.2.3", "--commit", "0123456789abcdef0123456789abcdef01234567", "--date", "2026-07-12T13:14:15Z"}, gitStatus: func(context.Context, string) error { return errDirtyCheckout }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			runner := runner{compiler: releasebuild.CompilerFunc(func(context.Context, releasebuild.CompileRequest) error { return nil }), gitStatus: test.gitStatus}

			// When
			err := runner.run(context.Background(), test.args, &bytes.Buffer{})

			// Then
			if err == nil {
				t.Fatal("run() error = nil, want rejection")
			}
		})
	}
}

func TestRunner_verify_reports_success(t *testing.T) {
	// Given
	dir := t.TempDir()
	manifest, err := releasebuild.NewBuilder(releasebuild.CompilerFunc(func(_ context.Context, request releasebuild.CompileRequest) error {
		return os.WriteFile(request.OutputPath, []byte("binary"), 0o755)
	})).Build(context.Background(), releasebuild.Request{SourceDir: releaseSourceRoot(t), OutputDir: dir, Identity: releasebuild.Identity{Version: "1.2.3", Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-07-12T13:14:15Z"}})
	if err != nil || len(manifest.Artifacts) == 0 {
		t.Fatalf("build fixture: manifest=%#v err=%v", manifest, err)
	}
	stdout := &bytes.Buffer{}

	// When
	err = (runner{}).run(context.Background(), []string{"verify", "--dir", dir}, stdout)

	// Then
	if err != nil {
		t.Fatalf("run verify: %v", err)
	}
	if !strings.Contains(stdout.String(), "releasebuild verify: ok") {
		t.Errorf("stdout = %q, want success receipt", stdout.String())
	}
}

func TestRunner_consume_emits_sanitized_receipt_when_inputs_are_valid(t *testing.T) {
	// Given
	var output bytes.Buffer
	r := runner{consume: func(_ context.Context, request releasebuild.ConsumeRequest) (releasebuild.Receipt, error) {
		if request.ReleaseDir != "release" || request.Destination != "destination" {
			t.Fatalf("unexpected request: %#v", request)
		}
		return releasebuild.Receipt{ArchiveSHA256: "archive", ClientBundleSHA256: "clients", Product: "acr-mcp", GOOS: "darwin", GOARCH: "arm64"}, nil
	}}

	// When
	err := r.run(context.Background(), []string{"consume", "--dir", "release", "--dest", "destination"}, &output)

	// Then
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got, want := output.String(), "{\"archive_sha256\":\"archive\",\"client_bundle_sha256\":\"clients\",\"product\":\"acr-mcp\",\"goos\":\"darwin\",\"goarch\":\"arm64\"}\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestCleanGitCheckout_rejects_non_repository_directory(t *testing.T) {
	// Given
	dir := t.TempDir()

	// When
	err := cleanGitCheckout(context.Background(), dir)

	// Then
	if err == nil {
		t.Fatal("cleanGitCheckout() error = nil, want non-repository rejection")
	}
}

func TestReleasebuild_help_and_invalid_commands_are_process_usable(t *testing.T) {
	// Given
	binary := buildReleasebuildBinary(t)
	tests := []struct {
		name       string
		args       []string
		wantExitOK bool
		wantOutput string
	}{
		{name: "root help", args: []string{"--help"}, wantExitOK: true, wantOutput: "Usage: releasebuild <build|verify|consume>"},
		{name: "build help", args: []string{"build", "--help"}, wantExitOK: true, wantOutput: "Usage: releasebuild build"},
		{name: "verify help", args: []string{"verify", "--help"}, wantExitOK: true, wantOutput: "Usage: releasebuild verify"},
		{name: "consume help", args: []string{"consume", "--help"}, wantExitOK: true, wantOutput: "Usage: releasebuild consume"},
		{name: "missing command", wantOutput: "Try 'releasebuild --help'"},
		{name: "invalid command", args: []string{"publish"}, wantOutput: "Try 'releasebuild --help'"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			command := exec.Command(binary, test.args...)
			var output []byte
			var err error
			if test.wantExitOK {
				output, err = command.Output()
			} else {
				output, err = command.CombinedOutput()
			}

			// Then
			if (err == nil) != test.wantExitOK {
				t.Fatalf("exit error = %v, want success=%t; output=%q", err, test.wantExitOK, output)
			}
			if !strings.Contains(string(output), test.wantOutput) {
				t.Errorf("output = %q, want %q", output, test.wantOutput)
			}
		})
	}
}

func buildReleasebuildBinary(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "releasebuild")
	command := exec.Command("go", "build", "-o", binary, "./cmd/releasebuild")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build releasebuild binary: %v: %s", err, output)
	}
	return binary
}

func releaseSourceRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}
