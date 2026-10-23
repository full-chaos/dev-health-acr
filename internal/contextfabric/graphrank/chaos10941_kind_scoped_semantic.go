package graphrank

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// applyKindScopedSemanticOffers (CHAOS-10941) runs deps.KindScopedSemantic for
// every hinted repository/project/team kind and returns the finds as OFFER-ONLY
// candidates. They merge into a private pool: never candidatesBySubject, so
// they cannot rank, corroborate or commit, and (nil identity maps) never take
// part in identity-collision tracking -- the same isolation applyKindCoverageFloor
// gives these kinds. allowExactMatch=false: a semantic proximity is never an
// exact/alias claim.
func applyKindScopedSemanticOffers(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, terms []string, hinted []contextfabric.SubjectKind, admission *contestAdmission) ([]contextfabric.SubjectCandidate, bool, error) {
	if deps.KindScopedSemantic == nil || len(hinted) == 0 {
		return nil, false, nil
	}
	boundedTerms := terms
	if len(boundedTerms) > kindCoverageMaxTermsPerKind {
		boundedTerms = boundedTerms[:kindCoverageMaxTermsPerKind]
	}
	private := make(map[string]contextfabric.SubjectCandidate)
	observationParentKey := make(map[string]string)
	observationBlocked := make(map[string]bool)
	degraded := false
	var found []contextfabric.SubjectCandidate
	for _, kind := range hinted {
		for _, term := range boundedTerms {
			results, _, kindDegraded, err := deps.KindScopedSemantic(ctx, term, kind, kindCoverageQueryLimit)
			if err != nil {
				return nil, degraded, err
			}
			if kindDegraded {
				degraded = true
			}
			for i := range results {
				results[i].Mechanism = contextfabric.MatchVector
			}
			mergeSearchResults(ctx, principal, request, deps, term, results, private, observationParentKey, observationBlocked, false, nil, nil, nil, admission)
		}
		found = append(found, candidatesOfKind(private, kind)...)
	}
	return found, degraded, nil
}
