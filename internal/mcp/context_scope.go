package mcp

import (
	"context"
	"errors"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolveScope derives the hosted RepositoryRef and RequestedScope for a
// context_for_task call. Explicit request fields always win; any
// repository or branch/commit gap is filled from local Git workspace
// discovery, using MCP roots (when the client supports them) and falling
// back to the process working directory. Changed-file discovery is a
// tri-state opt-in: it only ever runs when the caller both omits
// scope.files AND explicitly sets scope.include_changed_files to true;
// omitted or explicit false never auto-includes changed files, and an
// explicit scope.files always wins outright.
//
// Discovery is best-effort filler for an omitted repository/scope, not a
// hard requirement in the sense that resolveScope itself never errors
// when nothing is discoverable -- but repository omission specifically is
// only ever resolvable by successful local Git workspace discovery: the
// hosted ContextPacketRequest contract requires a non-empty repository
// slug, so a goal-only request that leaves resolveScope with an empty
// repo.Slug is not a valid hosted request. The caller (handleContextForTask)
// is responsible for rejecting that case as a typed validation failure
// before ever building a hosted request; resolveScope itself stays silent
// so its own no-error/empty-repo contract remains a single, predictable
// shape for every discovery gap. Two classes of discovery failure still
// surface as a hard error from this function: a genuinely malformed
// caller-supplied MCP root (control characters, too many roots), and the
// caller's own context being canceled or its deadline expiring -- the
// latter must never be silently swallowed as "nothing discoverable",
// since that would misreport a cancelled or timed-out request as a
// goal-only validation failure once handleContextForTask sees an empty
// repo.Slug. Every other discovery failure (no repo, ambiguous remote,
// detached HEAD with no commits, and so on) is silently treated as
// "nothing to fill in".
type resolvedTaskScope struct {
	Repository contractsv1.RepositoryRef
	Scope      contractsv1.RequestedScope
	Workspace  *sidecar.LocalWorkspaceSnapshot
}

func resolveTaskScope(ctx context.Context, session *mcpsdk.ServerSession, req contractsv1.MCPContextForTaskRequest) (resolvedTaskScope, error) {
	result := resolvedTaskScope{}
	repo := &result.Repository
	scope := &result.Scope
	if req.Repository != nil {
		repo.Slug = req.Repository.Slug
	}
	explicitFiles := false
	wantChangedFiles := false
	if req.Scope != nil {
		scope.Branch = req.Scope.Branch
		scope.CommitSHA = req.Scope.CommitSHA
		scope.TaskRef = req.Scope.TaskRef
		scope.Files = req.Scope.Files
		scope.AsOf = req.Scope.AsOf
		scope.TimeWindowDays = req.Scope.TimeWindowDays
		explicitFiles = len(req.Scope.Files) > 0
		wantChangedFiles = !explicitFiles && req.Scope.IncludeChangedFiles != nil && *req.Scope.IncludeChangedFiles
	}

	if repo.Slug != "" && scope.Branch != "" && scope.CommitSHA != "" && !wantChangedFiles {
		return result, nil
	}

	mcpRoots, rootsErr := resolveMCPFileRoots(ctx, session)
	if rootsErr != nil {
		if isPropagatedDiscoveryError(rootsErr) {
			return result, rootsErr
		}
		return result, nil
	}

	opts := sidecar.DiscoverOptions{
		MCPFileRoots:        mcpRoots,
		IncludeChangedFiles: wantChangedFiles,
	}
	info, err := sidecar.DiscoverWorkspace(ctx, opts)
	if err != nil {
		if isPropagatedDiscoveryError(err) {
			return result, err
		}
		return result, nil
	}

	// explicitRepoSlug is empty when the caller left repository resolution
	// to local discovery entirely, in which case every enriched field
	// below (repo slug, branch, commit, changed files) is drawn from this
	// same DiscoverWorkspace call and is therefore self-consistent by
	// construction. When the caller explicitly named a repository, this
	// discovered workspace's own identity must match it (case- and
	// whitespace-insensitively) before any of that workspace's local Git
	// state is attached to the caller's repository: otherwise an unrelated
	// local checkout -- a different repository entirely, or one with no
	// configured remote at all -- could silently supply the branch,
	// commit, or changed-file list credited to a repository it has
	// nothing to do with.
	explicitRepoSlug := repo.Slug
	if repo.Slug == "" && info.Remote != nil {
		repo.Slug = info.Remote.Slug()
	}

	if explicitRepoSlug != "" && !discoveredIdentityMatches(explicitRepoSlug, info.Remote) {
		// No local Git state from this (mismatched, or identity-less)
		// workspace is trusted for the caller's explicit repository. When
		// the caller did not explicitly ask for changed-file discovery,
		// this is silent -- repo.Slug (still the caller's explicit value)
		// and whatever scope fields were already explicit are returned
		// unchanged, exactly as if no workspace had been discoverable at
		// all. When the caller did explicitly request changed files,
		// silently returning none would be misleading (indistinguishable
		// from "no local changes"), so this returns a sanitized error
		// instead: ErrRepositoryScopeMismatch's message is a fixed string
		// carrying neither slug, so it is safe wherever a plain error is
		// safe.
		if wantChangedFiles {
			return result, ErrRepositoryScopeMismatch
		}
		return result, nil
	}

	if scope.Branch == "" {
		scope.Branch = info.Branch
	}
	if scope.CommitSHA == "" {
		scope.CommitSHA = info.CommitSHA
	}
	if wantChangedFiles {
		// A truncated changed-file list is indistinguishable from a
		// complete one to any caller: silently attaching the bounded prefix
		// to scope.Files would misrepresent an incomplete change set as
		// exhaustive. No MCP contract field represents "this list is
		// incomplete", so this fails closed with a sanitized typed error
		// instead of inventing a speculative new field.
		if info.ChangedFilesTruncated {
			return result, ErrChangedFilesTruncated
		}
		if len(info.ChangedFiles) > 0 {
			scope.Files = info.ChangedFiles
		}
	}
	workspace, snapshotErr := sidecar.NewLocalWorkspaceSnapshot(info, repo.Slug, wantChangedFiles)
	if snapshotErr == nil {
		result.Workspace = &workspace
	}
	return result, nil
}

func resolveScope(ctx context.Context, session *mcpsdk.ServerSession, req contractsv1.MCPContextForTaskRequest) (contractsv1.RepositoryRef, contractsv1.RequestedScope, error) {
	resolved, err := resolveTaskScope(ctx, session, req)
	return resolved.Repository, resolved.Scope, err
}

// isPropagatedDiscoveryError reports whether a workspace discovery
// failure must propagate to the caller rather than being silently
// treated as "nothing discoverable": either the caller's own context was
// canceled or its deadline expired -- which must surface as the distinct
// classify() cancelled/timeout categories, never a silent best-effort
// miss -- or malformed caller-supplied input (control characters, too
// many roots).
func isPropagatedDiscoveryError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, sidecar.ErrControlCharacters) || errors.Is(err, sidecar.ErrTooManyWorkspaceRoots)
}

// ErrRepositoryScopeMismatch is returned when the caller explicitly named
// a repository, the caller explicitly requested changed-file discovery,
// and the locally discovered Git workspace's identity is either absent
// (no configured remote) or does not match the requested repository.
// Its message is a fixed string carrying neither the requested nor the
// discovered slug, so it is always safe to surface, log, or classify
// verbatim.
var ErrRepositoryScopeMismatch = errors.New("mcp: the requested repository does not match the locally discovered Git workspace")

// ErrChangedFilesTruncated is returned when the caller explicitly opted
// into changed-file discovery (scope.include_changed_files=true) and the
// locally discovered Git workspace's true changed-file count exceeds
// sidecar's bounded discovery limit. Presenting the bounded, truncated
// prefix as scope.Files would misrepresent an incomplete change set as
// exhaustive and be indistinguishable from a genuinely complete one, so
// discovery fails closed instead. Its message is a fixed string carrying
// no path or count, so it is always safe to surface, log, or classify
// verbatim.
var ErrChangedFilesTruncated = errors.New("mcp: locally discovered changed files exceed the bounded discovery limit")

// discoveredIdentityMatches reports whether remote's normalized slug
// matches explicitSlug. A nil remote, or a RemoteInfo whose Slug() is
// empty because its owner or repo component never resolved, never
// matches: an explicitly requested repository must never be enriched
// from a workspace whose own identity could not be established.
func discoveredIdentityMatches(explicitSlug string, remote *sidecar.RemoteInfo) bool {
	if remote == nil {
		return false
	}
	discovered := remote.Slug()
	if discovered == "" {
		return false
	}
	return normalizeSlugForComparison(discovered) == normalizeSlugForComparison(explicitSlug)
}

// normalizeSlugForComparison lowercases and trims a repository slug so an
// explicit request-supplied value and one derived from local Git remote
// parsing can be compared for identity regardless of case or incidental
// whitespace. It performs no shape validation -- that is the hosted
// API's and sidecar.RemoteInfo.Slug()'s own concern, not this local,
// best-effort identity comparison.
func normalizeSlugForComparison(slug string) string {
	return strings.ToLower(strings.TrimSpace(slug))
}
