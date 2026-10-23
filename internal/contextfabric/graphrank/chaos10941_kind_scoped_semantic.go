package graphrank

import (
	"context"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// kindScopedSemanticOutcome is applyKindScopedSemanticOffers' whole report:
// the offer-only finds (strongest first) and the signals the arm must not
// swallow. truncated and the counters are DISCLOSURE only: the arm runs after
// the commit decision, so none of them may feed a gate.
type kindScopedSemanticOutcome struct {
	ran               bool
	candidates        []contextfabric.SubjectCandidate
	degraded          bool
	truncated         bool
	authzDropped      int
	traversalDegraded int
}

// applyKindScopedSemanticOffers (CHAOS-10941) runs deps.KindScopedSemantic for
// every hinted repository/project/team kind and returns the finds as OFFER-ONLY
// candidates, ordered by confidence (monotonic in cosine similarity) then
// canonical id so the offer cap keeps the strongest. They merge into a private
// pool: never candidatesBySubject, so they cannot rank, corroborate or commit,
// and (nil identity maps) never take part in identity-collision tracking --
// the same isolation applyKindCoverageFloor gives these kinds.
// allowExactMatch=false: a semantic proximity is never an exact/alias claim.
func applyKindScopedSemanticOffers(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, terms []string, hinted []contextfabric.SubjectKind, admission *contestAdmission) (kindScopedSemanticOutcome, error) {
	var outcome kindScopedSemanticOutcome
	if deps.KindScopedSemantic == nil || len(hinted) == 0 {
		return outcome, nil
	}
	outcome.ran = true
	boundedTerms := terms
	if len(boundedTerms) > kindCoverageMaxTermsPerKind {
		boundedTerms = boundedTerms[:kindCoverageMaxTermsPerKind]
	}
	private := make(map[string]contextfabric.SubjectCandidate)
	observationParentKey := make(map[string]string)
	observationBlocked := make(map[string]bool)
	for _, kind := range hinted {
		for _, term := range boundedTerms {
			results, truncated, degraded, err := deps.KindScopedSemantic(ctx, term, kind, kindCoverageQueryLimit)
			if err != nil {
				return kindScopedSemanticOutcome{}, err
			}
			outcome.truncated = outcome.truncated || truncated
			outcome.degraded = outcome.degraded || degraded
			for i := range results {
				results[i].Mechanism = contextfabric.MatchVector
			}
			traversalDegraded, authzDropped := mergeSearchResults(ctx, principal, request, deps, term, results, private, observationParentKey, observationBlocked, false, nil, nil, nil, admission)
			outcome.traversalDegraded += traversalDegraded
			outcome.authzDropped += authzDropped
		}
	}
	for _, candidate := range private {
		outcome.candidates = append(outcome.candidates, candidate)
	}
	sort.Slice(outcome.candidates, func(i, j int) bool {
		a, b := outcome.candidates[i], outcome.candidates[j]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.Subject.Kind != b.Subject.Kind {
			return a.Subject.Kind < b.Subject.Kind
		}
		return a.Subject.CanonicalID < b.Subject.CanonicalID
	})
	return outcome, nil
}
