package contextpacket

import (
	"context"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const RepositoryScopeQueryV1 = `SELECT toString(id), repo, ifNull(ref, '') FROM repos FINAL WHERE org_id = {org_id:String} AND repo = {repo_slug:String} LIMIT 2`

type ClickHouseScopeResolver struct{ client ClickHouseQueryClient }

func NewClickHouseScopeResolver(client ClickHouseQueryClient) *ClickHouseScopeResolver {
	return &ClickHouseScopeResolver{client: client}
}

func (r *ClickHouseScopeResolver) ResolveEvidenceScope(ctx context.Context, plan ReadPlan) (_ contractsv1.ResolvedScope, err error) {
	if r == nil || r.client == nil {
		return contractsv1.ResolvedScope{}, fmt.Errorf("contextpacket: clickhouse query client is required")
	}
	rows, err := r.client.Query(ctx, RepositoryScopeQueryV1, scopeBindings(plan))
	if err != nil {
		return contractsv1.ResolvedScope{}, fmt.Errorf("query repository scope: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close repository scope rows: %w", closeErr)
		}
	}()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return contractsv1.ResolvedScope{}, fmt.Errorf("iterate repository scope: %w", err)
		}
		return unresolvedReadScope(plan), nil
	}
	var repoID, repoSlug, repositoryBranch string
	if err := rows.Scan(&repoID, &repoSlug, &repositoryBranch); err != nil {
		return contractsv1.ResolvedScope{}, fmt.Errorf("scan repository scope: %w", err)
	}
	if rows.Next() {
		return contractsv1.ResolvedScope{}, fmt.Errorf("contextpacket: repository scope is ambiguous")
	}
	if err := rows.Err(); err != nil {
		return contractsv1.ResolvedScope{}, fmt.Errorf("iterate repository scope: %w", err)
	}
	if repoSlug != plan.RepoSlug {
		return contractsv1.ResolvedScope{}, ErrEvidenceScopeMismatch
	}
	return resolvedReadScope(plan, repoID, repositoryBranch), nil
}

func resolvedReadScope(plan ReadPlan, repoID, repositoryBranch string) contractsv1.ResolvedScope {
	scope := contractsv1.ResolvedScope{RepoID: repoID, RepoSlug: plan.RepoSlug, Branch: plan.Branch, CommitSHA: plan.CommitSHA, FallbackReasons: []string{}}
	switch {
	case plan.CommitSHA != "":
		scope.Resolution = contractsv1.ScopeExactCommit
	case plan.Branch != "":
		scope.Resolution = contractsv1.ScopeBranchFiltered
	case repositoryBranch != "":
		scope.Branch = repositoryBranch
		scope.Resolution = contractsv1.ScopeRepoFallback
		scope.FallbackReasons = []string{"repository_default_branch_fallback"}
	default:
		scope.Resolution = contractsv1.ScopeRepoFallback
		scope.FallbackReasons = []string{"repo_wide_evidence"}
	}
	return scope
}

func unresolvedReadScope(plan ReadPlan) contractsv1.ResolvedScope {
	return contractsv1.ResolvedScope{RepoSlug: plan.RepoSlug, Branch: plan.Branch, CommitSHA: plan.CommitSHA, Resolution: contractsv1.ScopeUnresolved, FallbackReasons: []string{"authorized_repository_not_found"}}
}
