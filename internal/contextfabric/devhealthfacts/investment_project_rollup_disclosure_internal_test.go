package devhealthfacts

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-12160: a project served its owning teams' roll-up states the
// project's own work units in the window, on the fact and in its coverage
// reason; a project served its own mix, and every other subject, does not.

func rollupTestFact(subject contextfabric.SubjectRef, source string, units int64) contextfabric.CanonicalFact {
	fact := cutTestFact(subject, source)
	fact.Fields["work_unit_count"] = contextfabric.IntegerFactValue(units)
	return fact
}

func TestStampProjectOwnWorkUnitsCountsTheRollupProjectsOwnUnits(t *testing.T) {
	t.Parallel()
	none, zeroEffort, native := cutTestSubject("none"), cutTestSubject("zero"), cutTestSubject("native")
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T", Label: "T"}
	facts := []contextfabric.CanonicalFact{
		rollupTestFact(none, contextfabric.InvestmentMixSourceOwningTeamRollup, 7240),
		rollupTestFact(zeroEffort, contextfabric.InvestmentMixSourceOwningTeamRollup, 7240),
		rollupTestFact(native, contextfabric.InvestmentMixSourceProjectNative, 9),
		{Kind: contextfabric.FactInvestment, Subject: team, Fields: map[string]contextfabric.FactValue{}},
	}
	bySubject := map[string]contextfabric.SubjectRef{"linear:none": none, "linear:zero": zeroEffort, "linear:native": native}
	stampProjectOwnWorkUnits(facts, bySubject, map[string]int64{"linear:zero": 3, "linear:native": 9})
	own := func(i int) (int64, bool) {
		v, ok := facts[i].Fields[investmentProjectOwnWorkUnitCountField]
		if !ok || v.Integer == nil {
			return 0, false
		}
		return *v.Integer, true
	}
	if got, ok := own(0); !ok || got != 0 {
		t.Errorf("roll-up project with no native row: own = %d (set %v), want 0", got, ok)
	}
	if got, ok := own(1); !ok || got != 3 {
		t.Errorf("roll-up project with zero-effort own units: own = %d (set %v), want 3", got, ok)
	}
	if _, ok := own(2); ok {
		t.Errorf("a native project carries %s; its own units are its work_unit_count", investmentProjectOwnWorkUnitCountField)
	}
	if _, ok := own(3); ok {
		t.Errorf("a team fact carries %s", investmentProjectOwnWorkUnitCountField)
	}
}

func TestDiscloseProjectRollupsNamesTheRollupInTheProjectsReason(t *testing.T) {
	t.Parallel()
	rollup, native := cutTestSubject("rollup"), cutTestSubject("native")
	rollupFact := rollupTestFact(rollup, contextfabric.InvestmentMixSourceOwningTeamRollup, 7240)
	rollupFact.Fields[investmentProjectOwnWorkUnitCountField] = contextfabric.IntegerFactValue(0)
	result := contextfabric.FactProviderResult{Facts: []contextfabric.CanonicalFact{rollupFact, rollupTestFact(native, contextfabric.InvestmentMixSourceProjectNative, 9)}}
	discloseProjectRollups(&result)
	want := "investment_project_owning_team_rollup: " + rollup.CanonicalID + ": the theme mix is the owning teams' roll-up over 7240 work units in the window, not the project's own; the project has 0 work units of its own in the window"
	if got := result.SubjectReasons[contextfabric.FactSubjectKey(rollup)]; got != want {
		t.Errorf("roll-up project reason = %q, want %q", got, want)
	}
	if got := result.SubjectReasons[contextfabric.FactSubjectKey(native)]; strings.Contains(got, investmentProjectOwningTeamRollup) {
		t.Errorf("native project reason names the roll-up: %q", got)
	}
	wantNote := "investment_project_owning_team_rollup for 1 of 2 projects with an investment fact: the theme mix is the owning teams' roll-up, not the project's own, because the project has no measurable work of its own in the window"
	if result.RequestNote != wantNote {
		t.Errorf("request note = %q, want %q", result.RequestNote, wantNote)
	}
	none := contextfabric.FactProviderResult{Facts: []contextfabric.CanonicalFact{rollupTestFact(native, contextfabric.InvestmentMixSourceProjectNative, 9)}}
	discloseProjectRollups(&none)
	if none.RequestNote != "" || len(none.SubjectReasons) != 0 {
		t.Errorf("a read with no roll-up carries note %q and reasons %v", none.RequestNote, none.SubjectReasons)
	}
}
