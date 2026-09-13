package falkorgraph

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// CHAOS-5654: a discovered cohort whose frame declares a servable member kind
// reaches that kind's population even when the question carries no term that
// matches a member. The exact-name census fetches only exactNameKinds, and it
// stays that narrow because every kind it names shares one capped query. Every
// other servable kind is fetched by the kind-scoped census below: term-free,
// one kind per query, under the same row bound and the same request-one-more
// truncation rule, admitted by the same gate as the exact-name census.
//
// The kinds come from contextfabric.CohortMemberKindFor, the seam allow-list,
// and from exactNameCensusCoversKind. This file names no kind of its own.

// CohortKindCensusDecision is the closed vocabulary RecordCohortKindCensus
// reports for one DiscoverContext call: whether the kind-scoped census ran,
// and when it did not, which condition stopped it.
type CohortKindCensusDecision string

const (
	// CohortKindCensusRan: the census gate admitted the call, the frame
	// declares a servable member kind, and the exact-name census does not
	// fetch that kind. The kind-scoped census ran for that kind.
	CohortKindCensusRan CohortKindCensusDecision = "ran"
	// CohortKindCensusNotAdmitted: the census gate refused the call (see
	// cohortExactNameCensusEligibility), or a subject is already committed.
	// A term-free fetch would widen a question that named its members.
	CohortKindCensusNotAdmitted CohortKindCensusDecision = "census_not_admitted"
	// CohortKindCensusNoServableMemberKind: the call was admitted, but the
	// frame declares no member kind the seam can serve, so no population
	// exists to fetch.
	CohortKindCensusNoServableMemberKind CohortKindCensusDecision = "no_servable_member_kind"
	// CohortKindCensusKindInExactNameCensus: the exact-name census already
	// fetches the declared kind, so a second fetch of it adds nothing.
	CohortKindCensusKindInExactNameCensus CohortKindCensusDecision = "kind_in_exact_name_census"
	// CohortKindCensusReadFailed: the kind-scoped census was attempted and the
	// store read failed, so DiscoverContext returns the error. Reported by the
	// reader, never by cohortKindCensusDecision, which decides before the read.
	CohortKindCensusReadFailed CohortKindCensusDecision = "read_failed"
)

// CohortKindCensusDecisionVocabulary returns every declared decision, in
// declaration order, so a test quantifies over what the line can carry.
func CohortKindCensusDecisionVocabulary() []CohortKindCensusDecision {
	return []CohortKindCensusDecision{
		CohortKindCensusRan,
		CohortKindCensusNotAdmitted,
		CohortKindCensusNoServableMemberKind,
		CohortKindCensusKindInExactNameCensus,
		CohortKindCensusReadFailed,
	}
}

// cohortKindCensusDecision decides whether the kind-scoped census runs. Pure.
//
// censusAdmitted is the exact-name census admission for the same call, so the
// two term-free arms can never disagree about whether a term-free fetch is
// allowed. servableKind is the servable kind contextfabric.CohortMemberKindFor
// returns: empty on every refusing reason, so an unservable declared kind can
// never reach the fetch.
func cohortKindCensusDecision(censusAdmitted bool, servableKind contextfabric.SubjectKind) CohortKindCensusDecision {
	switch {
	case !censusAdmitted:
		return CohortKindCensusNotAdmitted
	case servableKind == "":
		return CohortKindCensusNoServableMemberKind
	case exactNameCensusCoversKind(servableKind):
		return CohortKindCensusKindInExactNameCensus
	default:
		return CohortKindCensusRan
	}
}

// cohortKindCensusCandidates fetches every Subject node of the bound kinds in
// orgID's temporally-valid scope, term-free.
//
// CHAOS-5654: bounded by exactNameCandidateQueryLimit, the census pool bound.
// The query asks for one row more than the bound and reports truncated=true
// when that row comes back, the same rule chaos4348ExactNameCandidates uses.
// ORDER BY the canonical id makes a truncated fetch keep the same prefix on
// every call, so the cohort a truncated population yields is reproducible.
func (a *Adapter) cohortKindCensusCandidates(ctx context.Context, key, orgID string, kinds []string, temporal temporalFilter) ([]graphrank.CandidateNode, bool, error) {
	cypher := fmt.Sprintf(
		"MATCH (n:%s) WHERE n.%s = $org AND n.%s IN $kinds%s RETURN n ORDER BY n.%s LIMIT %d",
		labelSubject, propOrgID, propKind, temporal.predicate("n"), propCanonicalID, exactNameCandidateQueryLimit+1,
	)
	rows, err := a.api.query(ctx, key, cypher, temporal.bind(map[string]interface{}{"org": orgID, "kinds": kinds}), true)
	if err != nil {
		return nil, false, safeDependencyError("read kind census candidates", err)
	}
	truncated := len(rows) > exactNameCandidateQueryLimit
	if truncated {
		rows = rows[:exactNameCandidateQueryLimit]
	}
	candidates := make([]graphrank.CandidateNode, 0, len(rows))
	for _, r := range rows {
		n, ok := r["n"].(*node)
		if !ok || n == nil {
			continue
		}
		candidates = append(candidates, toCandidateNode(n))
	}
	return candidates, truncated, nil
}
