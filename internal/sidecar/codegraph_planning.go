package sidecar

import (
	"context"
	"slices"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

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
	if workspace.ChangedFilesState == LocalChangedFilesNotRequested {
		return nil, ErrCodeGraphArgumentsRejected
	}
	if len(workspace.ChangedFiles) > 0 && workspace.ChangedFilesState == LocalChangedFilesComplete && allowsCodeGraphAffected(request.RequestedCategories) {
		payload, err := p.runner.Affected(ctx, codeGraphAffectedRequest{GitRoot: workspace.GitRoot, Files: workspace.ChangedFiles})
		if err != nil {
			return nil, err
		}
		affected, err := decodeCodeGraphAffected(payload)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, affectedCandidates(affected)...)
		anchors = anchors[:min(1, len(anchors))]
	}
	if !allowsCodeGraphRelationships(request.RequestedCategories) {
		anchors = nil
	}
	for _, anchor := range anchors {
		relations, err := p.anchorCandidates(ctx, workspace.GitRoot, anchor.Name)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, relations...)
	}
	if request.TaskRef != "" && len(anchors) < 2 {
		payload, err := p.runner.Files(ctx, codeGraphFilesRequest{GitRoot: workspace.GitRoot, Filter: directoryForCodeGraphFiles(workspace.ChangedFiles)})
		if err != nil {
			return nil, err
		}
		files, err := decodeCodeGraphFiles(payload)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, fileCandidates(files)...)
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
	callees, err := p.runner.Callees(ctx, request)
	if err != nil {
		return nil, err
	}
	impact, err := p.runner.Impact(ctx, request)
	if err != nil {
		return nil, err
	}
	callerRelations, err := decodeCodeGraphRelations(callers, "callers")
	if err != nil {
		return nil, err
	}
	calleeRelations, err := decodeCodeGraphRelations(callees, "callees")
	if err != nil {
		return nil, err
	}
	impactRelations, err := decodeCodeGraphImpact(impact)
	if err != nil {
		return nil, err
	}
	return append(append(relationCandidates(codeGraphCommandCallers, "caller", callerRelations), relationCandidates(codeGraphCommandCallees, "callee", calleeRelations)...), relationCandidates(codeGraphCommandImpact, "impact", impactRelations)...), nil
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
	return len(categories) == 0 || slices.Contains(categories, contractsv1.CategoryAction) || slices.Contains(categories, contractsv1.CategoryEvidence)
}

func allowsCodeGraphRelationships(categories []contractsv1.PacketCategory) bool {
	return len(categories) == 0 || slices.Contains(categories, contractsv1.CategoryCause) || slices.Contains(categories, contractsv1.CategoryEvidence)
}
