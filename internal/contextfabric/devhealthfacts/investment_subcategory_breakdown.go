package devhealthfacts

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The investment sunburst's table (CHAOS-12554). With the sunburst switch on
// (ProviderOptions.InvestmentSunburstEnabled), a team or repository investment
// fact carries subcategory_breakdown, a declared detail table: a claim gets it
// only by naming it. ACR writes no SQL for it (lead ruling, CHAOS-12690): both
// rings come from the ops operations investmentBreakdownTeam and
// investmentBreakdownRepository, through the operation path source_health reads
// (OperationHolder.CallOperation), held to the same run_operation policy.
const (
	// InvestmentBreakdownTeamOperationName and
	// InvestmentBreakdownRepositoryOperationName are the registered ops
	// operations of one team's and one repository's investment breakdown.
	InvestmentBreakdownTeamOperationName       = "investmentBreakdownTeam"
	InvestmentBreakdownRepositoryOperationName = "investmentBreakdownRepository"

	subcategoryBreakdownField = contractsv1.ContextFabricRenderSubcategoryTableField
	// subcategoryBreakdownMeasure is the measure web's sunburst reads, and the
	// one the mix facts carry (D6027).
	subcategoryBreakdownMeasure = "ISSUE_SIZE"
	// subcategoryBreakdownTopN is the policy's largest topN. A breakdown has
	// at most 15 keys, so no answer is cut.
	subcategoryBreakdownTopN = 100
	// SubcategoryThemeCrossCheckTolerance is the relative tolerance of the
	// THEME call's cross-check against the sum of its subcategories (lead
	// ruling, CHAOS-12554): the O4 oracle's Float32 bound. ops groups both
	// dimensions from the same subcategory rows, so the two differ only by
	// the order a float64 sum runs in.
	SubcategoryThemeCrossCheckTolerance = 1e-6
)

// The closed reasons a team or repository investment fact carries no
// subcategory_breakdown table. Each one is served as a coverage reason and
// written on the withheld line (InvestmentSubcategoryBreakdownWithheld). A
// missing breakdown is never served as zeros.
const (
	SubcategoryWithheldCallerUnset     = "caller_unset"
	SubcategoryWithheldCallFailed      = "call_failed"
	SubcategoryWithheldNotServed       = "not_served"
	SubcategoryWithheldIncomplete      = "incomplete"
	SubcategoryWithheldUnreadable      = "unreadable"
	SubcategoryWithheldUnknownKey      = "unknown_key"
	SubcategoryWithheldThemeCrossCheck = "theme_cross_check"
	SubcategoryWithheldNoData          = "no_data"
	SubcategoryWithheldWindow          = "window_not_whole_days"
	// SubcategoryWithheldSeveralSubjects: the read holds more than one team or
	// repository mix fact, so no call is made (lead ruling, CHAOS-12554: never a
	// silent drop). It is served as coverage only and not written on the Warn
	// line: a cohort read is no failure.
	SubcategoryWithheldSeveralSubjects = "several_subjects"
)

// SubcategoryWithheldReasonVocabulary is every reason the withheld line
// carries. The coverage reason carries these and
// SubcategoryWithheldSeveralSubjects.
func SubcategoryWithheldReasonVocabulary() []string {
	return []string{
		SubcategoryWithheldCallerUnset, SubcategoryWithheldCallFailed, SubcategoryWithheldNotServed,
		SubcategoryWithheldIncomplete, SubcategoryWithheldUnreadable, SubcategoryWithheldUnknownKey,
		SubcategoryWithheldThemeCrossCheck, SubcategoryWithheldNoData, SubcategoryWithheldWindow,
	}
}

// subcategoryWithheldReasonPrefix starts the coverage reason of a withheld
// table.
const subcategoryWithheldReasonPrefix = "investment_subcategory_breakdown_withheld"

// subcategoryWithheld is why one subject's fact carries no table. theme and
// gap are set for the cross-check only; call is the closed call status of a
// call that was not served.
type subcategoryWithheld struct {
	subject contextfabric.SubjectRef
	reason  string
	call    string
	theme   string
	gap     float64
}

// coverageReason is the served sentence. It names the closed reason, and the
// call status when ops did not serve the call.
func (w subcategoryWithheld) coverageReason() string {
	reason := w.reason
	if w.call != "" {
		reason += ", call " + w.call
	}
	return subcategoryWithheldReasonPrefix + ": no subcategory breakdown is served for this subject (" + reason + "); it is missing, not zero"
}

// closedCallStatus is the call status of an outcome that was not served, kept
// only when it reads as the runner's closed tokens ("status" or
// "status:refusal_code").
var closedCallStatus = regexp.MustCompile(`^[a-z_]{1,64}(:[a-z_]{1,64})?$`)

// subcategoryBreakdownRequest is the variables of one call. The field order is
// the wire order.
type subcategoryBreakdownRequest struct {
	Batch subcategoryBreakdownBatch `json:"batch"`
}

// subcategoryBreakdownBatch sends no useInvestment: the policy forces it true
// (acr_forced), and refuses a client that names a forced path at all, even with
// the forced value.
type subcategoryBreakdownBatch struct {
	Breakdowns []subcategoryBreakdownItem `json:"breakdowns"`
	Filters    subcategoryBreakdownFilter `json:"filters"`
}

type subcategoryBreakdownItem struct {
	Dimension string                       `json:"dimension"`
	Measure   string                       `json:"measure"`
	DateRange subcategoryBreakdownWindowJS `json:"dateRange"`
	TopN      int                          `json:"topN"`
}

type subcategoryBreakdownWindowJS struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

type subcategoryBreakdownFilter struct {
	Scope subcategoryBreakdownScope `json:"scope"`
}

type subcategoryBreakdownScope struct {
	Level string   `json:"level"`
	IDs   []string `json:"ids"`
}

// subcategoryBreakdownVariables are the variables of one call: one breakdown
// of dimension under ISSUE_SIZE over the window, and the scope of the one
// subject. The policy forces the investment source on (batch.useInvestment).
// The id is the subject's canonical id: the policy's subject gate decides it
// and converts it to the ops form (the team id, or the repository uuid).
func subcategoryBreakdownVariables(dimension, level, canonicalID, startDate, endDate string) json.RawMessage {
	raw, _ := json.Marshal(subcategoryBreakdownRequest{Batch: subcategoryBreakdownBatch{
		Breakdowns: []subcategoryBreakdownItem{{
			Dimension: dimension, Measure: subcategoryBreakdownMeasure,
			DateRange: subcategoryBreakdownWindowJS{StartDate: startDate, EndDate: endDate}, TopN: subcategoryBreakdownTopN,
		}},
		Filters: subcategoryBreakdownFilter{Scope: subcategoryBreakdownScope{Level: level, IDs: []string{canonicalID}}},
	}})
	return raw
}

// subcategoryBreakdownDates is the investment window as the dates ops reads: a
// work unit counts when its end stamp is in [startDate, endDate), the rule the
// mix reads apply (workUnitEndStampPredicate). Only a window of whole UTC days
// with an exclusive end has dates; any other (a bound inside a day, an
// inclusive end, or all history, which has no start) is not sent, so ops never
// reads a different period than the fact states (reason window_not_whole_days).
func subcategoryBreakdownDates(bound factTimeBound) (startDate, endDate string, ok bool) {
	if !bound.active || !bound.hasStart || !bound.endExclusive || !bound.start.Before(bound.end) {
		return "", "", false
	}
	if !wholeUTCDay(bound.start) || !wholeUTCDay(bound.end) {
		return "", "", false
	}
	return bound.start.UTC().Format(time.DateOnly), bound.end.UTC().Format(time.DateOnly), true
}

func wholeUTCDay(t time.Time) bool {
	u := t.UTC()
	return u.Hour() == 0 && u.Minute() == 0 && u.Second() == 0 && u.Nanosecond() == 0
}

// subcategoryBreakdownScopeOf is the operation and scope level of a subject
// kind, or false for a kind the sunburst does not chart (the treemap's allow
// list without the organization: the ops entries serve a team and a
// repository).
func subcategoryBreakdownScopeOf(kind contextfabric.SubjectKind) (operation, level string, ok bool) {
	switch kind {
	case contextfabric.SubjectTeam:
		return InvestmentBreakdownTeamOperationName, "TEAM", true
	case contextfabric.SubjectRepository:
		return InvestmentBreakdownRepositoryOperationName, "REPO", true
	}
	return "", "", false
}

// attachSubcategoryBreakdown gives the read's one team or repository mix fact
// its subcategory_breakdown table, and returns why it could not when it could
// not. The table is read for one breakdown per read: a sunburst plots one
// breakdown, and a second subject would be a comparison the rule refuses. So a
// read of several subjects makes no call, and each of its subjects gets the
// closed reason several_subjects (never a silent drop).
func (p *InvestmentProvider) attachSubcategoryBreakdown(ctx context.Context, principal storage.Principal, orgID string, facts []contextfabric.CanonicalFact, bound factTimeBound) []subcategoryWithheld {
	var targets []int
	for index := range facts {
		fact := &facts[index]
		if fact.Kind != contextfabric.FactInvestment {
			continue
		}
		if _, ok := fact.Fields["theme_breakdown"]; !ok {
			continue
		}
		if _, _, charted := subcategoryBreakdownScopeOf(fact.Subject.Kind); !charted {
			continue
		}
		targets = append(targets, index)
	}
	if len(targets) == 0 {
		return nil
	}
	if len(targets) > 1 {
		several := make([]subcategoryWithheld, 0, len(targets))
		for _, index := range targets {
			several = append(several, subcategoryWithheld{subject: facts[index].Subject, reason: SubcategoryWithheldSeveralSubjects})
		}
		return several
	}
	fact := &facts[targets[0]]
	table, withheld := p.readSubcategoryBreakdown(ctx, principal, fact.Subject, bound)
	if withheld != nil {
		logSubcategoryBreakdownWithheld(ctx, orgID, *withheld)
		return []subcategoryWithheld{*withheld}
	}
	fact.Fields[subcategoryBreakdownField] = table
	return nil
}

// readSubcategoryBreakdown makes the two calls, THEME then SUBCATEGORY, with
// the same variables apart from the dimension (the policy caps a call at one
// breakdown), and builds the table.
func (p *InvestmentProvider) readSubcategoryBreakdown(ctx context.Context, principal storage.Principal, subject contextfabric.SubjectRef, bound factTimeBound) (contextfabric.FactValue, *subcategoryWithheld) {
	withheld := func(reason string) *subcategoryWithheld {
		return &subcategoryWithheld{subject: subject, reason: reason}
	}
	operation, level, _ := subcategoryBreakdownScopeOf(subject.Kind)
	startDate, endDate, ok := subcategoryBreakdownDates(bound)
	if !ok {
		return contextfabric.FactValue{}, withheld(SubcategoryWithheldWindow)
	}
	if !p.operations.IsSet() {
		return contextfabric.FactValue{}, withheld(SubcategoryWithheldCallerUnset)
	}
	items := make(map[string]map[string]float64, 2)
	for _, dimension := range []string{"THEME", "SUBCATEGORY"} {
		outcome, err := p.operations.CallOperation(ctx, principal, operation, subcategoryBreakdownVariables(dimension, level, subject.CanonicalID, startDate, endDate))
		switch {
		case err != nil:
			return contextfabric.FactValue{}, withheld(SubcategoryWithheldCallFailed)
		case !outcome.Served:
			out := withheld(SubcategoryWithheldNotServed)
			out.call = "unknown"
			if closedCallStatus.MatchString(outcome.Reason) {
				out.call = outcome.Reason
			}
			return contextfabric.FactValue{}, out
		case !outcome.Complete:
			return contextfabric.FactValue{}, withheld(SubcategoryWithheldIncomplete)
		}
		values, readable := parseInvestmentBreakdown(outcome.Data, dimension)
		if !readable {
			return contextfabric.FactValue{}, withheld(SubcategoryWithheldUnreadable)
		}
		items[dimension] = values
	}
	table, refusal := subcategoryBreakdownTable(items["THEME"], items["SUBCATEGORY"], bound.effectiveGrain(grainDaily))
	if refusal != nil {
		refusal.subject = subject
		return contextfabric.FactValue{}, refusal
	}
	return table, nil
}

// investmentBreakdownAnswer is the part of an ops analytics answer the table
// reads.
type investmentBreakdownAnswer struct {
	Analytics *struct {
		Breakdowns []struct {
			Dimension string `json:"dimension"`
			Items     []struct {
				Key   string   `json:"key"`
				Value *float64 `json:"value"`
			} `json:"items"`
		} `json:"breakdowns"`
	} `json:"analytics"`
}

// parseInvestmentBreakdown reads the one breakdown of an answer as key ->
// value. An answer with no breakdown, another dimension, a repeated key, or a
// value that is null, negative or not finite is not readable: no value is
// guessed.
func parseInvestmentBreakdown(data json.RawMessage, dimension string) (map[string]float64, bool) {
	var answer investmentBreakdownAnswer
	if err := json.Unmarshal(data, &answer); err != nil || answer.Analytics == nil || len(answer.Analytics.Breakdowns) != 1 {
		return nil, false
	}
	breakdown := answer.Analytics.Breakdowns[0]
	if !strings.EqualFold(breakdown.Dimension, dimension) {
		return nil, false
	}
	values := make(map[string]float64, len(breakdown.Items))
	for _, item := range breakdown.Items {
		if item.Value == nil || math.IsNaN(*item.Value) || math.IsInf(*item.Value, 0) || *item.Value < 0 {
			return nil, false
		}
		if _, repeated := values[item.Key]; repeated {
			return nil, false
		}
		values[item.Key] = *item.Value
	}
	return values, true
}

// subcategoryBreakdownTable builds the table from the two answers (lead ruling,
// CHAOS-12554):
//
//   - A key outside the canonical list with a value above 0, in either answer,
//     is a split the table cannot place: no table.
//   - Both answers with nothing above 0 is no data, never zeros.
//   - Each theme's THEME value must equal the sum of its subcategory values
//     within SubcategoryThemeCrossCheckTolerance, relative. A larger gap: no
//     table.
//   - A canonical subcategory absent from a complete answer is measured 0.
//   - share is the subcategory's value over the sum of every subcategory value;
//     theme_share is the sum of its theme's share cells. Every sum runs in one
//     fixed order, the sorted subcategory key, so amd64 and arm64 give the same
//     bits, and the contract's part-sum check, which adds the ring's cells in
//     that same order, holds exactly. Nothing is rounded.
//
// The rows are the 15 canonical subcategories in vocabulary order.
func subcategoryBreakdownTable(themes, subcategories map[string]float64, grain contextfabric.TemporalGrain) (contextfabric.FactValue, *subcategoryWithheld) {
	vocabulary := contractsv1.ContextFabricInvestmentSubcategoryVocabulary()
	sorted := slices.Sorted(slices.Values(vocabulary[:]))
	for key, value := range subcategories {
		if _, canonical := contractsv1.ContextFabricInvestmentSubcategoryTheme(key); !canonical && value > 0 {
			return contextfabric.FactValue{}, &subcategoryWithheld{reason: SubcategoryWithheldUnknownKey}
		}
	}
	for key, value := range themes {
		if !slices.Contains(canonicalInvestmentThemes[:], key) && value > 0 {
			return contextfabric.FactValue{}, &subcategoryWithheld{reason: SubcategoryWithheldUnknownKey}
		}
	}
	// Sums in the sorted key order.
	themeValue := make(map[string]float64, len(canonicalInvestmentThemes))
	total := 0.0
	for _, key := range sorted {
		theme, _ := contractsv1.ContextFabricInvestmentSubcategoryTheme(key)
		themeValue[theme] += subcategories[key]
		total += subcategories[key]
	}
	themeTotal := 0.0
	for _, theme := range slices.Sorted(slices.Values(canonicalInvestmentThemes[:])) {
		themeTotal += themes[theme]
	}
	if total <= 0 && themeTotal <= 0 {
		return contextfabric.FactValue{}, &subcategoryWithheld{reason: SubcategoryWithheldNoData}
	}
	for _, theme := range slices.Sorted(slices.Values(canonicalInvestmentThemes[:])) {
		if gap := relativeGap(themes[theme], themeValue[theme]); !(gap <= SubcategoryThemeCrossCheckTolerance) {
			return contextfabric.FactValue{}, &subcategoryWithheld{reason: SubcategoryWithheldThemeCrossCheck, theme: theme, gap: gap}
		}
	}
	share := make(map[string]float64, len(sorted))
	themeShare := make(map[string]float64, len(canonicalInvestmentThemes))
	for _, key := range sorted {
		theme, _ := contractsv1.ContextFabricInvestmentSubcategoryTheme(key)
		// A quotient, then a sum: no multiply-add a compiler could fuse.
		share[key] = subcategories[key] / total
		themeShare[theme] += share[key]
	}
	rows := make([]contextfabric.FactValueRow, 0, len(vocabulary))
	for _, key := range vocabulary {
		theme, _ := contractsv1.ContextFabricInvestmentSubcategoryTheme(key)
		rows = append(rows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
			contractsv1.ContextFabricRenderSubcategoryField: contextfabric.StringFactValue(key),
			contractsv1.ContextFabricRenderThemeField:       contextfabric.StringFactValue(theme),
			contractsv1.ContextFabricRenderThemeShareField:  contextfabric.NumberFactValue(share[key]),
			contractsv1.ContextFabricRenderThemeTotalField:  contextfabric.NumberFactValue(themeShare[theme]),
		}})
	}
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          []string{contractsv1.ContextFabricRenderSubcategoryField},
		Measures:     []string{contractsv1.ContextFabricRenderThemeShareField, contractsv1.ContextFabricRenderThemeTotalField},
		Observations: []string{contractsv1.ContextFabricRenderThemeField},
		Grain:        grain,
		Detail:       true,
		Rows:         rows,
	}), nil
}

// relativeGap is |a-b| over the larger magnitude, 0 when both are 0. A NaN
// fails every comparison against the tolerance, so it is refused.
func relativeGap(a, b float64) float64 {
	scale := math.Max(math.Abs(a), math.Abs(b))
	if scale == 0 {
		return 0
	}
	return math.Abs(a-b) / scale
}
