package sidecar

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// rootCandidate is an unvalidated workspace root paired with the precedence
// tier it came from.
type rootCandidate struct {
	Path   string
	Source RootSource
}

// resolveCandidateRoots builds the ordered candidate root list honoring
// discovery precedence: ExplicitRepoPath > MCPFileRoots > WorkingDirectory
// (falling back to os.Getwd()). Only the highest-precedence non-empty source
// contributes candidates.
func resolveCandidateRoots(opts DiscoverOptions) ([]rootCandidate, error) {
	if strings.TrimSpace(opts.ExplicitRepoPath) != "" {
		return []rootCandidate{{Path: opts.ExplicitRepoPath, Source: RootSourceExplicit}}, nil
	}

	if len(opts.MCPFileRoots) > 0 {
		if len(opts.MCPFileRoots) > MaxMCPFileRoots {
			return nil, fmt.Errorf("%w: got %d, max %d", ErrTooManyWorkspaceRoots, len(opts.MCPFileRoots), MaxMCPFileRoots)
		}
		candidates := make([]rootCandidate, 0, len(opts.MCPFileRoots))
		for _, root := range opts.MCPFileRoots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			candidates = append(candidates, rootCandidate{Path: root, Source: RootSourceMCPRoot})
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("%w: supplied MCP file roots were all empty", ErrNoWorkspaceRoot)
		}
		return candidates, nil
	}

	cwd := opts.WorkingDirectory
	if strings.TrimSpace(cwd) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoWorkspaceRoot, err)
		}
		cwd = wd
	}
	return []rootCandidate{{Path: cwd, Source: RootSourceCWD}}, nil
}

// validateRootPath resolves a candidate root string (an absolute plain path
// or a file:// URI) to a clean absolute directory path and rejects unsafe
// shapes: control characters, non-file URI schemes, relative paths, the
// root path itself being a symlink, and non-directory targets.
//
// The root path itself is checked with os.Lstat (not os.Stat), so a root
// that is a symlink is rejected outright rather than followed, even though
// an unrelated ancestor directory being a symlink (e.g. macOS's /tmp ->
// /private/tmp) is not treated as an escape.
func validateRootPath(raw string) (string, error) {
	if containsControlChar(raw) {
		return "", fmt.Errorf("%w: workspace root", ErrControlCharacters)
	}

	path := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("%w: %s", ErrInvalidWorkspaceRoot, raw)
		}
		if u.Scheme != "file" {
			return "", fmt.Errorf("%w: %s", ErrUnsupportedRootScheme, raw)
		}
		path = u.Path
		if path == "" {
			path = u.Opaque
		}
	}

	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: workspace root must be an absolute path: %s", ErrInvalidWorkspaceRoot, raw)
	}
	path = filepath.Clean(path)

	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrInvalidWorkspaceRoot, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %s", ErrWorkspaceRootSymlink, path)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrWorkspaceRootNotDir, path)
	}
	return path, nil
}

// resolveGitRootAmongCandidates validates each candidate root and resolves
// its Git working-tree root, then requires all valid candidates to agree on
// a single Git repository. Candidates that fail validation or are outside
// any Git repository are skipped when there is more than one candidate (so
// one bad MCP root does not block a good one), but a single candidate's
// error is returned directly. When two or more candidates resolve to
// distinct Git repositories, discovery is rejected as ambiguous rather than
// silently picking one.
func resolveGitRootAmongCandidates(ctx context.Context, candidates []rootCandidate) (string, RootSource, error) {
	type resolved struct {
		gitRoot   string
		canonical string
		source    RootSource
	}

	var results []resolved
	var lastErr error

	for _, c := range candidates {
		validated, err := validateRootPath(c.Path)
		if err != nil {
			if len(candidates) == 1 {
				return "", "", err
			}
			lastErr = err
			continue
		}

		gitRoot, err := gitShowToplevel(ctx, validated)
		if err != nil {
			if isContextErr(err) {
				return "", "", err
			}
			if len(candidates) == 1 {
				return "", "", err
			}
			lastErr = err
			continue
		}

		canonical := gitRoot
		if real, evalErr := filepath.EvalSymlinks(gitRoot); evalErr == nil {
			canonical = real
		}
		results = append(results, resolved{gitRoot: gitRoot, canonical: canonical, source: c.Source})
	}

	if len(results) == 0 {
		if lastErr != nil {
			return "", "", lastErr
		}
		return "", "", fmt.Errorf("%w: no candidate workspace root is inside a Git repository", ErrNotGitRepository)
	}

	unique := make(map[string]resolved, len(results))
	for _, r := range results {
		unique[r.canonical] = r
	}
	if len(unique) > 1 {
		roots := make([]string, 0, len(unique))
		for k := range unique {
			roots = append(roots, k)
		}
		sort.Strings(roots)
		return "", "", fmt.Errorf("%w: %s", ErrAmbiguousWorkspaceRoot, strings.Join(roots, ", "))
	}

	for _, r := range unique {
		return r.gitRoot, r.source, nil
	}
	// unreachable: unique has exactly one entry here.
	return "", "", fmt.Errorf("%w", ErrNotGitRepository)
}
