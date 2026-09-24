package devhealthfacts

import (
	"context"
	"fmt"
	"sort"

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

// repoMixRow is one (repository, theme-or-tracked-subcategory) weighted
// effort. WorkUnits is the count of distinct work units contributing to the
// repository (identical on every row of one repository and kind).
type repoMixRow struct {
	RepoID         string
	Kind           string // "theme" | "subcategory"
	Key            string
	WeightedEffort float64
	WorkUnits      int64
}

// repoMixStatement builds the repo-level split. It reads only persisted
// columns of work_unit_investments (argMax by computed_at per work unit),
// repos (name -> uuid for issue-side PR refs) and nothing else.
func repoMixStatement(timeBound factTimeBound) string {
	rangePredicate := themeInvestmentRangePredicate(timeBound, "from_ts", "to_ts")
	return withRowLimit(`SELECT repo_id, kind, key, weighted_effort, work_units FROM (
WITH latest AS (
	SELECT work_unit_id,
		argMax(repo_id, computed_at) AS repo_id,
		argMax(from_ts, computed_at) AS from_ts,
		argMax(to_ts, computed_at) AS to_ts,
		argMax(effort_value, computed_at) AS effort_value,
		argMax(theme_distribution_json, computed_at) AS theme_distribution_json,
		argMax(subcategory_distribution_json, computed_at) AS subcategory_distribution_json,
		argMax(structural_evidence_json, computed_at) AS structural_evidence_json
	FROM work_unit_investments
	WHERE org_id = {org_id:String}
	GROUP BY work_unit_id
),
windowed AS (
	SELECT * FROM latest WHERE 1` + rangePredicate + `
),
repo_lookup AS (
	SELECT toString(id) AS repo_uuid, argMax(repo, last_synced) AS repo,
		if(uniqExact(provider) = 1, argMax(provider, last_synced), '') AS provider
	FROM repos
	WHERE org_id = {org_id:String}
	GROUP BY id
),
pr_refs AS (
	SELECT work_unit_id, ref,
		splitByString('#pr', ref)[1] AS uuid_direct, '' AS lookup_provider, '' AS lookup_repo,
		splitByString('#pr', ref)[2] AS pr_number
	FROM windowed
	ARRAY JOIN JSONExtract(structural_evidence_json, 'prs', 'Array(String)') AS ref
	WHERE match(ref, '^[0-9a-fA-F-]{36}#pr[0-9]+$')
	UNION ALL
	SELECT work_unit_id, ref, '' AS uuid_direct, 'github' AS lookup_provider,
		substring(splitByChar('#', ref)[1], 6) AS lookup_repo, splitByChar('#', ref)[2] AS pr_number
	FROM windowed
	ARRAY JOIN JSONExtract(structural_evidence_json, 'issues', 'Array(String)') AS ref
	WHERE match(ref, '^ghpr:[^#]+#[0-9]+$')
	UNION ALL
	SELECT work_unit_id, ref, '' AS uuid_direct, 'gitlab' AS lookup_provider,
		substring(splitByChar('!', ref)[1], 8) AS lookup_repo, splitByChar('!', ref)[2] AS pr_number
	FROM windowed
	ARRAY JOIN JSONExtract(structural_evidence_json, 'issues', 'Array(String)') AS ref
	WHERE match(ref, '^gitlab:[^!]+![0-9]+$')
),
pr_parsed AS (
	SELECT pr_refs.work_unit_id AS work_unit_id, pr_refs.ref AS ref, pr_refs.pr_number AS pr_number,
		if(pr_refs.uuid_direct != '', pr_refs.uuid_direct, rl.repo_uuid) AS repo_uuid
	FROM pr_refs
	LEFT JOIN repo_lookup AS rl ON rl.provider = pr_refs.lookup_provider AND rl.repo = pr_refs.lookup_repo
),
refs AS (
	SELECT DISTINCT work_unit_id, repo_uuid,
		if(repo_uuid = '', ref, concat(repo_uuid, '#', pr_number)) AS ref_key
	FROM pr_parsed
),
counts AS (
	SELECT work_unit_id, repo_uuid, uniqExact(ref_key) AS c FROM refs GROUP BY work_unit_id, repo_uuid
),
totals AS (
	SELECT work_unit_id, sum(c) AS n FROM counts GROUP BY work_unit_id
),
split AS (
	SELECT counts.work_unit_id AS work_unit_id, counts.repo_uuid AS repo_uuid, counts.c / totals.n AS frac
	FROM counts INNER JOIN totals ON totals.work_unit_id = counts.work_unit_id
	WHERE counts.repo_uuid != ''
	UNION ALL
	SELECT work_unit_id, toString(repo_id) AS repo_uuid, 1.0 AS frac
	FROM windowed
	WHERE repo_id IS NOT NULL AND work_unit_id NOT IN (SELECT work_unit_id FROM totals)
),
attributed AS (
	SELECT split.repo_uuid AS repo_uuid, split.work_unit_id AS work_unit_id, split.frac * windowed.effort_value AS effort,
		windowed.theme_distribution_json AS theme_distribution_json,
		ifNull(windowed.subcategory_distribution_json['` + readers.BugfixSubcategoryKey + `'], 0.0) AS bugfix_share
	FROM split INNER JOIN windowed ON windowed.work_unit_id = split.work_unit_id
	WHERE split.repo_uuid IN {ids:Array(String)}
)
SELECT repo_uuid AS repo_id, 'theme' AS kind, theme_kv.1 AS key, sum(theme_kv.2 * effort) AS weighted_effort, uniqExact(work_unit_id) AS work_units
FROM attributed
ARRAY JOIN CAST(theme_distribution_json AS Array(Tuple(String, Float64))) AS theme_kv
GROUP BY repo_uuid, key
UNION ALL
SELECT repo_uuid AS repo_id, 'subcategory' AS kind, '` + readers.BugfixSubcategoryKey + `' AS key, sum(bugfix_share * effort) AS weighted_effort, uniqExact(work_unit_id) AS work_units
FROM attributed
GROUP BY repo_uuid
)`)
}

// repoMixChunk bounds how many repositories one statement reads. Each
// repository yields at most 6 rows (5 themes + the tracked subcategory), so
// 30 repositories stay strictly under maxFactRowsPerQuery (200). The row
// limit is therefore never reached and a partial mix can never be served as
// complete: more repositories are read by more statements, in a stable
// order, never by a silently truncated one.
const repoMixChunk = 30

func (p *InvestmentProvider) readRepoMixRows(ctx context.Context, orgID string, repoIDs []string, timeBound factTimeBound) ([]repoMixRow, error) {
	if len(repoIDs) == 0 {
		return nil, nil
	}
	sorted := append([]string(nil), repoIDs...)
	sort.Strings(sorted)
	extra := make([]readers.Binding, 0, 2)
	for _, b := range timeBound.bindings() {
		extra = append(extra, readers.Binding{Name: b.Name, Value: b.Value})
	}
	var rows []repoMixRow
	for start := 0; start < len(sorted); start += repoMixChunk {
		end := start + repoMixChunk
		if end > len(sorted) {
			end = len(sorted)
		}
		got := 0
		err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadRepositoryThemeMix", repoMixStatement(timeBound), orgID, sorted[start:end], func(row contextpacket.ClickHouseRowScanner) error {
			var r repoMixRow
			var workUnits uint64
			if err := row.Scan(&r.RepoID, &r.Kind, &r.Key, &r.WeightedEffort, &workUnits); err != nil {
				return err
			}
			r.WorkUnits = int64(workUnits)
			rows = append(rows, r)
			got++
			return nil
		}, extra...)
		if err != nil {
			return nil, err
		}
		if got >= maxFactRowsPerQuery {
			// Unreachable by construction (repoMixChunk * 6 < the limit);
			// if a future change breaks that, fail loudly, never serve a
			// truncated mix as complete.
			return nil, fmt.Errorf("repository theme mix chunk reached the row limit (%d rows for %d repositories)", got, end-start)
		}
	}
	return rows, nil
}

// repoThemeTotals folds one repository's rows into per-theme effort, the
// bugfix subcategory effort and the work-unit count.
type repoThemeTotals struct {
	theme     map[string]float64
	bugfix    float64
	workUnits int64
}

func groupRepoMix(rows []repoMixRow) map[string]*repoThemeTotals {
	out := map[string]*repoThemeTotals{}
	for _, r := range rows {
		t, ok := out[r.RepoID]
		if !ok {
			t = &repoThemeTotals{theme: map[string]float64{}}
			out[r.RepoID] = t
		}
		if r.WorkUnits > t.workUnits {
			t.workUnits = r.WorkUnits
		}
		switch r.Kind {
		case "theme":
			t.theme[r.Key] += r.WeightedEffort
		case "subcategory":
			if r.Key == readers.BugfixSubcategoryKey {
				t.bugfix += r.WeightedEffort
			}
		}
	}
	return out
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
	rows, err := p.readRepoMixRows(ctx, orgID, ids, timeBound)
	if err != nil {
		return rejected, err
	}
	grouped := groupRepoMix(rows)
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
