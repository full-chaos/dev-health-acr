package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
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
		return LocalIndexCapabilities{}, err
	}
	workspace, err := normalizeCodeGraphWorkspace(&p.workspace)
	if err != nil {
		return LocalIndexCapabilities{}, nil
	}
	status, err := p.status(ctx, workspace)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return LocalIndexCapabilities{}, err
		}
		return LocalIndexCapabilities{}, nil
	}
	return LocalIndexCapabilities{ProviderID: codeGraphProviderID, ProviderVersion: status.Version, Available: true, MaxItems: p.itemLimit(), MaxOutputTokens: p.tokenLimit()}, nil
}

func (p *CodeGraphLocalIndexProvider) ContextForTask(ctx context.Context, request LocalContextRequest) (LocalEvidenceBundle, error) {
	if err := ctx.Err(); err != nil {
		return LocalEvidenceBundle{}, err
	}
	if err := ValidateLocalContextRequest(request); err != nil {
		return LocalEvidenceBundle{}, ErrInvalidLocalContextRequest
	}
	workspace, err := normalizeCodeGraphWorkspace(request.Workspace)
	configuredWorkspace, configuredErr := normalizeCodeGraphWorkspace(&p.workspace)
	if err != nil || configuredErr != nil || !sameCodeGraphWorkspace(workspace, configuredWorkspace) {
		return LocalEvidenceBundle{}, ErrInvalidLocalContextRequest
	}
	status, err := p.status(ctx, workspace)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return LocalEvidenceBundle{}, err
		}
		return LocalEvidenceBundle{}, ErrLocalIndexUnavailable
	}
	candidates, err := p.collectCandidates(ctx, workspace, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return LocalEvidenceBundle{}, err
		}
		return LocalEvidenceBundle{}, ErrLocalIndexUnavailable
	}
	evidence, candidateTruncated, err := buildCodeGraphEvidence(candidates, min(request.MaxItems, p.itemLimit()), min(request.MaxOutputTokens, p.tokenLimit()))
	if err != nil {
		return LocalEvidenceBundle{}, ErrLocalIndexUnavailable
	}
	bundle := LocalEvidenceBundle{ProviderID: codeGraphProviderID, ProviderVersion: status.Version, QueryID: "query", QueryVersion: codeGraphJSONQueryVersion, IndexedAt: &status.LastIndexedAt, Warnings: []string{"indexed_commit_unknown"}, Truncated: candidateTruncated, Evidence: evidence}
	bundle, payloadTruncated, err := trimCodeGraphEvidence(bundle)
	if err != nil {
		return LocalEvidenceBundle{}, ErrLocalIndexUnavailable
	}
	bundle.Truncated = bundle.Truncated || payloadTruncated
	normalized, err := NormalizeLocalEvidenceBundleForRequest(request, LocalIndexCapabilities{ProviderID: codeGraphProviderID, ProviderVersion: status.Version, Available: true, MaxItems: p.itemLimit(), MaxOutputTokens: p.tokenLimit()}, bundle)
	if err != nil {
		return LocalEvidenceBundle{}, ErrLocalIndexUnavailable
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
	if err != nil || status.ProjectPath != workspace.GitRoot || status.IndexPath != filepath.Join(workspace.GitRoot, ".codegraph") || !trustedCodeGraphIndex(workspace.GitRoot) {
		return codeGraphStatus{}, errCodeGraphDecode
	}
	return status, nil
}

func trustedCodeGraphIndex(root string) bool {
	index := filepath.Join(root, ".codegraph")
	info, err := os.Lstat(index)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedIndex, err := filepath.EvalSymlinks(index)
	return err == nil && resolvedIndex == filepath.Join(resolvedRoot, ".codegraph")
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

var _ LocalIndexProvider = (*CodeGraphLocalIndexProvider)(nil)

func NewWorkspaceLocalIndexProvider(config LocalIndexConfig, snapshot LocalWorkspaceSnapshot) LocalIndexProvider {
	if config.Err != nil || config.Provider == LocalIndexProviderDisabled {
		return NewDisabledLocalIndexProvider()
	}
	return NewCodeGraphLocalIndexProvider(CodeGraphRunner{Config: config}, snapshot)
}
