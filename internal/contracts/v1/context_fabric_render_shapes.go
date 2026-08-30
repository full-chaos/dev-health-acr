package v1

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
// STATUS (CHAOS-4616): of the three rules this file's vocabulary declares,
// two have producers -- the cohort attention-score bars and the per-driver
// contribution stack, both proven on live data. `dated_fact_trend` is
// WITHDRAWN; see ContextFabricRenderRuleDatedFactTrend's own comment.
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
// on Presentation to learn how to draw it. Quadrant/treemap/sunburst/
// sankey/burndown/forecast are separate Kinds because each genuinely needs
// a payload "series" cannot carry (x/y pairs, a hierarchy, flows,
// scope-vs-time, distribution bands); those payloads land with their
// producers, not now.

// ContextFabricRenderKind is the CLOSED vocabulary of renderable answer
// shapes. Every member is defined now; producers land per shape.
type ContextFabricRenderKind string

const (
	// ContextFabricRenderKindSeries is one or more named series of
	// (label, value) points over a shared categorical or time axis --
	// drawn as bars, stacked bars, or a line per Presentation. The only
	// kind with a producer in CHAOS-4415 slice 1.
	ContextFabricRenderKindSeries ContextFabricRenderKind = "series"
	// ContextFabricRenderKindTable is an explicitly table-shaped
	// rendering. Declared for vocabulary completeness; today a table
	// reaches consumers as ContextFabricClaimedFact.Rows or
	// ContextFabricProjectedCohort.RankingTable, and nothing emits this
	// kind.
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
	// ContextFabricRenderRuleDatedFactTrend is WITHDRAWN (CHAOS-4616): no
	// producer selects it. It stays in the vocabulary so a document from a
	// server that DID produce one still validates, and so the returning
	// producer needs no contract change.
	//
	// It was withdrawn because deciding which columns of a row table are
	// measures and which are dimensions cannot be done from the table
	// alone -- three successive inferences were each defeated in review,
	// and the last shipped rule drew a line across two different work
	// scopes measured once each as though one had changed over time. The
	// information exists at the producer and is not on the wire; carrying
	// it is CHAOS-4627.
	ContextFabricRenderRuleDatedFactTrend ContextFabricRenderShapeRule = "dated_fact_trend"
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
	Series     []ContextFabricRenderSeries `json:"series"`
}

// ContextFabricRenderSeries is one named line/bar family within a shape.
// Every series in a shape shares the shape's axis: Points are aligned by
// Label, and a series with no point for a given label has a genuine gap
// there -- never a zero. Zero-filling a missing observation is the same
// defect as zero-filling a missing day in an append-only daily table.
type ContextFabricRenderSeries struct {
	// Key is a stable, machine-readable series identity (a signal family
	// name, a row column name, or a metric name). Label is its display
	// form.
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
	// one.
	ClaimID  string `json:"claim_id,omitempty"`
	RowIndex *int   `json:"row_index,omitempty"`
	Field    string `json:"field,omitempty"`
}
