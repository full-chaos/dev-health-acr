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

func nativeMixRow(project string, effortUnits uint64) []any {
	return []any{"linear:" + project, 6.0, 4.0, 0.0, 0.0, 0.0, 1.0, uint64(9), effortUnits, uint64(2), uint64(3)}
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
	client := &fakeClient{tables: []fakeTable{{match: "unit_span AS", rows: [][]any{nativeMixRow("a", 7)}}}}
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
	if got, want := [4]int64{factInt(t, fact, "work_unit_count"), factInt(t, fact, "effort_unit_count"), factInt(t, fact, "spanning_unit_count"), factInt(t, fact, "native_ambiguous_unit_count")}, [4]int64{9, 7, 2, 3}; got != want {
		t.Errorf("populations = %v, want %v (work, effort, spanning, ambiguous)", got, want)
	}
	if result.Truncated {
		t.Errorf("Truncated = true for one row")
	}
}

func TestProjectNativeThemeMixIgnoresARowWithNoEffortOrNoWeight(t *testing.T) {
	t.Parallel()
	noEffort := []any{"linear:a", 6.0, 4.0, 0.0, 0.0, 0.0, 1.0, uint64(9), uint64(0), uint64(2), uint64(0)}
	noWeight := []any{"linear:b", 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, uint64(3), uint64(3), uint64(0), uint64(0)}
	client := &fakeClient{tables: []fakeTable{{match: "unit_span AS", rows: [][]any{noEffort, noWeight}}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "unit_span AS", rows: nil}}}
	if _, err := readNativeMix(t, client, "a", "b"); err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	var native *capturedQuery
	for i := range client.queries {
		if strings.Contains(client.queries[i].statement, "unit_span AS") {
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
	rows := make([][]any, 0, limit+1)
	projects := make([]string, 0, limit+1)
	for i := 0; i <= limit; i++ {
		project := fmt.Sprintf("p%03d", i)
		projects = append(projects, project)
		rows = append(rows, nativeMixRow(project, 1))
	}
	client := &fakeClient{tables: []fakeTable{{match: "unit_span AS", rows: rows}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "unit_span AS", err: errors.New("boom")}}}
	if _, err := readNativeMix(t, client, "a"); err == nil || !strings.Contains(err.Error(), "query project native theme mix") {
		t.Fatalf("err = %v, want the native read named", err)
	}
}

// A project with no native weight and excluded ambiguous evidence serves a
// fact carrying only the excluded count; one with neither serves nothing.
func TestProjectNativeThemeMixDisclosesAnExclusionWithoutAMix(t *testing.T) {
	t.Parallel()
	onlyAmbiguous := []any{"linear:a", 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, uint64(0), uint64(0), uint64(0), uint64(2)}
	client := &fakeClient{tables: []fakeTable{{match: "unit_span AS", rows: [][]any{onlyAmbiguous}}}}
	result, err := readNativeMix(t, client, "a")
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want one fact carrying the excluded count", result.Facts)
	}
	fact := result.Facts[0]
	if got := factInt(t, fact, "native_ambiguous_unit_count"); got != 2 {
		t.Errorf("native_ambiguous_unit_count = %d, want 2", got)
	}
	for _, field := range []string{"investment_mix_source", "theme_feature_delivery"} {
		if _, has := fact.Fields[field]; has {
			t.Errorf("field %q present on a fact with no native mix", field)
		}
	}
}
