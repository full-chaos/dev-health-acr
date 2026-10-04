package devhealthfacts

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-go/readers"
)

// CHAOS-6560 / CHAOS-6559 (chris ruling 2026-09-24): the REPOSITORY is the
// primitive of investment attribution and a team's mix follows from what
// the team OWNS.
//
//   - A work unit's persisted effort_value * theme_share is split across the
//     repositories of its PR refs by PR-ref share (a partition: the parts sum
//     to the unit's own weighted effort, never double counted). A ref that
//     resolves to no repository is null-carrying: its share attaches to no
//     repository and is never redistributed. A unit with no PR ref at all
//     falls back to its own persisted repo_id when it has one, else it
//     reaches no repository.
//   - A team's mix is the SUM of its owned repositories' mixes
//     (team_repo_ownership, currently valid) -- never a member vote and never
//     a work-item majority vote. A repository owned by several teams counts
//     in each team's own mix (ownership is a coverage filter, never a
//     weight), and a repository is counted once per team however many
//     ownership sources name it.
//
// Nothing here recomputes a category or calls a model: every number is a sum
// of persisted distributions (AGENTS.md: UX renders only persisted).

// repoMixSource / repoMixBasis are the provenance every row carries.
const (
	repoMixSource = "work_unit_investments.theme_distribution_json"
	repoMixBasis  = "pr_ref_share_partition"
)

// repoMixRow is one repository's persisted-effort split in one requested
// window (Window indexes the bounds given to readRepoMixRows): per-theme weighted
// effort, the bugfix-subcategory weighted effort and the count of distinct
// work units attributed to it.
type repoMixRow struct {
	Window    int
	RepoID    string
	Theme     map[string]float64
	Bugfix    float64
	WorkUnits int64
}

// mixWindowParams names the bind parameters of window i: window 0 uses the
// standard bound names, later windows suffix theirs.
func mixWindowParams(i int) (startParam, endParam string) {
	if i == 0 {
		return boundStartParam, boundEndParam
	}
	return fmt.Sprintf("%s_w%d", boundStartParam, i), fmt.Sprintf("%s_w%d", boundEndParam, i)
}

// mixWindowPredicate is window i's row predicate over the latest-row columns
// from_ts/to_ts (the same half-open rule as themeInvestmentRangePredicate: a
// unit is in the window when it starts before the end and its inclusive last
// evidence instant is at or after the start). An inactive bound is always true.
func mixWindowPredicate(i int, b factTimeBound) string {
	if !b.active {
		return "1"
	}
	startParam, endParam := mixWindowParams(i)
	predicate := "from_ts < {" + endParam + ":DateTime64(6,'UTC')}"
	if b.hasStart {
		predicate += " AND to_ts >= {" + startParam + ":DateTime64(6,'UTC')}"
	}
	return predicate
}

// repoMixStatement builds the repo-level split for one or more windows. It
// reads only persisted columns of work_unit_investments (argMax by
// computed_at per work unit), repos (name -> uuid for issue-side PR refs) and
// nothing else.
//
// CHAOS-6594: work_unit_investments is read ONCE per request, however many
// windows are asked for. ClickHouse inlines a CTE at every reference, so the
// earlier chain (latest -> windowed -> pr_refs x3 -> split -> attributed)
// re-scanned the table ~7x and tripped the read-only user's max_bytes_to_read
// (error 307). Here the latest-row selection is one nested subquery; each
// unit's window memberships and PR/issue refs are derived from its own row
// with array functions (no second reference to the latest set); the per-unit
// ref total is a window function over the already-grouped rows; and the theme
// and bugfix sums come out of a single aggregation. repos is referenced once.
//
// CHAOS-7073 (K16): the latest-row selection matches ops'
// LatestWorkUnitInvestmentsSource on three points. A superseded work unit
// (work_unit_supersessions) is excluded; only the work units of the latest
// complete membership run are read, or every one when no run is recorded
// (investment_membership_scope.go); and the latest repo_id is taken with
// argMax over a tuple, so a latest row whose repo_id is NULL stays NULL
// instead of argMax skipping it and reviving an older row's repository.
// None of these reads work_unit_investments again.
func repoMixStatement(bounds []factTimeBound) string {
	memberships := make([]string, 0, len(bounds))
	for i, b := range bounds {
		memberships = append(memberships, fmt.Sprintf("if(%s, %d, -1)", mixWindowPredicate(i, b), i))
	}
	return withRowLimit(`SELECT toUInt8(win) AS window, repo_uuid AS repo_id,
	sumMap(mapApply((k, v) -> (k, v * effort), theme_distribution_json)) AS theme_effort,
	sum(bugfix_share * effort) AS bugfix_effort,
	uniqExact(work_unit_id) AS work_units
FROM (
	SELECT win, repo_uuid, work_unit_id, c / n * effort_value AS effort, theme_distribution_json, bugfix_share
	FROM (
		SELECT win, repo_uuid, work_unit_id, c,
			sum(c) OVER (PARTITION BY win, work_unit_id) AS n,
			effort_value, theme_distribution_json, bugfix_share
		FROM (
			SELECT parsed.win AS win, parsed.work_unit_id AS work_unit_id,
				if(parsed.uuid_direct != '', parsed.uuid_direct, ifNull(rl.repo_uuid, '')) AS repo_uuid,
				uniqExact(if(repo_uuid = '', parsed.ref_text, concat(repo_uuid, '#', parsed.pr_number))) AS c,
				any(parsed.effort_value) AS effort_value,
				any(parsed.theme_distribution_json) AS theme_distribution_json,
				any(parsed.bugfix_share) AS bugfix_share
			FROM (
				SELECT work_unit_id, effort_value, theme_distribution_json, bugfix_share, win,
					ref.1 AS uuid_direct, ref.2 AS lookup_provider, ref.3 AS lookup_repo, ref.4 AS pr_number, ref.5 AS ref_text
				FROM (
					SELECT work_unit_id, repo_id, effort_value, theme_distribution_json,
						ifNull(subcategory_distribution_json['` + readers.BugfixSubcategoryKey + `'], 0.0) AS bugfix_share,
						arrayFilter(w -> w >= 0, [` + strings.Join(memberships, ", ") + `]) AS wins,
						arrayConcat(
							arrayMap(r -> (splitByString('#pr', r)[1], '', '', splitByString('#pr', r)[2], r),
								arrayFilter(r -> match(r, '^[0-9a-fA-F-]{36}#pr[0-9]+$'), JSONExtract(structural_evidence_json, 'prs', 'Array(String)'))),
							arrayMap(r -> ('', 'github', substring(splitByChar('#', r)[1], 6), splitByChar('#', r)[2], r),
								arrayFilter(r -> match(r, '^ghpr:[^#]+#[0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)'))),
							arrayMap(r -> ('', 'gitlab', substring(splitByChar('!', r)[1], 8), splitByChar('!', r)[2], r),
								arrayFilter(r -> match(r, '^gitlab:[^!]+![0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)')))
						) AS pr_refs,
						if(empty(pr_refs) AND repo_id IS NOT NULL, [(toString(repo_id), '', '', '', '')], pr_refs) AS refs
					FROM (
						SELECT work_unit_id,
							(argMax(tuple(repo_id), computed_at)).1 AS repo_id,
							argMax(from_ts, computed_at) AS from_ts,
							argMax(to_ts, computed_at) AS to_ts,
							argMax(effort_value, computed_at) AS effort_value,
							argMax(theme_distribution_json, computed_at) AS theme_distribution_json,
							argMax(subcategory_distribution_json, computed_at) AS subcategory_distribution_json,
							argMax(structural_evidence_json, computed_at) AS structural_evidence_json
						FROM work_unit_investments
						WHERE org_id = {org_id:String}` + supersededWorkUnitIDsFilter() + investmentMembershipScopeFilter() + `
						GROUP BY work_unit_id
					)
				)
				ARRAY JOIN wins AS win
				ARRAY JOIN refs AS ref
			) AS parsed
			LEFT JOIN (
				SELECT toString(id) AS repo_uuid, argMax(repo, last_synced) AS repo,
					if(uniqExact(provider) = 1, argMax(provider, last_synced), '') AS provider
				FROM repos
				WHERE org_id = {org_id:String}
				GROUP BY id
			) AS rl ON rl.provider = parsed.lookup_provider AND rl.repo = parsed.lookup_repo
			GROUP BY win, work_unit_id, repo_uuid
		)
	)
	WHERE repo_uuid != '' AND repo_uuid IN {ids:Array(String)}
)
GROUP BY win, repo_uuid
ORDER BY win, repo_uuid`)
}

// repoMixChunk bounds how many repositories one statement reads. Each
// repository yields one row per window (at most 2), so 90 repositories stay
// strictly under maxFactRowsPerQuery (200): the row limit is never reached and
// a partial mix can never be served as complete. More repositories are read by
// more statements, in a stable order, never by a silently truncated one.
const repoMixChunk = 90

// readRepoMixRows reads the mix of repoIDs for every window in bounds with ONE
// pass over work_unit_investments per chunk of repositories; result[i] holds
// window i's rows.
func (p *InvestmentProvider) readRepoMixRows(ctx context.Context, orgID string, repoIDs []string, bounds ...factTimeBound) ([][]repoMixRow, error) {
	out := make([][]repoMixRow, len(bounds))
	if len(repoIDs) == 0 {
		return out, nil
	}
	sorted := append([]string(nil), repoIDs...)
	sort.Strings(sorted)
	var extra []readers.Binding
	for i, b := range bounds {
		startParam, endParam := mixWindowParams(i)
		for _, tb := range b.bindings() {
			name := endParam
			if tb.Name == boundStartParam {
				name = startParam
			}
			extra = append(extra, readers.Binding{Name: name, Value: tb.Value})
		}
	}
	statement := repoMixStatement(bounds)
	for start := 0; start < len(sorted); start += repoMixChunk {
		end := start + repoMixChunk
		if end > len(sorted) {
			end = len(sorted)
		}
		got := 0
		err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadRepositoryThemeMix", statement, orgID, sorted[start:end], func(row contextpacket.ClickHouseRowScanner) error {
			var r repoMixRow
			var window uint8
			var workUnits uint64
			if err := row.Scan(&window, &r.RepoID, &r.Theme, &r.Bugfix, &workUnits); err != nil {
				return err
			}
			if int(window) < 0 || int(window) >= len(out) {
				return fmt.Errorf("repository theme mix returned window %d for %d requested", window, len(out))
			}
			r.Window = int(window)
			r.WorkUnits = int64(workUnits)
			out[r.Window] = append(out[r.Window], r)
			got++
			return nil
		}, extra...)
		if err != nil {
			return nil, err
		}
		if got >= maxFactRowsPerQuery {
			// Unreachable by construction (repoMixChunk * windows < the
			// limit); if a future change breaks that, fail loudly, never
			// serve a truncated mix as complete.
			return nil, fmt.Errorf("repository theme mix chunk reached the row limit (%d rows for %d repositories)", got, end-start)
		}
	}
	return out, nil
}

// repoThemeTotals folds one repository's rows into per-theme effort, the
// bugfix subcategory effort and the work-unit count.
type repoThemeTotals struct {
	theme     map[string]float64
	bugfix    float64
	workUnits int64
	// repos counts the repositories folded into a team total (0 for a
	// single-repository total, where it is not meaningful).
	repos int64
}

// mixEffortSignificantDigits is the declared precision of every effort value
// the investment mix serves (weighted_effort, the shares derived from it and
// the prior-window shares). ClickHouse sums Float64 in a thread-dependent
// order, so the last digits of one persisted aggregate differ between two
// reads of the same rows; served as-is they make the same question carry two
// different inputs. Nine significant digits is far below what the effort
// carries (the persisted distributions are model estimates with two or three
// meaningful digits) and far above the last-digit noise (about 1e-16
// relative), so a rounding edge is crossed with probability near 1e-7 per
// value, never by a real change of the data.
const mixEffortSignificantDigits = 9

// roundMixEffort rounds v to mixEffortSignificantDigits significant digits.
func roundMixEffort(v float64) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(v, 'g', mixEffortSignificantDigits-1, 64), 64)
	if err != nil {
		return v
	}
	return rounded
}

func groupRepoMix(rows []repoMixRow) map[string]*repoThemeTotals {
	out := map[string]*repoThemeTotals{}
	defer func() {
		for _, t := range out {
			t.roundEffort()
		}
	}()
	for _, r := range rows {
		t, ok := out[r.RepoID]
		if !ok {
			t = &repoThemeTotals{theme: map[string]float64{}}
			out[r.RepoID] = t
		}
		if r.WorkUnits > t.workUnits {
			t.workUnits = r.WorkUnits
		}
		for theme, effort := range r.Theme {
			t.theme[theme] += effort
		}
		t.bugfix += r.Bugfix
	}
	return out
}

// roundEffort applies the declared precision to every effort sum of t.
func (t *repoThemeTotals) roundEffort() {
	for theme, v := range t.theme {
		t.theme[theme] = roundMixEffort(v)
	}
	t.bugfix = roundMixEffort(t.bugfix)
}

func (t *repoThemeTotals) total() float64 {
	sum := 0.0
	for _, theme := range canonicalInvestmentThemes {
		sum += t.theme[theme]
	}
	return sum
}

// themeBreakdownTable renders totals as the declared breakdown table: one
// row per canonical theme, share = weighted effort / total (never zero-fill
// of a missing repository -- the caller only calls this when total > 0).
func themeBreakdownTable(t *repoThemeTotals, grain contextfabric.TemporalGrain) contextfabric.FactValue {
	total := t.total()
	rows := make([]contextfabric.FactValueRow, 0, len(canonicalInvestmentThemes))
	for _, theme := range canonicalInvestmentThemes {
		fields := map[string]contextfabric.FactValue{
			"theme":           contextfabric.StringFactValue(theme),
			"share":           contextfabric.NumberFactValue(t.theme[theme] / total),
			"weighted_effort": contextfabric.NumberFactValue(t.theme[theme]),
			"source":          contextfabric.StringFactValue(repoMixSource),
			"attribution":     contextfabric.StringFactValue(repoMixBasis),
		}
		rows = append(rows, contextfabric.FactValueRow{Fields: fields})
	}
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          []string{"theme"},
		Measures:     []string{"share", "weighted_effort"},
		Observations: []string{"source", "attribution"},
		Grain:        grain,
		Rows:         rows,
	})
}

// readRepositoryThemeMix emits the repository-scope investment fact.
func (p *InvestmentProvider) readRepositoryThemeMix(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rejected int, err error) {
	ids, bySubject, rejected := subjectIndex(subjects, repositoryPrefix)
	if len(ids) == 0 {
		return rejected, nil
	}
	windows, err := p.readRepoMixRows(ctx, orgID, ids, timeBound)
	if err != nil {
		return rejected, err
	}
	grouped := groupRepoMix(windows[0])
	repoIDs := make([]string, 0, len(grouped))
	for id := range grouped {
		repoIDs = append(repoIDs, id)
	}
	sort.Strings(repoIDs)
	for _, repoID := range repoIDs {
		t := grouped[repoID]
		total := t.total()
		subject, ok := bySubject[repoID]
		if !ok || total <= 0 {
			continue
		}
		fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+6)
		for _, theme := range canonicalInvestmentThemes {
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(t.theme[theme] / total)
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(t.bugfix / total)
		fields["work_unit_count"] = contextfabric.IntegerFactValue(t.workUnits)
		fields["mix_source"] = contextfabric.StringFactValue(repoMixSource)
		fields["attribution_basis"] = contextfabric.StringFactValue(repoMixBasis)
		fields["theme_breakdown"] = themeBreakdownTable(t, timeBound.effectiveGrain(grainDaily))
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, repoID)},
		})
	}
	return rejected, nil
}

// teamOwnedRepoMix returns, per requested team id, the SUM of the mixes of
// the repositories the team owns as of timeBound, and (when prior is set) the
// same as of the prior window. A repository is counted once per team however
// many ownership sources name it; a team owning no repository with persisted
// work, or whose owned repositories carry zero effort, is absent from the
// result (never a fabricated zero mix). Ownership is read per window, but the
// mix of both windows comes from ONE pass over work_unit_investments.
func (p *InvestmentProvider) teamOwnedRepoMix(ctx context.Context, orgID string, teamIDs []string, timeBound factTimeBound, prior *factTimeBound) (current, priorMix map[string]*repoThemeTotals, err error) {
	bounds := []factTimeBound{timeBound}
	if prior != nil {
		bounds = append(bounds, *prior)
	}
	ownedByWindow := make([]map[string][]string, len(bounds))
	repoSet := map[string]bool{}
	for i, b := range bounds {
		owned, ownedErr := p.readTeamOwnedRepositories(ctx, orgID, teamIDs, b)
		if ownedErr != nil {
			return nil, nil, ownedErr
		}
		ownedByWindow[i] = owned
		for _, repos := range owned {
			for _, repoID := range repos {
				repoSet[repoID] = true
			}
		}
	}
	repoIDs := make([]string, 0, len(repoSet))
	for id := range repoSet {
		repoIDs = append(repoIDs, id)
	}
	sort.Strings(repoIDs)
	windows, err := p.readRepoMixRows(ctx, orgID, repoIDs, bounds...)
	if err != nil {
		return nil, nil, err
	}
	current = sumOwnedRepoMix(ownedByWindow[0], groupRepoMix(windows[0]))
	if prior != nil {
		priorMix = sumOwnedRepoMix(ownedByWindow[1], groupRepoMix(windows[1]))
	}
	return current, priorMix, nil
}

// readTeamOwnedRepositories reads team -> owned repository ids as of bound.
// An ownership row with no repo_id is resolved by repository name
// (ownedRepositoriesSource, CHAOS-7073 K11).
func (p *InvestmentProvider) readTeamOwnedRepositories(ctx context.Context, orgID string, teamIDs []string, bound factTimeBound) (map[string][]string, error) {
	statement := withRowLimit(`SELECT DISTINCT team_id, repo_key FROM ` +
		ownedRepositoriesSource(` AND team_id IN {ids:Array(String)}`+ownershipValidityPredicate(bound)) + "\nORDER BY team_id, repo_key")
	extra := make([]readers.Binding, 0, 2)
	for _, b := range bound.bindings() {
		extra = append(extra, readers.Binding{Name: b.Name, Value: b.Value})
	}
	owned := map[string][]string{}
	if err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamOwnedRepositories", statement, orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
		var teamID, repoID string
		if err := row.Scan(&teamID, &repoID); err != nil {
			return err
		}
		owned[teamID] = append(owned[teamID], repoID)
		return nil
	}, extra...); err != nil {
		return nil, err
	}
	return owned, nil
}

// sumOwnedRepoMix is the pure ownership aggregate: team total = sum of its
// owned repositories' totals. Kept separate so the invariant "sum of a
// team's owned-repo mixes == the team mix" is testable without a server.
func sumOwnedRepoMix(owned map[string][]string, perRepo map[string]*repoThemeTotals) map[string]*repoThemeTotals {
	out := map[string]*repoThemeTotals{}
	for teamID, repos := range owned {
		seen := map[string]bool{}
		team := &repoThemeTotals{theme: map[string]float64{}}
		orderedRepos := append([]string(nil), repos...)
		sort.Strings(orderedRepos)
		for _, repoID := range orderedRepos {
			t, ok := perRepo[repoID]
			if !ok || seen[repoID] {
				continue
			}
			seen[repoID] = true
			for _, theme := range canonicalInvestmentThemes {
				team.theme[theme] += t.theme[theme]
			}
			team.bugfix += t.bugfix
			team.repos++
		}
		if team.repos == 0 || team.total() <= 0 {
			continue
		}
		out[teamID] = team
	}
	return out
}
