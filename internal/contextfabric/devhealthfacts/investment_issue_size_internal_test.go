package devhealthfacts

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func sizingFieldsOf(total float64, units, sized int64) map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	investmentSizingFields(fields, total, units, sized)
	return fields
}

// The mix serves its measure and sizing coverage the way ops' REST investment
// response and explanation do: coverage and note left out for a mix with no
// unit, unsized_unit_count always, the note only below half.
func TestInvestmentSizingFieldsServeCoverageLikeOps(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		units, sized int64
		coverage     *float64
		unsized      int64
		note         string
	}{
		"mostly unsized":    {4, 1, ptr(0.25), 3, "Mostly unsized: 1 of 4 work units have an issue size. The other 3 have no size, so each counts as 1 work unit."},
		"none sized":        {3, 0, ptr(0), 3, "Mostly unsized: 0 of 3 work units have an issue size. The other 3 have no size, so each counts as 1 work unit."},
		"exactly half":      {4, 2, ptr(0.5), 2, ""},
		"all sized":         {3, 3, ptr(1), 0, ""},
		"no unit in window": {0, 0, nil, 0, ""},
	} {
		fields := sizingFieldsOf(2.5, c.units, c.sized)
		if got := *fields["measure"].String; got != "ISSUE_SIZE" {
			t.Errorf("%s: measure = %q", name, got)
		}
		if got := *fields["denominator"].Number; got != 2.5 {
			t.Errorf("%s: denominator = %v, want the sum of all themes", name, got)
		}
		if got := *fields["unsized_unit_count"].Integer; got != c.unsized {
			t.Errorf("%s: unsized_unit_count = %d, want %d", name, got, c.unsized)
		}
		coverage, has := fields["sizing_coverage"]
		switch {
		case c.coverage == nil && has:
			t.Errorf("%s: a mix with no unit serves sizing_coverage %v; it is not measured, never 0", name, coverage)
		case c.coverage != nil && (!has || *coverage.Number != *c.coverage):
			t.Errorf("%s: sizing_coverage = %v, want %v", name, coverage, *c.coverage)
		}
		note, hasNote := fields["sizing_note"]
		switch {
		case c.note == "" && hasNote:
			t.Errorf("%s: sizing_note = %q, want none", name, *note.String)
		case c.note != "" && (!hasNote || *note.String != c.note):
			t.Errorf("%s: sizing_note = %v, want %q", name, note, c.note)
		}
	}
}

func ptr(v float64) *float64 { return &v }

// A team's coverage counts a work unit once however many of its owned
// repositories the unit reaches: the per-repository rows keep the unit hashes
// and the team unions them.
func TestTeamCoverageCountsASharedUnitOnce(t *testing.T) {
	t.Parallel()
	themes := func() map[string]float64 { return map[string]float64{"feature_delivery": 1} }
	perRepo := groupRepoMix([]repoMixRow{
		{RepoID: "a", Theme: themes(), WorkUnits: 2, UnitHashes: []uint64{1, 2}, SizedUnitHashes: []uint64{1}},
		{RepoID: "b", Theme: themes(), WorkUnits: 2, UnitHashes: []uint64{2, 3}, SizedUnitHashes: []uint64{2}},
	})
	team := sumOwnedRepoMix(map[string][]string{"t": {"a", "b"}}, perRepo)["t"]
	units, sized := team.sizingUnits()
	if units != 3 || sized != 2 {
		t.Fatalf("team coverage = %d sized of %d units, want 2 of 3 (unit 2 is in both repositories)", sized, units)
	}
	if repo := perRepo["a"]; repo.workUnits != 2 {
		t.Fatalf("a repository keeps its own count, got %d", repo.workUnits)
	}
	linked := &repoThemeTotals{theme: themes(), unitSet: map[uint64]struct{}{3: {}, 4: {}}, sizedUnitSet: map[uint64]struct{}{4: {}}}
	totals := map[string]*repoThemeTotals{"t": team}
	addLinkedMix(totals, map[string]*repoThemeTotals{"t": linked})
	if units, sized = totals["t"].sizingUnits(); units != 4 || sized != 3 {
		t.Fatalf("with the project-linked units, coverage = %d sized of %d units, want 3 of 4 (unit 3 is in both sets)", sized, units)
	}
}

// Every statement of the repository, team and organization mix and of the unit
// listing reads the organization's units through the sized source: the weight
// is the unit's issue size (1 when unsized) and never a constant.
func TestInvestmentStatementsWeighByTheSizedSource(t *testing.T) {
	t.Parallel()
	b := factTimeBound{active: true, hasStart: true, start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), end: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	scope := subqueryMembershipScope
	for name, statement := range map[string]string{
		"repository mix":          repoMixStatement([]factTimeBound{b}),
		"organization mix":        orgMixStatement(b),
		"repository units":        investmentUnitsStatement(b, false),
		"organization units":      investmentOrgUnitsStatement(b, false, scope),
		"team linked pull mix":    linkedPullRequestMixStatement(b, scope, 101),
		"team linked pull split":  linkedPullRequestSplitRows([]string{"if(1, 0, -1)"}, scope, linkedPullRequestsSent),
		"requested split":         requestedSplitRows([]string{"if(1, 0, -1)"}, true, scope, true),
		"whole organization core": repoSplitCore([]string{"if(1, 0, -1)"}, "", "", true),
		"project roll-up themes":  projectRollupRepoThemesStatement(),
		"project roll-up bugfix":  projectRollupRepoBugfixStatement(),
		"project native values":   projectNativeThemeValuesStatement(),
	} {
		if strings.Contains(statement, "toFloat64(1) AS effort_value") {
			t.Errorf("%s: a unit still weighs a constant 1 (effort_value)", name)
		}
		if got := strings.Count(statement, "FROM work_unit_issue_size"); got != 1 {
			t.Errorf("%s: reads work_unit_issue_size %d times, want once (the sized source)", name, got)
		}
		if !strings.Contains(statement, "unit_size_weight") || !strings.Contains(statement, "unit_issue_size") {
			t.Errorf("%s: does not carry the unit size weight and issue size", name)
		}
	}
}

// A unit row names its own issue size beside the weight it counts with.
func TestUnitFactNamesItsIssueSize(t *testing.T) {
	t.Parallel()
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:t"}
	fact := unitFact(team, unitRow{WorkUnitID: "u", RepoID: "r", Share: 3, Effort: 8, IssueSize: 8, Theme: map[string]float64{"risk": 1}}, "org-1")
	if got := *fact.Fields["unit_issue_size"].Number; got != 8 {
		t.Errorf("unit_issue_size = %v, want 8", got)
	}
	unsized := unitFact(team, unitRow{WorkUnitID: "u2", RepoID: "r", Share: 1, Effort: 1, Theme: map[string]float64{"risk": 1}}, "org-1")
	if got := *unsized.Fields["unit_issue_size"].Number; got != 0 {
		t.Errorf("an unsized unit serves unit_issue_size %v, want 0 (it counts 1 in unit_effort_value)", got)
	}
}

// The sized source needs org_id on the folded units. In the SELECT that reads
// work_unit_investments it must be the table's own column: an alias of the
// same name there wins over the column in the WHERE and turns the org filter
// into a constant true, which reads every organization's units.
func TestProjectFoldsCarryTheOrgColumnNotAnAliasOfIt(t *testing.T) {
	t.Parallel()
	for name, statement := range map[string]string{
		"project roll-up themes": projectRollupRepoThemesStatement(),
		"project roll-up bugfix": projectRollupRepoBugfixStatement(),
		"project native values":  projectNativeThemeValuesStatement(),
	} {
		if strings.Contains(statement, "{org_id:String} AS org_id") {
			t.Errorf("%s: aliases org_id beside the org_id filter of work_unit_investments", name)
		}
		if !strings.Contains(statement, "GROUP BY work_unit_id, org_id") {
			t.Errorf("%s: does not carry the table's org_id column through the fold", name)
		}
	}
}
