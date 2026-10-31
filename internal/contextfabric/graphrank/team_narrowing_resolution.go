package graphrank

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TeamNarrowingDecision is how a requested team narrowing decides a set of
// subjects (CHAOS-12346; the CHAOS-12327 ownership reach rule), keyed by
// canonical id. In holds the subjects inside the requested teams, Out those
// decided outside them. A subject whose needed ownership is missing (absent,
// not current, or cut), and the organization, are in neither: a missing
// ownership never narrows.
type TeamNarrowingDecision struct {
	In, Out map[string]bool
}

// TeamNarrowingFunc decides subjects under the request's team narrowing.
// repositoriesOnly decides by repository ownership alone (a work item only
// through a linked pull request, a project never), for the census narrower.
type TeamNarrowingFunc func(ctx context.Context, subjects []contextfabric.SubjectRef, repositoriesOnly bool) (TeamNarrowingDecision, error)

// teamNarrowedByUnrestrictedPrincipal is true when the only thing restricting
// the request is the caller's own team narrowing of an otherwise unrestricted
// principal. Like repositoryNarrowedByUnrestrictedPrincipal, such a narrowing
// cannot hide anything the principal could not already see, so it does not
// make the census an existence oracle.
func teamNarrowedByUnrestrictedPrincipal(principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps) bool {
	return scopesUnrestricted(principal.RepositoryScopes) &&
		len(request.RequestedScope.TeamIDs) > 0 &&
		len(request.RequestedScope.RepositorySlugs) == 0 &&
		len(request.RequestedScope.ProjectIDs) == 0 &&
		deps.TeamNarrowing != nil
}

// isIdentityMatched reports whether a candidate matched a term by identity
// (exact label, alias or provider key): it is a subject the question names,
// so a team narrowing never drops it (Anchor B).
func isIdentityMatched(candidate contextfabric.SubjectCandidate) bool {
	for _, mechanism := range candidate.MatchMechanisms {
		switch mechanism {
		case contractsv1.ContextFabricMatchExact, contractsv1.ContextFabricMatchAlias, contractsv1.ContextFabricMatchProviderKey:
			return true
		}
	}
	return false
}

// teamNarrowedPool settles resolution toward a requested team (CHAOS-12346,
// lead ruling (i)/(ii)). It applies only when the first decision committed
// nothing and offers at least one candidate inside the requested teams. Then a
// pool candidate decided outside them leaves the pool as scope (never counted
// as an authorization drop) when every term it matched also matched an
// in-team offer. A candidate matched by identity (exact, alias, provider key),
// or whose ownership is missing, never leaves. ok is false, and the pool
// unchanged, when nothing leaves or the ownership read fails (which logs).
// A settled pool returns its scope, which narrows the offer builders' own
// inputs the same way (teamOfferScope.narrow).
func teamNarrowedPool(ctx context.Context, request contextfabric.InvestigationRequest, deps ResolveDeps, first contextfabric.SubjectResolution, pool map[string]contextfabric.SubjectCandidate) (map[string]contextfabric.SubjectCandidate, *teamOfferScope) {
	if deps.TeamNarrowing == nil || len(request.RequestedScope.TeamIDs) == 0 || len(first.Committed) > 0 || len(first.Candidates) == 0 || len(pool) < 2 {
		return pool, nil
	}
	subjects := make([]contextfabric.SubjectRef, 0, len(pool)+len(first.Candidates))
	for _, candidate := range pool {
		subjects = append(subjects, candidate.Subject)
	}
	for _, candidate := range first.Candidates {
		subjects = append(subjects, candidate.Subject)
	}
	decision, err := deps.TeamNarrowing(ctx, subjects, false)
	if err != nil {
		slog.WarnContext(ctx, "context fabric resolution: the team narrowing could not decide the candidates; offers are not narrowed",
			"request_id", contextfabric.SanitizeLogAttr(request.RequestID))
		return pool, nil
	}
	anyInTeam := false
	termInTeam := map[string]bool{}
	for _, candidate := range first.Candidates {
		if !decision.In[candidate.Subject.CanonicalID] {
			continue
		}
		anyInTeam = true
		for _, term := range candidate.MatchedTerms {
			termInTeam[term] = true
		}
	}
	if !anyInTeam {
		return pool, nil
	}
	scope := &teamOfferScope{termInTeam: termInTeam}
	kept := make(map[string]contextfabric.SubjectCandidate, len(pool))
	dropped := 0
	for key, candidate := range pool {
		if scope.leaves(decision, candidate) {
			dropped++
			continue
		}
		kept[key] = candidate
	}
	if dropped == 0 {
		return pool, nil
	}
	slog.DebugContext(ctx, "context fabric resolution: candidates outside the requested teams left the pool as scope",
		"request_id", contextfabric.SanitizeLogAttr(request.RequestID), "dropped", dropped, "kept", len(kept))
	return kept, scope
}

// teamOfferScope is the narrowing a settled pool was decided under
// (CHAOS-12346 review r1). The offer builders read finds outside the pool
// (the kind coverage floor, the low-population rescue, the kind-scoped
// semantic arm); they are narrowed by the same rule, so a candidate that left
// the pool never comes back through an offer.
type teamOfferScope struct {
	termInTeam map[string]bool
}

// leaves is the one drop rule: decided outside the requested teams, not
// matched by identity, and every term it matched has an in-team offer.
func (s *teamOfferScope) leaves(decision TeamNarrowingDecision, candidate contextfabric.SubjectCandidate) bool {
	return decision.Out[candidate.Subject.CanonicalID] && !isIdentityMatched(candidate) && everyTermInTeam(candidate.MatchedTerms, s.termInTeam)
}

// narrow applies leaves to each offer input, with one ownership read for all
// of them. A failed read leaves them unchanged, and logs.
func (s *teamOfferScope) narrow(ctx context.Context, request contextfabric.InvestigationRequest, deps ResolveDeps, inputs ...*[]contextfabric.SubjectCandidate) {
	if s == nil {
		return
	}
	var subjects []contextfabric.SubjectRef
	for _, input := range inputs {
		for _, candidate := range *input {
			subjects = append(subjects, candidate.Subject)
		}
	}
	if len(subjects) == 0 {
		return
	}
	decision, err := deps.TeamNarrowing(ctx, subjects, false)
	if err != nil {
		slog.WarnContext(ctx, "context fabric resolution: the team narrowing could not decide the offer-only finds; they are not narrowed",
			"request_id", contextfabric.SanitizeLogAttr(request.RequestID))
		return
	}
	for _, input := range inputs {
		kept := (*input)[:0:0]
		for _, candidate := range *input {
			if !s.leaves(decision, candidate) {
				kept = append(kept, candidate)
			}
		}
		*input = kept
	}
}

// everyTermInTeam: each term the candidate matched also matched an in-team
// candidate. A candidate with no recorded term is decided against the pool.
func everyTermInTeam(terms []string, termInTeam map[string]bool) bool {
	for _, term := range terms {
		if !termInTeam[term] {
			return false
		}
	}
	return true
}

// teamNarrowingSatisfierFilter is callerNarrowingSatisfierFilter for a team
// narrowing (CHAOS-12346 (ii)): it keeps the census satisfiers inside the
// requested teams' owned repositories. A satisfier whose ownership is missing
// leaves the set undecided (ok false), as a work item with no walk does for a
// repository narrowing, so nothing is narrowed on missing ownership.
func teamNarrowingSatisfierFilter(principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps) SatisfierNarrower {
	if !teamNarrowedByUnrestrictedPrincipal(principal, request, deps) {
		return nil
	}
	return func(ctx context.Context, kind CensusKind, canonicalIDs []string) ([]string, bool) {
		subjects := make([]contextfabric.SubjectRef, 0, len(canonicalIDs))
		for _, id := range canonicalIDs {
			subjects = append(subjects, contextfabric.SubjectRef{Kind: kind, CanonicalID: id})
		}
		decision, err := deps.TeamNarrowing(ctx, subjects, true)
		if err != nil {
			return nil, false
		}
		kept := make([]string, 0, len(canonicalIDs))
		for _, id := range canonicalIDs {
			switch {
			case decision.In[id]:
				kept = append(kept, id)
			case !decision.Out[id]:
				return nil, false
			}
		}
		return kept, true
	}
}
