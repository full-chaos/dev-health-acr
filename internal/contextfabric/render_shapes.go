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
	// MembersTruncated counts ranked cohort members the charts could not
	// carry. A cohort is bounded at 250 members and a series at 64 points,
	// so a large cohort's chart genuinely shows only the top of the
	// ranking. Silently drawing the top 64 and saying nothing would make a
	// partial ranking read as the whole one -- the same failure the
	// projection budget exists to prevent, one layer up. Zero on every
	// selection that lost nothing.
	MembersTruncated int
	// TrendsOmitted counts trends the rule selected and the answer had no
	// room to carry (ContextFabricRenderShapesMaxCount). A COUNT rather
	// than a skip reason, deliberately: the rule DID fire, so recording it
	// as skipped would make the per-rule accounting say two things at once
	// -- and a ninth shape is a rejected document, not a smaller answer.
	// Zero on every selection that lost nothing.
	TrendsOmitted int
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
	// The dated_fact_trend rule's reasons (CHAOS-4637). The rule is
	// evaluated ONCE over every claimed fact, so it exits through exactly
	// one of these, chosen by the FURTHEST stage any fact reached -- the
	// most actionable diagnosis rather than the first or the commonest.
	// A reader seeing no_time_series_table knows tables arrived and none
	// declared itself a series; one seeing claim_field_not_a_measure
	// knows a series arrived and the producer half is what is missing.
	//
	// RenderShapeSkipNoDeclaredTable -- no claimed fact carried a
	// DECLARED table. Either the fact carried no rows at all, or it
	// carried rows a producer never declared. Undeclared is never
	// charted (CHAOS-4627's ruled default): inferring a shape from the
	// rows is precisely the geometry inference this slice deletes.
	RenderShapeSkipNoDeclaredTable RenderShapeSkipReason = "no_declared_table"
	// RenderShapeSkipNoTimeSeriesTable -- declared tables arrived and
	// none of them is a time_series. This is where flow.go's
	// `scope_breakdown` lands, permanently: it declares Shape=breakdown
	// with Key=[provider, work_scope_id], so the rule that once drew a
	// line across two different work scopes cannot even consider it.
	// CHAOS-4616's correction, expressed as a declaration outcome rather
	// than as a guard inside the selector.
	RenderShapeSkipNoTimeSeriesTable RenderShapeSkipReason = "no_time_series_table"
	// RenderShapeSkipClaimFieldNotAMeasure -- a time_series table
	// arrived, and the claim's own Field is not one of its DECLARED
	// measures, so there is no single measure this claim is about.
	//
	// A trend plots ONE measure. Plotting several declared measures on
	// one value axis silently asserts they are commensurable, which
	// nothing on the wire says -- that is CHAOS-4625's separate,
	// designed comparison shape, and `Measures` being a declared list is
	// exactly what makes it expressible there. Until it exists, this
	// refuses rather than picks.
	RenderShapeSkipClaimFieldNotAMeasure RenderShapeSkipReason = "claim_field_not_a_measure"
	// RenderShapeSkipUnresolvableMeasureRoles (codex round 1 finding 1,
	// P1, EXECUTED) -- the table is DECLARED time_series and carries a
	// declared measure whose cells are not numeric, so this rule cannot
	// tell a per-row categorical OBSERVATION from a second IDENTITY
	// dimension, and one of those two readings makes the line a lie.
	//
	// This is the hole the one-key-column rule does NOT close. Putting the
	// work scope in KEY makes the key arity 2 and the table a breakdown by
	// definition; putting it in MEASURES leaves a declaration that is
	// internally consistent -- one key column, parsing as an instant,
	// distinct across rows, every column classified -- and the trend rule,
	// which reads only the claim's own measure, never looks at the scope
	// column. The CHAOS-4616 false line comes back through the
	// DECLARATION instead of through the geometry.
	//
	// WHY THIS IS A SELECTION REFUSAL AND NOT A VALIDATION RULE. The
	// obvious fix -- "a time_series measure must be numeric" -- was
	// written, and then EXECUTED against the merged producers: it
	// invalidates health.go's own CHAOS-4645 declaration, which carries
	// `severity` (a per-day categorical observation of ONE subject, and a
	// legitimate one) among its measures. `severity` and `work_scope_id`
	// are syntactically identical and semantically opposite, and the
	// declaration vocabulary has no third role to tell them apart: design
	// §5.1 admits only Key or Measures, so a varying non-identity column
	// has nowhere else to go. Refusing the DECLARATION would break a
	// correct producer; refusing to DRAW A LINE THROUGH IT costs nothing
	// today (health's time_series is dual-table and never reaches the
	// wire) and fails closed if it ever does.
	//
	// The real fix is a third declared role, which is CHAOS-4633's
	// vocabulary and not this slice's -- filed, with this reasoning.
	RenderShapeSkipUnresolvableMeasureRoles RenderShapeSkipReason = "unresolvable_measure_roles"
	// RenderShapeSkipNoPlottableMeasure -- the declared measure is
	// there and its cells cannot be plotted: fewer than two rows carry a
	// numeric value for it, or the declared axis column does not parse as
	// an instant. A hole in a line has to be either broken or bridged and
	// both are claims about data that is not there.
	RenderShapeSkipNoPlottableMeasure RenderShapeSkipReason = "no_plottable_measure"
	// RenderShapeSkipShapeBudgetSpent -- trends were selectable and the
	// answer's render-shape budget (ContextFabricRenderShapesMaxCount) was
	// already spent. Counted, never silent: emitting a ninth shape would
	// fail contract validation and turn a good answer into a rejected one.
	RenderShapeSkipShapeBudgetSpent RenderShapeSkipReason = "shape_budget_spent"
	// RenderShapeSkipNotPlanAuthorized (CHAOS-4636) means the geometry
	// admitted this shape and the PLAN did not authorize its kind.
	//
	// This is North Star check 10 made structural: rich views are
	// conditional on intent, never default. A grouped status list plans
	// RenderKinds=[table] and RequireRanking=false, so a cohort attention
	// BAR -- one ranking drawn across every group, for a question that
	// asked for per-group results -- is not merely wrong here, it was not
	// requested, and the plan is where that is said.
	RenderShapeSkipNotPlanAuthorized RenderShapeSkipReason = "not_plan_authorized"
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
	// CHAOS-4636: the plan gates the KIND before the geometry is consulted.
	// Both cohort rules produce a `series`, so a plan that does not
	// authorize `series` refuses both -- which is exactly what stops the
	// group-blind cross-group bar chart a grouped question would otherwise
	// get. A result with NO plan (every result written before the planning
	// stage existed, and every path that terminates before planning)
	// authorizes everything, so nothing that renders today stops rendering.
	seriesAuthorized := planAuthorizesRenderKind(result.AnswerPlan, contractsv1.ContextFabricRenderKindSeries)
	switch {
	case !seriesAuthorized:
		event.skip(contractsv1.ContextFabricRenderRuleCohortAttentionScore, RenderShapeSkipNotPlanAuthorized)
		event.skip(contractsv1.ContextFabricRenderRuleCohortDriverContribution, RenderShapeSkipNotPlanAuthorized)
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

	// CHAOS-4637: the dated_fact_trend rule RETURNS, and it returns
	// declaration-driven. It was withdrawn in acr #340 because deciding
	// which columns of a row table are measures and which are dimensions
	// cannot be done from the table alone -- three successive inferences
	// were each defeated in review. The information always existed at the
	// producer; CHAOS-4633/4645 declared it and this slice is where it is
	// finally READ. The rule is now one predicate over a declaration, and
	// there is no geometry left in it to defeat.
	trends, omitted, reason := datedFactTrendShapes(result, len(shapes))
	event.TrendsOmitted = omitted
	if len(trends) > 0 {
		shapes = append(shapes, trends...)
	} else {
		event.skip(contractsv1.ContextFabricRenderRuleDatedFactTrend, reason)
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

// planAuthorizesRenderKind reports whether the plan permits kind.
//
// A NIL plan authorizes everything. That is deliberate and is what keeps
// this slice non-regressive: results written before the planning stage
// existed, and paths that terminate before planning runs, must render
// exactly what they render today. An EMPTY RenderKinds list on a real plan
// also authorizes everything, for the same reason -- a family that has not
// declared its render kinds has not declared a restriction, and inferring
// one from silence is how a chart quietly disappears.
func planAuthorizesRenderKind(plan *contractsv1.ContextFabricAnswerPlan, kind contractsv1.ContextFabricRenderKind) bool {
	if plan == nil || len(plan.RenderKinds) == 0 {
		return true
	}
	for _, authorized := range plan.RenderKinds {
		if authorized == kind {
			return true
		}
	}
	return false
}

// datedFactTrendShapes is rule 3, CHAOS-4637: one line per claimed fact
// whose row table the PRODUCER DECLARED a time_series.
//
// THE WHOLE RULE IS A DECLARATION LOOKUP. There is no column scan, no
// numeric-column test, no id-name test, and no "other columns constant"
// check -- the three inferences that were each defeated in review, plus the
// correction that replaced them, are all gone, because every question they
// tried to answer is now answered on the wire:
//
//   - IS this a series? Table.Shape == time_series. A table whose identity
//     needs two columns cannot carry that shape (the declaration's own
//     arity rule), which is why flow.go's scope_breakdown -- the table that
//     produced the defect -- can never be selected here again.
//   - WHICH column is the axis? Table.Key[0]. Declared, and exactly one by
//     construction.
//   - WHICH column is the measure? The claim says what it is about:
//     ClaimedFact.Field, required to be one of Table.Measures. Exactly one
//     measure is plotted, so the value axis is commensurable by
//     construction rather than by hope. Several measures over time is a
//     COMPARISON and is CHAOS-4625's own designed shape; `Measures` being a
//     declared list is what makes that expressible, and this rule refuses
//     rather than quietly picking one.
//
// What remains is not inference: parsing the DECLARED axis cell to order
// the points, and unwrapping the DECLARED measure cell to a plottable
// number. Both are reads of a named column, and both refuse rather than
// guess when the cell will not yield -- a producer that declared a table it
// cannot satisfy gets no chart, not an approximated one.
//
// Returns the shapes, the number of further trends the shape budget had no
// room for, and -- only when nothing was selected -- the single closed
// reason why.
func datedFactTrendShapes(result InvestigationResult, alreadySelected int) (shapes []contractsv1.ContextFabricRenderShape, omitted int, reason RenderShapeSkipReason) {
	// The plan gates the KIND before any fact is consulted. North Star
	// check 10 is enforced by what the question asked for, never by what
	// the data happened to allow.
	if !planAuthorizesRenderKind(result.AnswerPlan, contractsv1.ContextFabricRenderKindSeries) {
		return nil, 0, RenderShapeSkipNotPlanAuthorized
	}
	budget := contractsv1.ContextFabricRenderShapesMaxCount - alreadySelected
	// furthest records the most advanced stage ANY fact reached, so the one
	// reported reason is the most actionable one. See the skip-reason
	// vocabulary for why the furthest stage rather than the first.
	furthest := trendStageNoDeclaredTable
	for _, fact := range result.ClaimedFacts {
		shape, stage := datedFactTrendShape(fact)
		if stage > furthest {
			furthest = stage
		}
		if shape == nil {
			continue
		}
		if len(shapes) >= budget {
			omitted++
			continue
		}
		shapes = append(shapes, *shape)
	}
	if len(shapes) > 0 {
		return shapes, omitted, ""
	}
	// Found by the CHAOS-4637 shape sweep, not by a review round: a fact
	// could reach trendStageSelected and still append nothing, if the shape
	// budget was already spent. trendStageReasons has no entry for
	// trendStageSelected -- correctly, since it is not a refusal -- so the
	// lookup returned the empty string and this rule would have recorded a
	// SKIP WITH NO REASON. Accounted() would then report `violated` and the
	// selector would be exactly the "exit path that forgets its reason"
	// CHAOS-4621 was filed about, introduced by the code that closes it.
	//
	// Not reachable today (the two cohort rules produce at most 2 of the 8
	// shapes a result may carry, so the trend rule always has room), which
	// is precisely why only an exhaustive sweep of the class would find it.
	if furthest == trendStageSelected {
		return nil, omitted, RenderShapeSkipShapeBudgetSpent
	}
	return nil, omitted, trendStageReasons[furthest]
}

// trendStage orders how far the rule got on one fact. Ordered so that
// "the furthest stage reached" is simply the maximum.
type trendStage int

const (
	trendStageNoDeclaredTable trendStage = iota
	trendStageNoTimeSeriesTable
	trendStageClaimFieldNotAMeasure
	trendStageUnresolvableMeasureRoles
	trendStageNoPlottableMeasure
	trendStageSelected
)

var trendStageReasons = map[trendStage]RenderShapeSkipReason{
	trendStageNoDeclaredTable:          RenderShapeSkipNoDeclaredTable,
	trendStageNoTimeSeriesTable:        RenderShapeSkipNoTimeSeriesTable,
	trendStageClaimFieldNotAMeasure:    RenderShapeSkipClaimFieldNotAMeasure,
	trendStageUnresolvableMeasureRoles: RenderShapeSkipUnresolvableMeasureRoles,
	trendStageNoPlottableMeasure:       RenderShapeSkipNoPlottableMeasure,
}

// datedFactTrendShape evaluates one claimed fact, returning the shape (or
// nil) and the stage it reached.
func datedFactTrendShape(fact ClaimedFact) (*contractsv1.ContextFabricRenderShape, trendStage) {
	table := fact.Table
	if table == nil {
		return nil, trendStageNoDeclaredTable
	}
	if table.Shape != contractsv1.ContextFabricFactTableShapeTimeSeries {
		return nil, trendStageNoTimeSeriesTable
	}
	if !table.HasMeasure(fact.Field) {
		return nil, trendStageClaimFieldNotAMeasure
	}
	// Key arity is 1 for a time_series by contract; this is the belt to
	// that braces, so a malformed declaration that somehow reached here
	// refuses instead of indexing out of range.
	if len(table.Key) != 1 {
		return nil, trendStageClaimFieldNotAMeasure
	}
	axis := table.Key[0]
	rows := fact.Rows
	if len(rows) < 2 || len(rows) > contractsv1.ContextFabricRenderPointsMaxCount {
		return nil, trendStageNoPlottableMeasure
	}
	if !everyDeclaredMeasureIsAQuantity(table, rows) {
		return nil, trendStageUnresolvableMeasureRoles
	}
	points, ok := trendPoints(fact, axis, fact.Field)
	if !ok {
		return nil, trendStageNoPlottableMeasure
	}
	return &contractsv1.ContextFabricRenderShape{
		Kind:         contractsv1.ContextFabricRenderKindSeries,
		Presentation: contractsv1.ContextFabricRenderPresentationLine,
		SelectedBy:   contractsv1.ContextFabricRenderRuleDatedFactTrend,
		Title:        clampRenderLabel(humanizeRenderTerm(fact.Field) + " over time — " + fact.Subject.Label),
		AxisKind:     contractsv1.ContextFabricRenderAxisTime,
		AxisLabel:    clampRenderLabel(axis),
		ValueLabel:   clampRenderLabel(humanizeRenderTerm(fact.Field)),
		Series: []contractsv1.ContextFabricRenderSeries{{
			Key:    fact.Field,
			Label:  humanizeRenderTerm(fact.Field),
			Points: points,
		}},
	}, trendStageSelected
}

// everyDeclaredMeasureIsAQuantity reports whether EVERY declared measure of
// this time_series -- not merely the one about to be plotted -- carries
// numbers.
//
// Checking the whole declaration rather than just the plotted column is the
// point. The plotted column is numeric by definition (it would not otherwise
// be plottable); the risk lives in the columns this rule does not read. A
// declared measure carrying strings is either a per-row categorical
// observation or a second entity's identity, and nothing in the declaration
// vocabulary distinguishes them. A line drawn through the second is the
// CHAOS-4616 defect; refusing both is the fail-closed reading, and the cost
// of refusing the first is a chart that was never drawn today anyway.
//
// Absent and explicitly-null cells are NOT disqualifying: a
// conditionally-computed measure (metrics' mttr_hours) is missing data, and
// missing data says nothing about roles.
func everyDeclaredMeasureIsAQuantity(table *contractsv1.ContextFabricClaimedFactTable, rows []ClaimedFactRow) bool {
	for _, row := range rows {
		for _, measure := range table.Measures {
			cell, present := row.Fields[measure]
			if !present || cell.Null {
				continue
			}
			if cell.Integer == nil && cell.Number == nil {
				return false
			}
		}
	}
	return true
}

// trendPoints reads the DECLARED axis and the DECLARED measure off every
// row and orders the points by elapsed time.
//
// It refuses the whole series rather than plotting a subset. A measure may
// legitimately be absent from an individual row of a declared table (a
// conditionally-computed column), but a LINE with a hole in it has to
// either break or bridge, and both state something the rows do not. A
// producer that wants a partial series declares a table that has one.
//
// Distinctness is by INSTANT, not by spelling: "2026-08-03T00:00:00Z" and
// "2026-08-02T17:00:00-07:00" are two spellings of one moment, and a
// raw-string check would call them distinct and stack two values on one
// axis position. The producer's own FactTable.Validate makes the key
// distinct, so a repeat here means the declaration and the rows disagree,
// and the rows are what a reader would see.
func trendPoints(fact ClaimedFact, axis, measure string) ([]contractsv1.ContextFabricRenderPoint, bool) {
	type dated struct {
		instant time.Time
		point   contractsv1.ContextFabricRenderPoint
	}
	seen := make(map[int64]struct{}, len(fact.Rows))
	entries := make([]dated, 0, len(fact.Rows))
	var withTime, withoutTime bool
	for index, row := range fact.Rows {
		raw, ok := renderStringCell(row.Fields[axis])
		if !ok {
			return nil, false
		}
		instant, hasTime, ok := parseRenderDate(raw)
		if !ok {
			return nil, false
		}
		if hasTime {
			withTime = true
		} else {
			withoutTime = true
		}
		if _, repeated := seen[instant.UnixNano()]; repeated {
			return nil, false
		}
		seen[instant.UnixNano()] = struct{}{}
		value, numeric := renderNumericCell(row.Fields[measure])
		if !numeric {
			return nil, false
		}
		rowIndex := index
		entries = append(entries, dated{instant: instant, point: contractsv1.ContextFabricRenderPoint{
			Label: raw,
			Value: value,
			Source: contractsv1.ContextFabricRenderPointSource{
				Kind:     contractsv1.ContextFabricRenderSourceClaimedFactRow,
				ClaimID:  fact.ClaimID,
				RowIndex: &rowIndex,
				Field:    measure,
			},
		}})
	}
	// A time axis is POSITIONED by elapsed time. Mixing a bare date with a
	// zoned timestamp makes one elapsed-time scale ill-defined, so the
	// column must be all one shape.
	if withTime && withoutTime {
		return nil, false
	}
	if len(entries) < 2 {
		return nil, false
	}
	sort.SliceStable(entries, func(a, b int) bool { return entries[a].instant.Before(entries[b].instant) })
	points := make([]contractsv1.ContextFabricRenderPoint, 0, len(entries))
	for _, entry := range entries {
		points = append(points, entry.point)
	}
	return points, true
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

// renderStringCell unwraps a row cell that must be a present string.
func renderStringCell(value ScalarValue) (string, bool) {
	if value.String == nil {
		return "", false
	}
	return *value.String, true
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
	}
	return 0, false
}

// renderShapeRules is the CLOSED set of rules this selector evaluates. It
// is the denominator the accounting invariant below is total over.
var renderShapeRules = []contractsv1.ContextFabricRenderShapeRule{
	contractsv1.ContextFabricRenderRuleCohortAttentionScore,
	contractsv1.ContextFabricRenderRuleCohortDriverContribution,
	contractsv1.ContextFabricRenderRuleDatedFactTrend,
}

// Accounted is CHAOS-4621's structural invariant, as code rather than as a
// habit: EVERY rule this selector evaluates exits through exactly one
// recorded outcome -- it selected at least one shape, or it recorded
// exactly one closed skip reason. Never both, never neither, never two
// reasons.
//
// The same defect class was closed FOUR times case by case in the CHAOS-4415
// / 4616 work: a refusal that left no trace, or that recorded the wrong
// reason, or that went invisible as soon as some other rule produced a
// shape. Case-by-case fixes cannot stop the fifth. This makes the counts
// TOTAL, so a new exit path that forgets its reason is a failure by
// construction rather than a silence nobody notices.
//
// Reported as an error rather than panicking: a violation is a telemetry
// defect, and refusing to serve a good answer over one would be a worse
// outcome than serving it with the defect recorded.
func (e RenderShapeSelectionEvent) Accounted() error {
	selected := map[contractsv1.ContextFabricRenderShapeRule]int{}
	for _, selection := range e.Selected {
		selected[selection.Rule]++
	}
	skipped := map[contractsv1.ContextFabricRenderShapeRule]int{}
	for _, skip := range e.Skipped {
		if skip.Reason == "" {
			return fmt.Errorf("rule %q recorded a skip with no reason", skip.Rule)
		}
		skipped[skip.Rule]++
	}
	known := make(map[contractsv1.ContextFabricRenderShapeRule]struct{}, len(renderShapeRules))
	for _, rule := range renderShapeRules {
		known[rule] = struct{}{}
		switch {
		case selected[rule] > 0 && skipped[rule] > 0:
			return fmt.Errorf("rule %q both selected %d shape(s) and recorded %d skip reason(s)", rule, selected[rule], skipped[rule])
		case selected[rule] == 0 && skipped[rule] == 0:
			return fmt.Errorf("rule %q recorded no outcome at all", rule)
		case skipped[rule] > 1:
			return fmt.Errorf("rule %q recorded %d skip reasons, want exactly one", rule, skipped[rule])
		}
	}
	for rule := range selected {
		if _, ok := known[rule]; !ok {
			return fmt.Errorf("rule %q selected a shape but is not in the evaluated rule set", rule)
		}
	}
	for rule := range skipped {
		if _, ok := known[rule]; !ok {
			return fmt.Errorf("rule %q recorded a skip but is not in the evaluated rule set", rule)
		}
	}
	return nil
}
