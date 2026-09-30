package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
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
	for _, units := range groups {
		for _, u := range units {
			ids = append(ids, u.id)
			effort, fd, op, zero, bugfix = append(effort, u.effort), append(fd, u.fd), append(op, u.op), append(zero, 0.0), append(bugfix, u.bg)
			pUnit, pProvider, pProject, pMulti = append(pUnit, u.id), append(pProvider, "linear"), append(pProject, u.project), append(pMulti, u.multi)
			if u.other {
				pUnit, pProvider, pProject, pMulti = append(pUnit, u.id), append(pProvider, "linear"), append(pProject, "zz-unrequested"), append(pMulti, 0)
			}
		}
	}
	return []fakeTable{
		{match: "AS unit_ids", rows: [][]any{{ids, int64(1)}}},
		{match: "groupArray(project_provider)", rows: [][]any{{pUnit, pProvider, pProject, pMulti}}},
		{match: "groupArray(theme_feature_delivery)", rows: [][]any{{ids, effort, fd, op, zero, zero, zero}}},
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
	tables[1] = fakeTable{match: "groupArray(project_provider)", err: errors.New("boom")} // the placement phase fails
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
