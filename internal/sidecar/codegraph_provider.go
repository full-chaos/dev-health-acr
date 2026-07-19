package sidecar

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const codeGraphProviderID = "codegraph"

// CodeGraphLocalIndexProvider consumes a trusted existing CodeGraph index
// through ADR-0005's fixed JSON subprocess contract only.
type CodeGraphLocalIndexProvider struct {
	runner    CodeGraphRunner
	workspace LocalWorkspace
	mu        sync.RWMutex
	evidence  map[string]LocalExpandedEvidence
}

func NewCodeGraphLocalIndexProvider(runner CodeGraphRunner, workspace LocalWorkspace) *CodeGraphLocalIndexProvider {
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
	if err := ValidateLocalContextRequest(request); err != nil || !validRequestedCategories(request.RequestedCategories) {
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
	bundle := LocalEvidenceBundle{ProviderID: codeGraphProviderID, ProviderVersion: status.Version, QueryID: "query", QueryVersion: codeGraphJSONQueryVersion, IndexedAt: &status.LastIndexedAt, Evidence: evidence}
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

func (p *CodeGraphLocalIndexProvider) status(ctx context.Context, workspace LocalWorkspace) (codeGraphStatus, error) {
	payload, err := p.runner.Status(ctx, workspace.Root)
	if err != nil {
		return codeGraphStatus{}, err
	}
	status, err := decodeCodeGraphStatus(payload)
	if err != nil || !sameLocalPath(status.ProjectPath, workspace.Root) || !sameLocalPath(filepath.Dir(filepath.Dir(status.IndexPath)), workspace.Root) {
		return codeGraphStatus{}, errCodeGraphDecode
	}
	return status, nil
}

func (p *CodeGraphLocalIndexProvider) collectCandidates(ctx context.Context, workspace LocalWorkspace, request LocalContextRequest) ([]codeGraphCandidate, error) {
	query, err := p.runner.Query(ctx, codeGraphQueryRequest{GitRoot: workspace.Root, Search: codeGraphSearch(request), Limit: p.itemLimit()})
	if err != nil {
		return nil, err
	}
	nodes, err := decodeCodeGraphQuery(query)
	if err != nil {
		return nil, err
	}
	candidates := nodeCandidates(nodes)
	anchors := nodes[:min(2, len(nodes))]
	if len(workspace.TargetFiles) > 0 {
		affected, affectedErr := p.runner.Affected(ctx, codeGraphAffectedRequest{GitRoot: workspace.Root, Files: workspace.TargetFiles})
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
	for _, anchor := range anchors {
		relations, relationErr := p.anchorCandidates(ctx, workspace.Root, anchor.Name)
		if relationErr != nil {
			return nil, relationErr
		}
		candidates = append(candidates, relations...)
	}
	if request.TaskRef != "" && len(anchors) < 2 {
		files, filesErr := p.runner.Files(ctx, codeGraphFilesRequest{GitRoot: workspace.Root, Filter: directoryForCodeGraphFiles(workspace.TargetFiles)})
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

func sameCodeGraphWorkspace(left, right LocalWorkspace) bool {
	if left.RepositorySlug != right.RepositorySlug || left.Root != right.Root || left.Branch != right.Branch || left.CommitSHA != right.CommitSHA || left.Detached != right.Detached || left.TargetFilesTruncated != right.TargetFilesTruncated || len(left.TargetFiles) != len(right.TargetFiles) {
		return false
	}
	for index := range left.TargetFiles {
		if left.TargetFiles[index] != right.TargetFiles[index] {
			return false
		}
	}
	return true
}

func sameLocalPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func codeGraphSearch(request LocalContextRequest) string {
	parts := []string{request.Task}
	if request.TaskRef != "" {
		parts = append(parts, request.TaskRef)
	}
	parts = append(parts, request.RequestedCategories...)
	return strings.Join(parts, " ")
}

var _ LocalIndexProvider = (*CodeGraphLocalIndexProvider)(nil)
