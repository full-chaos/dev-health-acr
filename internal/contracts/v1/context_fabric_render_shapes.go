package v1

import "strings"

// CHAOS-4415 slice 1: the conditional renderable shape a Context Fabric
// answer may carry.
//
// WHY THIS EXISTS. North Star check 10 makes rich views "conditional on
// intent, never default", and check 8 makes a bare score a defect. Before
// this contract the only structured rendering an answer carried was
// ContextFabricClaimedFact.Rows (CHAOS-4347) and
// ContextFabricProjectedCohort.RankingTable (CHAOS-4398 PR3) -- both
// tables. A consumer that wanted a chart had to decide FOR ITSELF whether
// one was warranted and which columns to plot, from heuristics over row
// shape. That is the defect chris reported on 2026-08-29 19:59 PDT: the
// teams answer rendered the ranked-teams table and nothing else, although
// the answer carried a per-team attention score, a per-driver contribution
// breakdown, and dated readiness/workload records. Two consumers looking at
// the same answer could legitimately draw different charts, or none.
//
// So the SELECTION moves to the service, and a chart becomes a claimed
// fact like any other:
//
// STATUS (CHAOS-4616, CHAOS-4637): three of the rules this file's vocabulary
// declares have a producer -- the cohort attention-score bars, the
// per-driver contribution stack, and `dated_fact_trend`. The trend rule was
// withdrawn in CHAOS-4616 and has been live again since CHAOS-4637; see
// ContextFabricRenderRuleDatedFactTrend's own comment. The others are declared
// ahead of their producers.
//
//  1. Kind is a CLOSED vocabulary, fixed in full here and now
//     (ContextFabricRenderKind) so a consumer can exhaustively switch on it
//     and a later producer never widens the wire under it. Only "series" has
//     a producer in this slice; the other seven are declared, documented and
//     validated, with their producers filed as CHAOS-4415 sub-issues.
//  2. SelectedBy names the DETERMINISTIC rule that fired
//     (ContextFabricRenderShapeRule). A shape without a rule is not a shape:
//     "the engine felt like drawing one" is exactly the default-charting
//     this contract exists to forbid, and the rule id is what makes the
//     decision auditable from the answer alone.
//  3. Every Value is accompanied by a ContextFabricRenderPointSource naming
//     WHERE in this same document the number came from, and validation
//     RESOLVES that source and requires exact equality (see
//     validateRenderShapes). A number that does not resolve, or resolves to
//     a different value, is a rejected document -- not a rendering warning.
//     This is the ClaimedFact.Rows discipline (rows are attached from the
//     cited canonical fact, never model-authored) carried into chart space:
//     a chart is a claimed fact, and no chart number is ever authored,
//     re-derived, rounded, aggregated, or interpolated by whatever built the
//     shape.
//
// WHY BARS ARE A "series" AND NOT THEIR OWN KIND. A bar chart, a stacked
// bar chart and a line chart are the SAME data -- an ordered set of
// (label, value) points, optionally grouped into several series -- drawn
// three ways. Making "bar"/"stacked_bar" their own Kind values would put a
// PRESENTATION choice into the vocabulary that describes DATA, and would
// force every consumer to implement three identical payload readers. Kind
// therefore names the data shape and Presentation names the encoding, so a
// consumer switches once on Kind to learn how to read the payload and once
// on Presentation to learn how to draw it.
//
// WHY A TREEMAP CARRIES THE SERIES PAYLOAD (CHAOS-4610). Kind still names what
// a consumer draws, and the rich views plan settled that treemap, table,
// quadrant and a two-level sunburst or sankey reuse "series" and resolve by the
// same source discipline. Only forecast needs a payload of its own (distribution
// bands). So this file declares no second payload: a Kind other than "series"
// reads the same Series field, and validateRenderShapes holds what each one
// requires. A treemap is one series of part-of-whole points of one table (see
// ContextFabricRenderRuleInvestmentThemeShare).
//
// A TABLE READS THE SAME PAYLOAD (CHAOS-12513). A side-by-side comparison is one
// series per subject and one point per measure, so a cell is a point: a label, a
// value and a source that resolves like any chart number. A measure a side lacks
// has no point, so its cell is empty and never zero (see
// ContextFabricRenderRuleComparisonSideBySide).
//
// A SUNBURST READS THE SAME PAYLOAD (CHAOS-12554). Its two rings are series: one
// series keyed ContextFabricRenderSunburstThemeSeriesKey is the inner ring, one
// point per theme, and every other series is the outer ring of one theme, keyed
// by that theme's token, one point per subcategory. The series key is what links
// a ring to its parent (see ContextFabricRenderRuleInvestmentSubcategoryShare).
//
// A SANKEY READS THE SAME PAYLOAD (CHAOS-12578). Its flows run from a team's
// repositories to the themes their effort went to: one series per theme, keyed by
// the theme's token, and one point per repository with effort in that theme,
// labelled with the repository's served word. A point is one link and its value
// the link's weighted effort (see ContextFabricRenderRuleInvestmentRepositoryFlow).

// ContextFabricRenderKind is the CLOSED vocabulary of renderable answer
// shapes. Every member is defined now; producers land per shape.
type ContextFabricRenderKind string

const (
	// ContextFabricRenderKindSeries is one or more named series of
	// (label, value) points over a shared categorical or time axis --
	// drawn as bars, stacked bars, or a line per Presentation. The only
	// kind with a producer in CHAOS-4415 slice 1.
	ContextFabricRenderKindSeries ContextFabricRenderKind = "series"
	// ContextFabricRenderKindTable is an explicitly table-shaped rendering: the
	// side-by-side of ContextFabricRenderRuleComparisonSideBySide, its only
	// rule. Nothing emits this kind yet. Other tables reach consumers as
	// ContextFabricClaimedFact.Rows or
	// ContextFabricProjectedCohort.RankingTable.
	ContextFabricRenderKindTable ContextFabricRenderKind = "table"
	// ContextFabricRenderKindQuadrant is the two-measure landscape
	// quadrant of the purpose contract §7 ("is X moving with Y").
	ContextFabricRenderKindQuadrant ContextFabricRenderKind = "quadrant"
	// ContextFabricRenderKindTreemap, ContextFabricRenderKindSunburst and
	// ContextFabricRenderKindSankey are the purpose contract §5.5
	// investment representations: part-of-whole, nested part-of-whole,
	// and flow between categories.
	ContextFabricRenderKindTreemap  ContextFabricRenderKind = "treemap"
	ContextFabricRenderKindSunburst ContextFabricRenderKind = "sunburst"
	ContextFabricRenderKindSankey   ContextFabricRenderKind = "sankey"
	// ContextFabricRenderKindBurndown is remaining scope over time for a
	// project status question with required children.
	ContextFabricRenderKindBurndown ContextFabricRenderKind = "burndown"
	// ContextFabricRenderKindForecast is the Monte Carlo completion
	// distribution of a capacity question (CHAOS-101 lineage; seeded per
	// CHAOS-4141).
	ContextFabricRenderKindForecast ContextFabricRenderKind = "forecast"
)

// ContextFabricRenderPresentation is the CLOSED vocabulary of visual
// encodings for a "series" shape. It never changes what the numbers mean --
// a consumer that ignores it entirely and renders a table of the points has
// still rendered the truth.
type ContextFabricRenderPresentation string

const (
	// ContextFabricRenderPresentationBars is one bar per point, one
	// series.
	ContextFabricRenderPresentationBars ContextFabricRenderPresentation = "bars"
	// ContextFabricRenderPresentationStackedBars stacks every series'
	// point for the same Label into one bar. A stacked bar CLAIMS the
	// parts sum to a meaningful whole, so a producer emits it only where
	// the parts genuinely compose (a member's per-driver weight
	// contributions summing to that member's own score).
	ContextFabricRenderPresentationStackedBars ContextFabricRenderPresentation = "stacked_bars"
	// ContextFabricRenderPresentationLine is a line per series over a
	// time axis.
	ContextFabricRenderPresentationLine ContextFabricRenderPresentation = "line"
)

// ContextFabricRenderAxisKind states whether Point.Label is a category name
// or an ISO-8601 date/date-time. A consumer positions a "time" axis by
// elapsed time, never by index -- evenly spacing unevenly-sampled
// observations is a claim the data does not make.
type ContextFabricRenderAxisKind string

const (
	ContextFabricRenderAxisCategory ContextFabricRenderAxisKind = "category"
	ContextFabricRenderAxisTime     ContextFabricRenderAxisKind = "time"
)

// ContextFabricRenderShapeRule is the CLOSED vocabulary of DETERMINISTIC
// selection rules. Exactly one rule produced any given shape, and the same
// (interpretation, cohort, claimed facts) always selects the same shapes --
// that is what makes "conditional on intent" a checkable property instead
// of a style guideline. Documented in
// docs/design/context-fabric-subject-model-and-cohort-answers.md §10.
type ContextFabricRenderShapeRule string

const (
	// ContextFabricRenderRuleCohortAttentionScore fires when the question
	// asked for a cohort AND at least one member carries a computed
	// score. Produces the per-member attention-score bars.
	ContextFabricRenderRuleCohortAttentionScore ContextFabricRenderShapeRule = "cohort_attention_score"
	// ContextFabricRenderRuleCohortDriverContribution fires when the
	// attention-score rule fired AND at least one ranked member carries
	// drivers. Produces the stacked per-driver contribution breakdown --
	// check 8's "scores help prioritize; drivers explain", drawn.
	ContextFabricRenderRuleCohortDriverContribution ContextFabricRenderShapeRule = "cohort_driver_contribution"
	// ContextFabricRenderRuleDatedFactTrend fires when the plan authorizes a
	// series and a claimed fact's row table DECLARES itself a time_series
	// (CHAOS-4637, declaration-driven; selector: datedFactTrendShapes in
	// internal/contextfabric/render_shapes.go, called from
	// SelectRenderShapes). The axis is the declared key and exactly one
	// declared measure (the claim's Field) is plotted; a table the producer
	// declared but cannot satisfy gets no chart, never a partial or
	// approximated one. Produces the dated line series.
	//
	// History: CHAOS-4616 withdrew the rule because deciding which columns of
	// a row table are measures and which are dimensions cannot be done from
	// the table alone -- three successive inferences were each defeated in
	// review, and the last shipped rule drew a line across two different work
	// scopes measured once each as though one had changed over time.
	// CHAOS-4627 put the declaration on the wire and CHAOS-4637 made the rule
	// read it, so there is no geometry left to infer from.
	ContextFabricRenderRuleDatedFactTrend ContextFabricRenderShapeRule = "dated_fact_trend"
	// ContextFabricRenderRuleInvestmentThemeShare is the part-of-whole of one
	// subject's investment (CHAOS-4610): one point per row of the claimed
	// fact's theme_breakdown table, with the row's share as the value. It is
	// the only rule that yields a treemap, and a treemap has no other rule.
	// Declared here ahead of its producer, as the vocabulary always is: the
	// wire widens first, so consumers can pin it before a shape is emitted.
	ContextFabricRenderRuleInvestmentThemeShare ContextFabricRenderShapeRule = "investment_theme_share"
	// ContextFabricRenderRuleComparisonSideBySide is the side-by-side table of an
	// explicit comparison (CHAOS-12513): one series per compared subject, keyed
	// by the subject's canonical id, and one point per measure, labelled with the
	// served fact_field word of that measure. Each point copies one claim's own
	// value, so the table restates what the answer claims and adds no number. It
	// is the only rule that yields a table, and a table has no other rule.
	// Declared ahead of its producer: the wire widens first.
	ContextFabricRenderRuleComparisonSideBySide ContextFabricRenderShapeRule = "comparison_side_by_side"
	// ContextFabricRenderRuleInvestmentSubcategoryShare is the two-ring
	// part-of-whole of one subject's investment (CHAOS-12554): the inner ring is
	// the canonical themes and the outer ring the canonical subcategories of
	// each, all from one claimed fact's subcategory_breakdown table. It is the
	// only rule that yields a sunburst, and a sunburst has no other rule.
	// Declared ahead of its producer: the wire widens first.
	ContextFabricRenderRuleInvestmentSubcategoryShare ContextFabricRenderShapeRule = "investment_subcategory_share"
	// ContextFabricRenderRuleInvestmentRepositoryFlow is one team's investment as
	// flows from its repositories to the themes (CHAOS-12578; the CHAOS-4610
	// sankey): every link of one claimed fact's repository_theme_breakdown table
	// with effort above 0. It fires only on a team's allocate_investment frame that
	// asks by repository (children_of_scope, member kind repository). It is the
	// only rule that yields a sankey, and a sankey has no other rule. Declared
	// ahead of its producer: the wire widens first.
	ContextFabricRenderRuleInvestmentRepositoryFlow ContextFabricRenderShapeRule = "investment_repository_flow"
	// ContextFabricRenderRuleLandscapePosition is where teams stand on two
	// landscape measures on one day (CHAOS-12623; the CHAOS-4611 quadrant): x is
	// a team's lines of churn and y its delivery units, both over the 30 days to
	// that day, each a raw value one claim states. It shows position, not cause
	// and not movement (the daily series serves movement). It is the only rule
	// that yields a quadrant, and a quadrant has no other rule. Declared ahead of
	// its producer: the wire widens first.
	ContextFabricRenderRuleLandscapePosition ContextFabricRenderShapeRule = "landscape_position"
)

// The quadrant's fields (CHAOS-12623). A point's x cites a claim of
// ContextFabricRenderLandscapeXField and its y a claim of
// ContextFabricRenderLandscapeYField, both of one team's landscape fact. The day
// they speak for is the ContextFabricRenderLandscapeDayField of the one row of
// that fact's ContextFabricRenderLandscapeTableField, which every claim on the
// fact carries. Only this pair is allowed: raw values, never a normalized
// coordinate, percentile or score (the platform guardrail for quadrants).
const (
	ContextFabricRenderLandscapeTableField = "area_breakdown"
	ContextFabricRenderLandscapeDayField   = "as_of_day"
	ContextFabricRenderLandscapeXField     = "churn_loc_30d"
	ContextFabricRenderLandscapeYField     = "delivery_units_30d"
)

// ContextFabricLandscapePositionLimitation is the one line an answer serves
// while it carries a landscape_position quadrant (CHAOS-12626; North Star check
// 7: a position establishes where a team stands, not why). It is
// service-authored, so no model caveat displaces it, and it leaves the answer
// with the quadrant.
const ContextFabricLandscapePositionLimitation = "The quadrant shows where each team stood on churn and delivery units on one day. It locates the teams; it does not explain why a team stands where it does."

// The sankey's table (CHAOS-12578). ContextFabricRenderRepositoryFlowTableField
// has one row per (repository, theme) link of a team's investment, keyed by
// ContextFabricRenderRepositoryField (the repository's canonical id) and
// ContextFabricRenderThemeField, with the repository's served word
// (ContextFabricRenderRepositoryWordField), the link's weighted effort
// (ContextFabricRenderEffortField) and the team's weighted effort in that theme
// (ContextFabricRenderThemeEffortField, the value theme_breakdown serves).
//
// ContextFabricRenderRepositoryFlowMinRepositories and
// ContextFabricRenderRepositoryFlowMaxRepositories bound the repositories a
// sankey draws: below 2 there is no flow to show, and past 64 the shape is
// refused rather than folded into an "other" node (lead ruling on CHAOS-4610).
const (
	ContextFabricRenderRepositoryFlowTableField      = "repository_theme_breakdown"
	ContextFabricRenderRepositoryField               = "repository"
	ContextFabricRenderRepositoryWordField           = "repository_label"
	ContextFabricRenderEffortField                   = "weighted_effort"
	ContextFabricRenderThemeEffortField              = "theme_weighted_effort"
	ContextFabricRenderRepositoryFlowMinRepositories = 2
	ContextFabricRenderRepositoryFlowMaxRepositories = 64
)

// ContextFabricRepositoryCanonicalIDPrefix starts every repository subject's
// canonical id. A sankey node of a repository row carries one, so a node is a
// repository and never a person (CHAOS-12578). The contextfabric minter uses
// this constant.
const ContextFabricRepositoryCanonicalIDPrefix = "repository:"

// The sunburst's table (CHAOS-12554). ContextFabricRenderSubcategoryTableField
// has one row per canonical subcategory, keyed by
// ContextFabricRenderSubcategoryField, with the row's theme
// (ContextFabricRenderThemeField), the subcategory's share
// (ContextFabricRenderThemeShareField) and the share of its whole theme
// (ContextFabricRenderThemeTotalField, the value theme_breakdown serves).
//
// ContextFabricRenderSunburstThemeSeriesKey keys the inner ring, whose points
// cite each theme's ContextFabricRenderThemeTotalField. Every other series of a
// sunburst is keyed by a canonical theme token, the ring parent of its points,
// which cite each subcategory's share.
const (
	ContextFabricRenderSubcategoryTableField  = "subcategory_breakdown"
	ContextFabricRenderSubcategoryField       = "subcategory"
	ContextFabricRenderThemeTotalField        = "theme_share"
	ContextFabricRenderSunburstThemeSeriesKey = "themes"
)

// ContextFabricRenderShareSumTolerance is the relative tolerance of a shape's
// part-sum check: parts read from the same work units as a served whole add up
// to that whole. A sunburst's subcategory shares add up to their theme's share
// (CHAOS-12554); on real data the two differ only by float rounding, 2.2e-16
// relative at most. It applies as |sum - whole| <= tolerance x max(|sum|,
// |whole|), and to that check alone: every plotted number is an exact copy of
// its cell (lead ruling on CHAOS-12554). Another shape with the same part-sum
// check uses this one constant.
const ContextFabricRenderShareSumTolerance = 1e-12

// contextFabricInvestmentSubcategories is the canonical subcategory list, the
// one the ops taxonomy registry (internal/jobs/workgraph/units/taxonomy.go)
// generates. A subcategory belongs to the theme before its first dot. Its
// words are served from the label registry (ContextFabricVocabularySubcategory),
// never from a map a consumer keeps (CHAOS-4690).
var contextFabricInvestmentSubcategories = [...]string{
	"feature_delivery.customer",
	"feature_delivery.enablement",
	"feature_delivery.roadmap",
	"maintenance.debt",
	"maintenance.refactor",
	"maintenance.upgrade",
	"operational.incident_response",
	"operational.on_call",
	"operational.support",
	"quality.bugfix",
	"quality.reliability",
	"quality.testing",
	"risk.compliance",
	"risk.security",
	"risk.vulnerability",
}

// ContextFabricInvestmentSubcategoryVocabulary returns the canonical
// subcategories in published order.
func ContextFabricInvestmentSubcategoryVocabulary() [len(contextFabricInvestmentSubcategories)]string {
	return contextFabricInvestmentSubcategories
}

// ContextFabricInvestmentSubcategoryTheme returns the theme of a canonical
// subcategory. A token outside the canonical list has none.
func ContextFabricInvestmentSubcategoryTheme(subcategory string) (string, bool) {
	for _, member := range contextFabricInvestmentSubcategories {
		if member == subcategory {
			theme, _, _ := strings.Cut(subcategory, ".")
			return theme, true
		}
	}
	return "", false
}

// ContextFabricRenderThemeTableField is the claimed fact table an
// investment_theme_share shape plots, ContextFabricRenderThemeShareField is the
// row field its points cite, and ContextFabricRenderThemeField is the row field
// its labels come from.
const (
	ContextFabricRenderThemeTableField = "theme_breakdown"
	ContextFabricRenderThemeShareField = "share"
	ContextFabricRenderThemeField      = "theme"
)

// contextFabricInvestmentThemeLabels is the closed map from a canonical theme
// token to its served word. The themes are fixed by the product model, so the
// words are too, and a consumer never maps a token itself (CHAOS-4690).
var contextFabricInvestmentThemeLabels = map[string]string{
	"feature_delivery": "Feature Delivery",
	"operational":      "Operational / Support",
	"maintenance":      "Maintenance / Tech Debt",
	"quality":          "Quality / Reliability",
	"risk":             "Risk / Security",
}

// ContextFabricInvestmentThemeLabel returns the served word of a canonical
// theme token. A token outside the closed set has no word.
func ContextFabricInvestmentThemeLabel(theme string) (string, bool) {
	word, known := contextFabricInvestmentThemeLabels[theme]
	return word, known
}

// ContextFabricInvestmentThemeCount is how many rows a whole theme table holds.
// The themes are fixed, and the producer emits every one, so a table with fewer
// was cut by a budget and is not the whole.
func ContextFabricInvestmentThemeCount() int {
	return len(contextFabricInvestmentThemeLabels)
}

// ContextFabricRenderValueUnit is the CLOSED vocabulary of units a shape may
// declare for its values. It exists so a consumer formats a number without
// parsing ValueLabel, which is a served word and no format: reading a unit out of
// display text is a guess. An empty unit claims nothing, and the value is shown
// as served.
type ContextFabricRenderValueUnit string

const (
	// ContextFabricRenderValueUnitRatio is a share from 0 to 1. Validation
	// holds every value of the shape to that range.
	ContextFabricRenderValueUnitRatio ContextFabricRenderValueUnit = "ratio"
)

// ContextFabricRenderPointSourceKind is the CLOSED vocabulary of places a
// chart number may come FROM. Each member names the fields a source of that
// kind must carry; validateRenderShapes resolves them.
type ContextFabricRenderPointSourceKind string

const (
	// ContextFabricRenderSourceCohortMemberScore resolves to
	// ContextFabricCohortMember.Score for SubjectCanonicalID. Requires
	// SubjectCanonicalID; every other field must be empty.
	ContextFabricRenderSourceCohortMemberScore ContextFabricRenderPointSourceKind = "cohort_member_score"
	// ContextFabricRenderSourceCohortDriverWeight resolves to that
	// member's ContextFabricCohortMemberDriver.WeightContributed for
	// Signal. Requires SubjectCanonicalID and Signal.
	ContextFabricRenderSourceCohortDriverWeight ContextFabricRenderPointSourceKind = "cohort_driver_weight_contributed"
	// ContextFabricRenderSourceClaimedFactRow resolves to
	// ClaimedFacts[ClaimID].Rows[RowIndex].Fields[Field]. Requires
	// ClaimID, RowIndex and Field.
	ContextFabricRenderSourceClaimedFactRow ContextFabricRenderPointSourceKind = "claimed_fact_row"
	// ContextFabricRenderSourceClaimedFactValue resolves to
	// ClaimedFacts[ClaimID].Value. Requires ClaimID and Field, and Field must be
	// the claim's own Field. A derived claim does not resolve: its Field is not
	// looked up on a fact, so it has no stored value to copy.
	ContextFabricRenderSourceClaimedFactValue ContextFabricRenderPointSourceKind = "claimed_fact_value"
)

// Render shape bounds. Deliberately small: this contract exists to carry a
// handful of charts an answer genuinely warrants, not an arbitrary
// dashboard. PointsMaxCount matches ContextFabricClaimedFactMaxRows, the
// cap on the row table a trend is derived from, so a shape can never be
// asked to hold more points than its source table can hold rows.
const (
	ContextFabricRenderShapesMaxCount = 8
	ContextFabricRenderSeriesMaxCount = 8
	ContextFabricRenderPointsMaxCount = ContextFabricClaimedFactMaxRows
	ContextFabricRenderLabelMaxLength = 256
	// ContextFabricRenderSourceFieldMaxLength matches the row-key bound
	// validateScalarMap enforces on a claimed fact's row fields. A source
	// naming a longer field could never resolve to a real cell, so the two
	// bounds are the same bound and are written as one.
	ContextFabricRenderSourceFieldMaxLength = 128
	// ContextFabricRenderPointExactIntegerBound is the largest magnitude an
	// integer source may carry and still be plottable.
	//
	// Point.Value is a float64 (a chart axis is continuous; JSON has one
	// number type), and beyond 2^53 consecutive integers stop being
	// distinguishable in that type: 9007199254740993 and 9007199254740992
	// are the same float64. A source carrying such a value would let a
	// chart claim a DIFFERENT number than the row it cites while comparing
	// equal -- the exact failure the resolve-and-compare rule exists to
	// prevent, reintroduced by a silent cast. Such a point is refused
	// rather than approximated: a chart that cannot carry the number
	// faithfully must not carry it at all.
	ContextFabricRenderPointExactIntegerBound = int64(1) << 53
)

// ContextFabricRenderShape is ONE renderable shape a Context Fabric answer
// carries. See this file's header for the design and for why bars are a
// "series" presentation rather than their own Kind.
type ContextFabricRenderShape struct {
	// ShapeID is opaque and unique within one answer. No consumer parses
	// it; it exists so a consumer can key a rendered panel stably across
	// re-reads of the same result.
	ShapeID string                  `json:"shape_id"`
	Kind    ContextFabricRenderKind `json:"kind"`
	// Presentation is required for Kind "series" and MUST be empty for
	// every other kind -- an encoding for a payload that kind does not
	// carry is a contradiction on the wire, not a harmless extra.
	Presentation ContextFabricRenderPresentation `json:"presentation,omitempty"`
	SelectedBy   ContextFabricRenderShapeRule    `json:"selected_by"`
	// Title, AxisLabel and ValueLabel are SHORT, non-judgmental labels
	// built from closed vocabulary and canonical subject labels. They
	// never carry narration: a shape describes what is plotted, and the
	// judgment about it lives in the drivers.
	Title      string                      `json:"title"`
	AxisKind   ContextFabricRenderAxisKind `json:"axis_kind"`
	AxisLabel  string                      `json:"axis_label"`
	ValueLabel string                      `json:"value_label"`
	// ValueUnit is optional. See ContextFabricRenderValueUnit.
	ValueUnit ContextFabricRenderValueUnit `json:"value_unit,omitempty"`
	Series    []ContextFabricRenderSeries  `json:"series"`
}

// ContextFabricRenderSeries is one named line/bar family within a shape.
// Every series in a shape shares the shape's axis: Points are aligned by
// Label, and a series with no point for a given label has a genuine gap
// there -- never a zero. Zero-filling a missing observation is the same
// defect as zero-filling a missing day in an append-only daily table.
type ContextFabricRenderSeries struct {
	// Key is a stable, machine-readable series identity (a signal family
	// name, a row column name, or a metric name; on a comparison, the
	// subject's canonical id). Label is its display form.
	Key    string                     `json:"key"`
	Label  string                     `json:"label"`
	Points []ContextFabricRenderPoint `json:"points"`
}

// ContextFabricRenderPoint is one plotted number and its provenance.
type ContextFabricRenderPoint struct {
	// Label is the axis position: a category name, or an ISO-8601
	// date/date-time when the shape's AxisKind is "time".
	Label  string                         `json:"label"`
	Value  float64                        `json:"value"`
	Source ContextFabricRenderPointSource `json:"source"`
}

// ContextFabricRenderPointSource names where in THIS SAME document Value
// came from. Validation resolves it and requires exact float equality --
// see validateRenderShapes. Carrying provenance rather than only the value
// is what makes a tampered or re-derived chart number a rejected document
// instead of an undetectable one.
type ContextFabricRenderPointSource struct {
	Kind ContextFabricRenderPointSourceKind `json:"kind"`
	// SubjectCanonicalID names a cohort member (cohort_member_score,
	// cohort_driver_weight_contributed).
	SubjectCanonicalID string `json:"subject_canonical_id,omitempty"`
	// Signal names the member's driver family
	// (cohort_driver_weight_contributed).
	Signal string `json:"signal,omitempty"`
	// ClaimID, RowIndex and Field address one cell of a claimed fact's
	// row table (claimed_fact_row). RowIndex is a pointer so "row 0" is
	// distinguishable from "no row index given" -- the exact
	// zero-value-vs-absent trap that makes a bad source read as a valid
	// one. claimed_fact_value uses ClaimID and Field alone.
	ClaimID  string `json:"claim_id,omitempty"`
	RowIndex *int   `json:"row_index,omitempty"`
	Field    string `json:"field,omitempty"`
}
