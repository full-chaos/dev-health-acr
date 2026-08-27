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
//
// TRUNCATION HONESTY (codex review, HIGH; resolved as an accurate-disclosure
// fix, not a NEW commit-safety gate -- see the correction below). A silent
// cap here would let a match past the cutoff go permanently unreachable,
// with nothing in the run's own artifacts saying so ("a measurement that
// did not happen must FAIL, loudly" -- AGENTS.md). Mirrors runFulltextQuery's
// own "request one more row than the caller's budget" discipline
// (falkorgraph/queries.go): the fetch below asks for
// exactNameCandidateQueryLimit+1 and reports truncated=true whenever the
// extra row comes back, trimming to the limit either way. See
// ResolveDeps.ExactNameCandidates' own doc comment (resolve.go) for exactly
// what the caller does with that signal, and what it does NOT protect
// against -- resolution.go's own exactIndex commit gate ALREADY documents,
// pre-existing and unrelated to this ticket, that an exact-label match is
// deliberately allowed to outrank ANY truncation signal ("a duplicate label
// hidden entirely behind the truncation boundary... unresolvable by label
// under any rule" -- resolution.go ~line 590). Codex's original framing
// ("this arm's truncation could let a wrong exact match auto-commit") does
// not hold: that residual risk exists identically for ordinary Search's own
// exact-match path today, and always has. What this signal DOES do: reach
// every OTHER gate that reads searchTruncated (LoneFloor, TopFloor, the
// tied-statistical-top check) honestly, and make a truncated fetch visible
// in the run's own trace/report artifacts instead of silently claiming
// completeness it did not have.
const exactNameCandidateQueryLimit = 2000

// chaos4348ExactNameCandidates is graphrank.ResolveDeps.ExactNameCandidates'
// production implementation: every repository/project/team node in orgID's
// authorized, temporally-valid scope, ONE unranked equality-eligible fetch
// per resolution -- see that field's own doc comment (resolve.go) for why
// this is deliberately NOT another ranked db.idx.fulltext.queryNodes call.
// graphrank.applyExactNameArm does the actual term-equality filtering in Go,
// reusing NodeCandidate's own label/alias/provider-alias check; this
// function's only job is retrieval (and reporting whether that retrieval
// was complete -- see exactNameCandidateQueryLimit's own doc comment).
func (a *Adapter) chaos4348ExactNameCandidates(ctx context.Context, key, orgID string, temporal temporalFilter) ([]graphrank.CandidateNode, bool, error) {
	cypher := fmt.Sprintf(
		"MATCH (n:%s) WHERE n.%s = $org AND n.%s IN $kinds%s RETURN n LIMIT %d",
		labelSubject, propOrgID, propKind, temporal.predicate("n"), exactNameCandidateQueryLimit+1,
	)
	rows, err := a.api.query(ctx, key, cypher, temporal.bind(map[string]interface{}{"org": orgID, "kinds": exactNameKinds}), true)
	if err != nil {
		return nil, false, safeDependencyError("read exact-name candidates", err)
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
