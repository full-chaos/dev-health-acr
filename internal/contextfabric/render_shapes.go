package contextfabric

import (
	"fmt"
	"sort"
	"strconv"
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
	// MembersTruncated counts ranked cohort members the charts could not
	// carry. A cohort is bounded at 250 members and a series at 64 points,
	// so a large cohort's chart genuinely shows only the top of the
	// ranking. Silently drawing the top 64 and saying nothing would make a
	// partial ranking read as the whole one -- the same failure the
	// projection budget exists to prevent, one layer up. Zero on every
	// selection that lost nothing.
	MembersTruncated int
	// SeriesTruncated counts numeric columns a trend could not carry. A
	// shape holds at most 8 series, so a wide row table yields a PARTIAL
	// trend. Same reason MembersTruncated exists: reporting a healthy
	// 8-series shape for a 9-column fact lets a consumer read a partial
	// trend as a complete one (codex round 2, P3). Zero on every
	// selection that lost nothing.
	SeriesTruncated int
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
	// RenderShapeSkipMixedScopeRows -- the row table's rows are not
	// observations of the same thing (CHAOS-4616). See rowsShareOneScope.
	RenderShapeSkipMixedScopeRows RenderShapeSkipReason = "mixed_scope_rows"
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
		// A series is capped at 64 points and a cohort at 250 members, so a
		// large cohort's chart shows only the top of the ranking. The loss
		// is COUNTED, never silent: a chart of the top 64 that says nothing
		// reads as a chart of the whole cohort.
		if len(ranked) > contractsv1.ContextFabricRenderPointsMaxCount {
			event.MembersTruncated = len(ranked) - contractsv1.ContextFabricRenderPointsMaxCount
			ranked = ranked[:contractsv1.ContextFabricRenderPointsMaxCount]
		}
		labels := disambiguatedMemberLabels(ranked)
		shapes = append(shapes, cohortAttentionScoreShape(result.Cohort.Kind, ranked, labels))
		if contribution, reason := cohortDriverContributionShape(result.Cohort.Kind, ranked, labels); contribution != nil {
			shapes = append(shapes, *contribution)
		} else {
			event.skip(contractsv1.ContextFabricRenderRuleCohortDriverContribution, reason)
		}
	}

	trends, seriesTruncated, mixedScope := datedFactTrendShapes(result.ClaimedFacts)
	event.SeriesTruncated = seriesTruncated
	// A refused mixed-scope fact is reported whether or not some OTHER fact
	// produced a trend. Recording the reason only when the rule produced
	// nothing at all made a refusal invisible the moment any trend
	// succeeded (codex P2, EXECUTED) -- and a silent refusal is exactly the
	// undiagnosable-from-artifacts failure this file's telemetry exists to
	// prevent.
	if mixedScope {
		event.skip(contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipMixedScopeRows)
	}
	if len(trends) == 0 && !mixedScope {
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
	return ranked
}

// disambiguatedMemberLabels maps each member's canonical id to the axis
// label to plot it under. A point label is an axis POSITION, so two members
// sharing a display label would silently collapse onto one bar; where that
// happens every colliding member is qualified by its canonical id instead.
// The canonical id is already on the wire in the same shape's point source,
// so this discloses nothing new.
func disambiguatedMemberLabels(members []CohortMember) map[string]string {
	labels := make(map[string]string, len(members))
	// Disambiguation runs AFTER clamping, on the string that actually
	// reaches the axis. Doing it before was a real defect (codex round 1,
	// P2): two DISTINCT labels sharing their first 256 bytes each looked
	// unique, were clamped to the same value, and collided -- which the
	// contract validator then rejected, turning an otherwise valid
	// investigation into a failed one.
	//
	// The suffix is an ordinal, not the canonical id: a canonical id can
	// itself be long enough to be clamped away, so appending one would
	// reintroduce the collision it was meant to fix. The full identity of
	// every point is still on the wire in its own source.
	used := make(map[string]struct{}, len(members))
	for _, member := range members {
		label := clampRenderLabel(member.Subject.Label)
		if _, taken := used[label]; taken {
			for ordinal := 2; ; ordinal++ {
				suffix := fmt.Sprintf(" (%d)", ordinal)
				candidate := clampRenderLabel(label, len(suffix)) + suffix
				if _, exists := used[candidate]; !exists {
					label = candidate
					break
				}
			}
		}
		used[label] = struct{}{}
		labels[member.Subject.CanonicalID] = label
	}
	return labels
}

// clampRenderLabel keeps a label inside the contract bound. Labels are
// identity, not prose, so a clamp here is a display concern only -- no
// number and no judgment can be lost to it.
// clampRenderLabel keeps a label inside the contract bound, leaving room for
// an optional suffix the caller will append. It cuts on RUNE boundaries: a
// byte slice can split a multi-byte character and produce invalid UTF-8,
// which a JSON encoder then mangles into a replacement character -- a label
// nobody chose. Labels are identity, not prose, so a clamp is a display
// concern only: no number and no judgment can be lost to it.
func clampRenderLabel(label string, reserve ...int) string {
	limit := contractsv1.ContextFabricRenderLabelMaxLength
	for _, r := range reserve {
		limit -= r
	}
	if limit < 1 {
		limit = 1
	}
	runes := []rune(label)
	if len(runes) <= limit {
		return label
	}
	return string(runes[:limit])
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
// datedFactTrendShapes also reports whether ANY fact was refused
// specifically because its rows span more than one scope, so the skip reason
// names that rather than collapsing into the generic "no dated rows" -- a
// producer emitting a cross-scope table and a producer emitting no dated
// rows at all are different problems, and a reader diagnosing a missing
// chart must be able to tell them apart from the run's own artifacts.
func datedFactTrendShapes(facts []ClaimedFact) (shapes []contractsv1.ContextFabricRenderShape, truncatedTotal int, mixedScope bool) {
	for _, fact := range facts {
		shape, truncated, ok, mixed := datedFactTrendShape(fact)
		if mixed {
			mixedScope = true
		}
		if !ok {
			continue
		}
		truncatedTotal += truncated
		shapes = append(shapes, shape)
	}
	return shapes, truncatedTotal, mixedScope
}

func datedFactTrendShape(fact ClaimedFact) (shape contractsv1.ContextFabricRenderShape, truncated int, ok bool, mixedScope bool) {
	rows := fact.Rows
	if len(rows) < 2 || len(rows) > contractsv1.ContextFabricRenderPointsMaxCount {
		return contractsv1.ContextFabricRenderShape{}, 0, false, false
	}
	columns := renderRowColumns(rows)
	dateColumn, ordering, found := dateAxisColumn(rows, columns)
	if !found {
		return contractsv1.ContextFabricRenderShape{}, 0, false, false
	}
	// Scope is checked only once this table has SOMETHING to plot. A dated
	// table with no plottable column could never have been a trend, so
	// blaming a scope split for it would send a reader after the wrong
	// producer (codex P2, EXECUTED) -- "no dated rows" is the honest reason.
	plottable := false
	for _, column := range columns {
		if column != dateColumn && plottableRenderColumn(rows, column) {
			plottable = true
			break
		}
	}
	if !plottable {
		return contractsv1.ContextFabricRenderShape{}, 0, false, false
	}
	if !rowsShareOneScope(rows, dateColumn, columns) {
		return contractsv1.ContextFabricRenderShape{}, 0, false, true
	}
	var series []contractsv1.ContextFabricRenderSeries
	for _, column := range columns {
		if column == dateColumn || !plottableRenderColumn(rows, column) {
			continue
		}
		if len(series) >= contractsv1.ContextFabricRenderSeriesMaxCount {
			// Counted, not silently skipped: a shape reporting 8 healthy
			// series for a 9-column fact reads as a complete trend.
			truncated++
			continue
		}
		points := make([]contractsv1.ContextFabricRenderPoint, 0, len(ordering))
		for _, index := range ordering {
			value, numeric := renderNumericCell(rows[index].Fields[column])
			if !numeric {
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
		return contractsv1.ContextFabricRenderShape{}, 0, false, false
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
	}, truncated, true, false
}

// rowsShareOneScope reports whether every row describes the SAME subject
// scope, so that plotting them against time is a trend rather than a
// comparison of different things.
//
// CHAOS-4616 (Urgent), found by inspecting a live chart: a `flow` fact for
// team `fullchaos` carried two rows, day 2026-07-20 with
// work_scope_id=full.chaos/chaos-ops and day 2026-08-30 with
// work_scope_id=full.chaos/dev-health-ops — two different scopes measured
// ONCE EACH. The rule keyed only on "a distinct same-shaped date column plus
// numeric columns" and drew wip_count_end_of_day rising 0 -> 1 across them,
// which reads as one scope changing over time. Every number was copied
// faithfully and the chart still said something the data does not: the same
// defect class this contract exists to prevent, arrived at through the axis
// instead of through a value.
//
// The scope identity is every column that is NEITHER the date axis NOR a
// plotted numeric series — a string, boolean or null cell that varies across
// rows means the rows are split by that dimension. A column that never
// varies is provenance (a constant `provider`, a team name) and is ignored,
// so it cannot block a legitimate trend.
//
// This REFUSES a mixed-scope table rather than picking the largest scope's
// rows or emitting a series per scope. Both alternatives were considered and
// are worse here: selecting one scope silently drops the others (the
// silent-truncation class this file already had to fix twice), and a series
// per scope is a different claim — a comparison between scopes, not a trend
// — which needs its own selection rule and its own name rather than being
// smuggled in under `dated_fact_trend`. Widening to a per-scope shape is
// tracked separately; refusing is the honest minimum.
func rowsShareOneScope(rows []ClaimedFactRow, dateColumn string, columns []string) bool {
	for _, column := range columns {
		if column == dateColumn || plottableRenderColumn(rows, column) {
			continue
		}
		var first string
		for i, row := range rows {
			value := scopeCellKey(row.Fields[column])
			if i == 0 {
				first = value
				continue
			}
			if value != first {
				return false
			}
		}
	}
	return true
}

// scopeCellKey renders a dimension cell as a comparable string. An ABSENT
// cell and a present one are deliberately different keys: a row that omits
// the dimension is not known to share it. Numbers are keyed too, because an
// identifier column is a dimension whatever its type -- keying only strings
// let a numeric id present on one row and absent on another compare equal as
// "absent" (codex P1, second half).
func scopeCellKey(value ScalarValue) string {
	switch {
	case value.String != nil:
		return "s:" + *value.String
	case value.Boolean != nil:
		if *value.Boolean {
			return "b:true"
		}
		return "b:false"
	case value.Integer != nil:
		return "i:" + strconv.FormatInt(*value.Integer, 10)
	case value.Number != nil:
		return "n:" + strconv.FormatFloat(*value.Number, 'g', -1, 64)
	case value.Null:
		return "null"
	default:
		return "absent"
	}
}

// identifierColumn reports whether a column NAMES something rather than
// measures it.
//
// An identifier is a dimension whatever its type. Before this, the scope
// check skipped every numeric column, so a numeric `team_id` of 101 and 202
// was treated as a plottable series: the rule drew "team id over time"
// beside the real measure, which is nonsense on its own AND hid the scope
// split this rule exists to refuse (codex P1, EXECUTED).
//
// Name-based, and deliberately the SAME notion the CHAOS-4355 axis chooser
// already uses (`ordinalAxisPreferenceScore` in the web/ask-dev
// `fact-rows.ts` deprioritises `id`/`*_id` for exactly this reason), so the
// two ends agree about what an identifier looks like. A name test is
// admittedly a heuristic; the alternative -- inferring "this integer is an
// id" from its values -- is a worse one, and a producer that needs a
// genuinely numeric MEASURE simply must not name it `*_id`.
func identifierColumn(column string) bool {
	lower := strings.ToLower(column)
	return lower == "id" || strings.HasSuffix(lower, "_id")
}

// plottableRenderColumn is a column a trend may draw as a series: numeric on
// every row AND not an identifier.
func plottableRenderColumn(rows []ClaimedFactRow, column string) bool {
	return !identifierColumn(column) && numericRenderColumn(rows, column)
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
		// Distinctness is by INSTANT, not by spelling. "2026-08-03T00:00:00Z"
		// and "2026-08-02T17:00:00-07:00" are two spellings of one moment: a
		// raw-string check calls them distinct, and the axis -- which is
		// positioned by elapsed time -- then stacks two different values on
		// one x position (codex round 1, P2). Comparing the parsed instant is
		// the same question the renderer will ask.
		distinct := map[int64]struct{}{}
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
			if _, repeated := distinct[instant.UnixNano()]; repeated {
				usable = false
				break
			}
			distinct[instant.UnixNano()] = struct{}{}
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

// renderNumericCell unwraps a row cell to a plottable number.
//
// An integer past 2^53 is NOT plottable and is refused here rather than
// cast: beyond that bound a float64 cannot distinguish adjacent integers, so
// a point built from one would claim a value that differs from the row it
// cites -- and contractsv1's resolver refuses it, which would turn a valid
// answer into a rejected one at the last gate. Refusing here means the
// column simply is not chartable and the fact keeps its table.
func renderNumericCell(value ScalarValue) (float64, bool) {
	switch {
	case value.Number != nil:
		return *value.Number, true
	case value.Integer != nil:
		if *value.Integer > contractsv1.ContextFabricRenderPointExactIntegerBound ||
			*value.Integer < -contractsv1.ContextFabricRenderPointExactIntegerBound {
			return 0, false
		}
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
