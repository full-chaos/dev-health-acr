package contextfabric

import (
	"fmt"
	"sort"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4415 slice 1: DETERMINISTIC selection of the renderable shapes an
// answer warrants.
//
// This file is the whole of "conditional on intent, never default" (North
// Star check 10). Three rules, each with an explicit firing condition, each
// producing a shape whose every number is COPIED -- never computed -- from
// the cohort or the claimed facts the same result already carries. There is
// no fallback branch and no "looks chartable" heuristic: a question no rule
// fires for gets no shape, which is the common case.
//
// Placement in the engine matters and is deliberate: SelectRenderShapes runs
// AFTER synthesis, after cohort narration, and after the commit-affirmation
// gate, on the FINAL result, immediately before Validate. A model therefore
// cannot author a shape (there is no draft field for one), and a shape can
// never describe content a later composer removed.
//
// Determinism is a checkable property, not a claim: the same
// (interpretation, cohort, claimed facts) always yields the same shapes in
// the same order, because every ordering here is total -- attention rank,
// then a name tiebreak; claim order for facts; sorted signal names for
// series. Nothing reads a map without sorting it first.

// RenderShapeSelection is one selected shape, reduced to closed-vocabulary
// values and counts -- what telemetry may carry. It holds no label, subject
// or number, so a log line can never leak corpus content.
type RenderShapeSelection struct {
	Kind         contractsv1.ContextFabricRenderKind
	Presentation contractsv1.ContextFabricRenderPresentation
	Rule         contractsv1.ContextFabricRenderShapeRule
	SeriesCount  int
	PointCount   int
}

// RenderShapeSelectionEvent is the decision-basis record for THIS
// selection pass. Shape is the interpreted question shape the rules were
// evaluated against, so a reader diagnosing "why did the teams answer get
// no chart" can see whether the question was even read as a cohort. Skipped
// names the rules that were eligible on intent but produced nothing, with
// the closed reason -- an outcome-affecting branch that only ever reaches a
// default silently is exactly the failure mode acr/AGENTS.md's
// diagnosis-in-artifacts rule exists to prevent.
type RenderShapeSelectionEvent struct {
	Shape    contractsv1.ContextFabricInvestigationShape
	Selected []RenderShapeSelection
	Skipped  []RenderShapeSkip
}

// RenderShapeSkip records one rule that did not produce a shape.
type RenderShapeSkip struct {
	Rule   contractsv1.ContextFabricRenderShapeRule
	Reason RenderShapeSkipReason
}

// RenderShapeSkipReason is the CLOSED vocabulary of why an eligible rule
// produced nothing.
type RenderShapeSkipReason string

const (
	// RenderShapeSkipNotCohortIntent -- the question was not read as a
	// cohort question, so no cohort rule may fire even if cohort data
	// happens to be present (check 1: never answer the nearest measurable
	// question).
	RenderShapeSkipNotCohortIntent RenderShapeSkipReason = "not_cohort_intent"
	// RenderShapeSkipNoRankedMember -- a cohort question whose members
	// carry no computed score.
	RenderShapeSkipNoRankedMember RenderShapeSkipReason = "no_ranked_member"
	// RenderShapeSkipNoDrivers -- ranked members carry no driver
	// families, so there is nothing to break the score into.
	RenderShapeSkipNoDrivers RenderShapeSkipReason = "no_drivers"
	// RenderShapeSkipTooManySignals -- more distinct driver families than
	// a stack can carry. The shape is skipped WHOLE rather than
	// truncated: a stacked bar claims its parts sum to the score, and a
	// stack missing segments claims something false.
	RenderShapeSkipTooManySignals RenderShapeSkipReason = "too_many_signals"
	// RenderShapeSkipNoDatedRows -- no claimed fact carries a row table
	// with a usable date axis and a numeric column.
	RenderShapeSkipNoDatedRows RenderShapeSkipReason = "no_dated_rows"
	// RenderShapeSkipShapeBudget -- a rule that would have fired but the
	// per-answer shape cap was already reached.
	RenderShapeSkipShapeBudget RenderShapeSkipReason = "shape_budget"
)

// cohortIntentShapes are the interpreted shapes a cohort chart is an answer
// TO. Carrying cohort data is NOT on its own a reason to draw one: a
// single-subject answer can legitimately carry a ranked cohort as context,
// and charting it would answer a question nobody asked. "open" is excluded
// for the same reason -- an unshaped question has not asked for a ranking.
var cohortIntentShapes = map[contractsv1.ContextFabricInvestigationShape]struct{}{
	contractsv1.ContextFabricShapeExplicitCohort:   {},
	contractsv1.ContextFabricShapeDiscoveredCohort: {},
}

// SelectRenderShapes applies the CHAOS-4415 selection rules to a FINAL
// investigation result and returns the shapes plus the decision record.
//
// Pure: it reads result and returns values, mutating nothing, so the engine
// can record telemetry before deciding to attach. Every returned number is
// a verbatim copy of one already in result, addressed by a
// ContextFabricRenderPointSource that
// ContextFabricInvestigationResult.Validate then resolves and compares.
func SelectRenderShapes(result InvestigationResult) ([]contractsv1.ContextFabricRenderShape, RenderShapeSelectionEvent) {
	event := RenderShapeSelectionEvent{Shape: result.Interpretation.Shape}
	shapes := make([]contractsv1.ContextFabricRenderShape, 0, 3)

	_, cohortIntent := cohortIntentShapes[result.Interpretation.Shape]
	ranked := rankedCohortMembers(result.Cohort)
	switch {
	case !cohortIntent:
		event.skip(contractsv1.ContextFabricRenderRuleCohortAttentionScore, RenderShapeSkipNotCohortIntent)
		event.skip(contractsv1.ContextFabricRenderRuleCohortDriverContribution, RenderShapeSkipNotCohortIntent)
	case len(ranked) == 0:
		event.skip(contractsv1.ContextFabricRenderRuleCohortAttentionScore, RenderShapeSkipNoRankedMember)
		event.skip(contractsv1.ContextFabricRenderRuleCohortDriverContribution, RenderShapeSkipNoRankedMember)
	default:
		labels := disambiguatedMemberLabels(ranked)
		shapes = append(shapes, cohortAttentionScoreShape(result.Cohort.Kind, ranked, labels))
		if contribution, reason := cohortDriverContributionShape(result.Cohort.Kind, ranked, labels); contribution != nil {
			shapes = append(shapes, *contribution)
		} else {
			event.skip(contractsv1.ContextFabricRenderRuleCohortDriverContribution, reason)
		}
	}

	trends := datedFactTrendShapes(result.ClaimedFacts)
	if len(trends) == 0 {
		event.skip(contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipNoDatedRows)
	}
	for _, trend := range trends {
		if len(shapes) >= contractsv1.ContextFabricRenderShapesMaxCount {
			event.skip(contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipShapeBudget)
			break
		}
		shapes = append(shapes, trend)
	}

	if len(shapes) == 0 {
		return nil, event
	}
	for i := range shapes {
		shapes[i].ShapeID = fmt.Sprintf("rs_%d", i+1)
		event.Selected = append(event.Selected, RenderShapeSelection{
			Kind:         shapes[i].Kind,
			Presentation: shapes[i].Presentation,
			Rule:         shapes[i].SelectedBy,
			SeriesCount:  len(shapes[i].Series),
			PointCount:   countPoints(shapes[i]),
		})
	}
	return shapes, event
}

func (e *RenderShapeSelectionEvent) skip(rule contractsv1.ContextFabricRenderShapeRule, reason RenderShapeSkipReason) {
	e.Skipped = append(e.Skipped, RenderShapeSkip{Rule: rule, Reason: reason})
}

func countPoints(shape contractsv1.ContextFabricRenderShape) int {
	total := 0
	for _, series := range shape.Series {
		total += len(series.Points)
	}
	return total
}

// rankedCohortMembers returns the members a chart may plot -- ranking
// actually computed AND a score present -- in AttentionRank order, capped
// at the per-series point bound. An unranked or unscored member is not
// plotted at all rather than plotted as zero: "insufficient evidence" and
// "scored zero" are different states (check 12), and a bar of height zero
// says the second.
func rankedCohortMembers(cohort *Cohort) []CohortMember {
	if cohort == nil {
		return nil
	}
	ranked := make([]CohortMember, 0, len(cohort.Members))
	for _, member := range cohort.Members {
		if member.RankingComputed && member.Score != nil {
			ranked = append(ranked, member)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].AttentionRank != ranked[j].AttentionRank {
			return ranked[i].AttentionRank < ranked[j].AttentionRank
		}
		return ranked[i].Subject.CanonicalID < ranked[j].Subject.CanonicalID
	})
	if len(ranked) > contractsv1.ContextFabricRenderPointsMaxCount {
		ranked = ranked[:contractsv1.ContextFabricRenderPointsMaxCount]
	}
	return ranked
}

// disambiguatedMemberLabels maps each member's canonical id to the axis
// label to plot it under. A point label is an axis POSITION, so two members
// sharing a display label would silently collapse onto one bar; where that
// happens every colliding member is qualified by its canonical id instead.
// The canonical id is already on the wire in the same shape's point source,
// so this discloses nothing new.
func disambiguatedMemberLabels(members []CohortMember) map[string]string {
	counts := make(map[string]int, len(members))
	for _, member := range members {
		counts[member.Subject.Label]++
	}
	labels := make(map[string]string, len(members))
	for _, member := range members {
		label := member.Subject.Label
		if counts[label] > 1 {
			label = fmt.Sprintf("%s (%s)", label, member.Subject.CanonicalID)
		}
		labels[member.Subject.CanonicalID] = clampRenderLabel(label)
	}
	return labels
}

// clampRenderLabel keeps a label inside the contract bound. Labels are
// identity, not prose, so a clamp here is a display concern only -- no
// number and no judgment can be lost to it.
func clampRenderLabel(label string) string {
	if len(label) <= contractsv1.ContextFabricRenderLabelMaxLength {
		return label
	}
	return label[:contractsv1.ContextFabricRenderLabelMaxLength]
}

// cohortAttentionScoreShape is rule 1: the per-member attention score, one
// bar per ranked member, in rank order.
func cohortAttentionScoreShape(kind contractsv1.ContextFabricSubjectKind, ranked []CohortMember, labels map[string]string) contractsv1.ContextFabricRenderShape {
	noun := string(kind)
	points := make([]contractsv1.ContextFabricRenderPoint, 0, len(ranked))
	for _, member := range ranked {
		points = append(points, contractsv1.ContextFabricRenderPoint{
			Label: labels[member.Subject.CanonicalID],
			// Verbatim. rankedCohortMembers already proved Score is
			// non-nil, and Validate re-resolves this against the cohort.
			Value: *member.Score,
			Source: contractsv1.ContextFabricRenderPointSource{
				Kind:               contractsv1.ContextFabricRenderSourceCohortMemberScore,
				SubjectCanonicalID: member.Subject.CanonicalID,
			},
		})
	}
	return contractsv1.ContextFabricRenderShape{
		Kind:         contractsv1.ContextFabricRenderKindSeries,
		Presentation: contractsv1.ContextFabricRenderPresentationBars,
		SelectedBy:   contractsv1.ContextFabricRenderRuleCohortAttentionScore,
		Title:        clampRenderLabel("Attention score by " + noun),
		AxisKind:     contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel:    clampRenderLabel(noun),
		ValueLabel:   "attention score",
		Series: []contractsv1.ContextFabricRenderSeries{{
			Key:    "attention_score",
			Label:  "Attention score",
			Points: points,
		}},
	}
}

// cohortDriverContributionShape is rule 2: what each ranked member's score
// is MADE OF, one stacked segment per driver family. This is check 8 drawn
// -- "scores help prioritize; drivers explain, never a bare score" -- and
// it is why the score bars alone were not enough for chris's 08-29 report.
//
// Returns nil plus the closed skip reason when the rule cannot fire, so the
// caller records WHY rather than a silent absence.
func cohortDriverContributionShape(kind contractsv1.ContextFabricSubjectKind, ranked []CohortMember, labels map[string]string) (*contractsv1.ContextFabricRenderShape, RenderShapeSkipReason) {
	signals := map[string]struct{}{}
	for _, member := range ranked {
		for _, driver := range member.Drivers {
			signals[driver.Signal] = struct{}{}
		}
	}
	if len(signals) == 0 {
		return nil, RenderShapeSkipNoDrivers
	}
	if len(signals) > contractsv1.ContextFabricRenderSeriesMaxCount {
		return nil, RenderShapeSkipTooManySignals
	}
	ordered := make([]string, 0, len(signals))
	for signal := range signals {
		ordered = append(ordered, signal)
	}
	sort.Strings(ordered)

	series := make([]contractsv1.ContextFabricRenderSeries, 0, len(ordered))
	for _, signal := range ordered {
		points := make([]contractsv1.ContextFabricRenderPoint, 0, len(ranked))
		for _, member := range ranked {
			for _, driver := range member.Drivers {
				if driver.Signal != signal {
					continue
				}
				points = append(points, contractsv1.ContextFabricRenderPoint{
					Label: labels[member.Subject.CanonicalID],
					// Verbatim: the points this family actually added to
					// this member's score. A member with no such family
					// gets NO point -- never a zero segment, which would
					// claim the family was measured and contributed
					// nothing.
					Value: driver.WeightContributed,
					Source: contractsv1.ContextFabricRenderPointSource{
						Kind:               contractsv1.ContextFabricRenderSourceCohortDriverWeight,
						SubjectCanonicalID: member.Subject.CanonicalID,
						Signal:             signal,
					},
				})
				break
			}
		}
		if len(points) == 0 {
			continue
		}
		series = append(series, contractsv1.ContextFabricRenderSeries{
			Key:    signal,
			Label:  humanizeRenderTerm(signal),
			Points: points,
		})
	}
	if len(series) == 0 {
		return nil, RenderShapeSkipNoDrivers
	}
	return &contractsv1.ContextFabricRenderShape{
		Kind:         contractsv1.ContextFabricRenderKindSeries,
		Presentation: contractsv1.ContextFabricRenderPresentationStackedBars,
		SelectedBy:   contractsv1.ContextFabricRenderRuleCohortDriverContribution,
		Title:        clampRenderLabel("Score contribution by driver, per " + string(kind)),
		AxisKind:     contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel:    clampRenderLabel(string(kind)),
		ValueLabel:   "points contributed",
		Series:       series,
	}, ""
}

// humanizeRenderTerm turns a closed-vocabulary signal name into a display
// label ("investment_mix" -> "Investment mix"). Cosmetic only: Key carries
// the machine identity, and no consumer should read Label.
func humanizeRenderTerm(term string) string {
	spaced := strings.ReplaceAll(term, "_", " ")
	if spaced == "" {
		return spaced
	}
	return strings.ToUpper(spaced[:1]) + spaced[1:]
}

// datedFactTrendShapes is rule 3: a claimed fact whose row table carries a
// real date axis becomes a trend line.
//
// The rule is intentionally strict, and every clause exists because its
// absence would produce a chart that claims more than the data says:
//
//   - EVERY row must carry the date column, all in the SAME shape (all
//     date-only or all with a time component). A time axis is POSITIONED by
//     elapsed time; a row missing its date would silently degrade the whole
//     chart to index spacing, and mixing a bare date with a zoned timestamp
//     makes one elapsed-time scale ill-defined.
//   - The dates must be DISTINCT. A repeated date is two values at one axis
//     position, which is a table, not a series.
//   - At least two distinct dates. One point is not a trend.
//   - A numeric column plots; a column with any non-numeric present value
//     does not.
//
// A fact that fails any clause simply gets no trend -- it keeps its row
// table, which was already renderable.
func datedFactTrendShapes(facts []ClaimedFact) []contractsv1.ContextFabricRenderShape {
	var shapes []contractsv1.ContextFabricRenderShape
	for _, fact := range facts {
		shape, ok := datedFactTrendShape(fact)
		if !ok {
			continue
		}
		shapes = append(shapes, shape)
	}
	return shapes
}

func datedFactTrendShape(fact ClaimedFact) (contractsv1.ContextFabricRenderShape, bool) {
	rows := fact.Rows
	if len(rows) < 2 || len(rows) > contractsv1.ContextFabricRenderPointsMaxCount {
		return contractsv1.ContextFabricRenderShape{}, false
	}
	columns := renderRowColumns(rows)
	dateColumn, ordering, ok := dateAxisColumn(rows, columns)
	if !ok {
		return contractsv1.ContextFabricRenderShape{}, false
	}
	var series []contractsv1.ContextFabricRenderSeries
	for _, column := range columns {
		if column == dateColumn || !numericRenderColumn(rows, column) {
			continue
		}
		if len(series) >= contractsv1.ContextFabricRenderSeriesMaxCount {
			break
		}
		points := make([]contractsv1.ContextFabricRenderPoint, 0, len(ordering))
		for _, index := range ordering {
			value, ok := renderNumericCell(rows[index].Fields[column])
			if !ok {
				continue
			}
			label, _ := renderStringCell(rows[index].Fields[dateColumn])
			rowIndex := index
			points = append(points, contractsv1.ContextFabricRenderPoint{
				Label: label,
				Value: value,
				Source: contractsv1.ContextFabricRenderPointSource{
					Kind:     contractsv1.ContextFabricRenderSourceClaimedFactRow,
					ClaimID:  fact.ClaimID,
					RowIndex: &rowIndex,
					Field:    column,
				},
			})
		}
		if len(points) < 2 {
			continue
		}
		series = append(series, contractsv1.ContextFabricRenderSeries{
			Key:    column,
			Label:  humanizeRenderTerm(column),
			Points: points,
		})
	}
	if len(series) == 0 {
		return contractsv1.ContextFabricRenderShape{}, false
	}
	return contractsv1.ContextFabricRenderShape{
		Kind:         contractsv1.ContextFabricRenderKindSeries,
		Presentation: contractsv1.ContextFabricRenderPresentationLine,
		SelectedBy:   contractsv1.ContextFabricRenderRuleDatedFactTrend,
		Title:        clampRenderLabel(humanizeRenderTerm(fact.Field) + " over time — " + fact.Subject.Label),
		AxisKind:     contractsv1.ContextFabricRenderAxisTime,
		AxisLabel:    clampRenderLabel(dateColumn),
		ValueLabel:   clampRenderLabel(humanizeRenderTerm(fact.Field)),
		Series:       series,
	}, true
}

// renderRowColumns is first-seen column order across the row set, so shape
// construction is deterministic even though Go map iteration is not.
func renderRowColumns(rows []ClaimedFactRow) []string {
	seen := map[string]struct{}{}
	var order []string
	for _, row := range rows {
		keys := make([]string, 0, len(row.Fields))
		for key := range row.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			order = append(order, key)
		}
	}
	return order
}

// dateAxisColumn returns the first column (in deterministic column order)
// that satisfies every date-axis clause, plus the row indices sorted by
// that column's parsed instant.
func dateAxisColumn(rows []ClaimedFactRow, columns []string) (string, []int, bool) {
	for _, column := range columns {
		instants := make([]time.Time, len(rows))
		distinct := map[string]struct{}{}
		var withTime, withoutTime bool
		usable := true
		for i, row := range rows {
			raw, ok := renderStringCell(row.Fields[column])
			if !ok {
				usable = false
				break
			}
			instant, hasTime, ok := parseRenderDate(raw)
			if !ok {
				usable = false
				break
			}
			if hasTime {
				withTime = true
			} else {
				withoutTime = true
			}
			if _, repeated := distinct[raw]; repeated {
				usable = false
				break
			}
			distinct[raw] = struct{}{}
			instants[i] = instant
		}
		if !usable || (withTime && withoutTime) || len(distinct) < 2 {
			continue
		}
		ordering := make([]int, len(rows))
		for i := range ordering {
			ordering[i] = i
		}
		sort.SliceStable(ordering, func(a, b int) bool {
			return instants[ordering[a]].Before(instants[ordering[b]])
		})
		return column, ordering, true
	}
	return "", nil, false
}

// parseRenderDate accepts the ISO-8601 forms every producer in this
// codebase emits for a day column, and REJECTS anything Go's own parser
// would silently normalize. It reports whether the value carried a time
// component so a mixed-shape column can be refused.
func parseRenderDate(raw string) (time.Time, bool, bool) {
	for _, layout := range []string{"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if instant, err := time.Parse(layout, raw); err == nil {
			return instant, true, true
		}
	}
	if instant, err := time.Parse("2006-01-02", raw); err == nil {
		return instant, false, true
	}
	return time.Time{}, false, false
}

// numericRenderColumn is true when the column is present on every row with
// a numeric value. "Present on every row" is stricter than the row table's
// own rules on purpose: a trend line with a hole in it would have to either
// break the line or bridge the gap, and both are claims about data that is
// not there.
func numericRenderColumn(rows []ClaimedFactRow, column string) bool {
	for _, row := range rows {
		if _, ok := renderNumericCell(row.Fields[column]); !ok {
			return false
		}
	}
	return true
}

func renderNumericCell(value ScalarValue) (float64, bool) {
	switch {
	case value.Number != nil:
		return *value.Number, true
	case value.Integer != nil:
		return float64(*value.Integer), true
	default:
		return 0, false
	}
}

func renderStringCell(value ScalarValue) (string, bool) {
	if value.String == nil {
		return "", false
	}
	return *value.String, true
}
