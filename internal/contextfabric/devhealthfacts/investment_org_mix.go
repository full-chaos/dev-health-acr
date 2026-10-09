package devhealthfacts

import (
	"context"
	"fmt"

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
// A PR ref that resolves to no repository attaches to no repository
// (null-carrying, as in a repository's own mix). Its share of the ref-carrying
// effort is served as unresolved_effort_share and is not part of the shares.

const (
	orgMixScope       = "organization"
	orgMixBasisSuffix = "_over_all_repositories"
	// investmentOrgRestrictedReason is the closed limitation for a caller
	// whose grant is bound to repositories: a partial sum presented as the
	// organization would be wrong, so no total is served.
	investmentOrgRestrictedReason = "org-level fact; caller scope is repository-bound"
)

type orgMixRow struct {
	Theme            map[string]float64
	Bugfix           float64
	WorkUnits        int64
	Repositories     int64
	ResolvedEffort   float64
	UnresolvedEffort float64
}

// orgMixStatement sums the per-(work unit, repository) split of
// repoSplitCore over every repository. Rows of a ref that resolved to no
// repository (repo_uuid = ”) contribute only to the unresolved effort.
func orgMixStatement(bound factTimeBound) string {
	memberships := []string{fmt.Sprintf("if(%s, 0, -1)", mixWindowPredicate(0, bound))}
	statement := `SELECT
	sumMap(mapApply((k, v) -> (k, if(repo_uuid != '', v * effort, 0.)), theme_distribution_json)) AS theme_effort,
	sumIf(bugfix_share * effort, repo_uuid != '') AS bugfix_effort,
	uniqExactIf(work_unit_id, repo_uuid != '') AS work_units,
	uniqExactIf(repo_uuid, repo_uuid != '') AS repositories,
	sumIf(effort, repo_uuid != '') AS resolved_effort,
	sumIf(effort, repo_uuid = '') AS unresolved_effort,
	toString(min(span_from)) AS span_from
FROM (
	SELECT repo_uuid, work_unit_id, c / n * effort_value AS effort, theme_distribution_json, bugfix_share, span_from
	FROM (
		SELECT win, repo_uuid, work_unit_id, c,
			sum(c) OVER (PARTITION BY win, work_unit_id) AS n,
			effort_value, theme_distribution_json, bugfix_share, span_from
		FROM (
` + repoSplitCore(memberships, "", "") + `		)
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
		var spanFrom string
		if err := row.Scan(&out.Theme, &out.Bugfix, &workUnits, &repositories, &out.ResolvedEffort, &out.UnresolvedEffort, &spanFrom); err != nil {
			return err
		}
		if spanErr := recordInvestmentSpanText(ctx, spanFrom, workUnits > 0 || out.UnresolvedEffort > 0); spanErr != nil {
			return spanErr
		}
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
// per requested own-organization subject, none when the window holds no
// persisted effort (never a zero mix).
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
	t := &repoThemeTotals{theme: row.Theme, bugfix: row.Bugfix, workUnits: row.WorkUnits, repos: row.Repositories}
	if t.theme == nil {
		t.theme = map[string]float64{}
	}
	total := t.total()
	if total <= 0 {
		return rejected, false, nil
	}
	unresolvedShare := 0.0
	if all := row.ResolvedEffort + row.UnresolvedEffort; all > 0 {
		unresolvedShare = row.UnresolvedEffort / all
	}
	for _, subject := range own {
		fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+8)
		for _, theme := range canonicalInvestmentThemes {
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(t.theme[theme] / total))
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(roundMixEffort(t.bugfix / total))
		fields["work_unit_count"] = contextfabric.IntegerFactValue(t.workUnits)
		fields["theme_breakdown"] = themeBreakdownTable(t, timeBound.effectiveGrain(grainDaily))
		fields["scope"] = contextfabric.StringFactValue(orgMixScope)
		fields["repositories_in_scope"] = contextfabric.IntegerFactValue(t.repos)
		fields["unresolved_effort_share"] = contextfabric.NumberFactValue(roundMixEffort(unresolvedShare))
		fields["mix_source"] = contextfabric.StringFactValue(repoMixSource)
		fields["attribution_basis"] = contextfabric.StringFactValue(repoMixBasis + orgMixBasisSuffix)
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityOrganization, orgID)},
		})
	}
	return rejected, false, nil
}
