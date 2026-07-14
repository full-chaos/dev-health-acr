package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const workspaceUsageLine = "Usage: acr-mcp workspace --path <dir>"

// workspaceReport is the safe, secret-free JSON shape `acr-mcp workspace`
// prints. Every field is derived from local, read-only Git state only (see
// sidecar.DiscoverWorkspace's own doc comment): no network access is
// performed and nothing here can carry a credential.
type workspaceReport struct {
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	GitRoot   string `json:"git_root,omitempty"`
	Remote    string `json:"remote,omitempty"`
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Detached  bool   `json:"detached,omitempty"`
}

func workspaceHelpRequested(args []string) bool {
	return len(args) == 1 && isHelpArg(args[0])
}

// parseWorkspaceArgs requires an explicit --path so this command never
// silently discovers whatever the process's own working directory happens
// to be -- the same "no implicit default destination" discipline
// parseDiagnosticsArgs already applies to --output.
func parseWorkspaceArgs(args []string) (string, error) {
	path := ""
	seenPath := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--path":
			if seenPath || i+1 >= len(args) {
				return "", errors.New("workspace arguments are invalid")
			}
			i++
			path = args[i]
			seenPath = true
		case strings.HasPrefix(args[i], "--path="):
			if seenPath {
				return "", errors.New("workspace arguments are invalid")
			}
			path = strings.TrimPrefix(args[i], "--path=")
			seenPath = true
		default:
			return "", errors.New("workspace arguments are invalid")
		}
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("workspace requires an explicit --path <dir>")
	}
	return path, nil
}

func printWorkspaceUsage() {
	fmt.Fprintln(os.Stdout, workspaceUsageLine)
}

// runWorkspaceCommand exercises the exact same read-only, network-free
// local Git workspace discovery the "context_for_task" MCP tool relies on
// (sidecar.DiscoverWorkspace) directly from the CLI, so it can be probed
// against a real mounted Git workspace -- e.g. a container's read-only
// bind-mounted /workspace -- without needing a full STDIO MCP handshake
// against a hosted API. It performs no network access and never runs an
// interactive server, preserving the sidecar's STDIO-only, no-service
// contract.
func runWorkspaceCommand(args []string) int {
	if workspaceHelpRequested(args) {
		printWorkspaceUsage()
		return 0
	}
	path, err := parseWorkspaceArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace: invalid arguments")
		printWorkspaceUsage()
		return 2
	}
	info, discoverErr := sidecar.DiscoverWorkspace(context.Background(), sidecar.DiscoverOptions{ExplicitRepoPath: path})
	if discoverErr != nil {
		printJSON(workspaceReport{Status: "error", Detail: discoverErr.Error()})
		return 1
	}
	report := workspaceReport{
		Status:    "ok",
		GitRoot:   info.GitRoot,
		Branch:    info.Branch,
		CommitSHA: info.CommitSHA,
		Detached:  info.Detached,
	}
	if info.Remote != nil {
		report.Remote = info.Remote.Slug()
	}
	printJSON(report)
	return 0
}
