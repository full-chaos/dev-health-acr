package sidecar

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
		return LocalEvidenceBundle{}, ErrLocalIndexUnavailable
	}
	candidates, err := p.collectCandidates(ctx, workspace, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return LocalEvidenceBundle{}, err
		}
		return LocalEvidenceBundle{}, ErrLocalIndexUnavailable
	}
	evidence, err := buildCodeGraphEvidence(candidates, min(request.MaxItems, p.itemLimit()), min(request.MaxOutputTokens, p.tokenLimit()))
	if err != nil {
		return LocalEvidenceBundle{}, ErrLocalIndexUnavailable
	}
	bundle := LocalEvidenceBundle{ProviderID: codeGraphProviderID, ProviderVersion: status.Version, QueryID: "query", QueryVersion: codeGraphJSONQueryVersion, IndexedAt: &status.LastIndexedAt, Warnings: []string{"indexed_commit_unknown"}, Evidence: evidence}
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
	if err != nil || !sameLocalPath(status.ProjectPath, workspace.GitRoot) || !sameLocalPath(status.IndexPath, filepath.Join(workspace.GitRoot, ".codegraph")) {
		return codeGraphStatus{}, errCodeGraphDecode
	}
	return status, nil
}

func (p *CodeGraphLocalIndexProvider) collectCandidates(ctx context.Context, workspace LocalWorkspaceSnapshot, request LocalContextRequest) ([]codeGraphCandidate, error) {
	query, err := p.runner.Query(ctx, codeGraphQueryRequest{GitRoot: workspace.GitRoot, Search: codeGraphSearch(request), Limit: p.itemLimit()})
	if err != nil {
		return nil, err
	}
	nodes, err := decodeCodeGraphQuery(query)
	if err != nil {
		return nil, err
	}
	candidates := nodeCandidates(nodes)
	anchors := nodes[:min(2, len(nodes))]
	if len(workspace.ChangedFiles) > 0 && allowsCodeGraphAffected(request.RequestedCategories) {
		affected, affectedErr := p.runner.Affected(ctx, codeGraphAffectedRequest{GitRoot: workspace.GitRoot, Files: workspace.ChangedFiles})
		if affectedErr != nil {
			return nil, affectedErr
		}
		result, decodeErr := decodeCodeGraphAffected(affected)
		if decodeErr != nil {
			return nil, decodeErr
		}
		candidates = append(candidates, affectedCandidates(result)...)
		if len(anchors) > 1 {
			anchors = anchors[:1]
		}
	}
	if !allowsCodeGraphRelationships(request.RequestedCategories) {
		anchors = nil
	}
	for _, anchor := range anchors {
		relations, relationErr := p.anchorCandidates(ctx, workspace.GitRoot, anchor.Name)
		if relationErr != nil {
			return nil, relationErr
		}
		candidates = append(candidates, relations...)
	}
	if request.TaskRef != "" && len(anchors) < 2 {
		files, filesErr := p.runner.Files(ctx, codeGraphFilesRequest{GitRoot: workspace.GitRoot, Filter: directoryForCodeGraphFiles(workspace.ChangedFiles)})
		if filesErr != nil {
			return nil, filesErr
		}
		decoded, decodeErr := decodeCodeGraphFiles(files)
		if decodeErr != nil {
			return nil, decodeErr
		}
		candidates = append(candidates, fileCandidates(decoded)...)
	}
	sort.Slice(candidates, func(left, right int) bool { return codeGraphCandidateLess(candidates[left], candidates[right]) })
	if duplicateCodeGraphCandidates(candidates) {
		return nil, errCodeGraphDecode
	}
	return candidates, nil
}

func (p *CodeGraphLocalIndexProvider) anchorCandidates(ctx context.Context, root, symbol string) ([]codeGraphCandidate, error) {
	request := codeGraphQueryRequest{GitRoot: root, Search: symbol, Limit: p.itemLimit()}
	callers, err := p.runner.Callers(ctx, request)
	if err != nil {
		return nil, err
	}
	calleePayload, err := p.runner.Callees(ctx, request)
	if err != nil {
		return nil, err
	}
	impactPayload, err := p.runner.Impact(ctx, request)
	if err != nil {
		return nil, err
	}
	callerRelations, err := decodeCodeGraphRelations(callers, "callers")
	if err != nil {
		return nil, err
	}
	calleeRelations, err := decodeCodeGraphRelations(calleePayload, "callees")
	if err != nil {
		return nil, err
	}
	impactRelations, err := decodeCodeGraphImpact(impactPayload)
	if err != nil {
		return nil, err
	}
	return append(append(relationCandidates("caller", callerRelations), relationCandidates("callee", calleeRelations)...), relationCandidates("impact", impactRelations)...), nil
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

func sameLocalPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func codeGraphSearch(request LocalContextRequest) string {
	parts := []string{request.Goal}
	if request.TaskRef != "" {
		parts = append(parts, request.TaskRef)
	}
	for _, category := range request.RequestedCategories {
		parts = append(parts, string(category))
	}
	return strings.Join(parts, " ")
}

func allowsCodeGraphAffected(categories []contractsv1.PacketCategory) bool {
	return len(categories) == 0 || hasCodeGraphCategory(categories, contractsv1.CategoryAction) || hasCodeGraphCategory(categories, contractsv1.CategoryEvidence)
}

func allowsCodeGraphRelationships(categories []contractsv1.PacketCategory) bool {
	return len(categories) == 0 || hasCodeGraphCategory(categories, contractsv1.CategoryCause) || hasCodeGraphCategory(categories, contractsv1.CategoryEvidence)
}

func hasCodeGraphCategory(categories []contractsv1.PacketCategory, wanted contractsv1.PacketCategory) bool {
	for _, category := range categories {
		if category == wanted {
			return true
		}
	}
	return false
}

var _ LocalIndexProvider = (*CodeGraphLocalIndexProvider)(nil)

func NewWorkspaceLocalIndexProvider(config LocalIndexConfig, snapshot LocalWorkspaceSnapshot) LocalIndexProvider {
	if config.Err != nil || config.Provider == LocalIndexProviderDisabled {
		return NewDisabledLocalIndexProvider()
	}
	return NewCodeGraphLocalIndexProvider(CodeGraphRunner{Config: config}, snapshot)
}
