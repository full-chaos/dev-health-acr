package devhealthfacts

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// workUnitInvestmentsReadExceptions are the reads of work_unit_investments that
// do not take the evidence-gate predicate, each with the reason it needs none.
var workUnitInvestmentsReadExceptions = map[string]string{
	"investment.go|investmentWatermarkStatement":                          "freshness probe: max(computed_at) and a row count, no mix",
	"investment_empty_cause.go|ownedUnitCountStatement":                   "empty-cause probe: names which stage dropped a team's units; a team whose units are all gated is named before it runs",
	"investment_project_mix_phased.go|projectMixScopeStatementScoped":     "phase 0 pins every in-window unit, gated or not, so the later phases can count the gated ones",
	"investment_project_mix_phased.go|projectNativePlacementStatement":    "placement only; the native mix drops gated units in Go by the flag of the theme values statement",
	"investment_project_mix_phased.go|projectNativeBugfixValuesStatement": "joined in Go to the theme values, whose flag drops gated units",
	"investment_project_rollup_mix.go|projectRollupMixStatement":          "parity oracle of the phased roll-up, never served",
	"investment_project_native_mix.go|projectNativeMixStatement":          "parity oracle of the phased native mix, never served",
}

// TestEveryWorkUnitInvestmentsReadTakesTheGatePredicateOrIsANamedException
// enumerates every read of work_unit_investments in this package's sources.
func TestEveryWorkUnitInvestmentsReadTakesTheGatePredicateOrIsANamedException(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	reads := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(body), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "FROM work_unit_investments") || strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			reads++
			name, start, end := enclosingDeclaration(lines, i)
			key := file + "|" + name
			seen[key] = true
			if strings.Contains(strings.Join(lines[start:end], "\n"), "latestUnitGatedSQL") {
				if _, listed := workUnitInvestmentsReadExceptions[key]; listed {
					t.Errorf("%s takes the gate predicate and is also on the exception list", key)
				}
				continue
			}
			if _, listed := workUnitInvestmentsReadExceptions[key]; !listed {
				t.Errorf("%s:%d reads work_unit_investments without latestUnitGatedSQL and is not a named exception", file, i+1)
			}
		}
	}
	if reads < 10 {
		t.Fatalf("found %d reads of work_unit_investments: the scan measured nothing", reads)
	}
	for key := range workUnitInvestmentsReadExceptions {
		if !seen[key] {
			t.Errorf("exception %s names no read", key)
		}
	}
}

// enclosingDeclaration is the top-level func, const or var that holds line i,
// and its line range.
func enclosingDeclaration(lines []string, i int) (name string, start, end int) {
	for start = i; start > 0; start-- {
		if declarationName(lines[start]) != "" {
			break
		}
	}
	name = declarationName(lines[start])
	for end = i + 1; end < len(lines); end++ {
		if declarationName(lines[end]) != "" {
			break
		}
	}
	return name, start, end
}

func declarationName(line string) string {
	for _, prefix := range []string{"func (", "func ", "const ", "var "} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		if prefix == "func (" {
			if close := strings.Index(rest, ") "); close >= 0 {
				rest = rest[close+2:]
			}
		}
		end := strings.IndexAny(rest, " ([=")
		if end < 0 {
			return rest
		}
		return rest[:end]
	}
	return ""
}

func TestQueryVersionMovedPastTheEvidenceGatedMix(t *testing.T) {
	t.Parallel()
	if QueryVersion <= "devhealthfacts.clickhouse.v255" {
		t.Fatalf("QueryVersion = %q, want v256 or later -- an answer saved when the investment mix held the uniform prior of evidence-gated units must not be reused", QueryVersion)
	}
}

func TestEvidenceGatedStatusesAreTheOpsGateStatuses(t *testing.T) {
	t.Parallel()
	if evidenceGatedStatuses != "('insufficient_evidence', 'no_text_sources')" {
		t.Fatalf("gated statuses = %s", evidenceGatedStatuses)
	}
	if latestUnitGatedSQL != "argMax(categorization_status, computed_at) IN "+evidenceGatedStatuses {
		t.Fatalf("gate predicate = %s, want the latest row's status", latestUnitGatedSQL)
	}
}

// Every statement that serves a mix, a count of the mix or the unit listing
// takes the gate on the latest row and keeps gated units out of what it serves.
func TestInvestmentMixStatementsKeepEvidenceGatedUnitsOutOfTheMix(t *testing.T) {
	t.Parallel()
	bound := factTimeBound{active: true, hasStart: true, start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), end: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	for name, c := range map[string]struct {
		statement string
		want      []string
	}{
		"repository mix": {repoMixStatement([]factTimeBound{bound}), []string{
			"if(gated, 0., v * effort)", "sumIf(bugfix_share * effort, NOT gated)", "uniqExactIf(work_unit_id, NOT gated) AS work_units",
			"groupUniqArrayIf(cityHash64(work_unit_id), win >= 0 AND NOT gated)", "AS uncategorized_unit_hashes",
		}},
		"organization mix": {orgMixStatement(bound), []string{
			"win >= 0 AND repo_uuid != '' AND NOT gated", "AS uncategorized_units", "AS categorized_units",
		}},
		"team linked mix": {linkedPullRequestMixStatement(bound, subqueryMembershipScope, 10), []string{
			"if(m.gated, 0., v * m.effort)", "AS uncategorized_unit_hashes",
		}},
		"repository unit listing":   {investmentUnitsStatement(bound, false), []string{"HAVING argMax(reaches, computed_at) AND NOT gated"}},
		"organization unit listing": {investmentOrgUnitsStatement(bound, false, subqueryMembershipScope), []string{"WHERE win >= 0 AND NOT gated"}},
		"project roll-up themes": {projectRollupRepoThemesStatement(), []string{
			"l.link_kind = 'repo' AND NOT u.gated", "AS uncategorized_units", "HAVING work_units > 0 OR uncategorized_units > 0",
		}},
		"project roll-up bugfix":   {projectRollupRepoBugfixStatement(), []string{"l.link_kind = 'repo' AND NOT u.gated"}},
		"project roll-up evidence": {projectRollupEvidenceArmStatement(), []string{"WHERE repo_id IS NULL AND NOT gated"}},
		"project native values":    {projectNativeThemeValuesStatement(), []string{"groupArray(gated)"}},
	} {
		if strings.Count(c.statement, latestUnitGatedSQL) != 1 {
			t.Errorf("%s: takes the gate predicate %d times, want once", name, strings.Count(c.statement, latestUnitGatedSQL))
		}
		for _, want := range c.want {
			if !strings.Contains(c.statement, want) {
				t.Errorf("%s: statement lacks %q", name, want)
			}
		}
	}
}

// routeClient answers each statement with the rows of the first route whose
// marker it contains, and a project input digest with zeros.
type routeClient struct{ routes []route }

type route struct {
	marker string
	rows   [][]any
}

func (c *routeClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if n := strings.Count(statement, "(SELECT (count(), sum(cityHash64("); n > 0 {
		row := make([]any, 2*n)
		for i := range row {
			row[i] = uint64(0)
		}
		return &reflectScanner{rows: [][]any{row}}, nil
	}
	for _, r := range c.routes {
		if strings.Contains(statement, r.marker) {
			return &reflectScanner{rows: r.rows}, nil
		}
	}
	return &reflectScanner{}, nil
}

// reflectScanner sets each destination from its row value; a column the row
// does not hold stays zero.
type reflectScanner struct {
	rows [][]any
	i    int
}

func (s *reflectScanner) Next() bool {
	if s.i >= len(s.rows) {
		return false
	}
	s.i++
	return true
}

func (s *reflectScanner) Scan(dest ...any) error {
	row := s.rows[s.i-1]
	for index, target := range dest {
		if index >= len(row) {
			continue
		}
		v := reflect.ValueOf(row[index])
		into := reflect.ValueOf(target).Elem()
		if !v.Type().AssignableTo(into.Type()) {
			return fmt.Errorf("column %d: %T into %T", index, row[index], target)
		}
		into.Set(v)
	}
	return nil
}

func (s *reflectScanner) Close() error { return nil }
func (s *reflectScanner) Err() error   { return nil }

var uncategorizedAt = time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)

func uncategorizedRepoMixRow(repo string, themes map[string]float64, units uint64, unitHashes, gatedHashes []uint64) []any {
	return []any{uint8(0), repo, themes, 0.0, units, unitHashes, []uint64{}, uncategorizedAt, gatedHashes}
}

func readInvestmentWith(t *testing.T, client contextpacket.ClickHouseQueryClient, subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
	t.Helper()
	result, err := newInvestmentProvider(client).ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return result
}

func investmentFactOf(result contextfabric.FactProviderResult, subject contextfabric.SubjectRef) *contextfabric.CanonicalFact {
	for i := range result.Facts {
		if contextfabric.FactSubjectKey(result.Facts[i].Subject) == contextfabric.FactSubjectKey(subject) {
			return &result.Facts[i]
		}
	}
	return nil
}

func servedReasons(result contextfabric.FactProviderResult, subject contextfabric.SubjectRef) string {
	return result.Reason + " | " + result.RequestNote + " | " + result.SubjectReasons[contextfabric.FactSubjectKey(subject)]
}

func assertUncategorizedCoverage(t *testing.T, label string, fact *contextfabric.CanonicalFact, categorized, gated int64) {
	t.Helper()
	if fact == nil {
		t.Fatalf("%s: no fact", label)
	}
	if got := fact.Fields["work_unit_count"].Integer; fact.Subject.Kind != contextfabric.SubjectTeam && (got == nil || *got != categorized) {
		t.Errorf("%s: work_unit_count = %v, want %d (the categorized units only)", label, got, categorized)
	}
	if got := fact.Fields["uncategorized_unit_count"].Integer; got == nil || *got != gated {
		t.Errorf("%s: uncategorized_unit_count = %v, want %d", label, got, gated)
	}
	want := float64(gated) / float64(categorized+gated)
	if got := fact.Fields["uncategorized_unit_share"].Number; got == nil || math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s: uncategorized_unit_share = %v, want %v", label, got, want)
	}
}

func TestInvestmentRepositoryMixLeavesGatedUnitsOutAndSaysHowMany(t *testing.T) {
	t.Parallel()
	mixed := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: repositoryPrefix + "r-mixed"}
	gatedOnly := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: repositoryPrefix + "r-gated"}
	plain := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: repositoryPrefix + "r-plain"}
	client := &routeClient{routes: []route{{marker: "AS sized_unit_hashes", rows: [][]any{
		uncategorizedRepoMixRow("r-gated", map[string]float64{"feature_delivery": 0, "risk": 0}, 0, []uint64{}, []uint64{5, 6}),
		uncategorizedRepoMixRow("r-mixed", map[string]float64{"feature_delivery": 1.5, "maintenance": 0.5}, 2, []uint64{1, 2}, []uint64{3, 4}),
		uncategorizedRepoMixRow("r-plain", map[string]float64{"quality": 1}, 1, []uint64{7}, []uint64{}),
	}}}}
	result := readInvestmentWith(t, client, mixed, gatedOnly, plain)
	fact := investmentFactOf(result, mixed)
	assertUncategorizedCoverage(t, "mixed", fact, 2, 2)
	if got := *fact.Fields["theme_feature_delivery"].Number; math.Abs(got-0.75) > 1e-9 {
		t.Errorf("theme_feature_delivery = %v, want 0.75 over the two categorized units", got)
	}
	if got := servedReasons(result, mixed); !strings.Contains(got, investmentUnitsUncategorized) || !strings.Contains(got, "2 of 4 work units") {
		t.Errorf("mixed repository reasons = %q, want the uncategorized count 2 of 4", got)
	}
	assertUncategorizedCoverage(t, "plain", investmentFactOf(result, plain), 1, 0)
	if got := result.SubjectReasons[contextfabric.FactSubjectKey(plain)]; strings.Contains(got, investmentUnitsUncategorized) {
		t.Errorf("a repository with no gated unit carries %q", got)
	}
	if f := investmentFactOf(result, gatedOnly); f != nil {
		t.Fatalf("an all-gated repository serves a fact: %#v", f.Fields)
	}
	if got := servedReasons(result, gatedOnly); !strings.Contains(got, "2 of 2 work units") || !strings.Contains(got, "uncategorized_unit_share is 1 and no theme mix is served") {
		t.Errorf("all-gated repository reasons = %q, want 2 of 2 and no theme mix", got)
	}
}

func TestInvestmentTeamWhoseUnitsAreAllGatedServesNoMixAndSaysWhy(t *testing.T) {
	t.Parallel()
	mixed, gatedOnly := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: teamPrefix + "T1"}, contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: teamPrefix + "T2"}
	client := &routeClient{routes: []route{
		{marker: "SELECT toString(max(computed_at)), count() FROM work_unit_investments", rows: [][]any{{"2026-09-18 00:00:00.000", uint64(4)}}},
		{marker: "AS sized_unit_hashes", rows: [][]any{
			uncategorizedRepoMixRow("r1", map[string]float64{"feature_delivery": 2}, 2, []uint64{1, 2}, []uint64{3}),
			uncategorizedRepoMixRow("r2", map[string]float64{"feature_delivery": 0}, 0, []uint64{}, []uint64{8, 9}),
		}},
		{marker: "FROM team_repo_ownership", rows: [][]any{{"T1", "r1"}, {"T2", "r2"}}},
	}}
	result := readInvestmentWith(t, client, mixed, gatedOnly)
	assertUncategorizedCoverage(t, "T1", investmentFactOf(result, mixed), 2, 1)
	if f := investmentFactOf(result, gatedOnly); f != nil {
		t.Fatalf("an all-gated team serves a fact: %#v", f.Fields)
	}
	if !strings.Contains(result.RequestNote, investmentUnitsUncategorized+" for 1") {
		t.Errorf("request note = %q, want the uncategorized bucket for 1 team", result.RequestNote)
	}
	if strings.Contains(result.RequestNote, investmentTeamNoUnitInWindow) || strings.Contains(result.RequestNote, investmentTeamUnitsUnattrib) {
		t.Errorf("request note = %q puts the all-gated team in another bucket", result.RequestNote)
	}
	if got := result.SubjectReasons[contextfabric.FactSubjectKey(gatedOnly)]; !strings.Contains(got, "2 of 2 work units") || !strings.Contains(got, "uncategorized_unit_share is 1 and no theme mix is served") {
		t.Errorf("all-gated team reason = %q", got)
	}
}

func TestInvestmentOrganizationMixLeavesGatedUnitsOutAndSaysHowMany(t *testing.T) {
	t.Parallel()
	org := contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: organizationPrefix + "org-1"}
	client := &routeClient{routes: []route{{marker: "AS unattributed_effort", rows: [][]any{
		{map[string]float64{"feature_delivery": 1, "risk": 1}, 0.0, uint64(2), uint64(0), uint64(1), 2.0, 0.0, uncategorizedAt, uint64(2), uint64(2)},
	}}}}
	result := readInvestmentWith(t, client, org)
	assertUncategorizedCoverage(t, "organization", investmentFactOf(result, org), 2, 2)
	if got := servedReasons(result, org); !strings.Contains(got, "2 of 4 work units") {
		t.Errorf("organization reasons = %q", got)
	}

	client = &routeClient{routes: []route{{marker: "AS unattributed_effort", rows: [][]any{
		{map[string]float64{}, 0.0, uint64(0), uint64(0), uint64(0), 0.0, 0.0, uncategorizedAt, uint64(3), uint64(0)},
	}}}}
	result = readInvestmentWith(t, client, org)
	if f := investmentFactOf(result, org); f != nil {
		t.Fatalf("an all-gated organization serves a fact: %#v", f.Fields)
	}
	if got := servedReasons(result, org); !strings.Contains(got, "3 of 3 work units") || !strings.Contains(got, "uncategorized_unit_share is 1 and no theme mix is served") {
		t.Errorf("all-gated organization reasons = %q", got)
	}
}

func uncategorizedProjectSubject(t *testing.T, id string) contextfabric.SubjectRef {
	t.Helper()
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{"linear", id}, nil)
	if err != nil || omitted {
		t.Fatalf("project id: %v %v", err, omitted)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID}
}

func TestInvestmentProjectMixesLeaveGatedUnitsOutAndSayHowMany(t *testing.T) {
	t.Parallel()
	rollup, gatedOnly, native := uncategorizedProjectSubject(t, "p1"), uncategorizedProjectSubject(t, "p2"), uncategorizedProjectSubject(t, "p3")
	client := &routeClient{routes: []route{
		{marker: "groupArrayIf(work_unit_id, in_window)", rows: [][]any{{[]string{"u1", "u2", "u3", "u4", "u5", "u6", "u7", "u8"}, []int64{1, 1, 1, 1, 1, 1, 1, 1}, uncategorizedAt}}},
		{marker: "groupArray(link_kind)", rows: [][]any{{[]string{"linear"}, []string{"p1"}, []string{"repo"}, []string{"r:R"}, [][]string{{"T"}}}}},
		{marker: "AS first_unit_from", rows: [][]any{
			{"linear:p1", 1.5, 0.0, 0.5, 0.0, 0.0, uint64(2), uint64(1), uint64(1), uncategorizedAt, uint64(0), uint64(2)},
			{"linear:p2", 0.0, 0.0, 0.0, 0.0, 0.0, uint64(0), uint64(0), uint64(0), uncategorizedAt, uint64(0), uint64(3)},
		}},
		{marker: "AS bugfix_weighted", rows: [][]any{{"linear:p1", 0.0}}},
		{marker: "groupArray(multi_placed)", rows: [][]any{{[]string{"u5", "u6", "u7", "u8"}, []string{"linear", "linear", "linear", "linear"}, []string{"p2", "p2", "p3", "p3"}, []uint8{0, 0, 0, 0}}}},
		{marker: "groupArray(effort_value)", rows: [][]any{{
			[]string{"u5", "u6", "u7", "u8"}, []float64{1, 1, 1, 1},
			[]float64{0.2, 0.2, 1, 0.2}, []float64{0.2, 0.2, 0, 0.2}, []float64{0.2, 0.2, 0, 0.2}, []float64{0.2, 0.2, 0, 0.2}, []float64{0.2, 0.2, 0, 0.2},
			[]time.Time{uncategorizedAt, uncategorizedAt, uncategorizedAt, uncategorizedAt}, []uint8{0, 0, 0, 0}, []uint8{1, 1, 0, 1},
		}}},
		{marker: "groupArray(bugfix_share)", rows: [][]any{{[]string{"u5", "u6", "u7", "u8"}, []float64{0, 0, 0, 0}}}},
	}}
	result := readInvestmentWith(t, client, rollup, gatedOnly, native)
	assertUncategorizedCoverage(t, "roll-up", investmentFactOf(result, rollup), 2, 2)
	if got := servedReasons(result, rollup); !strings.Contains(got, "2 of 4 work units") {
		t.Errorf("roll-up project reasons = %q", got)
	}
	nativeFact := investmentFactOf(result, native)
	assertUncategorizedCoverage(t, "native", nativeFact, 1, 1)
	if got := *nativeFact.Fields["theme_feature_delivery"].Number; math.Abs(got-1) > 1e-9 {
		t.Errorf("native theme_feature_delivery = %v, want 1 from the one categorized unit, never the 0.2 prior", got)
	}
	if f := investmentFactOf(result, gatedOnly); f != nil {
		t.Fatalf("an all-gated project serves a fact: %#v", f.Fields)
	}
	if got := servedReasons(result, gatedOnly); !strings.Contains(got, "3 of 3 work units") || !strings.Contains(got, "uncategorized_unit_share is 1 and no theme mix is served") {
		t.Errorf("all-gated project reasons = %q", got)
	}
}

func TestInvestmentDeclaresTheUncategorizedCoverageFields(t *testing.T) {
	t.Parallel()
	byName := map[string][]contextfabric.FactFieldDeclaration{}
	for _, field := range newInvestmentProvider(nil).Capability().Fields {
		byName[field.Name] = append(byName[field.Name], field)
	}
	for name, want := range map[string]struct {
		typ  contextfabric.FactFieldType
		unit string
	}{
		"uncategorized_unit_count": {contextfabric.FactFieldInteger, "count"},
		"uncategorized_unit_share": {contextfabric.FactFieldNumber, "ratio"},
	} {
		decls := byName[name]
		if len(decls) == 0 {
			t.Errorf("%s is not declared", name)
			continue
		}
		var kinds []string
		for _, d := range decls {
			if d.Type != want.typ || d.Unit != want.unit || !d.Nullable {
				t.Errorf("%s declared %+v, want %s %s nullable", name, d, want.typ, want.unit)
			}
			for _, k := range d.SubjectKinds {
				kinds = append(kinds, string(k))
			}
		}
		sort.Strings(kinds)
		if got := strings.Join(kinds, ","); got != "organization,project,repository,team" {
			t.Errorf("%s declared for %s, want every investment subject kind", name, got)
		}
	}
}

func TestOrganizationStatementCountsEveryUnitOfTheWindowByTheGate(t *testing.T) {
	t.Parallel()
	statement := orgMixStatement(factTimeBound{})
	for _, want := range []string{
		"uniqExactIf(work_unit_id, win >= 0 AND gated) AS uncategorized_units",
		"uniqExactIf(work_unit_id, win >= 0 AND NOT gated) AS categorized_units",
	} {
		if !strings.Contains(statement, want) {
			t.Errorf("the organization statement must split every unit of the window by the gate, including one with no repository reference; want %q in:\n%s", want, statement)
		}
	}
}

// An organization whose only in-window unit is gated and carries no repository reference (repo_uuid ”): the unit is
// left out of the mix and counted, so the answer is no fact and the share-1 reason, never silence.
func TestInvestmentOrganizationWhoseOnlyUnitIsGatedAndHasNoReferenceSaysShareIsOne(t *testing.T) {
	t.Parallel()
	org := contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: organizationPrefix + "org-1"}
	client := &routeClient{routes: []route{{marker: "AS unattributed_effort", rows: [][]any{
		{map[string]float64{}, 0.0, uint64(0), uint64(0), uint64(0), 0.0, 0.0, uncategorizedAt, uint64(1), uint64(0)},
	}}}}
	result := readInvestmentWith(t, client, org)
	if f := investmentFactOf(result, org); f != nil {
		t.Fatalf("an organization of one gated no-reference unit serves a fact: %#v", f.Fields)
	}
	got := servedReasons(result, org)
	if !strings.Contains(got, "1 of 1 work units") || !strings.Contains(got, "uncategorized_unit_share is 1 and no theme mix is served") {
		t.Errorf("reasons = %q, want 1 of 1 and the share-1 reason", got)
	}
}

// Categorized and gated units that reach no repository: the denominator counts both, so the share is gated / all units
// of the window and cannot pass 1 (here 1 of 3).
func TestInvestmentOrganizationWithUnreferencedCategorizedAndGatedUnitsKeepsTheShareUnderOne(t *testing.T) {
	t.Parallel()
	org := contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: organizationPrefix + "org-1"}
	client := &routeClient{routes: []route{{marker: "AS unattributed_effort", rows: [][]any{
		{map[string]float64{}, 0.0, uint64(0), uint64(0), uint64(0), 0.0, 2.0, uncategorizedAt, uint64(1), uint64(2)},
	}}}}
	result := readInvestmentWith(t, client, org)
	fact := investmentFactOf(result, org)
	if fact == nil {
		t.Fatal("the unattributed effort is disclosed as a fact")
	}
	if got := *fact.Fields["uncategorized_unit_count"].Integer; got != 1 {
		t.Errorf("uncategorized_unit_count = %d, want 1", got)
	}
	share := *fact.Fields["uncategorized_unit_share"].Number
	if share > 1 || math.Abs(share-1.0/3.0) > 1e-9 {
		t.Errorf("uncategorized_unit_share = %v, want 1/3 and never above 1", share)
	}
	if got := servedReasons(result, org); !strings.Contains(got, "1 of 3 work units") {
		t.Errorf("reasons = %q, want 1 of 3", got)
	}
}

// The organization listing filters on gated in a WHERE above the select that names its columns: a column the select
// does not carry is an unknown identifier on a real ClickHouse (code 47), which no fake row can show.
func TestOrganizationUnitListingSelectsTheGateColumnItFiltersOn(t *testing.T) {
	t.Parallel()
	statement := investmentOrgUnitsStatement(factTimeBound{}, false, subqueryMembershipScope)
	if !strings.Contains(statement, "from_ts, to_ts, prs, gated\n") {
		t.Errorf("the organization unit listing must select gated beside the columns it filters on:\n%s", statement)
	}
	if !strings.Contains(statement, "WHERE win >= 0 AND NOT gated") {
		t.Errorf("the organization unit listing must leave gated units out:\n%s", statement)
	}
}
