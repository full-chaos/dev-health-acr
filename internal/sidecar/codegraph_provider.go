package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const codeGraphProviderID = "codegraph"

// CodeGraphLocalIndexProvider consumes a trusted existing CodeGraph index
// through ADR-0005's fixed JSON subprocess contract only.
type CodeGraphLocalIndexProvider struct {
	runner    CodeGraphRunner
	workspace LocalWorkspaceSnapshot
	mu        sync.RWMutex
	evidence  map[string]LocalExpandedEvidence
}

func NewCodeGraphLocalIndexProvider(runner CodeGraphRunner, workspace LocalWorkspaceSnapshot) *CodeGraphLocalIndexProvider {
	return &CodeGraphLocalIndexProvider{runner: runner, workspace: workspace, evidence: map[string]LocalExpandedEvidence{}}
}

func (p *CodeGraphLocalIndexProvider) Capabilities(ctx context.Context) (LocalIndexCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return LocalIndexCapabilities{}, localIndexFailure(err)
	}
	workspace, err := normalizeCodeGraphWorkspace(&p.workspace)
	if err != nil {
		return LocalIndexCapabilities{}, localIndexFailure(errCodeGraphMismatch)
	}
	status, err := p.status(ctx, workspace)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return LocalIndexCapabilities{}, localIndexFailure(err)
		}
		return LocalIndexCapabilities{}, localIndexFailure(err)
	}
	classification := mergeCodeGraphClassification(status, workspace)
	if classification.omit(p.runner.Config.StalePolicy) {
		return LocalIndexCapabilities{}, newLocalIndexError(LocalIndexErrorStale, LocalIndexStatusUnavailable, classification.Freshness, classification.Warnings, classification.staleCause())
	}
	return LocalIndexCapabilities{ProviderID: codeGraphProviderID, ProviderVersion: status.Version, Available: true, MaxItems: p.itemLimit(), MaxOutputTokens: p.tokenLimit(), Status: classification.Status, Freshness: classification.Freshness}, nil
}

func (p *CodeGraphLocalIndexProvider) ContextForTask(ctx context.Context, request LocalContextRequest) (LocalEvidenceBundle, error) {
	if err := ctx.Err(); err != nil {
		return LocalEvidenceBundle{}, localIndexFailure(err)
	}
	if err := ValidateLocalContextRequest(request); err != nil {
		return LocalEvidenceBundle{}, ErrInvalidLocalContextRequest
	}
	workspace, err := normalizeCodeGraphWorkspace(request.Workspace)
	configuredWorkspace, configuredErr := normalizeCodeGraphWorkspace(&p.workspace)
	if err != nil || configuredErr != nil || !sameCodeGraphWorkspaceScope(workspace, configuredWorkspace) || (workspace.ChangedFilesState == LocalChangedFilesComplete && !sameCodeGraphWorkspace(workspace, configuredWorkspace)) {
		return LocalEvidenceBundle{}, newLocalIndexError(LocalIndexErrorWorktreeMismatch, LocalIndexStatusUnavailable, LocalIndexFreshnessStale, []string{"local_worktree_mismatch"}, ErrInvalidLocalContextRequest)
	}
	status, err := p.status(ctx, workspace)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return LocalEvidenceBundle{}, localIndexFailure(err)
		}
		return LocalEvidenceBundle{}, localIndexFailure(err)
	}
	classification := mergeCodeGraphClassification(status, workspace)
	classification.Warnings = canonicalBundleWarnings(classification.Warnings, false, status.IndexedCommit)
	if classification.omit(p.runner.Config.StalePolicy) {
		return LocalEvidenceBundle{}, newLocalIndexError(LocalIndexErrorStale, LocalIndexStatusUnavailable, classification.Freshness, classification.Warnings, classification.staleCause())
	}
	candidates, err := p.collectCandidates(ctx, workspace, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return LocalEvidenceBundle{}, localIndexFailure(err)
		}
		return LocalEvidenceBundle{}, localIndexFailure(err)
	}
	evidence, candidateTruncated, err := buildCodeGraphEvidence(candidates, min(request.MaxItems, p.itemLimit()), min(request.MaxOutputTokens, p.tokenLimit()))
	if err != nil {
		return LocalEvidenceBundle{}, localIndexFailure(err)
	}
	bundle := LocalEvidenceBundle{ProviderID: codeGraphProviderID, ProviderVersion: status.Version, QueryID: "query", QueryVersion: codeGraphJSONQueryVersion, IndexedAt: &status.LastIndexedAt, Status: classification.Status, Freshness: classification.Freshness, Truncated: candidateTruncated, Evidence: evidence}
	bundle.Warnings = canonicalBundleWarnings(classification.Warnings, bundle.Truncated, bundle.IndexedCommit)
	bundle, payloadTruncated, err := trimCodeGraphEvidence(bundle)
	if err != nil {
		return LocalEvidenceBundle{}, localIndexFailure(err)
	}
	bundle.Truncated = bundle.Truncated || payloadTruncated
	normalized, err := NormalizeLocalEvidenceBundleForRequest(request, LocalIndexCapabilities{ProviderID: codeGraphProviderID, ProviderVersion: status.Version, Available: true, MaxItems: p.itemLimit(), MaxOutputTokens: p.tokenLimit(), Status: classification.Status, Freshness: classification.Freshness}, bundle)
	if err != nil {
		return LocalEvidenceBundle{}, localIndexFailure(err)
	}
	p.remember(normalized.Evidence)
	return normalized, nil
}

func (p *CodeGraphLocalIndexProvider) ResolveEvidence(ctx context.Context, locator string) (LocalExpandedEvidence, error) {
	if err := ctx.Err(); err != nil {
		return LocalExpandedEvidence{}, err
	}
	if !boundedLocalLocator(locator) {
		return LocalExpandedEvidence{}, ErrLocalEvidenceNotFound
	}
	p.mu.RLock()
	evidence, found := p.evidence[locator]
	p.mu.RUnlock()
	if !found {
		return LocalExpandedEvidence{}, ErrLocalEvidenceNotFound
	}
	return evidence, nil
}

func (p *CodeGraphLocalIndexProvider) status(ctx context.Context, workspace LocalWorkspaceSnapshot) (codeGraphStatus, error) {
	payload, err := p.runner.Status(ctx, workspace.GitRoot)
	if err != nil {
		return codeGraphStatus{}, err
	}
	status, err := decodeCodeGraphStatus(payload)
	if err != nil {
		return codeGraphStatus{}, err
	}
	if status.ProjectPath != workspace.GitRoot || status.IndexPath != filepath.Join(workspace.GitRoot, ".codegraph") {
		return codeGraphStatus{}, errCodeGraphMismatch
	}
	if !trustedCodeGraphIndex(workspace.GitRoot) {
		return codeGraphStatus{}, errCodeGraphMissing
	}
	return status, nil
}

func mergeCodeGraphClassification(status codeGraphStatus, workspace LocalWorkspaceSnapshot) localIndexClassification {
	classification := classifyCodeGraphStatus(status)
	workspaceClassification := classifyCodeGraphWorkspace(workspace)
	classification.Warnings = append(classification.Warnings, workspaceClassification.Warnings...)
	if workspaceClassification.Status == LocalIndexStatusDegraded {
		classification.Status = LocalIndexStatusDegraded
	}
	if workspaceClassification.Freshness == LocalIndexFreshnessStale {
		classification.Freshness = LocalIndexFreshnessStale
	}
	return classification
}

func trustedCodeGraphIndex(root string) bool {
	index := filepath.Join(root, ".codegraph")
	info, err := os.Lstat(index)
	if err != nil {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if err := verifyCurrentUserOwnedDir(resolvedRoot); err != nil {
			return false
		}
		if verifyCurrentUserOwned(info) != nil {
			return false
		}
		resolvedIndex, err := filepath.EvalSymlinks(index)
		return err == nil && resolvedIndex == filepath.Join(resolvedRoot, ".codegraph")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	managedRoot := filepath.Join(home, ".omo", "codegraph", "projects")
	resolvedManagedRoot, err := filepath.EvalSymlinks(managedRoot)
	if err != nil || verifyCurrentUserOwnedDir(resolvedManagedRoot) != nil {
		return false
	}
	resolvedIndex, err := filepath.EvalSymlinks(index)
	if err != nil || filepath.Dir(resolvedIndex) != resolvedManagedRoot || strings.Contains(filepath.Base(resolvedIndex), string(filepath.Separator)) {
		return false
	}
	if err := verifyCurrentUserOwnedDir(resolvedIndex); err != nil {
		return false
	}
	if !trustedCodeGraphRepositoryRoot(resolvedRoot, home) {
		return false
	}
	return trustedCodeGraphDB(resolvedIndex)
}

func trustedCodeGraphRepositoryRoot(root, home string) bool {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return false
	}
	if verifyCurrentUserOwned(info) == nil {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !trustedCodeGraphGroupWritableMetadata(int(stat.Uid), int(stat.Gid), info.Mode().Perm(), os.Geteuid(), os.Getegid()) {
		return false
	}
	if !codeGraphACLCheck(root) {
		return false
	}
	for ancestor := filepath.Dir(root); ; ancestor = filepath.Dir(ancestor) {
		ancestorInfo, ancestorErr := os.Stat(ancestor)
		if ancestorErr != nil || !ancestorInfo.IsDir() || verifyCurrentUserOwned(ancestorInfo) != nil {
			return false
		}
		if ancestor == home {
			return true
		}
		if ancestor == filepath.Dir(ancestor) || !strings.HasPrefix(ancestor, home+string(filepath.Separator)) {
			return false
		}
	}
}

func trustedCodeGraphGroupWritableMetadata(uid, gid int, mode os.FileMode, effectiveUID, effectiveGID int) bool {
	return uid == effectiveUID && gid == effectiveGID && mode&0o002 == 0 && mode&0o020 != 0
}

type codeGraphDBGuard struct {
	file *os.File
	info os.FileInfo
}

func openManagedCodeGraphDB(root string) (codeGraphDBGuard, error) {
	index, err := os.Lstat(filepath.Join(root, ".codegraph"))
	if err != nil || index.Mode()&os.ModeSymlink == 0 {
		return codeGraphDBGuard{}, nil
	}
	if !trustedCodeGraphIndex(root) {
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	file, err := os.Open(filepath.Join(root, ".codegraph", "codegraph.db"))
	if err != nil {
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	info, err := file.Stat()
	if err != nil || verifyCurrentUserOwned(info) != nil {
		_ = file.Close()
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	return codeGraphDBGuard{file, info}, nil
}

func (g codeGraphDBGuard) unchanged(root string) bool {
	if g.file == nil {
		return true
	}
	defer g.file.Close()
	current, err := os.Stat(filepath.Join(root, ".codegraph", "codegraph.db"))
	return err == nil && trustedCodeGraphIndex(root) && os.SameFile(g.info, current)
}

func verifyCurrentUserOwned(info os.FileInfo) error {
	return verifyCurrentUserOnlyOwnership(info)
}

func verifyCurrentUserOwnedDir(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return errUntrustedFileOwnership
	}
	return verifyCurrentUserOwned(info)
}

func trustedCodeGraphDB(index string) bool {
	info, err := os.Lstat(filepath.Join(index, "codegraph.db"))
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() > 0 && verifyCurrentUserOwned(info) == nil
}

func (p *CodeGraphLocalIndexProvider) itemLimit() int {
	if p.runner.Config.MaxItems > 0 && p.runner.Config.MaxItems <= maxLocalEvidenceItems {
		return p.runner.Config.MaxItems
	}
	return 5
}

func (p *CodeGraphLocalIndexProvider) tokenLimit() int {
	if p.runner.Config.MaxOutputTokens > 0 && p.runner.Config.MaxOutputTokens <= maxLocalEvidenceTokens {
		return p.runner.Config.MaxOutputTokens
	}
	return 1000
}

func (p *CodeGraphLocalIndexProvider) remember(evidence []LocalExpandedEvidence) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resolved := make(map[string]LocalExpandedEvidence, len(evidence))
	for _, item := range evidence {
		resolved[item.Locator] = item
	}
	p.evidence = resolved
}

func sameCodeGraphWorkspace(left, right LocalWorkspaceSnapshot) bool {
	if left.Repository != right.Repository || left.GitRoot != right.GitRoot || left.Branch != right.Branch || left.CommitSHA != right.CommitSHA || left.Detached != right.Detached || left.ChangedFilesState != right.ChangedFilesState || len(left.ChangedFiles) != len(right.ChangedFiles) {
		return false
	}
	for index := range left.ChangedFiles {
		if left.ChangedFiles[index] != right.ChangedFiles[index] {
			return false
		}
	}
	return true
}

func sameCodeGraphWorkspaceScope(left, right LocalWorkspaceSnapshot) bool {
	return left.Repository == right.Repository && left.GitRoot == right.GitRoot && left.Branch == right.Branch && left.CommitSHA == right.CommitSHA && left.Detached == right.Detached
}

var _ LocalIndexProvider = (*CodeGraphLocalIndexProvider)(nil)

func NewWorkspaceLocalIndexProvider(config LocalIndexConfig, snapshot LocalWorkspaceSnapshot) LocalIndexProvider {
	if config.Err != nil {
		return newUnavailableLocalIndexProvider(errors.Join(errLocalIndexConfigInvalid, config.Err))
	}
	if config.Provider == LocalIndexProviderDisabled {
		return newUnavailableLocalIndexProvider(errLocalIndexDisabled)
	}
	return NewCodeGraphLocalIndexProvider(CodeGraphRunner{Config: config}, snapshot)
}
