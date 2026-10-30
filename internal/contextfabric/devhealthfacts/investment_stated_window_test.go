package devhealthfacts_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12142. On the current axis the investment read used to ignore the
// answer's evidence window: an answer that said "trailing_30d" carried an
// investment mix over all history. A window the caller stated now bounds the
// read exactly as a range read of the same [start, end) does; an inferred
// window, an all_time window and no window keep the all-history read.

var (
	statedWindowStart = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	statedWindowEnd   = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
)

// statedWindowSubjects is one subject of each kind the investment read
// serves, each with the rows its read needs to reach its work-unit statements.
func statedWindowSubjects() []struct {
	subject contextfabric.SubjectRef
	tables  []fakeTable
} {
	themes := map[string]float64{"feature_delivery": 1}
	owned := []fakeTable{
		{match: "FROM team_repo_ownership", rows: [][]any{{"T", "repo-A"}}},
		{match: "FROM work_unit_investments", rows: [][]any{themeMixRow("A", "", themes, 0), {uint8(255), "repo-A", themes, 0.0, uint64(1), []uint64{1}, []uint64{}, statedWindowStart}}},
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

type statedWindowRead struct {
	queries []capturedQuery
	result  contextfabric.FactProviderResult
}

func readInvestmentWith(t *testing.T, subject contextfabric.SubjectRef, tables []fakeTable, query contextfabric.FactQuery) statedWindowRead {
	t.Helper()
	client := &fakeClient{tables: tables}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	query.Kind = contextfabric.FactInvestment
	query.Subjects = []contextfabric.SubjectRef{subject}
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, query)
	if err != nil {
		t.Fatalf("%s: ReadFacts: %v", subject.CanonicalID, err)
	}
	return statedWindowRead{queries: client.queries, result: result}
}

func currentAxisWindowQuery(window *contractsv1.ContextFabricRequestedEvidenceWindow, stated bool) contextfabric.FactQuery {
	return contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: window}, EvidenceWindowStated: stated}
}

func trailing30dWindow() *contractsv1.ContextFabricRequestedEvidenceWindow {
	start, end := statedWindowStart, statedWindowEnd
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

// workUnitStatements counts the statements that read work_unit_investments,
// and how many of them carry the window's end-stamp bound.
func workUnitStatements(queries []capturedQuery) (total, bounded int) {
	for _, q := range queries {
		if !strings.Contains(q.statement, "work_unit_investments") {
			continue
		}
		total++
		if strings.Contains(q.statement, "to_ts < {time_end:DateTime64(6,'UTC')}") && strings.Contains(q.statement, "to_ts >= {time_start:DateTime64(6,'UTC')}") {
			bounded++
		}
	}
	return total, bounded
}

func TestAStatedCurrentAxisWindowReadsInvestmentAsTheSameRange(t *testing.T) {
	t.Parallel()
	for _, tc := range statedWindowSubjects() {
		start, end := statedWindowStart, statedWindowEnd
		ranged := readInvestmentWith(t, tc.subject, tc.tables, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, TimeEndExclusive: true,
		})
		stated := readInvestmentWith(t, tc.subject, tc.tables, currentAxisWindowQuery(trailing30dWindow(), true))

		total, bounded := workUnitStatements(stated.queries)
		if total == 0 || bounded != total {
			t.Errorf("%s: %d of %d work_unit_investments statements carry the window's end-stamp bound; want all", tc.subject.CanonicalID, bounded, total)
		}
		// The window's own bounds reach a work-unit statement. A team read also
		// reads the prior window of the same width, as a range read does.
		windowBound := false
		for _, q := range stated.queries {
			var gotStart, gotEnd any
			for _, binding := range q.bindings {
				switch binding.Name {
				case "time_start":
					gotStart = binding.Value
				case "time_end":
					gotEnd = binding.Value
				}
			}
			if strings.Contains(q.statement, "work_unit_investments") && gotStart == any(start) && gotEnd == any(end) {
				windowBound = true
			}
		}
		if !windowBound {
			t.Errorf("%s: no work_unit_investments statement is bound to the stated window [%v, %v)", tc.subject.CanonicalID, start, end)
		}
		if got, want := statementsAndBindings(stated.queries), statementsAndBindings(ranged.queries); got != want {
			t.Errorf("%s: the stated current-axis window does not read as the range [start, end) does\nstated:\n%s\nrange:\n%s", tc.subject.CanonicalID, got, want)
		}
		if !reflect.DeepEqual(stated.result, ranged.result) {
			t.Errorf("%s: result differs from the range read\nstated: %+v\nrange:  %+v", tc.subject.CanonicalID, stated.result, ranged.result)
		}
	}
}

func TestAnUnstatedCurrentAxisWindowKeepsTheAllHistoryInvestmentRead(t *testing.T) {
	t.Parallel()
	allTime := &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowAllTime}
	for _, tc := range statedWindowSubjects() {
		none := readInvestmentWith(t, tc.subject, tc.tables, currentAxisWindowQuery(nil, false))
		if _, bounded := workUnitStatements(none.queries); bounded != 0 {
			t.Fatalf("%s: the no-window read carries a window bound in %d statements", tc.subject.CanonicalID, bounded)
		}
		for name, query := range map[string]contextfabric.FactQuery{
			"inferred trailing_30d":  currentAxisWindowQuery(trailing30dWindow(), false),
			"stated all_time":        currentAxisWindowQuery(allTime, true),
			"stated flag, no window": currentAxisWindowQuery(nil, true),
			"stated, start only":     currentAxisWindowQuery(&contractsv1.ContextFabricRequestedEvidenceWindow{Start: &statedWindowStart}, true),
			"stated, end only":       currentAxisWindowQuery(&contractsv1.ContextFabricRequestedEvidenceWindow{End: &statedWindowEnd}, true),
		} {
			read := readInvestmentWith(t, tc.subject, tc.tables, query)
			if got, want := statementsAndBindings(read.queries), statementsAndBindings(none.queries); got != want {
				t.Errorf("%s, %s: the read differs from the no-window read\ngot:\n%s\nno window:\n%s", tc.subject.CanonicalID, name, got, want)
			}
			if !reflect.DeepEqual(read.result, none.result) {
				t.Errorf("%s, %s: result differs from the no-window read\ngot:       %+v\nno window: %+v", tc.subject.CanonicalID, name, read.result, none.result)
			}
		}
	}
}

// A non-current axis keeps its own bound: a stated window flag never replaces
// an as-of instant (the contract allows a window only on the current axis).
func TestAStatedWindowFlagLeavesAnAsOfInvestmentReadAlone(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range statedWindowSubjects() {
		plain := readInvestmentWith(t, tc.subject, tc.tables, contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}})
		flagged := readInvestmentWith(t, tc.subject, tc.tables, contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf, EvidenceWindow: trailing30dWindow()}, EvidenceWindowStated: true})
		if got, want := statementsAndBindings(flagged.queries), statementsAndBindings(plain.queries); got != want {
			t.Errorf("%s: the as-of read with a stated window flag differs from the plain as-of read\ngot:\n%s\nplain:\n%s", tc.subject.CanonicalID, got, want)
		}
	}
}
