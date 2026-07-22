package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/releasebuild"
)

var errDirtyCheckout = errors.New("git checkout is dirty")

const rootUsage = `Usage: releasebuild <build|verify|consume> [flags]

Commands:
	build   create deterministic release archives
	verify  verify release archives and checksums
	consume verify and extract the host acr-mcp release artifact
`

const buildUsage = `Usage: releasebuild build --out DIR --version VERSION --commit SHA --date UTC_TIMESTAMP [--root DIR]

Creates deterministic archives for the supported release matrix.
`

const verifyUsage = `Usage: releasebuild verify --dir DIR

Verifies the release manifest and SHA256SUMS.
`

const consumeUsage = `Usage: releasebuild consume --dir DIR --dest DIR

Verifies and extracts the current-host acr-mcp release artifact.
`

type runner struct {
	compiler  releasebuild.Compiler
	gitStatus func(context.Context, string) error
	consume   func(context.Context, releasebuild.ConsumeRequest) (releasebuild.Receipt, error)
}

func main() {
	r := runner{compiler: releasebuild.GoCompiler{}, gitStatus: cleanGitCheckout}
	if err := r.run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "releasebuild:", err)
		fmt.Fprintln(os.Stderr, "Try 'releasebuild --help' for usage.")
		os.Exit(1)
	}
}

func (r runner) run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("expected build or verify command")
	}
	if helpRequested(args) {
		_, err := fmt.Fprint(output, rootUsage)
		return err
	}
	switch args[0] {
	case "build":
		return r.build(ctx, args[1:], output)
	case "verify":
		return r.verify(args[1:], output)
	case "consume":
		return r.consumeRelease(ctx, args[1:], output)
	default:
		return fmt.Errorf("unsupported command %q", args[0])
	}
}

func (r runner) consumeRelease(ctx context.Context, args []string, output io.Writer) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(output, consumeUsage)
		return err
	}
	flags := flag.NewFlagSet("consume", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "release directory")
	destination := flags.String("dest", "", "empty extraction destination")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("consume does not accept positional arguments")
	}
	consume := r.consume
	if consume == nil {
		consume = releasebuild.Consume
	}
	receipt, err := consume(ctx, releasebuild.ConsumeRequest{ReleaseDir: *dir, Destination: *destination})
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode receipt: %w", err)
	}
	_, err = fmt.Fprintln(output, string(encoded))
	return err
}

func (r runner) build(ctx context.Context, args []string, output io.Writer) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(output, buildUsage)
		return err
	}
	flags := flag.NewFlagSet("build", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "repository root")
	out := flags.String("out", "", "empty release output directory")
	version := flags.String("version", "", "canonical release version")
	commit := flags.String("commit", "", "full commit SHA")
	date := flags.String("date", "", "UTC commit timestamp")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("build does not accept positional arguments")
	}
	identity := releasebuild.Identity{Version: *version, Commit: *commit, Date: *date}
	if err := identity.Validate(); err != nil {
		return err
	}
	rootPath, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(rootPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("repository root must be a directory")
	}
	status := r.gitStatus
	if status == nil {
		status = cleanGitCheckout
	}
	if err := status(ctx, rootPath); err != nil {
		return err
	}
	compiler := r.compiler
	if compiler == nil {
		compiler = releasebuild.GoCompiler{}
	}
	if _, err := releasebuild.NewBuilder(compiler).Build(ctx, releasebuild.Request{SourceDir: rootPath, OutputDir: *out, Identity: identity}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "releasebuild build: %s\n", filepath.Join(*out, "release-manifest.json"))
	return err
}

func (r runner) verify(args []string, output io.Writer) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(output, verifyUsage)
		return err
	}
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "release directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("verify does not accept positional arguments")
	}
	if err := releasebuild.Verify(*dir); err != nil {
		return err
	}
	_, err := fmt.Fprintln(output, "releasebuild verify: ok")
	return err
}

func helpRequested(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help")
}

func cleanGitCheckout(ctx context.Context, root string) error {
	check := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--is-inside-work-tree")
	inside, err := check.Output()
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return fmt.Errorf("repository root is not a git worktree")
	}
	command := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain=v1", "--untracked-files=all")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("inspect git checkout: %w", err)
	}
	if strings.TrimSpace(string(output)) != "" {
		return errDirtyCheckout
	}
	return nil
}
