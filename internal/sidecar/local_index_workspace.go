package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const maxLocalRequestedCategories = 8

func NewLocalWorkspaceSnapshot(info WorkspaceInfo, expectedSlug string, filesRequested bool) (LocalWorkspaceSnapshot, error) {
	if info.Remote == nil || !strings.EqualFold(strings.TrimSpace(info.Remote.Slug()), strings.TrimSpace(expectedSlug)) {
		return LocalWorkspaceSnapshot{}, ErrInvalidLocalContextRequest
	}
	state := LocalChangedFilesNotRequested
	if filesRequested {
		state = LocalChangedFilesComplete
		if info.ChangedFilesTruncated {
			state = LocalChangedFilesTruncated
		}
	}
	return normalizeCodeGraphWorkspace(&LocalWorkspaceSnapshot{GitRoot: info.GitRoot, Repository: LocalRepositoryIdentity{Host: info.Remote.Host, Slug: info.Remote.Slug()}, Branch: info.Branch, CommitSHA: info.CommitSHA, Detached: info.Detached, ChangedFiles: info.ChangedFiles, ChangedFilesState: state})
}

func normalizeCodeGraphWorkspace(workspace *LocalWorkspaceSnapshot) (LocalWorkspaceSnapshot, error) {
	if workspace == nil || workspace.ChangedFilesState == LocalChangedFilesTruncated || (workspace.ChangedFilesState != LocalChangedFilesNotRequested && workspace.ChangedFilesState != LocalChangedFilesComplete) {
		return LocalWorkspaceSnapshot{}, ErrInvalidLocalContextRequest
	}
	if !validRepositorySlug(workspace.Repository.Slug) || !validCodeGraphText(workspace.Repository.Host, maxLocalEvidenceLocatorBytes) || !validCommitSHA(workspace.CommitSHA) || (workspace.Detached && workspace.Branch != "") || (!workspace.Detached && !validCodeGraphText(workspace.Branch, maxLocalTaskIDBytes)) {
		return LocalWorkspaceSnapshot{}, ErrInvalidLocalContextRequest
	}
	root, err := canonicalCodeGraphRoot(workspace.GitRoot)
	if err != nil {
		return LocalWorkspaceSnapshot{}, ErrInvalidLocalContextRequest
	}
	files, err := normalizeRepositoryPaths(workspace.ChangedFiles)
	if err != nil {
		return LocalWorkspaceSnapshot{}, ErrInvalidLocalContextRequest
	}
	normalized := *workspace
	normalized.GitRoot = root
	normalized.ChangedFiles = files
	return normalized, nil
}

func validRequestedCategories(categories []contractsv1.PacketCategory) bool {
	if len(categories) > maxLocalRequestedCategories {
		return false
	}
	seen := map[contractsv1.PacketCategory]bool{}
	for _, category := range categories {
		if seen[category] || (category != contractsv1.CategoryState && category != contractsv1.CategoryPressure && category != contractsv1.CategoryCause && category != contractsv1.CategoryEvidence && category != contractsv1.CategoryAction) {
			return false
		}
		seen[category] = true
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
	for part := range strings.SplitSeq(value, "/") {
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
