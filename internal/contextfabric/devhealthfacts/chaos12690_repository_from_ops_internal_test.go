package devhealthfacts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12690 I1a: with InvestmentRepositoryFromOps the repository mix is the
// ops answer; off, nothing changes.

type statementCountingClient struct{ queries atomic.Int64 }

func (c *statementCountingClient) Query(_ context.Context, _ string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.queries.Add(1)
	return &reflectScanner{}, nil
}

// withUnitCounts puts evidenceQualityStats on an ops answer.
func withUnitCounts(outcome OperationOutcome, total, uncategorized int64, sized *int64) OperationOutcome {
	var answer map[string]any
	_ = json.Unmarshal(outcome.Data, &answer)
	stats := map[string]any{"total": total, "uncategorizedCount": uncategorized, "__typename": "EvidenceQualityStats"}
	if sized != nil {
		stats["sizedUnitCount"] = *sized
	}
	answer["analytics"].(map[string]any)["evidenceQualityStats"] = stats
	outcome.Data, _ = json.Marshal(answer)
	return outcome
}

func opsMixCaller(sized *int64) *breakdownCaller {
	c := servingCaller()
	c.theme = withUnitCounts(c.theme, 5, 1, sized)
	c.subcategory = withUnitCounts(c.subcategory, 5, 1, sized)
	return c
}

func readRepositoryFromOps(t *testing.T, enabled bool, caller OperationCaller, subjects ...contextfabric.SubjectRef) (contextfabric.FactProviderResult, *statementCountingClient) {
	t.Helper()
	holder := NewOperationHolder()
	holder.Set(caller)
	client := &statementCountingClient{}
	provider := newInvestmentProvider(client)
	provider.operations = holder
	provider.repositoryFromOps = enabled
	if len(subjects) == 0 {
		subjects = []contextfabric.SubjectRef{sunburstRepo}
	}
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: sunburstWindow()},
		Kind: contextfabric.FactInvestment, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return result, client
}

func TestCHAOS12690RepositoryMixIsTheOpsAnswer(t *testing.T) {
	t.Parallel()
	sized := int64(3)
	caller := opsMixCaller(&sized)
	result, client := readRepositoryFromOps(t, true, caller)
	if client.queries.Load() != 0 {
		t.Fatalf("the mix read ClickHouse %d times; it is the ops answer", client.queries.Load())
	}
	if len(caller.calls) != 2 || caller.calls[0].operation != "investmentBreakdownRepository" {
		t.Fatalf("calls %+v", caller.calls)
	}
	fact := investmentFactOf(result, sunburstRepo)
	if fact == nil {
		t.Fatalf("no fact; reasons %q", servedReasons(result, sunburstRepo))
	}
	const total = 30.0
	for theme, want := range map[string]float64{"feature_delivery": 19.75 / total, "maintenance": 3 / total, "operational": 4.75 / total, "quality": 2.5 / total, "risk": 0} {
		got := fact.Fields[contextfabric.FactFieldTheme(theme)].Number
		if got == nil || *got != roundMixEffort(want) {
			t.Errorf("theme %s = %v, want %v", theme, got, roundMixEffort(want))
		}
	}
	if got := fact.Fields[contextfabric.FactFieldThemeQualityBugfix].Number; got == nil || *got != roundMixEffort(2.5/total) {
		t.Errorf("bugfix = %v", got)
	}
	// 5 units in the window, 1 under the evidence gate: 4 categorized, 3 sized.
	if got := fact.Fields["work_unit_count"].Integer; got == nil || *got != 4 {
		t.Errorf("work_unit_count = %v, want 4", got)
	}
	if got := fact.Fields["uncategorized_unit_count"].Integer; got == nil || *got != 1 {
		t.Errorf("uncategorized_unit_count = %v, want 1", got)
	}
	if got := fact.Fields["sizing_coverage"].Number; got == nil || *got != 0.75 {
		t.Errorf("sizing_coverage = %v, want 0.75", got)
	}
	for field, want := range map[string]string{"mix_source": repoOpsSource, "attribution_basis": repoOpsBasis} {
		if got := fact.Fields[field].String; got == nil || *got != want {
			t.Errorf("%s = %v, want %s", field, got, want)
		}
	}
	if reasons := servedReasons(result, sunburstRepo); strings.Contains(reasons, opsMixWithheldPrefix) {
		t.Errorf("a full answer carries a withheld reason: %q", reasons)
	}
}

func TestCHAOS12690SwitchOffMakesNoOpsCall(t *testing.T) {
	t.Parallel()
	caller := opsMixCaller(nil)
	readRepositoryFromOps(t, false, caller)
	if len(caller.calls) != 0 {
		t.Fatalf("switch off made %d ops calls", len(caller.calls))
	}
}

func TestCHAOS12690SizingCoverageNotServedIsNamedNotZero(t *testing.T) {
	t.Parallel()
	result, _ := readRepositoryFromOps(t, true, opsMixCaller(nil))
	fact := investmentFactOf(result, sunburstRepo)
	if fact == nil {
		t.Fatal("no fact")
	}
	for _, field := range []string{"sizing_coverage", "unsized_unit_count"} {
		if _, ok := fact.Fields[field]; ok {
			t.Errorf("%s is served although ops did not serve the sized unit count", field)
		}
	}
	if reasons := servedReasons(result, sunburstRepo); !strings.Contains(reasons, opsMixWithheldPrefix+": "+RepoOpsWithheldNoSizing) {
		t.Errorf("reasons %q lack the sizing reason", reasons)
	}
}

func TestCHAOS12690AnImplausibleSizedCountIsNotServed(t *testing.T) {
	t.Parallel()
	sized := int64(9) // more than the 4 categorized units
	result, _ := readRepositoryFromOps(t, true, opsMixCaller(&sized))
	fact := investmentFactOf(result, sunburstRepo)
	if fact == nil {
		t.Fatal("no fact")
	}
	if _, ok := fact.Fields["sizing_coverage"]; ok {
		t.Error("a sized count above the categorized units was served")
	}
}

func TestCHAOS12690RefusedOrUnreadableCallsServeNoFact(t *testing.T) {
	t.Parallel()
	missingStats := servingCaller()
	for name, caller := range map[string]*breakdownCaller{
		"not_served":    {theme: OperationOutcome{Reason: "refused:operation_not_served_for_caller"}, subcategory: OperationOutcome{}},
		"incomplete":    {theme: OperationOutcome{Served: true}, subcategory: OperationOutcome{Served: true}},
		"call_error":    {err: fmt.Errorf("down")},
		"missing_stats": missingStats,
	} {
		t.Run(name, func(t *testing.T) {
			result, _ := readRepositoryFromOps(t, true, caller)
			if fact := investmentFactOf(result, sunburstRepo); fact != nil {
				t.Fatalf("a fact is served: %+v", fact.Fields)
			}
			if reasons := servedReasons(result, sunburstRepo); !strings.Contains(reasons, opsMixWithheldPrefix) {
				t.Fatalf("no named reason: %q", reasons)
			}
		})
	}
}

func TestCHAOS12690ReadCapNamesTheRepositoriesPastIt(t *testing.T) {
	t.Parallel()
	sized := int64(3)
	caller := opsMixCaller(&sized)
	subjects := make([]contextfabric.SubjectRef, 0, repositoryOpsReadCap+1)
	for i := range repositoryOpsReadCap + 1 {
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: fmt.Sprintf("%s8d0f8f5c-0b55-4f43-9f7e-3b0f6f0d1a%02d", repositoryPrefix, i)})
	}
	result, _ := readRepositoryFromOps(t, true, caller, subjects...)
	if len(caller.calls) != 2*repositoryOpsReadCap {
		t.Fatalf("%d calls, want %d", len(caller.calls), 2*repositoryOpsReadCap)
	}
	if reasons := servedReasons(result, subjects[repositoryOpsReadCap]); !strings.Contains(reasons, RepoOpsWithheldCap) {
		t.Fatalf("the repository past the cap is not named: %q", reasons)
	}
}

func TestCHAOS12690VersionJoinsTheReuseKeyOnlyWhenOn(t *testing.T) {
	t.Parallel()
	if QueryVersionFor(false, false) != QueryVersion {
		t.Fatal("switch off must keep the version")
	}
	if QueryVersionFor(true, false) == QueryVersion || !strings.HasPrefix(QueryVersionFor(true, false), QueryVersion) || QueryVersionFor(false, true) == QueryVersionFor(true, false) || QueryVersionFor(true, true) == QueryVersionFor(true, false) {
		t.Fatalf("switch on version %q", QueryVersionFor(true, false))
	}
	on, off := newInvestmentProvider(nil), newInvestmentProvider(nil)
	on.repositoryFromOps = true
	if on.Capability().Version == off.Capability().Version {
		t.Fatal("the capability version does not move with the switch")
	}
}

func TestCHAOS12690DisagreeingThemeAndSubcategoryAnswersServeNoFact(t *testing.T) {
	t.Parallel()
	sized := int64(3)
	for name, mutate := range map[string]func(*breakdownCaller){
		"subcategory_above_its_theme": func(c *breakdownCaller) {
			sub := sunburstSubcategories()
			sub["quality.bugfix"] = 100
			c.subcategory = withUnitCounts(breakdownAnswer("SUBCATEGORY", sub), 5, 1, &sized)
		},
		"unknown_positive_subcategory": func(c *breakdownCaller) {
			sub := sunburstSubcategories()
			sub["quality.invented"] = 1
			c.subcategory = withUnitCounts(breakdownAnswer("SUBCATEGORY", sub), 5, 1, &sized)
		},
	} {
		t.Run(name, func(t *testing.T) {
			caller := opsMixCaller(&sized)
			mutate(caller)
			result, _ := readRepositoryFromOps(t, true, caller)
			if fact := investmentFactOf(result, sunburstRepo); fact != nil {
				t.Fatalf("a fact is served over disagreeing answers: bugfix=%v", fact.Fields[contextfabric.FactFieldThemeQualityBugfix].Number)
			}
			if reasons := servedReasons(result, sunburstRepo); !strings.Contains(reasons, opsMixWithheldPrefix) {
				t.Fatalf("no named reason: %q", reasons)
			}
		})
	}
}

func TestCHAOS12690DisagreeingUnitCountsServeNoFact(t *testing.T) {
	t.Parallel()
	sized := int64(3)
	otherSized := int64(2)
	for name, sub := range map[string]OperationOutcome{
		"total":         withUnitCounts(breakdownAnswer("SUBCATEGORY", sunburstSubcategories()), 101, 0, &sized),
		"uncategorized": withUnitCounts(breakdownAnswer("SUBCATEGORY", sunburstSubcategories()), 5, 2, &sized),
		"sized":         withUnitCounts(breakdownAnswer("SUBCATEGORY", sunburstSubcategories()), 5, 1, &otherSized),
		"sized_absent":  withUnitCounts(breakdownAnswer("SUBCATEGORY", sunburstSubcategories()), 5, 1, nil),
	} {
		t.Run(name, func(t *testing.T) {
			caller := opsMixCaller(&sized)
			caller.subcategory = sub
			result, _ := readRepositoryFromOps(t, true, caller)
			if fact := investmentFactOf(result, sunburstRepo); fact != nil {
				t.Fatalf("a fact is served over disagreeing counts: %+v", fact.Fields["work_unit_count"])
			}
			if reasons := servedReasons(result, sunburstRepo); !strings.Contains(reasons, RepoOpsWithheldCountsDisagree) {
				t.Fatalf("no named reason: %q", reasons)
			}
		})
	}
}

// CHAOS-13059: the investment mix SQL reads every range as [start, end) (the
// work unit predicate is to_ts < end whether or not the bound says the end is
// exclusive, mixWindowPredicate), so a read_facts range window, whose end is
// inclusive, is the same period as a trailing window with that end. The ops
// call must not refuse it.
func TestCHAOS13059AWholeDayInclusiveRangeIsSentAsTheSameEndExclusiveDays(t *testing.T) {
	t.Parallel()
	start, end := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	startDate, endDate, ok := subcategoryBreakdownDates(factTimeBound{active: true, hasStart: true, start: start, end: end})
	if !ok || startDate != "2026-10-01" || endDate != "2026-10-31" {
		t.Fatalf("inclusive whole-day range: %q %q %v, want 2026-10-01..2026-10-31", startDate, endDate, ok)
	}
	exclusiveStart, exclusiveEnd, _ := subcategoryBreakdownDates(factTimeBound{active: true, hasStart: true, start: start, end: end, endExclusive: true})
	if exclusiveStart != startDate || exclusiveEnd != endDate {
		t.Fatalf("inclusive and exclusive forms of one range differ: %s..%s vs %s..%s", startDate, endDate, exclusiveStart, exclusiveEnd)
	}
	for name, bound := range map[string]factTimeBound{
		"end inside a day":   {active: true, hasStart: true, start: start, end: end.Add(6 * time.Hour)},
		"start inside a day": {active: true, hasStart: true, start: start.Add(time.Hour), end: end},
		"no start":           {active: true, end: end},
	} {
		if _, _, ok := subcategoryBreakdownDates(bound); ok {
			t.Errorf("%s: sent to ops", name)
		}
	}
}

func TestCHAOS13059ARangeReadServesTheRepositoryFactFromOps(t *testing.T) {
	t.Parallel()
	sized := int64(3)
	caller := opsMixCaller(&sized)
	start, end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	holder := NewOperationHolder()
	holder.Set(caller)
	provider := newInvestmentProvider(&statementCountingClient{})
	provider.operations = holder
	provider.repositoryFromOps = true
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{sunburstRepo},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fact := investmentFactOf(result, sunburstRepo); fact == nil {
		t.Fatalf("a range read served no fact: %q", servedReasons(result, sunburstRepo))
	}
	if len(caller.calls) != 2 || !strings.Contains(caller.calls[0].variables, `"startDate":"2026-09-01","endDate":"2026-09-30"`) {
		t.Fatalf("calls %+v", caller.calls)
	}
}
