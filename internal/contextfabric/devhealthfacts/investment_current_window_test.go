package devhealthfacts_test

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12142. On the current axis the investment read used to ignore the
// answer's evidence window and read all history. It now resolves the current
// axis as every window-bound rollup does (the metrics rule): the committed
// window when the answer carries one, all history for the all_time window, else
// the platform default of the trailing 90 days (DW0). The window bounds the
// work units by end stamp; everything else stays the current axis: currently
// owned ownership, no prior-window read, no historical disclosure.

var (
	committedWindowStart = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	committedWindowEnd   = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
)

// currentWindowSubjects is one subject of each kind the investment read
// serves, each with the rows its read needs to reach its work-unit statements.
func currentWindowSubjects() []struct {
	subject contextfabric.SubjectRef
	tables  []fakeTable
} {
	themes := map[string]float64{"feature_delivery": 1}
	owned := []fakeTable{
		{match: "FROM team_repo_ownership", rows: [][]any{{"T", "repo-A"}}},
		{match: "FROM work_unit_investments", rows: [][]any{themeMixRow("A", "", themes, 0), {uint8(255), "repo-A", themes, 0.0, uint64(1), []uint64{1}, []uint64{}, committedWindowStart}}},
	}
	return []struct {
		subject contextfabric.SubjectRef
		tables  []fakeTable
	}{
		{teamSubject("T"), owned},
		{repoSubject("repo-A"), owned},
		{projectSubject("linear", "p1"), nil},
		{organizationSubject("org-1"), nil},
	}
}

type currentWindowRead struct {
	queries []capturedQuery
	result  contextfabric.FactProviderResult
}

func readInvestmentWith(t *testing.T, subject contextfabric.SubjectRef, tables []fakeTable, query contextfabric.FactQuery) currentWindowRead {
	t.Helper()
	client := &fakeClient{tables: tables}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	query.Kind = contextfabric.FactInvestment
	query.Subjects = []contextfabric.SubjectRef{subject}
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, query)
	if err != nil {
		t.Fatalf("%s: ReadFacts: %v", subject.CanonicalID, err)
	}
	return currentWindowRead{queries: client.queries, result: result}
}

func currentAxisQuery(window *contractsv1.ContextFabricRequestedEvidenceWindow) contextfabric.FactQuery {
	return contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: window}}
}

func exclusiveRangeQuery(start, end time.Time) contextfabric.FactQuery {
	return contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, TimeEndExclusive: true}
}

func committedTrailing30d() *contractsv1.ContextFabricRequestedEvidenceWindow {
	start, end := committedWindowStart, committedWindowEnd
	return &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &end, RelativeID: contractsv1.ContextFabricRelativeWindowTrailing30D}
}

func statementsAndBindings(queries []capturedQuery) string {
	var b strings.Builder
	for _, q := range queries {
		fmt.Fprintf(&b, "%s\n", q.statement)
		for _, binding := range q.bindings {
			fmt.Fprintf(&b, "  %s=%v\n", binding.Name, binding.Value)
		}
	}
	return b.String()
}

var (
	unitWindowEnd   = regexp.MustCompile(`to_ts < \{time_end(_w[0-9]+)?:DateTime64\(6,'UTC'\)\}`)
	unitWindowStart = regexp.MustCompile(`to_ts >= \{time_start(_w[0-9]+)?:DateTime64\(6,'UTC'\)\}`)
)

// windowedOwnership is the historical ownership rule (ownershipValidityPredicate
// and readers.OwnershipValidityPredicate on an active bound).
const windowedOwnership = "(valid_to IS NULL OR valid_to > {"

// workUnitStatements counts the statements that read work_unit_investments,
// and how many of them carry a window's end-stamp bound.
func workUnitStatements(queries []capturedQuery) (total, bounded int) {
	for _, q := range queries {
		if !strings.Contains(q.statement, "work_unit_investments") {
			continue
		}
		total++
		if unitWindowEnd.MatchString(q.statement) && unitWindowStart.MatchString(q.statement) {
			bounded++
		}
	}
	return total, bounded
}

// assertUnitWindowOnly pins a current-axis windowed read: every work-unit
// statement bounded by [start, end) and every window binding equal to it (so
// no prior window is read), ownership on the currently-owned rule, the same
// statement count and facts as the all-history read (the fake rows ignore the
// predicates), and no historical disclosure.
func assertUnitWindowOnly(t *testing.T, name string, read, allHistory currentWindowRead, start, end time.Time) {
	t.Helper()
	if total, bounded := workUnitStatements(read.queries); total == 0 || bounded != total {
		t.Errorf("%s: %d of %d work_unit_investments statements carry the window's end-stamp bound; want all", name, bounded, total)
	}
	windowBindings := 0
	for _, q := range read.queries {
		if strings.Contains(q.statement, windowedOwnership) {
			t.Errorf("%s: a statement reads ownership with the historical rule:\n%s", name, q.statement)
		}
		for _, binding := range q.bindings {
			switch {
			case strings.HasPrefix(binding.Name, "time_start"):
				windowBindings++
				if binding.Value != any(start) {
					t.Errorf("%s: binding %s = %v, want the window start %v", name, binding.Name, binding.Value, start)
				}
			case strings.HasPrefix(binding.Name, "time_end"):
				windowBindings++
				if binding.Value != any(end) {
					t.Errorf("%s: binding %s = %v, want the window end %v", name, binding.Name, binding.Value, end)
				}
			}
		}
	}
	if windowBindings == 0 {
		t.Errorf("%s: no statement binds the window", name)
	}
	if len(read.queries) != len(allHistory.queries) {
		t.Errorf("%s: %d statements, the all-history read issues %d; a current-axis window adds no read", name, len(read.queries), len(allHistory.queries))
	}
	if !reflect.DeepEqual(read.result.Facts, allHistory.result.Facts) || read.result.State != allHistory.result.State {
		t.Errorf("%s: facts or state differ from the all-history read over the same rows\nwindow:      %s %+v\nall history: %s %+v", name, read.result.State, read.result.Facts, allHistory.result.State, allHistory.result.Facts)
	}
	for _, historical := range []string{"investment_ownership_as_synced", "no rows were retained at or before the requested time", "project reach on a historical read"} {
		if strings.Contains(read.result.Reason, historical) || strings.Contains(read.result.RequestNote, historical) {
			t.Errorf("%s: a current-axis read carries the historical disclosure %q: reason %q, note %q", name, historical, read.result.Reason, read.result.RequestNote)
		}
	}
}

func allTimeWindow() *contractsv1.ContextFabricRequestedEvidenceWindow {
	return &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowAllTime}
}

func TestACommittedCurrentAxisWindowBoundsTheInvestmentWorkUnits(t *testing.T) {
	t.Parallel()
	for _, tc := range currentWindowSubjects() {
		read := readInvestmentWith(t, tc.subject, tc.tables, currentAxisQuery(committedTrailing30d()))
		allHistory := readInvestmentWith(t, tc.subject, tc.tables, currentAxisQuery(allTimeWindow()))
		assertUnitWindowOnly(t, tc.subject.CanonicalID, read, allHistory, committedWindowStart, committedWindowEnd)
		if strings.Contains(read.result.RequestNote, "investment_default_window") {
			t.Errorf("%s: a committed window carries the default-window note: %q", tc.subject.CanonicalID, read.result.RequestNote)
		}
	}
}

// Not parallel: it sets the package clock.
func TestWithNoCommittedWindowInvestmentReadsTheDefault90Days(t *testing.T) {
	now := time.Date(2026, 10, 30, 15, 4, 5, 0, time.UTC)
	restore := devhealthfacts.SetClockForTest(func() time.Time { return now })
	defer restore()
	end := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -90)
	const note = "investment_default_window: no period was given, so investment is read over the platform default window, the trailing 90 days ending at 00:00 UTC today, by work-unit end stamp"
	for _, tc := range currentWindowSubjects() {
		allHistory := readInvestmentWith(t, tc.subject, tc.tables, currentAxisQuery(allTimeWindow()))
		for name, window := range map[string]*contractsv1.ContextFabricRequestedEvidenceWindow{
			"no window":  nil,
			"start only": {Start: &committedWindowStart},
			"end only":   {End: &committedWindowEnd},
		} {
			label := tc.subject.CanonicalID + ", " + name
			read := readInvestmentWith(t, tc.subject, tc.tables, currentAxisQuery(window))
			assertUnitWindowOnly(t, label, read, allHistory, start, end)
			if !strings.Contains(read.result.RequestNote, note) {
				t.Errorf("%s: RequestNote = %q, want the default-window note %q", label, read.result.RequestNote, note)
			}
		}
	}
}

func TestTheAllTimeWindowReadsInvestmentOverAllHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range currentWindowSubjects() {
		read := readInvestmentWith(t, tc.subject, tc.tables, currentAxisQuery(allTimeWindow()))
		total, bounded := workUnitStatements(read.queries)
		if total == 0 || bounded != 0 {
			t.Errorf("%s: %d of %d work_unit_investments statements carry a window bound; want none for all_time", tc.subject.CanonicalID, bounded, total)
		}
		for _, q := range read.queries {
			for _, binding := range q.bindings {
				if strings.HasPrefix(binding.Name, "time_start") || strings.HasPrefix(binding.Name, "time_end") {
					t.Errorf("%s: all_time read binds %s=%v", tc.subject.CanonicalID, binding.Name, binding.Value)
				}
			}
		}
		if strings.Contains(read.result.RequestNote, "investment_default_window") {
			t.Errorf("%s: the all_time read carries the default-window note: %q", tc.subject.CanonicalID, read.result.RequestNote)
		}
	}
}

// The historical axes keep their own bound and rules: a range read still reads
// ownership with the historical rule, and an as-of read ignores a window (the
// contract allows a window only on the current axis).
func TestAHistoricalInvestmentReadKeepsItsOwnBound(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range currentWindowSubjects() {
		plain := readInvestmentWith(t, tc.subject, tc.tables, contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}})
		windowed := readInvestmentWith(t, tc.subject, tc.tables, contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf, EvidenceWindow: committedTrailing30d()}})
		if got, want := statementsAndBindings(windowed.queries), statementsAndBindings(plain.queries); got != want {
			t.Errorf("%s: the as-of read with a window differs from the plain as-of read\ngot:\n%s\nplain:\n%s", tc.subject.CanonicalID, got, want)
		}
	}
	ranged := readInvestmentWith(t, teamSubject("T"), currentWindowSubjects()[0].tables, exclusiveRangeQuery(committedWindowStart, committedWindowEnd))
	historical := false
	for _, q := range ranged.queries {
		historical = historical || strings.Contains(q.statement, windowedOwnership)
	}
	if !historical {
		t.Errorf("a team range read no longer reads ownership with the historical rule:\n%s", statementsAndBindings(ranged.queries))
	}
}

// windowStatementRecorder records every statement and answers through inner.
type windowStatementRecorder struct {
	inner      contextpacket.ClickHouseQueryClient
	mu         sync.Mutex
	statements []string
}

func (c *windowStatementRecorder) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.mu.Lock()
	c.statements = append(c.statements, statement)
	c.mu.Unlock()
	return c.inner.Query(ctx, statement, bindings)
}

// The universal fake answers every statement, so a team read reaches its
// project-link ownership statements and a project read serves its theme-mix
// fact. On a current-axis window both keep the current axis: no statement
// reads ownership with the historical rule, and population_window stays
// current. The range read of the same subject is the control that shows both
// checks can see the historical form. Not parallel: it sets the package clock.
func TestACurrentAxisWindowKeepsCurrentOwnershipAndPopulationWindow(t *testing.T) {
	restore := devhealthfacts.SetClockForTest(func() time.Time { return time.Date(2026, 10, 30, 15, 4, 5, 0, time.UTC) })
	defer restore()
	read := func(kind contextfabric.SubjectKind, q contextfabric.FactQuery) (historicalOwnership int, populationWindows []string) {
		t.Helper()
		canonical, row := clockTestSubjectID(kind)
		client := &windowStatementRecorder{inner: &universalClient{subjectID: row, rows: 1, rules: clockTestRules()}}
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
		q.Kind, q.Subjects = contextfabric.FactInvestment, []contextfabric.SubjectRef{{Kind: kind, CanonicalID: canonical, Label: "subject-1"}}
		result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-x", RepositoryScopes: []string{"*"}}, q)
		if err != nil {
			t.Fatalf("%s: ReadFacts: %v", kind, err)
		}
		if len(result.Facts) == 0 {
			t.Fatalf("%s: no fact, so this cell measures nothing (reason %q)", kind, result.Reason)
		}
		for _, statement := range client.statements {
			if strings.Contains(statement, windowedOwnership) {
				historicalOwnership++
			}
		}
		for _, fact := range result.Facts {
			if v, ok := fact.Fields["population_window"]; ok && v.String != nil {
				populationWindows = append(populationWindows, *v.String)
			}
		}
		return historicalOwnership, populationWindows
	}
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectTeam, contextfabric.SubjectProject} {
		if historical, _ := read(kind, exclusiveRangeQuery(committedWindowStart, committedWindowEnd)); historical == 0 {
			t.Fatalf("%s: control: the range read issues no historical ownership statement, so the check below cannot fail", kind)
		}
		for name, q := range map[string]contextfabric.FactQuery{"committed window": currentAxisQuery(committedTrailing30d()), "no window": currentAxisQuery(nil)} {
			historical, windows := read(kind, q)
			if historical != 0 {
				t.Errorf("%s, %s: %d statements read ownership with the historical rule; want the currently-owned rule", kind, name, historical)
			}
			if kind == contextfabric.SubjectProject && (len(windows) == 0 || windows[0] != "current") {
				t.Errorf("%s, %s: population_window = %v, want [current]", kind, name, windows)
			}
		}
	}
	if _, windows := read(contextfabric.SubjectProject, exclusiveRangeQuery(committedWindowStart, committedWindowEnd)); len(windows) == 0 || windows[0] != "requested_range" {
		t.Errorf("control: the project range read's population_window = %v, want [requested_range]", windows)
	}

	// A team that owns no repository takes the projects-only ownership check
	// (team_projects_only.go), which keeps the current rule too.
	ownsNothing := func(q contextfabric.FactQuery) int {
		t.Helper()
		read := readInvestmentWith(t, teamSubject("N"), nil, q)
		historical := 0
		for _, captured := range read.queries {
			if strings.Contains(captured.statement, windowedOwnership) {
				historical++
			}
		}
		return historical
	}
	if ownsNothing(exclusiveRangeQuery(committedWindowStart, committedWindowEnd)) == 0 {
		t.Fatalf("control: the range read of a team that owns nothing issues no historical ownership statement")
	}
	for name, q := range map[string]contextfabric.FactQuery{"committed window": currentAxisQuery(committedTrailing30d()), "no window": currentAxisQuery(nil)} {
		if historical := ownsNothing(q); historical != 0 {
			t.Errorf("team owning nothing, %s: %d statements read ownership with the historical rule", name, historical)
		}
	}
}

// investmentAllHistory is the current axis over all history: the all_time
// window. A test that pins a mix over fixed seed dates reads with it, so the
// 90-day default (CHAOS-12142), which moves with the real clock, does not move
// the population the test pins.
func investmentAllHistory() contextfabric.TimeContext {
	return contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: allTimeWindow()}
}
