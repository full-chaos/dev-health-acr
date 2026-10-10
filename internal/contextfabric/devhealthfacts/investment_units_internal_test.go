package devhealthfacts

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestInvestmentUnitsStatementReadsWorkUnitInvestmentsOnceAndKeysetOnlyWithACursor(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for name, bound := range map[string]factTimeBound{
		"current axis":  {},
		"range":         {active: true, hasStart: true, start: start, end: end},
		"point in time": {active: true, end: end},
	} {
		first := investmentUnitsStatement(bound, false)
		next := investmentUnitsStatement(bound, true)
		for label, statement := range map[string]string{"first": first, "next": next} {
			if got := strings.Count(statement, "work_unit_investments"); got != 1 {
				t.Errorf("%s/%s: statement names work_unit_investments %d times, want 1", name, label, got)
			}
			if got := strings.Count(statement, "FROM repos"); got != 1 {
				t.Errorf("%s/%s: statement references repos %d times, want 1", name, label, got)
			}
			if strings.Contains(statement, "WITH ") {
				t.Errorf("%s/%s: statement uses a CTE; ClickHouse inlines it at every reference", name, label)
			}
			if !strings.Contains(statement, "ORDER BY share DESC, work_unit_id ASC, repo_uuid ASC\nLIMIT {"+unitLimitParam+":UInt32}") {
				t.Errorf("%s/%s: statement is not ordered by the cursor key and limited by the page size", name, label)
			}
		}
		if strings.Contains(first, unitCursorShareParam) {
			t.Errorf("%s: the first page statement carries a cursor predicate", name)
		}
		for _, param := range []string{unitCursorShareParam, unitCursorIDParam, unitCursorRepoParam} {
			if !strings.Contains(next, param) {
				t.Errorf("%s: the next page statement lacks %s", name, param)
			}
		}
	}
}

// The unit listing and the mix statement share one split: with no unit
// columns repoSplitCore is the mix statement's own text.
func TestRepoMixStatementEmbedsTheSharedSplit(t *testing.T) {
	t.Parallel()
	statement := repoMixStatement([]factTimeBound{{}})
	core := repoSplitCore([]string{"if(1, 0, -1)"}, "", "", true)
	if !strings.Contains(statement, core) {
		t.Fatal("the mix statement no longer embeds the shared split verbatim")
	}
	units := investmentUnitsStatement(factTimeBound{}, false)
	withUnitColumns := repoSplitCore([]string{"if(1, 0, -1)"}, " from_ts, to_ts,", ",\n\t\t\t\tany(parsed.from_ts) AS from_ts, any(parsed.to_ts) AS to_ts,\n\t\t\t\tgroupUniqArray(parsed.pr_number) AS prs,\n\t\t\t\tgroupUniqArray(parsed.ref_text) AS ref_texts", false)
	if !strings.Contains(units, withUnitColumns) {
		t.Fatal("the unit statement does not embed the shared split")
	}
}

// Weighted-sum identity on rows: the mix the repository path computes from
// the same rows equals sum(share * theme) / sum(share) of the unit facts.
func TestUnitFactsWeightedSumIsTheMix(t *testing.T) {
	t.Parallel()
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:t"}
	units := []unitRow{
		{WorkUnitID: "u1", RepoID: "r1", Share: 5, Effort: 10, Theme: map[string]float64{"feature_delivery": 1}},
		{WorkUnitID: "u2", RepoID: "r1", Share: 8, Effort: 8, Theme: map[string]float64{"risk": 0.5, "quality": 0.5}},
		{WorkUnitID: "u1", RepoID: "r2", Share: 5, Effort: 10, Theme: map[string]float64{"feature_delivery": 1}},
	}
	totals := &repoThemeTotals{theme: map[string]float64{}}
	weighted := map[string]float64{}
	var shares float64
	for _, u := range units {
		for theme, p := range u.Theme {
			totals.theme[theme] += u.Share * p
		}
		fact := unitFact(team, u, "org-1")
		share := *fact.Fields["share_in_scope"].Number
		shares += share
		for _, theme := range canonicalInvestmentThemes {
			weighted[theme] += share * *fact.Fields["unit_"+contextfabric.FactFieldTheme(theme)].Number
		}
	}
	for _, theme := range canonicalInvestmentThemes {
		if math.Abs(weighted[theme]-totals.theme[theme]) > 1e-12 {
			t.Errorf("%s: weighted sum %v != mix effort %v", theme, weighted[theme], totals.theme[theme])
		}
		if math.Abs(weighted[theme]/shares-totals.theme[theme]/totals.total()) > 1e-12 {
			t.Errorf("%s: weighted share differs from the mix share", theme)
		}
	}
}

func TestUnitFactFieldsAreDeclaredAndRefsAreBounded(t *testing.T) {
	t.Parallel()
	capability := newInvestmentProvider(nil).Capability()
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:t"}
	var prs []string
	for i := 1; i <= unitRefsPerFact+10; i++ {
		prs = append(prs, strconv.Itoa(i))
	}
	long := strings.Repeat("x", unitUnresolvedRefMaxBytes+50)
	var unresolved []string
	for i := 0; i < unitUnresolvedRefsPerFact+5; i++ {
		unresolved = append(unresolved, long+strconv.Itoa(i))
	}
	fact := unitFact(team, unitRow{WorkUnitID: "u", RepoID: "r", Share: 1, Effort: 2, Theme: map[string]float64{"risk": 1}, PRs: prs, UnresolvedN: 25, UnresolvedRefs: unresolved}, "org-1")
	for name := range fact.Fields {
		if _, ok := capability.FieldDeclaration(name, contextfabric.SubjectTeam); !ok {
			t.Errorf("unit fact field %q is not declared for a team subject; the reader would drop it", name)
		}
		if _, ok := capability.FieldDeclaration(name, contextfabric.SubjectRepository); !ok {
			t.Errorf("unit fact field %q is not declared for a repository subject", name)
		}
	}
	if got := len(fact.EvidenceRefIDs); got != 1+unitRefsPerFact {
		t.Errorf("refs = %d, want the repository ref and %d pull request refs", got, unitRefsPerFact)
	}
	if got := *fact.Fields["unit_pull_request_count"].Integer; got != int64(len(prs)) {
		t.Errorf("unit_pull_request_count = %d, want the full count %d", got, len(prs))
	}
	if got := len(strings.Split(*fact.Fields["unit_unresolved_refs"].String, ",")); got != unitUnresolvedRefsPerFact {
		t.Errorf("unresolved handles = %d, want %d", got, unitUnresolvedRefsPerFact)
	}
}

// Boundaries of the bounds: one more than the cap is cut, exactly the cap is
// kept whole, and a non-numeric pull request number is never minted as a ref.
func TestUnitFactBoundsHoldAtTheirEdges(t *testing.T) {
	t.Parallel()
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:t"}
	handles := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "ghpr:acme/missing#" + strconv.Itoa(100+i)
		}
		return out
	}
	for n, want := range map[int]int{unitUnresolvedRefsPerFact - 1: unitUnresolvedRefsPerFact - 1, unitUnresolvedRefsPerFact: unitUnresolvedRefsPerFact, unitUnresolvedRefsPerFact + 1: unitUnresolvedRefsPerFact} {
		fact := unitFact(team, unitRow{WorkUnitID: "u", RepoID: "r", Theme: map[string]float64{"risk": 1}, UnresolvedN: uint64(n), UnresolvedRefs: handles(n)}, "org-1")
		if got := len(strings.Split(*fact.Fields["unit_unresolved_refs"].String, ",")); got != want {
			t.Errorf("%d unresolved handles served as %d, want %d", n, got, want)
		}
	}
	for length, want := range map[int]int{unitUnresolvedRefMaxBytes: unitUnresolvedRefMaxBytes, unitUnresolvedRefMaxBytes + 1: unitUnresolvedRefMaxBytes} {
		fact := unitFact(team, unitRow{WorkUnitID: "u", RepoID: "r", Theme: map[string]float64{"risk": 1}, UnresolvedN: 1, UnresolvedRefs: []string{strings.Repeat("h", length)}}, "org-1")
		if got := len(*fact.Fields["unit_unresolved_refs"].String); got != want {
			t.Errorf("a %d byte handle served as %d bytes, want %d", length, got, want)
		}
	}
	fact := unitFact(team, unitRow{WorkUnitID: "u", RepoID: "r", Theme: map[string]float64{"risk": 1}, PRs: []string{"12", "not-a-number", "7"}}, "org-1")
	if got := len(fact.EvidenceRefIDs); got != 3 {
		t.Errorf("refs = %v, want the repository ref and the two numeric pull requests", fact.EvidenceRefIDs)
	}
	if got := *fact.Fields["unit_pull_request_count"].Integer; got != 3 {
		t.Errorf("unit_pull_request_count = %d, want 3 (the count is of stored references)", got)
	}
	share := unitFact(team, unitRow{WorkUnitID: "u", RepoID: "r", Share: 3, Effort: 11, Theme: map[string]float64{"risk": 1}}, "org-1")
	if got := *share.Fields["share_in_scope"].Number; got != 3 {
		t.Errorf("share_in_scope = %v, want the row share 3 (not the effort)", got)
	}
	if got := *share.Fields["unit_effort_value"].Number; got != 11 {
		t.Errorf("unit_effort_value = %v, want 11", got)
	}
}

// The page fact's counts and totals cover rows a restricted caller cannot
// read, so every one of them is declared an aggregate.
func TestUnitPageTotalsAreDeclaredAggregates(t *testing.T) {
	t.Parallel()
	capability := newInvestmentProvider(nil).Capability()
	for _, name := range []string{"units_returned", "page_share_total", "units_refs_unresolved", "scope_share_total", "scope_unit_rows"} {
		field, ok := capability.FieldDeclaration(name, contextfabric.SubjectTeam)
		if !ok || !field.Aggregate {
			t.Errorf("%s: declared=%v aggregate=%v, want an aggregate", name, ok, field.Aggregate)
		}
	}
	for _, name := range []string{"share_in_scope", "work_unit_id", "next_cursor"} {
		if field, ok := capability.FieldDeclaration(name, contextfabric.SubjectTeam); ok && field.Aggregate {
			t.Errorf("%s must not be an aggregate", name)
		}
	}
}

// unit_unresolved_refs can name a repository the caller has no grant for; as an
// opaque reference it is withheld from a repository-restricted caller.
func TestUnresolvedHandlesAreAnOpaqueReference(t *testing.T) {
	t.Parallel()
	capability := newInvestmentProvider(nil).Capability()
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectTeam, contextfabric.SubjectRepository} {
		field, ok := capability.FieldDeclaration("unit_unresolved_refs", kind)
		if !ok || field.SubjectRef == nil || field.SubjectRef.IDForm != contextfabric.FactSubjectIDOpaque {
			t.Errorf("%s: unit_unresolved_refs declared=%v ref=%+v, want an opaque reference", kind, ok, field.SubjectRef)
		}
	}
	fact := unitFact(contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:t"}, unitRow{WorkUnitID: "u", RepoID: "r", Theme: map[string]float64{"risk": 1}, PRs: []string{"", "5"}, UnresolvedN: 1, UnresolvedRefs: []string{"ghpr:x/y#1"}}, "org-1")
	if got := *fact.Fields["unit_pull_request_count"].Integer; got != 2 {
		t.Errorf("unit_pull_request_count = %d on the raw row; the scan removes the empty fallback number before unitFact", got)
	}
	if got := withoutEmpty([]string{"", "5", "", "7"}); len(got) != 2 || got[0] != "5" || got[1] != "7" {
		t.Errorf("withoutEmpty = %v, want [5 7]", got)
	}
}
