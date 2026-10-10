package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// nativePhasedUnit is one work unit of the phased native read (CHAOS-7271): its
// placement in a project, optionally also in an unrequested one (spanning), its
// multi-placed flag and its values.
type nativePhasedUnit struct {
	id, project        string
	other              bool
	multi              uint8
	effort, fd, op, bg float64
	from               time.Time
}

// nativeMixUnitsFor builds the units of one project whose phased read
// aggregates to: work units, effort units (effort 1 each; the theme totals fd and
// op and the bugfix share sit on the first), spanning units and multi-placed units.
func nativeMixUnitsFor(project string, work, effortN, spanning, multi int, fd, op, bugfix float64) []nativePhasedUnit {
	units := make([]nativePhasedUnit, 0, work)
	for i := 0; i < work; i++ {
		u := nativePhasedUnit{id: fmt.Sprintf("%s-u%d", project, i), project: project}
		if i < effortN {
			u.effort = 1
		}
		if i == 0 && effortN > 0 {
			u.fd, u.op, u.bg = fd, op, bugfix
		}
		u.other = i < spanning
		if i < multi {
			u.multi = 1
		}
		units = append(units, u)
	}
	return units
}

// nativeMixUnits is the default project: 9 work units, effortUnits of them with
// effort, feature_delivery 6, operational 4, bugfix 1, 2 spanning, 3 multi-placed.
func nativeMixUnits(project string, effortUnits int) []nativePhasedUnit {
	return nativeMixUnitsFor(project, 9, effortUnits, 2, 3, 6, 4, 1)
}

// nativePhasedTables answers the four statements of the phased native read.
func nativePhasedTables(groups ...[]nativePhasedUnit) []fakeTable {
	var ids, pUnit, pProvider, pProject []string
	var pMulti []uint8
	var effort, fd, op, zero, bugfix []float64
	var versions []int64
	var froms []time.Time
	for _, units := range groups {
		for _, u := range units {
			ids = append(ids, u.id)
			versions = append(versions, 1)
			if u.from.IsZero() {
				froms = append(froms, clockSpanStart)
			} else {
				froms = append(froms, u.from)
			}
			effort, fd, op, zero, bugfix = append(effort, u.effort), append(fd, u.fd), append(op, u.op), append(zero, 0.0), append(bugfix, u.bg)
			pUnit, pProvider, pProject, pMulti = append(pUnit, u.id), append(pProvider, "linear"), append(pProject, u.project), append(pMulti, u.multi)
			if u.other {
				pUnit, pProvider, pProject, pMulti = append(pUnit, u.id), append(pProvider, "linear"), append(pProject, "zz-unrequested"), append(pMulti, 0)
			}
		}
	}
	return []fakeTable{
		{match: "AS unit_ids", rows: [][]any{{ids, versions, clockSpanStart}}},
		{match: "groupArray(multi_placed)", rows: [][]any{{pUnit, pProvider, pProject, pMulti}}},
		{match: "groupArray(theme_feature_delivery)", rows: [][]any{{ids, effort, fd, op, zero, zero, zero, froms}}},
		{match: "groupArray(bugfix_share)", rows: [][]any{{ids, bugfix}}},
	}
}

func readNativeMix(t *testing.T, client *fakeClient, projects ...string) (contextfabric.FactProviderResult, error) {
	t.Helper()
	subjects := make([]contextfabric.SubjectRef, len(projects))
	for i, project := range projects {
		subjects[i] = projectSubject("linear", project)
	}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	return provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: subjects,
	})
}

func TestProjectNativeThemeMixFromScannedRow(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: nativePhasedTables(nativeMixUnits("a", 7))}
	result, err := readNativeMix(t, client, "a")
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly one", result.Facts)
	}
	fact := result.Facts[0]
	if got := factString(t, fact, "investment_mix_source"); got != "project_native" {
		t.Fatalf("investment_mix_source = %q", got)
	}
	if got := factNumber(t, fact, "theme_feature_delivery"); got != 0.6 {
		t.Errorf("theme_feature_delivery = %v, want 0.6", got)
	}
	if got := factNumber(t, fact, "theme_quality_bugfix"); got != 0.1 {
		t.Errorf("theme_quality_bugfix = %v, want 0.1", got)
	}
	if got, want := [4]int64{factInt(t, fact, "work_unit_count"), factInt(t, fact, "effort_unit_count"), factInt(t, fact, "spanning_unit_count"), factInt(t, fact, "native_multi_placed_unit_count")}, [4]int64{9, 7, 2, 3}; got != want {
		t.Errorf("populations = %v, want %v (work, effort, spanning, multi-placed)", got, want)
	}
	if result.Truncated {
		t.Errorf("Truncated = true for one row")
	}
}

func TestProjectNativeThemeMixIgnoresARowWithNoEffortOrNoWeight(t *testing.T) {
	t.Parallel()
	noEffort := nativeMixUnitsFor("a", 9, 0, 2, 0, 6, 4, 1)
	noWeight := nativeMixUnitsFor("b", 3, 3, 0, 0, 0, 0, 0)
	client := &fakeClient{tables: nativePhasedTables(noEffort, noWeight)}
	result, err := readNativeMix(t, client, "a", "b")
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want none: no effort units, or no weight, is not a mix", result.Facts)
	}
}

func TestProjectNativeThemeMixIsBoundToTheOrganizationAndRequestedProjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: nativePhasedTables(nativeMixUnits("a", 1))}
	if _, err := readNativeMix(t, client, "a", "b"); err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	// Every phase of the native read carries the organisation and the requested
	// projects; the placement is the statement that selects them.
	var native *capturedQuery
	for i := range client.queries {
		if strings.Contains(client.queries[i].statement, "groupArray(project_provider)") {
			native = &client.queries[i]
		}
	}
	if native == nil {
		t.Fatal("no native theme mix statement was issued")
	}
	var org string
	var ids []string
	for _, binding := range native.bindings {
		switch binding.Name {
		case "org_id":
			org, _ = binding.Value.(string)
		case "ids":
			ids, _ = binding.Value.([]string)
		}
	}
	if org != "org-1" || len(ids) != 2 || ids[0] != "linear:a" || ids[1] != "linear:b" {
		t.Fatalf("org_id = %q ids = %v", org, ids)
	}
}

func TestProjectNativeThemeMixProbeRowIsEvidenceOfTruncationNeverServed(t *testing.T) {
	t.Parallel()
	const limit = 200
	var groups [][]nativePhasedUnit
	projects := make([]string, 0, limit+1)
	for i := 0; i <= limit; i++ {
		project := fmt.Sprintf("p%03d", i)
		projects = append(projects, project)
		groups = append(groups, nativeMixUnits(project, 1))
	}
	client := &fakeClient{tables: nativePhasedTables(groups...)}
	result, err := readNativeMix(t, client, projects...)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if !result.Truncated {
		t.Fatalf("Truncated = false with %d rows read", limit+1)
	}
	if len(result.Facts) != limit {
		t.Fatalf("served %d facts, want the %d cap", len(result.Facts), limit)
	}
}

func TestProjectNativeThemeMixReadFailureIsReported(t *testing.T) {
	t.Parallel()
	tables := nativePhasedTables(nativeMixUnits("a", 1))
	tables[1] = fakeTable{match: "groupArray(multi_placed)", err: errors.New("boom")} // the placement phase fails
	client := &fakeClient{tables: tables}
	if _, err := readNativeMix(t, client, "a"); err == nil || !strings.Contains(err.Error(), "query project native theme mix") {
		t.Fatalf("err = %v, want the native read named", err)
	}
}

// A project with no native weight and multi-placed units serves a fact
// carrying only the multi-placed count; one with neither serves nothing.
func TestProjectNativeThemeMixDisclosesMultiPlacedUnitsWithoutAMix(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: nativePhasedTables(nativeMixUnitsFor("a", 3, 0, 0, 2, 0, 0, 0))}
	result, err := readNativeMix(t, client, "a")
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want one fact carrying the multi-placed count", result.Facts)
	}
	fact := result.Facts[0]
	if got := factInt(t, fact, "native_multi_placed_unit_count"); got != 2 {
		t.Errorf("native_multi_placed_unit_count = %d, want 2", got)
	}
	for _, field := range []string{"investment_mix_source", "theme_feature_delivery"} {
		if _, has := fact.Fields[field]; has {
			t.Errorf("field %q present on a fact with no native mix", field)
		}
	}
}

func TestProjectInvestmentWindowBeforeTheStoredSpanNamesIt(t *testing.T) {
	t.Parallel()
	read := func(start time.Time) contextfabric.FactProviderResult {
		t.Helper()
		client := &fakeClient{tables: nativePhasedTables(nativeMixUnits("a", 7))}
		end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
		result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{projectSubject("linear", "a")},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("read: err=%v facts=%d reason=%q", err, len(result.Facts), result.Reason)
		}
		return result
	}
	// The stored span starts 2026-01-01.
	if reason := read(time.Date(2025, 9, 28, 0, 0, 0, 0, time.UTC)).Reason; !strings.Contains(reason, "investment_window_beyond_stored_history") || !strings.Contains(reason, "2026-01-01T00:00:00Z") || !strings.Contains(reason, "earliest persisted work unit of the organization starts") || strings.Contains(reason, "own span is not derived") || !strings.Contains(reason, "investment_project_window_first_unit") {
		t.Errorf("window before the span: reason %q", reason)
	}
	if reason := read(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)).Reason; strings.Contains(reason, "investment_window_beyond_stored_history") {
		t.Errorf("window inside the span carries the limitation: %q", reason)
	}
}

func TestProjectInvestmentSpanIsStatedWhenNoUnitOverlapsTheWindow(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: nativePhasedTables()}
	start, end := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{projectSubject("linear", "a")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 0 || !strings.Contains(result.Reason, "investment_window_beyond_stored_history") {
		t.Errorf("facts=%d reason=%q, want no fact and the organization span reason", len(result.Facts), result.Reason)
	}
}

// A project's own earliest linked unit among the units overlapping the window:
// when the window starts before it, the reason names that start and says the
// history before the window was not read; it is never a stored-history claim.
func TestProjectInvestmentNamesTheProjectsOwnEarliestLinkedUnitInTheWindow(t *testing.T) {
	t.Parallel()
	read := func(start time.Time, units []nativePhasedUnit) string {
		t.Helper()
		client := &fakeClient{tables: nativePhasedTables(units)}
		end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
		result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{projectSubject("linear", "a")},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("read: err=%v facts=%d reason=%q", err, len(result.Facts), result.Reason)
		}
		return result.Reason
	}
	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	units := nativeMixUnits("a", 7)
	for i := range units {
		units[i].from = march.Add(time.Duration(i) * 24 * time.Hour)
	}
	// The organization's earliest unit starts 2026-01-01 (before the window), the
	// project's own earliest linked unit 2026-03-01 (after it).
	reason := read(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), units)
	if !strings.Contains(reason, "investment_project_window_first_unit") || !strings.Contains(reason, "2026-03-01T00:00:00Z") || !strings.Contains(reason, "history before the window was not read") {
		t.Errorf("reason %q, want the project's own first linked unit 2026-03-01 and the not-read statement", reason)
	}
	if strings.Contains(reason, "investment_window_beyond_stored_history") || strings.Contains(reason, "stored history starts") {
		t.Errorf("reason %q makes a stored-history claim for a window inside the organization's span", reason)
	}
	// A window that starts after the project's earliest unit says nothing about it.
	if reason := read(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), units); strings.Contains(reason, "investment_project_window_first_unit") {
		t.Errorf("window after the project's first unit carries its own-span reason: %q", reason)
	}
}

// A unit whose start is the epoch default (no real start) never becomes the
// project's earliest; a window starting exactly at the earliest unit's start is
// not before it.
func TestProjectInvestmentOwnEarliestSkipsAnEpochStartAndIsStrictlyBefore(t *testing.T) {
	t.Parallel()
	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	read := func(start time.Time) string {
		t.Helper()
		units := nativeMixUnits("a", 7)
		for i := range units {
			units[i].from = march.Add(time.Duration(i) * 24 * time.Hour)
		}
		units[0].from = time.Unix(0, 0).UTC()
		client := &fakeClient{tables: nativePhasedTables(units)}
		end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
		result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{projectSubject("linear", "a")},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("read: err=%v facts=%d reason=%q", err, len(result.Facts), result.Reason)
		}
		return result.Reason
	}
	if reason := read(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)); !strings.Contains(reason, "investment_project_window_first_unit") || !strings.Contains(reason, "starts 2026-03-02T00:00:00Z") {
		t.Errorf("reason %q, want the earliest real start 2026-03-02 (unit 0 has an epoch start and is skipped)", reason)
	}
	if reason := read(time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)); strings.Contains(reason, "investment_project_window_first_unit") {
		t.Errorf("a window starting exactly at the earliest unit's start carries the own-span reason: %q", reason)
	}
}

// flipInputsClient makes the first read attempt see its inputs change (the
// closing digests differ from the baseline) and serves that attempt's unit
// values from an older start; the second attempt is stable and serves March.
type flipInputsClient struct {
	stale, stable *fakeClient
	digests       int
	values        int
}

func (c *flipInputsClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "sum(cityHash64(") {
		value := []uint64{1, 2, 2, 2}[c.digests%4]
		c.digests++
		row := make([]any, 64)
		for i := range row {
			row[i] = value
		}
		return &fakeScanner{rows: [][]any{row}}, nil
	}
	if strings.Contains(statement, "groupArray(theme_feature_delivery)") {
		c.values++
		if c.values == 1 {
			return c.stale.Query(ctx, statement, bindings)
		}
	}
	return c.stable.Query(ctx, statement, bindings)
}

// A span read on an attempt whose inputs then changed is discarded: only the
// attempt that is served may name the project's earliest unit.
func TestProjectInvestmentOwnEarliestComesOnlyFromTheServedAttempt(t *testing.T) {
	t.Parallel()
	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	mk := func(from time.Time) *fakeClient {
		units := nativeMixUnits("a", 7)
		for i := range units {
			units[i].from = from
		}
		return &fakeClient{tables: nativePhasedTables(units)}
	}
	client := &flipInputsClient{stale: mk(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)), stable: mk(march)}
	start, end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{projectSubject("linear", "a")},
	})
	if err != nil || len(result.Facts) != 1 {
		t.Fatalf("read: err=%v facts=%d reason=%q", err, len(result.Facts), result.Reason)
	}
	if client.values != 2 {
		t.Fatalf("unit values were read %d times, want 2 (one changed attempt, one served)", client.values)
	}
	if !strings.Contains(result.Reason, "investment_project_window_first_unit") || !strings.Contains(result.Reason, "starts 2026-03-01T00:00:00Z") {
		t.Errorf("reason %q, want the served attempt's earliest unit 2026-03-01", result.Reason)
	}
}
