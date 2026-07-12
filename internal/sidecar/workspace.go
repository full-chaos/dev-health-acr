package sidecar

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// DefaultMaxChangedFiles bounds the changed-file listing when the caller
// does not supply a positive DiscoverOptions.MaxChangedFiles.
const DefaultMaxChangedFiles = 200

// MaxMCPFileRoots bounds how many MCP file roots DiscoverWorkspace will
// consider. A caller (an MCP client relaying its own roots capability) that
// supplies more than this many roots gets a typed error rather than
// unbounded validation/Git-invocation work driven by untrusted input.
const MaxMCPFileRoots = 32

// RootSource identifies how a workspace root candidate was selected, per the
// discovery precedence: explicit value > supplied MCP file root > cwd.
type RootSource string

const (
	RootSourceExplicit RootSource = "explicit"
	RootSourceMCPRoot  RootSource = "mcp_root"
	RootSourceCWD      RootSource = "cwd"
)

// Typed discovery errors. Callers should use errors.Is against these
// sentinels rather than matching on message text.
var (
	ErrNoWorkspaceRoot        = errors.New("sidecar: no workspace root candidate is available")
	ErrInvalidWorkspaceRoot   = errors.New("sidecar: workspace root is invalid")
	ErrWorkspaceRootSymlink   = errors.New("sidecar: workspace root must not be a symlink")
	ErrWorkspaceRootNotDir    = errors.New("sidecar: workspace root is not a directory")
	ErrUnsupportedRootScheme  = errors.New("sidecar: workspace root URI scheme is not supported")
	ErrTooManyWorkspaceRoots  = errors.New("sidecar: too many MCP file roots were supplied")
	ErrControlCharacters      = errors.New("sidecar: value contains control characters")
	ErrNotGitRepository       = errors.New("sidecar: path is not inside a Git repository")
	ErrAmbiguousWorkspaceRoot = errors.New("sidecar: candidate roots resolve to more than one Git repository")
	ErrUnsupportedRemote      = errors.New("sidecar: Git remote URL shape is not supported")
	ErrAmbiguousRemote        = errors.New("sidecar: multiple Git remotes and no origin to disambiguate")
	ErrNoCommits              = errors.New("sidecar: Git repository has no commits yet")
	ErrGitOutputTooLarge      = errors.New("sidecar: Git command produced more output than the bounded read allows")
)

// RemoteInfo is a normalized, safe-shape Git remote identity.
type RemoteInfo struct {
	Name  string
	Host  string
	Owner string
	Repo  string
}

// Slug returns the normalized "owner/repo" identity, or the empty string if
// either component is unset.
func (r RemoteInfo) Slug() string {
	if r.Owner == "" || r.Repo == "" {
		return ""
	}
	return r.Owner + "/" + r.Repo
}

// WorkspaceInfo is the bounded result of local Git workspace discovery.
// Every field is derived from local Git state only; no network access is
// performed.
type WorkspaceInfo struct {
	RootSource            RootSource
	GitRoot               string
	Remote                *RemoteInfo
	Branch                string
	CommitSHA             string
	Detached              bool
	ChangedFiles          []string
	ChangedFilesTruncated bool
}

// DiscoverOptions configures workspace discovery.
//
// Precedence (highest first): ExplicitRepoPath, MCPFileRoots,
// WorkingDirectory (falling back to the process working directory when
// unset). Only the highest-precedence non-empty source is consulted.
type DiscoverOptions struct {
	// ExplicitRepoPath is a caller-supplied repository path override, e.g.
	// from an explicit tool argument. Highest precedence.
	ExplicitRepoPath string
	// MCPFileRoots are workspace roots supplied by the MCP client's roots
	// capability (plain absolute paths or file:// URIs). Consulted only
	// when ExplicitRepoPath is empty.
	MCPFileRoots []string
	// WorkingDirectory overrides the process working directory fallback.
	// Consulted only when ExplicitRepoPath and MCPFileRoots are both empty.
	// Empty means use os.Getwd().
	WorkingDirectory string
	// IncludeChangedFiles opts into the bounded changed-file listing.
	IncludeChangedFiles bool
	// MaxChangedFiles bounds the changed-file listing. Non-positive values
	// fall back to DefaultMaxChangedFiles.
	MaxChangedFiles int
}

// DiscoverWorkspace performs bounded local Git workspace discovery: it
// resolves a single unambiguous Git repository from the configured
// precedence, then reads the Git root, normalized remote owner/repo,
// current branch (empty when HEAD is detached), current commit SHA, and,
// optionally, a bounded deterministic list of changed files.
//
// DiscoverWorkspace never invokes a shell; all Git invocations use
// exec.CommandContext with a fixed argv. It performs no network access.
func DiscoverWorkspace(ctx context.Context, opts DiscoverOptions) (WorkspaceInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	candidates, err := resolveCandidateRoots(opts)
	if err != nil {
		return WorkspaceInfo{}, err
	}

	gitRoot, source, err := resolveGitRootAmongCandidates(ctx, candidates)
	if err != nil {
		return WorkspaceInfo{}, err
	}

	branch, detached, err := gitCurrentBranch(ctx, gitRoot)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if containsControlChar(branch) {
		return WorkspaceInfo{}, fmt.Errorf("%w: branch name", ErrControlCharacters)
	}

	commitSHA, err := gitHeadCommit(ctx, gitRoot)
	if err != nil {
		return WorkspaceInfo{}, err
	}

	remote, err := discoverRemote(ctx, gitRoot)
	if err != nil {
		return WorkspaceInfo{}, err
	}

	info := WorkspaceInfo{
		RootSource: source,
		GitRoot:    gitRoot,
		Remote:     remote,
		Branch:     branch,
		CommitSHA:  commitSHA,
		Detached:   detached,
	}

	if opts.IncludeChangedFiles {
		maxFiles := opts.MaxChangedFiles
		if maxFiles <= 0 {
			maxFiles = DefaultMaxChangedFiles
		}
		files, truncated, cfErr := gitChangedFiles(ctx, gitRoot, maxFiles)
		if cfErr != nil {
			return WorkspaceInfo{}, cfErr
		}
		info.ChangedFiles = files
		info.ChangedFilesTruncated = truncated
	}

	return info, nil
}

// discoverRemote resolves the canonical remote for owner/repo normalization:
// "origin" when present, the sole remote when there is exactly one and no
// origin, nil when there are no remotes, and a typed ambiguity error when
// there are multiple remotes and no origin to disambiguate.
func discoverRemote(ctx context.Context, gitRoot string) (*RemoteInfo, error) {
	names, err := gitRemoteNames(ctx, gitRoot)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}

	chosen := ""
	for _, n := range names {
		if n == "origin" {
			chosen = n
			break
		}
	}
	if chosen == "" {
		if len(names) == 1 {
			chosen = names[0]
		} else {
			sorted := append([]string(nil), names...)
			sort.Strings(sorted)
			return nil, fmt.Errorf("%w: %s", ErrAmbiguousRemote, strings.Join(sorted, ", "))
		}
	}

	remoteURL, err := gitRemoteURL(ctx, gitRoot, chosen)
	if err != nil {
		return nil, err
	}
	info, err := parseRemoteURL(chosen, remoteURL)
	if err != nil {
		return nil, err
	}
	return &info, nil
}
