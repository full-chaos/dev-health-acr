package falkorgraph

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// exactNameKinds is the fixed, closed set CHAOS-4348's exact-name arm
// covers -- byte-identical to graphrank's own isAliasLookupScopedKind set
// (repository/project/team). Not derived from graphrank at call time
// (graphrank.IsAliasLookupScopedKind has no enumerable-list counterpart,
// only the predicate) -- kept as its own small, explicit literal here, the
// same "listed explicitly so a change shows up in the diff" discipline
// devhealthfacts' allProvidersForKindAudit already uses (chaos4099_capability_
// kinds_test.go).
var exactNameKinds = []string{"repository", "project", "team"}

// exactNameCandidateQueryLimit bounds chaos4348ExactNameCandidates' single
// per-resolution fetch. Not a calibrated recall/cost tradeoff (this ticket
// has no live measurement slot to calibrate one either, same posture
// chaos4038_kind_coverage.go's own kindCoverageMaxTermsPerKind states) --
// generous relative to any real deployment's repository+project+team count
// (kiac: 11+20+3=34) while still bounding a pathological organization.
const exactNameCandidateQueryLimit = 2000

// chaos4348ExactNameCandidates is graphrank.ResolveDeps.ExactNameCandidates'
// production implementation: every repository/project/team node in orgID's
// authorized, temporally-valid scope, ONE unranked equality-eligible fetch
// per resolution -- see that field's own doc comment (resolve.go) for why
// this is deliberately NOT another ranked db.idx.fulltext.queryNodes call.
// graphrank.applyExactNameArm does the actual term-equality filtering in Go,
// reusing NodeCandidate's own label/alias/provider-alias check; this
// function's only job is retrieval.
func (a *Adapter) chaos4348ExactNameCandidates(ctx context.Context, key, orgID string, temporal temporalFilter) ([]graphrank.CandidateNode, error) {
	cypher := fmt.Sprintf(
		"MATCH (n:%s) WHERE n.%s = $org AND n.%s IN $kinds%s RETURN n LIMIT %d",
		labelSubject, propOrgID, propKind, temporal.predicate("n"), exactNameCandidateQueryLimit,
	)
	rows, err := a.api.query(ctx, key, cypher, temporal.bind(map[string]interface{}{"org": orgID, "kinds": exactNameKinds}), true)
	if err != nil {
		return nil, safeDependencyError("read exact-name candidates", err)
	}
	candidates := make([]graphrank.CandidateNode, 0, len(rows))
	for _, r := range rows {
		n, ok := r["n"].(*node)
		if !ok || n == nil {
			continue
		}
		candidates = append(candidates, toCandidateNode(n))
	}
	return candidates, nil
}
