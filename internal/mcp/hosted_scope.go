package mcp

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ErrHostedRepositoryRequired reports a hosted context_for_task call that
// named no repository. A hosted process serves callers it cannot see the
// workspace of, so the repository is never discovered: the caller passes it.
var ErrHostedRepositoryRequired = errors.New("mcp: a hosted context_for_task call requires repository.slug")

// ErrHostedChangedFilesUnsupported reports a hosted context_for_task call
// that asked for workspace-derived changed files. Only the caller can see
// its own working tree, so the caller lists the paths itself.
var ErrHostedChangedFilesUnsupported = errors.New("mcp: scope.include_changed_files is not available on a hosted server")

const (
	hostedRepositoryRequiredMessage = "this hosted server cannot detect your workspace, so context_for_task requires repository.slug (owner/name, for example acme/billing); optionally add scope.branch, scope.commit_sha or scope.files; see the context_for_task input schema"
	hostedChangedFilesMessage       = "this hosted server cannot read your working tree, so scope.include_changed_files=true is not available; list the paths in scope.files instead; see the context_for_task input schema"
)

// Closed vocabulary of the scope_source field of the hosted scope event.
const (
	hostedScopeExplicit     = "explicit_repository"
	hostedScopeMissing      = "repository_missing"
	hostedScopeChangedFiles = "changed_files_unsupported"
)

// hostedMode reports whether this process serves callers whose workspace it
// cannot see. It is plain process configuration, never derived from a
// request or a session.
func (p *ProcessConfig) hostedMode() bool {
	return p != nil && p.HostedMode
}

// resolveHostedTaskScope derives the repository and scope of a hosted
// context_for_task call from the request ALONE. It takes no session and no
// context: it cannot list MCP roots, read the working directory, run Git or
// consult a local index, because none of those describe the caller's
// workspace on a hosted server. A missing repository and a request for
// workspace-derived changed files are typed refusals naming the input the
// caller must pass, never a substitute discovered from the host.
func resolveHostedTaskScope(req contractsv1.MCPContextForTaskRequest) (resolvedTaskScope, string, error) {
	result := resolvedTaskScope{}
	if req.Repository != nil {
		result.Repository.Slug = req.Repository.Slug
	}
	if req.Scope != nil {
		result.Scope.Branch = req.Scope.Branch
		result.Scope.CommitSHA = req.Scope.CommitSHA
		result.Scope.TaskRef = req.Scope.TaskRef
		result.Scope.Files = req.Scope.Files
		result.Scope.AsOf = req.Scope.AsOf
		result.Scope.TimeWindowDays = req.Scope.TimeWindowDays
		if req.Scope.IncludeChangedFiles != nil && *req.Scope.IncludeChangedFiles {
			return resolvedTaskScope{}, hostedScopeChangedFiles, ErrHostedChangedFilesUnsupported
		}
	}
	if result.Repository.Slug == "" {
		return resolvedTaskScope{}, hostedScopeMissing, ErrHostedRepositoryRequired
	}
	return result, hostedScopeExplicit, nil
}

// logHostedScope records the scope decision at the point it is taken.
func logHostedScope(ctx context.Context, cfg *ProcessConfig, source string, req contractsv1.MCPContextForTaskRequest) {
	if cfg == nil || cfg.diagnostics == nil {
		return
	}
	var branch, commit bool
	files := 0
	if req.Scope != nil {
		branch = req.Scope.Branch != ""
		commit = req.Scope.CommitSHA != ""
		files = len(req.Scope.Files)
	}
	fields := eventspec.NewMCPHostedContextScopeFields("context_for_task", source, branch, commit, files)
	cfg.diagnostics.InfoContext(ctx, eventspec.MCPHostedContextScope.Msg, fields.SlogArgs()...)
}
