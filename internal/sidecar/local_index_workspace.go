package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxLocalRequestedCategories = 8

func normalizeCodeGraphWorkspace(workspace *LocalWorkspace) (LocalWorkspace, error) {
	if workspace == nil || workspace.TargetFilesTruncated {
		return LocalWorkspace{}, ErrInvalidLocalContextRequest
	}
	if !validRepositorySlug(workspace.RepositorySlug) || !validCommitSHA(workspace.CommitSHA) || (workspace.Detached && workspace.Branch != "") || (!workspace.Detached && !validCodeGraphText(workspace.Branch, maxLocalTaskIDBytes)) {
		return LocalWorkspace{}, ErrInvalidLocalContextRequest
	}
	root, err := canonicalCodeGraphRoot(workspace.Root)
	if err != nil {
		return LocalWorkspace{}, ErrInvalidLocalContextRequest
	}
	files, err := normalizeRepositoryPaths(workspace.TargetFiles)
	if err != nil {
		return LocalWorkspace{}, ErrInvalidLocalContextRequest
	}
	normalized := *workspace
	normalized.Root = root
	normalized.TargetFiles = files
	return normalized, nil
}

func validRequestedCategories(categories []string) bool {
	if len(categories) > maxLocalRequestedCategories {
		return false
	}
	for _, category := range categories {
		if !validCodeGraphText(category, maxLocalEvidenceTitleBytes) {
			return false
		}
	}
	return true
}

func canonicalCodeGraphRoot(root string) (string, error) {
	if root == "" || containsControlChar(root) || !filepath.IsAbs(root) {
		return "", ErrInvalidWorkspaceRoot
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", ErrInvalidWorkspaceRoot
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", ErrInvalidWorkspaceRoot
	}
	return canonical, nil
}

func trustedCodeGraphRoot(root string) bool {
	_, err := canonicalCodeGraphRoot(root)
	return err == nil
}

func normalizeRepositoryPaths(paths []string) ([]string, error) {
	if len(paths) > DefaultMaxChangedFiles {
		return nil, fmt.Errorf("too many paths")
	}
	normalized := append([]string(nil), paths...)
	sort.Strings(normalized)
	if len(normalized) == 0 {
		return normalized, nil
	}
	output := normalized[:0]
	for _, path := range normalized {
		if !validRepositoryRelativePath(path) {
			return nil, fmt.Errorf("invalid path")
		}
		if len(output) > 0 && output[len(output)-1] == path {
			return nil, fmt.Errorf("duplicate path")
		}
		output = append(output, path)
	}
	return output, nil
}

func validRepositoryRelativePath(value string) bool {
	if !validCodeGraphText(value, maxLocalEvidenceLocatorBytes) || filepath.IsAbs(value) || strings.HasPrefix(value, "\\") || hasWindowsAbsolutePathPrefix(value) || strings.Contains(value, "\\") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validRepositorySlug(value string) bool {
	return validCodeGraphText(value, maxLocalEvidenceLocatorBytes) && !strings.Contains(value, "\\") && !strings.HasPrefix(value, "/") && !strings.Contains(value, "..")
}

func validCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validCodeGraphText(value string, maximum int) bool {
	if !boundedNonEmpty(value, maximum) || !utf8.ValidString(value) || strings.HasPrefix(value, "-") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
