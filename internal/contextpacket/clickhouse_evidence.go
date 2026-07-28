package contextpacket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (s *ClickHouseEvidenceStore) ResolveEvidence(ctx context.Context, principal storage.Principal, evidenceRefID string) (contractsv1.ExpandedEvidence, error) {
	if err := ctx.Err(); err != nil {
		return contractsv1.ExpandedEvidence{}, err
	}
	if s.rows == nil || s.codec == nil || strings.TrimSpace(principal.OrgID) == "" || !validOpaqueEvidenceRefID(evidenceRefID) {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	rows, ok := s.rows.(EvidenceReferenceRows)
	if !ok {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	handle, err := s.codec.Parse(evidenceRefID)
	if err != nil {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	candidates, err := rows.AuthorizedRepositories(ctx, principal.OrgID, principal.RepositoryScopes)
	if err != nil {
		if ctx.Err() != nil {
			return contractsv1.ExpandedEvidence{}, ctx.Err()
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return contractsv1.ExpandedEvidence{}, fmt.Errorf("resolve authorized evidence repositories: %w", err)
		}
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	if len(candidates) == 0 || len(candidates) > maxEvidenceCandidateRepos {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	routed, ok := s.routeEvidenceHandle(principal, handle, candidates)
	if !ok {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	references, err := rows.ResolveEvidenceReference(ctx, principal.OrgID, routed, handle.QueryID, handle.LocatorHash())
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return contractsv1.ExpandedEvidence{}, fmt.Errorf("resolve evidence reference: %w", err)
	}
	matched, ok := s.matchEvidenceHandle(principal, handle, routed, references)
	if err != nil || !ok {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	if handle.RepositoryWide {
		matched.Evidence.Source.DisplayLabel += repositoryWideSourceLabelSuffix
		matched.Evidence.Metadata = withRepositoryWideScope(matched.Evidence.Metadata)
	}
	matched.Evidence.EvidenceRefID = evidenceRefID
	return s.resolver.Expand(ctx, EvidenceExpansionInput{Evidence: matched.Evidence, Excerpt: matched.Excerpt})
}

func (s *ClickHouseEvidenceStore) routeEvidenceHandle(principal storage.Principal, handle EvidenceHandle, candidates []contractsv1.ResolvedScope) (contractsv1.ResolvedScope, bool) {
	var routed contractsv1.ResolvedScope
	routes := 0
	for _, scope := range candidates {
		if auth.AuthorizeRepository(principal, scope.RepoSlug) == nil && s.codec.RoutesTo(handle, principal.OrgID, scope.RepoID) {
			routes++
			routed = scope
		}
	}
	return routed, routes == 1
}

func (s *ClickHouseEvidenceStore) matchEvidenceHandle(principal storage.Principal, handle EvidenceHandle, scope contractsv1.ResolvedScope, references []EvidenceReference) (EvidenceReference, bool) {
	var matched EvidenceReference
	matches := 0
	for _, reference := range references {
		if reference.RepoSlug == scope.RepoSlug && reference.Evidence.SourceVersion == handle.QueryID && s.codec.Matches(handle, principal.OrgID, scope.RepoID, reference.Evidence.EvidenceRefID) {
			matches++
			matched = reference
		}
	}
	return matched, matches == 1
}
