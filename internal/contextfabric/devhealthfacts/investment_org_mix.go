package devhealthfacts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// The organization's investment mix is ONE fact whose theme shares come from
// the latest row of every work unit (argMax by computed_at), split across
// repositories by the same PR-ref partition weight as a repository's mix and
// then summed over every repository with persisted work in the window. Each
// repository is therefore counted once: the mix never goes through team
// ownership, so a repository owned by several teams is not counted per team.
//
// Effort that reaches no repository is never folded into the shares and never
// dropped silently: a PR reference that resolves to no repository, and a unit
// that carries no PR reference and no repository at all, count in
// unattributed_effort_share (their share of all effort the statement sees).

const (
	orgMixScope       = "organization"
	orgMixBasisSuffix = "_over_all_repositories"
	// investmentOrgRestrictedReason is the closed limitation for a caller
	// whose grant is bound to repositories: a partial sum presented as the
	// organization would be wrong, so no total is served.
	investmentOrgRestrictedReason = "org-level fact; caller scope is repository-bound"
)

type orgMixRow struct {
	Theme              map[string]float64
	Bugfix             float64
	WorkUnits          int64
	Repositories       int64
	ResolvedEffort     float64
	UnattributedEffort float64
	EarliestUnit       time.Time
}

// orgMixStatement sums the per-(work unit, repository) split of
// repoSplitCore over every repository. Rows of a ref that resolved to no
// repository (repo_uuid = ”) contribute only to the unattributed effort.
func orgMixStatement(bound factTimeBound) string {
	memberships := []string{fmt.Sprintf("if(%s, 0, -1)", mixWindowPredicate(0, bound))}
	statement := `SELECT
	sumMap(mapApply((k, v) -> (k, if(win >= 0 AND repo_uuid != '', v * effort, 0.)), theme_distribution_json)) AS theme_effort,
	sumIf(bugfix_share * effort, win >= 0 AND repo_uuid != '') AS bugfix_effort,
	uniqExactIf(work_unit_id, win >= 0 AND repo_uuid != '') AS work_units,
	uniqExactIf(repo_uuid, win >= 0 AND repo_uuid != '') AS repositories,
	sumIf(effort, win >= 0 AND repo_uuid != '') AS resolved_effort,
	sumIf(effort, win >= 0 AND repo_uuid = '') AS unattributed_effort,
	min(span_from) AS span_from
FROM (
	SELECT win, repo_uuid, work_unit_id, c / n * effort_value AS effort, theme_distribution_json, bugfix_share, span_from
	FROM (
		SELECT win, repo_uuid, work_unit_id, c,
			sum(c) OVER (PARTITION BY win, work_unit_id) AS n,
			effort_value, theme_distribution_json, bugfix_share, span_from
		FROM (
` + orgSplitCore(memberships) + `		)
	)
)`
	return withRowLimit(statement)
}

func (p *InvestmentProvider) readOrgMixRow(ctx context.Context, orgID string, bound factTimeBound) (orgMixRow, error) {
	var out orgMixRow
	var extra []readers.Binding
	for _, tb := range bound.bindings() {
		extra = append(extra, readers.Binding{Name: tb.Name, Value: tb.Value})
	}
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadOrganizationThemeMix", orgMixStatement(bound), orgID, []string{}, func(row contextpacket.ClickHouseRowScanner) error {
		var workUnits, repositories uint64
		var spanFrom time.Time
		if err := row.Scan(&out.Theme, &out.Bugfix, &workUnits, &repositories, &out.ResolvedEffort, &out.UnattributedEffort, &spanFrom); err != nil {
			return err
		}
		out.EarliestUnit = spanFrom
		out.WorkUnits = int64(workUnits)
		out.Repositories = int64(repositories)
		return nil
	}, extra...)
	return out, err
}

// organizationSubjectsOfCaller returns the requested organization subjects
// that name the caller's own organization (either identity form) and how many
// named another or an unusable one.
func organizationSubjectsOfCaller(subjects []contextfabric.SubjectRef, orgID string) (own []contextfabric.SubjectRef, rejected int) {
	for _, subject := range subjectsOfKind(subjects, contextfabric.SubjectOrganization) {
		if subject.CanonicalID == orgID || subject.CanonicalID == organizationPrefix+orgID {
			own = append(own, subject)
			continue
		}
		rejected++
	}
	return own, rejected
}

// readOrganizationThemeMix emits the organization-scope investment fact: one
// for the organization, none when the window holds no persisted effort (never
// a zero mix). When all of the window's effort is unattributed the fact carries
// no theme shares and no breakdown, only the counts and the unattributed share
// (1): the effort is disclosed, never presented as a mix.
func (p *InvestmentProvider) readOrganizationThemeMix(ctx context.Context, principal storage.Principal, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rejected int, restricted bool, err error) {
	own, rejected := organizationSubjectsOfCaller(subjects, orgID)
	if len(own) == 0 {
		return rejected, false, nil
	}
	if sourceHealthRestricted(principal) {
		return rejected, true, nil
	}
	row, err := p.readOrgMixRow(ctx, orgID, timeBound)
	if err != nil {
		return rejected, false, err
	}
	// Both accepted spellings of the organization id name ONE organization:
	// one fact, under the first spelling asked for.
	own = own[:1]
	for _, subject := range own {
		recordInvestmentSpan(ctx, subject.CanonicalID, row.EarliestUnit)
	}
	t := &repoThemeTotals{theme: row.Theme, bugfix: row.Bugfix, workUnits: row.WorkUnits, repos: row.Repositories}
	if t.theme == nil {
		t.theme = map[string]float64{}
	}
	total := t.total()
	if total <= 0 && row.UnattributedEffort <= 0 {
		return rejected, false, nil
	}
	unattributedShare := 0.0
	if all := row.ResolvedEffort + row.UnattributedEffort; all > 0 {
		unattributedShare = row.UnattributedEffort / all
	}
	for _, subject := range own {
		fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+8)
		if total > 0 {
			for _, theme := range canonicalInvestmentThemes {
				fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(t.theme[theme] / total))
			}
			fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(roundMixEffort(t.bugfix / total))
			fields["theme_breakdown"] = themeBreakdownTable(t, timeBound.effectiveGrain(grainDaily))
		}
		fields["work_unit_count"] = contextfabric.IntegerFactValue(t.workUnits)
		fields["scope"] = contextfabric.StringFactValue(orgMixScope)
		fields["repositories_in_scope"] = contextfabric.IntegerFactValue(t.repos)
		fields["unattributed_effort_share"] = contextfabric.NumberFactValue(roundMixEffort(unattributedShare))
		fields["mix_source"] = contextfabric.StringFactValue(repoMixSource)
		fields["attribution_basis"] = contextfabric.StringFactValue(repoMixBasis + orgMixBasisSuffix)
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityOrganization, orgID)},
		})
	}
	return rejected, false, nil
}

// orgNoRefUnit names the one placeholder reference a unit with no reference
// and no repository of its own gets in the organization statement, so its
// effort lands in the unattributed share instead of vanishing.
const orgNoRefUnit = "[('', '', '', '', concat('unit:', work_unit_id))]"

// orgSplitCore is the repository split with one addition: a unit that has no
// PR reference and no repository of its own keeps its effort as an
// unattributed row (repo_uuid ”) rather than being dropped by the reference
// join. Repository and team reads keep their own rule (such a unit reaches no
// repository).
func orgSplitCore(memberships []string) string {
	core := repoSplitCore(memberships, "", "", true)
	return strings.Replace(core, repoRefsExpression+" AS refs",
		"if(empty("+repoRefsExpression+"), "+orgNoRefUnit+", "+repoRefsExpression+") AS refs", 1)
}
