package devhealthfacts

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-go/readers"
)

// The work units behind an investment allocation. A unit's row in one
// repository carries share_in_scope = c / n * effort_value (c: distinct PR refs
// of the unit in that repository, n: distinct refs of the unit overall, a ref
// that resolves to no repository counting in n only) -- the exact weight
// repoMixStatement sums, so
//
//	sum(share_in_scope * unit_theme_X) / sum(share_in_scope) == theme_X
//
// of the fact the same read serves for the subject, over every page of the
// listing. A unit that reaches several repositories has one row per
// repository. Nothing here recomputes a category: every probability is the
// unit's persisted theme_distribution_json value.

const (
	unitCursorShareParam = "unit_cursor_share"
	unitCursorIDParam    = "unit_cursor_id"
	unitCursorRepoParam  = "unit_cursor_repo"
	unitLimitParam       = "unit_limit"

	// unitRefsPerFact bounds the pull request refs one unit row cites; the
	// full count rides unit_pull_request_count and the omission is disclosed.
	unitRefsPerFact = 25
	// unitUnresolvedRefsPerFact bounds the unresolved issue keys one unit row
	// names; the full count rides unit_refs_unresolved.
	unitUnresolvedRefsPerFact = 20
	unitUnresolvedRefMaxBytes = 120
)

const unitFactReasonCut = contextfabric.InvestmentUnitsCutReason

type unitRow struct {
	WorkUnitID     string
	RepoID         string
	Share          float64
	Effort         float64
	Theme          map[string]float64
	From, To       time.Time
	PRs            []string
	UnresolvedN    uint64
	UnresolvedRefs []string
	ScopeTotal     float64
	ScopeUnits     uint64
}

// investmentUnitsStatement lists one window's unit rows of the repositories in
// {ids}, ordered by share descending, work unit, repository, one page.
func investmentUnitsStatement(b factTimeBound, withCursor bool) string {
	return investmentUnitsStatementScoped(b, withCursor, subqueryMembershipScope)
}

// investmentUnitsStatementScoped is investmentUnitsStatement with the resolved
// membership scope.
func investmentUnitsStatementScoped(b factTimeBound, withCursor bool, scope membershipScope) string {
	memberships := []string{fmt.Sprintf("if(%s, %d, -1)", mixWindowPredicate(0, b), 0)}
	core := repoSplitCoreScoped(memberships, " from_ts, to_ts,",
		",\n\t\t\t\tany(parsed.from_ts) AS from_ts, any(parsed.to_ts) AS to_ts,\n\t\t\t\tgroupUniqArray(parsed.pr_number) AS prs,\n\t\t\t\tgroupUniqArray(parsed.ref_text) AS ref_texts", false, scope)
	keyset := ""
	if withCursor {
		keyset = `
WHERE share < toFloat64({` + unitCursorShareParam + `:String})
	OR (share = toFloat64({` + unitCursorShareParam + `:String}) AND (work_unit_id > {` + unitCursorIDParam + `:String}
		OR (work_unit_id = {` + unitCursorIDParam + `:String} AND repo_uuid > {` + unitCursorRepoParam + `:String})))`
	}
	return `SELECT work_unit_id, repo_uuid, share, effort_value, theme_distribution_json, from_ts, to_ts, prs, unresolved_n, unresolved_refs, scope_total, scope_units
FROM (
	SELECT work_unit_id, repo_uuid, c / n * effort_value AS share, effort_value, theme_distribution_json, from_ts, to_ts, prs, unresolved_n, unresolved_refs,
		sum(c / n * effort_value) OVER () AS scope_total, count() OVER () AS scope_units
	FROM (
		SELECT win, repo_uuid, work_unit_id, c,
			sum(c) OVER (PARTITION BY win, work_unit_id) AS n,
			sum(if(repo_uuid = '', c, 0)) OVER (PARTITION BY win, work_unit_id) AS unresolved_n,
			arrayFlatten(groupArray(if(repo_uuid = '', ref_texts, [])) OVER (PARTITION BY win, work_unit_id)) AS unresolved_refs,
			effort_value, theme_distribution_json, from_ts, to_ts, prs
		FROM (
` + core + `		)
	)
	WHERE repo_uuid != '' AND repo_uuid IN {ids:Array(String)}
)` + keyset + `
ORDER BY share DESC, work_unit_id ASC, repo_uuid ASC
LIMIT {` + unitLimitParam + `:UInt32}`
}

// readInvestmentUnits reads one page of the units behind the allocation of
// subject (a team: the repositories it owns; a repository: itself).
func (p *InvestmentProvider) readInvestmentUnits(ctx context.Context, orgID string, subject contextfabric.SubjectRef, rawID string, request contextfabric.InvestmentUnitsRequest, timeBound factTimeBound) (facts []contextfabric.CanonicalFact, more bool, err error) {
	var repoIDs []string
	switch subject.Kind {
	case contextfabric.SubjectRepository:
		repoIDs = []string{rawID}
	case contextfabric.SubjectTeam:
		owned, ownedErr := p.readTeamOwnedRepositories(ctx, orgID, []string{rawID}, timeBound)
		if ownedErr != nil {
			return nil, false, ownedErr
		}
		seen := map[string]bool{}
		for _, repoID := range owned[rawID] {
			if !seen[repoID] {
				seen[repoID] = true
				repoIDs = append(repoIDs, repoID)
			}
		}
		sort.Strings(repoIDs)
	default:
		return nil, false, nil
	}
	pageSize := request.Max
	if pageSize <= 0 {
		pageSize = contextfabric.InvestmentUnitsDefaultMax
	}
	if pageSize > contextfabric.InvestmentUnitsMaxMax {
		pageSize = contextfabric.InvestmentUnitsMaxMax
	}
	var rows []unitRow
	if len(repoIDs) > 0 {
		var extra []readers.Binding
		for _, tb := range timeBound.bindings() {
			extra = append(extra, readers.Binding{Name: tb.Name, Value: tb.Value})
		}
		extra = append(extra, readers.Binding{Name: unitLimitParam, Value: strconv.Itoa(pageSize + 1)})
		if request.Cursor != nil {
			extra = append(extra,
				readers.Binding{Name: unitCursorShareParam, Value: strconv.FormatFloat(request.Cursor.Share, 'g', -1, 64)},
				readers.Binding{Name: unitCursorIDParam, Value: request.Cursor.WorkUnitID},
				readers.Binding{Name: unitCursorRepoParam, Value: request.Cursor.RepoID},
			)
		}
		scope, scopeErr := p.resolveMembershipScope(ctx, orgID)
		if scopeErr != nil {
			return nil, false, scopeErr
		}
		extra = append(extra, scope.bindings()...)
		scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadInvestmentUnits", investmentUnitsStatementScoped(timeBound, request.Cursor != nil, scope), orgID, repoIDs, func(row contextpacket.ClickHouseRowScanner) error {
			var r unitRow
			if err := row.Scan(&r.WorkUnitID, &r.RepoID, &r.Share, &r.Effort, &r.Theme, &r.From, &r.To, &r.PRs, &r.UnresolvedN, &r.UnresolvedRefs, &r.ScopeTotal, &r.ScopeUnits); err != nil {
				return err
			}
			r.PRs = withoutEmpty(r.PRs)
			rows = append(rows, r)
			return nil
		}, extra...)
		if scanErr != nil {
			return nil, false, scanErr
		}
	}
	if len(rows) > pageSize {
		more = true
		rows = rows[:pageSize]
	}

	page := map[string]contextfabric.FactValue{
		"unit_kind":      contextfabric.StringFactValue(contextfabric.InvestmentUnitPageKind),
		"unit_weight":    contextfabric.StringFactValue(contextfabric.InvestmentUnitsWeight),
		"units_returned": contextfabric.IntegerFactValue(int64(len(rows))),
	}
	pageShare := 0.0
	unresolvedByUnit := map[string]uint64{}
	for _, r := range rows {
		pageShare += r.Share
		unresolvedByUnit[r.WorkUnitID] = r.UnresolvedN
	}
	var unresolved uint64
	for _, n := range unresolvedByUnit {
		unresolved += n
	}
	page["page_share_total"] = contextfabric.NumberFactValue(pageShare)
	page["units_refs_unresolved"] = contextfabric.IntegerFactValue(int64(unresolved))
	if len(rows) > 0 {
		page["scope_share_total"] = contextfabric.NumberFactValue(rows[0].ScopeTotal)
		page["scope_unit_rows"] = contextfabric.IntegerFactValue(int64(rows[0].ScopeUnits))
	}
	if more && len(rows) > 0 {
		last := rows[len(rows)-1]
		page["next_cursor"] = contextfabric.StringFactValue(contextfabric.EncodeInvestmentUnitsCursor(contextfabric.InvestmentUnitsCursor{Share: last.Share, WorkUnitID: last.WorkUnitID, RepoID: last.RepoID}))
	}
	ownRef := evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, rawID)
	if subject.Kind == contextfabric.SubjectRepository {
		ownRef = evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, rawID)
	}
	facts = append(facts, contextfabric.CanonicalFact{
		Kind: contextfabric.FactInvestment, Subject: subject, Fields: page,
		EvidenceRefIDs: []string{ownRef},
	})
	for _, r := range rows {
		facts = append(facts, unitFact(subject, r))
	}
	return facts, more, nil
}

func unitFact(subject contextfabric.SubjectRef, r unitRow) contextfabric.CanonicalFact {
	fields := map[string]contextfabric.FactValue{
		"unit_kind":               contextfabric.StringFactValue(contextfabric.InvestmentUnitKind),
		"work_unit_id":            contextfabric.StringFactValue(r.WorkUnitID),
		"repository_id":           contextfabric.StringFactValue(r.RepoID),
		"unit_from":               contextfabric.StringFactValue(r.From.UTC().Format(time.RFC3339)),
		"unit_to":                 contextfabric.StringFactValue(r.To.UTC().Format(time.RFC3339)),
		"share_in_scope":          contextfabric.NumberFactValue(r.Share),
		"unit_effort_value":       contextfabric.NumberFactValue(r.Effort),
		"unit_pull_request_count": contextfabric.IntegerFactValue(int64(len(r.PRs))),
		"unit_refs_unresolved":    contextfabric.IntegerFactValue(int64(r.UnresolvedN)),
		"unit_mix_source":         contextfabric.StringFactValue(repoMixSource),
		"unit_attribution_basis":  contextfabric.StringFactValue(repoMixBasis),
	}
	for _, theme := range canonicalInvestmentThemes {
		fields["unit_"+contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(r.Theme[theme])
	}
	if len(r.UnresolvedRefs) > 0 {
		handles := append([]string(nil), r.UnresolvedRefs...)
		sort.Strings(handles)
		if len(handles) > unitUnresolvedRefsPerFact {
			handles = handles[:unitUnresolvedRefsPerFact]
		}
		for i, h := range handles {
			if len(h) > unitUnresolvedRefMaxBytes {
				handles[i] = h[:unitUnresolvedRefMaxBytes]
			}
		}
		fields["unit_unresolved_refs"] = contextfabric.StringFactValue(strings.Join(handles, ","))
	}
	prs := append([]string(nil), r.PRs...)
	sort.Strings(prs)
	refs := []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, r.RepoID)}
	for i, number := range prs {
		if i >= unitRefsPerFact {
			break
		}
		if _, err := strconv.ParseUint(number, 10, 64); err != nil {
			continue
		}
		refs = append(refs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, r.RepoID+":"+number))
	}
	return contextfabric.CanonicalFact{Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields, EvidenceRefIDs: refs}
}

// withoutEmpty drops the empty pull request number the repository fallback
// (a unit with no PR reference, split by its own repo_id) carries: it is a
// reference to a repository, not to a pull request.
func withoutEmpty(values []string) []string {
	out := values[:0:0]
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
