package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

// This file closes the numeric-drift CLASS.
//
// Four separate rounds of review found the same defect in different places:
// a Go validator bound and its published JSON Schema bound disagreed, so a
// document could pass validation and still violate the contract it claims
// to satisfy (Go looser), or the contract promised something the service
// would reject (Go stricter). Each was fixed individually and the next one
// appeared somewhere else.
//
// TestSchemaAndGoBoundsAgree enumerates EVERY maxItems and maxLength in
// both canonical schemas and requires each to be accounted for: either it
// matches the Go bound that enforces it, or it appears in
// asymmetricBounds with a reason. A new bound added to either side without
// a decision fails the build.

// goBound records the Go-enforced value for one schema bound.
type goBound struct {
	// value is what the WRITE path enforces. Reads may accept more (see
	// contextFabricLegacyBounds) -- that asymmetry is deliberate and is
	// covered by its own tests, not this one.
	value int
	// why documents an intentional mismatch. Empty means the Go bound is
	// expected to equal the schema bound exactly.
	why string
}

// asymmetricBounds names schema bounds the behavioural probe cannot judge,
// with the reason. Every entry is a claim that a reviewer looked and found
// the two sides agree despite what the probe measured -- it is not a place
// to park a disagreement.
//
// It was empty until round 18, when enumerating the minimum-side keywords
// reached the first bound of this shape. That is worth keeping in mind
// before adding a second: every disagreement found before this one was a
// defect.
var asymmetricBounds = map[string]goBound{
	// An OPTIONAL string with a minLength. The Go field is
	// `model_identity,omitempty`, so an empty Go string serializes to an
	// ABSENT property -- and model_identity is not in VersionSet.required,
	// so absence is exactly what the schema permits. minLength binds only
	// a property that is present, which is precisely what Go enforces:
	// `v.ModelIdentity != "" && !validModelIdentity(...)`.
	//
	// The probe reports "Go accepts 0" because it can only drive the Go
	// value to the empty string, which it cannot distinguish from the
	// absent field that empty string actually becomes. The two sides
	// agree; the instrument cannot express the difference.
	"common#$defs.VersionSet.properties.model_identity.minLength": {
		value: 1,
		why:   "optional field: an empty Go string is an omitted JSON property, which the schema allows, and minLength binds only a present one",
	},
}

// knownDisagreement records a schema bound that genuinely does NOT equal
// what the Go write path enforces -- a real defect this file exists to
// surface, not something it can quietly resolve by picking a side.
// CHAOS-4867's full-population audit (main's ruling, 2026-09-02) found
// these fourteen. Which side moves is chris's call, filed as each
// disagreement's own producer-side ticket -- not fixed in this PR. Both
// numbers are pinned: a schema-side drift fails HERE (the walk compares
// bound.value against schemaValue, same as any other declarative entry);
// a Go-side drift fails in
// TestKnownDisagreementsGoSideStillMatchesRecordedValue below, which
// exercises the actual Validate call site (or, where the Go value comes
// from a named constant rather than a literal, the constant itself) at
// the recorded value.
type knownDisagreement struct {
	schemaValue int
	goValue     int
	why         string
}

var knownDisagreements = map[string]knownDisagreement{
	// Root cause: uniqueTrimmedStrings (validate_context_fabric_helpers.go)
	// hardcodes stringLengthBetween(value, 1, maximum) -- the floor is
	// never parameterized, so every caller gets floor 1 regardless of what
	// its own schema declares. Three callers declare a floor of 8.
	"common#$defs.CohortMemberDriver.properties.source_claimed_fact_ids.items.minLength": {8, 1, "uniqueTrimmedStrings hardcodes floor 1, validate_context_fabric_helpers.go:464; filed as its own producer-side ticket"},
	"common#$defs.DriverJudgment.properties.claimed_fact_ids.items.minLength":            {8, 1, "uniqueTrimmedStrings hardcodes floor 1, validate_context_fabric_helpers.go:464; filed as its own producer-side ticket"},
	"common#$defs.Finding.properties.claimed_fact_ids.items.minLength":                   {8, 1, "uniqueTrimmedStrings hardcodes floor 1, validate_context_fabric_helpers.go:464; filed as its own producer-side ticket"},
	// Root cause: boundedEvidenceRefs(x, 500, false) -- the ceiling is a
	// literal repeated at each of these four ingest-projection call sites
	// (validate_context_fabric_projection.go), every one 500, none 200.
	"common#$defs.ContentProjection.properties.evidence_ref_ids.maxItems":      {200, 500, "boundedEvidenceRefs(c.EvidenceRefIDs,500,false), validate_context_fabric_projection.go:168; filed as its own producer-side ticket (CHAOS-4895 family)"},
	"common#$defs.EntityProjection.properties.evidence_ref_ids.maxItems":       {200, 500, "boundedEvidenceRefs(e.EvidenceRefIDs,500,false), validate_context_fabric_projection.go:108; filed as its own producer-side ticket (CHAOS-4895 family)"},
	"common#$defs.EpisodeProjection.properties.evidence_ref_ids.maxItems":      {200, 500, "boundedEvidenceRefs(e.EvidenceRefIDs,500,false), validate_context_fabric_projection.go:178; filed as its own producer-side ticket (CHAOS-4895 family)"},
	"common#$defs.RelationshipProjection.properties.evidence_ref_ids.maxItems": {200, 500, "boundedEvidenceRefs(r.EvidenceRefIDs,500,false), validate_context_fabric_projection.go:137; filed as its own producer-side ticket (CHAOS-4895)"},
	// Independent per-field literals, one ingest Validate call site each.
	"common#$defs.ContentProjection.properties.body.minLength":           {1, 0, "stringLengthBetween(c.Body,0,100000), validate_context_fabric_projection.go:168; filed as its own producer-side ticket"},
	"common#$defs.ContentProjection.properties.content_digest.minLength": {16, 8, "stringLengthBetween(c.ContentDigest,8,256), validate_context_fabric_projection.go:168; filed as its own producer-side ticket"},
	"common#$defs.EpisodeProjection.properties.summary.maxLength":        {12000, 8000, "stringLengthBetween(TrimSpace(e.Summary),1,8000), validate_context_fabric_projection.go:178; filed as its own producer-side ticket"},
	"common#$defs.ProjectionTombstone.properties.kind.maxLength":         {128, 64, "stringLengthBetween(TrimSpace(t.Kind),1,64), validate_context_fabric_projection.go:205; filed as its own producer-side ticket"},
	"common#$defs.ProjectionTombstone.properties.reason.maxLength":       {1000, 2000, "stringLengthBetween(TrimSpace(t.Reason),1,2000), validate_context_fabric_projection.go:205; filed as its own producer-side ticket"},
	// Root cause: both derive from the closed structure-need-kind
	// vocabulary (ContextFabricStructureNeedKindCount = 5, see
	// context_fabric_structure_types.go), not from the schema's numbers.
	"result#properties.confirmed_structure.maxItems":      {4, 5, "len(r.ConfirmedStructure) > ContextFabricStructureNeedKindCount, validate_context_fabric_result.go:1531; filed as its own producer-side ticket"},
	"result#properties.structure_offer_snapshot.maxItems": {80, 100, "len(r.StructureOfferSnapshot) > ContextFabricStructureNeedKindCount*contextFabricStructureNeedsMaxOptions (5*20), validate_context_fabric_result.go:1549; filed as its own producer-side ticket"},
}

// TestSchemaAndGoBoundsAgree proves the write-path Go bounds match the
// published schema for every bound the answer surface depends on.
//
// Coverage is enforced from the SCHEMA side: the schemas are enumerated and
// every bound must resolve. That direction matters, because the failure
// mode this closes is a schema bound nobody remembered to mirror in Go.
func TestSchemaAndGoBoundsAgree(t *testing.T) {
	documents := schemaDocuments(t)

	// goBoundsByPath maps a schema bound to the value the write path
	// enforces. Paths not listed are covered by the structural checks
	// below rather than a numeric comparison.
	goBoundsByPath := map[string]int{
		// CHAOS-4690: CoverageDetail's own bounds, mapped to the write
		// path's constants/clauses (ContextFabricCoverageDetail.Validate).
		// count.minimum is Go's own "must be non-negative" clause;
		// source shares SourceObservation's 1..128 name bound; the two
		// kind arrays share the 32-entry cap the validator enforces.
		"common#$defs.CoverageDetail.properties.count.minimum":            0,
		"common#$defs.CoverageDetail.properties.raw.maxLength":            ContextFabricCoverageDetailRawMaxLength,
		"common#$defs.CoverageDetail.properties.source.minLength":         1,
		"common#$defs.CoverageDetail.properties.source.maxLength":         128,
		"common#$defs.CoverageDetail.properties.supported_kinds.maxItems": contextFabricCoverageDetailKindsMaxCount,
		"common#$defs.CoverageDetail.properties.skipped_kinds.maxItems":   contextFabricCoverageDetailKindsMaxCount,
		// Result-level answer text and collections.
		"result#properties.direct_judgment.maxLength":      contextFabricWriteBounds.judgmentLength,
		"result#properties.current_state.maxLength":        contextFabricWriteBounds.judgmentLength,
		"result#properties.deterministic_answer.maxLength": contextFabricWriteBounds.deterministicAnswerLength,
		"result#properties.limitations.maxItems":           contextFabricWriteBounds.narrativeCount,
		// The displaced count can never exceed the list it counts drops
		// from, so it derives from the same write bound rather than
		// naming a number of its own.
		"result#properties.limitations_displaced.maximum": contextFabricWriteBounds.narrativeCount,
		// The floor is mapped explicitly, not left to the pattern
		// classifier. The "result#properties." catch-all would otherwise
		// swallow it as "bounded by the shared helpers", which is not true
		// of this field: Go rejects a negative count with its own clause.
		// Round-18's ruled mutation (minimum 0 -> 1) passed against that
		// catch-all before this entry existed.
		"result#properties.limitations_displaced.minimum": 0,
		// CHAOS-4636: the answer plan's own bounds, every one mapped
		// explicitly rather than left to a pattern classifier. The floors
		// especially: Go rejects each of these with its own clause (see
		// ContextFabricAnswerPlanBudget.Validate and
		// ContextFabricPlanNarrowing.Validate), so a catch-all that excused
		// them as "bounded by a shared helper" would be describing
		// something untrue -- exactly the hole round 18's ruled 0 -> 1
		// mutation walked through on limitations_displaced above.
		"common#$defs.AnswerPlan.properties.render_kinds.maxItems":              ContextFabricAnswerPlanRenderKindsMaxCount,
		"common#$defs.AnswerPlan.properties.axes.maxItems":                      ContextFabricAnswerPlanAxesMaxCount,
		"common#$defs.AnswerPlan.properties.fact_kinds.maxItems":                ContextFabricFactKindCount,
		"common#$defs.AnswerPlan.properties.narrowing.maxItems":                 ContextFabricPlanNarrowingMaxCount,
		"common#$defs.AnswerPlanBudget.properties.max_items.minimum":            0,
		"common#$defs.AnswerPlanBudget.properties.max_serialized_bytes.minimum": 0,
		"common#$defs.AnswerPlanBudget.properties.max_members.minimum":          0,
		"common#$defs.AnswerPlanBudget.properties.synthesis_headroom.minimum":   0,
		"common#$defs.PlanNarrowing.properties.before.minimum":                  0,
		"common#$defs.PlanNarrowing.properties.after.minimum":                   0,
		// CHAOS-4636: a group's Total is its member count BEFORE narrowing,
		// and ContextFabricCohortGroup.Validate rejects a total below the
		// members it lists -- which makes a negative total impossible by a
		// clause of its own, not by a shared helper.
		"common#$defs.CohortGroup.properties.total.minimum":     0,
		"result#properties.limitations.items.maxLength":         contextFabricWriteBounds.narrativeLength,
		"result#properties.warnings.maxItems":                   contextFabricWriteBounds.narrativeCount,
		"result#properties.warnings.items.maxLength":            contextFabricWriteBounds.narrativeLength,
		"result#properties.drivers.maxItems":                    ContextFabricDriversMaxCount,
		"result#properties.remaining_work.maxItems":             ContextFabricRemainingWorkMaxCount,
		"result#properties.readiness_gaps.maxItems":             ContextFabricReadinessGapsMaxCount,
		"result#properties.conflicts.maxItems":                  ContextFabricConflictsMaxCount,
		"result#properties.claimed_facts.maxItems":              ContextFabricClaimedFactsMaxCount,
		"result#properties.evidence_ref_ids.maxItems":           ContextFabricEvidenceRefIDsMaxCount,
		"result#properties.strongest_pressures.items.maxLength": ContextFabricStrongestPressureMaxLength,

		// Shapes the answer surface copies or projects.
		"common#$defs.CohortMember.properties.inclusion_reasons.maxItems":                   contextFabricWriteBounds.cohortInclusionReasons,
		"common#$defs.CohortMember.properties.inclusion_reasons.items.maxLength":            contextFabricWriteBounds.cohortInclusionReasonLength,
		"common#$defs.SubjectCandidate.properties.matched_terms.maxItems":                   contextFabricWriteBounds.matchedTerms,
		"common#$defs.SubjectCandidate.properties.matched_terms.items.maxLength":            contextFabricWriteBounds.matchedTermLength,
		"common#$defs.SubjectCandidate.properties.match_reasons.maxItems":                   contextFabricWriteBounds.matchReasons,
		"common#$defs.SubjectCandidate.properties.match_reasons.items.maxLength":            contextFabricWriteBounds.matchReasonLength,
		"common#$defs.Coverage.properties.sources.maxItems":                                 contextFabricWriteBounds.coverageEntries,
		"common#$defs.Coverage.properties.degraded_reasons.maxItems":                        contextFabricWriteBounds.coverageEntries,
		"common#$defs.RelationshipPath.properties.why_relevant.maxLength":                   contextFabricWriteBounds.pathWhyRelevantLength,
		"common#$defs.RelationshipPath.properties.evidence_ref_ids.maxItems":                contextFabricWriteBounds.pathEvidenceRefs,
		"common#$defs.RelationshipPath.properties.nodes.maxItems":                           contextFabricRelationshipPathMaxNodes,
		"common#$defs.RelationshipPath.properties.edges.maxItems":                           contextFabricRelationshipPathMaxNodes - 1,
		"common#$defs.DriverJudgment.properties.evidence_ref_ids.maxItems":                  contextFabricWriteBounds.nestedEvidenceRefs,
		"common#$defs.Finding.properties.evidence_ref_ids.maxItems":                         contextFabricWriteBounds.nestedEvidenceRefs,
		"common#$defs.FactRequirement.properties.parameters.additionalProperties.maxLength": contextFabricWriteBounds.factParameterValueLength,
		"common#$defs.DriverJudgment.properties.title.maxLength":                            ContextFabricDriverTitleMaxLength,
		"common#$defs.DriverJudgment.properties.summary.maxLength":                          ContextFabricDriverSummaryMaxLength,
		"common#$defs.DriverJudgment.properties.qualification.maxLength":                    ContextFabricDriverQualificationMaxLength,
		"common#$defs.ClaimedFact.properties.field.maxLength":                               ContextFabricClaimedFieldMaxLength,
		// CHAOS-4637: the declared table. Mapped DECLARATIVELY rather than
		// probed for the same reason the $defs-nested entries above are:
		// ContextFabricClaimedFactTable is validated as a whole against
		// the rows beside it (validateClaimedFactTable), so perturbing one
		// field in isolation -- which the generic probe must do -- breaks
		// the key-present-on-every-row invariant and the probe would
		// attribute the resulting error to the wrong bound.
		"common#$defs.ClaimedFactTable.properties.field.minLength":          1,
		"common#$defs.ClaimedFactTable.properties.field.maxLength":          ContextFabricClaimedFieldMaxLength,
		"common#$defs.ClaimedFactTable.properties.key.minItems":             1,
		"common#$defs.ClaimedFactTable.properties.key.maxItems":             ContextFabricFactTableKeyMaxCount,
		"common#$defs.ClaimedFactTable.properties.key.items.minLength":      1,
		"common#$defs.ClaimedFactTable.properties.key.items.maxLength":      ContextFabricFactTableColumnMaxLength,
		"common#$defs.ClaimedFactTable.properties.measures.maxItems":        ContextFabricFactTableMeasuresMaxCount,
		"common#$defs.ClaimedFactTable.properties.measures.items.minLength": 1,
		"common#$defs.ClaimedFactTable.properties.measures.items.maxLength": ContextFabricFactTableColumnMaxLength,
		// CHAOS-4680: the third declared role, mapped declaratively for the
		// identical reason key/measures above are.
		"common#$defs.ClaimedFactTable.properties.observations.maxItems":        ContextFabricFactTableObservationsMaxCount,
		"common#$defs.ClaimedFactTable.properties.observations.items.minLength": 1,
		"common#$defs.ClaimedFactTable.properties.observations.items.maxLength": ContextFabricFactTableColumnMaxLength,
		"common#$defs.ClaimedFactTable.properties.order_by.minLength":           1,
		"common#$defs.ClaimedFactTable.properties.order_by.maxLength":           ContextFabricFactTableColumnMaxLength,
		"common#$defs.InterpretedQuestion.properties.subject_terms.maxItems":    contextFabricWriteBounds.interpretationTerms,
		"common#$defs.InterpretedQuestion.properties.comparison_terms.maxItems": contextFabricWriteBounds.interpretationTerms,
		// CHAOS-4398 PR2: CohortMemberDriver lives one level under
		// CohortMember.Drivers (a slice of a $defs-nested struct) AND its
		// own Validate() entangles Value/Weight/WeightContributed with the
		// enclosing member's Score (Sum(WeightContributed)==*Score) --
		// perturbing one field in isolation, which the generic probe needs
		// to do, cannot hold that invariant, so this is declaratively
		// mapped rather than probed, the same reasoning as the
		// $defs-nested entries above.
		"common#$defs.CohortMemberDriver.properties.value.minimum":              0,
		"common#$defs.CohortMemberDriver.properties.value.maximum":              1,
		"common#$defs.CohortMemberDriver.properties.weight.minimum":             0,
		"common#$defs.CohortMemberDriver.properties.weight_contributed.minimum": 0,
		// CHAOS-4398 PR3: concentration is only present/probeable for
		// investment_mix drivers (the allOf/if-then above already proves
		// that presence rule structurally); its own [0,1] range bound is
		// declaratively mapped for the SAME cross-field-entanglement
		// reason as value/weight/weight_contributed above.
		"common#$defs.CohortMemberDriver.properties.concentration.minimum": 0,
		"common#$defs.CohortMemberDriver.properties.concentration.maximum": 1,
		// Not probeable, and honestly so (codex round-9 F1): no valid
		// document can carry more fact_requirements than there are distinct
		// kinds, so the probe cannot build a control past the vocabulary and
		// reports unreachable instead of a rejection. Mapped explicitly here
		// rather than left to the result-level catch-all classification,
		// which is what let the 50-vs-64 drift sit unnoticed.
		"common#$defs.InterpretedQuestion.properties.fact_requirements.maxItems": ContextFabricFactRequirementsMaxCount,
		"common#$defs.FactRequirement.properties.parameters.maxProperties":       ContextFabricFactRequirementParametersMaxCount,
		// Also unprobeable, for the same honest reason (codex round-12):
		// Finding.kind is a closed vocabulary, so no filler string of any
		// length is a valid value and the probe cannot isolate the length
		// bound. Mapped rather than left to the Finding.properties
		// classification, which would have absorbed it silently -- the
		// measurement showed it sliding from proved into the residual
		// bucket the moment the vocabulary closed.
		"common#$defs.Finding.properties.kind.maxLength": ContextFabricFindingKindMaxLength,
		// CHAOS-4415: render-shape bounds. Mapped rather than probed --
		// a render shape is only reachable through a document whose
		// points must also RESOLVE against that same document's cohort
		// and claimed facts, so a generic probe cannot build a control
		// that isolates a length or count bound (it would be rejected by
		// the resolution rule first, scoring "unreachable" rather than a
		// proof). Each value is the named constant validateRenderShapes
		// itself enforces.
		"common#$defs.RenderShape.properties.series.minItems":         1,
		"common#$defs.RenderShape.properties.series.maxItems":         ContextFabricRenderSeriesMaxCount,
		"common#$defs.RenderSeries.properties.points.minItems":        1,
		"common#$defs.RenderSeries.properties.points.maxItems":        ContextFabricRenderPointsMaxCount,
		"common#$defs.RenderSeries.properties.key.minLength":          1,
		"common#$defs.RenderSeries.properties.key.maxLength":          ContextFabricRenderLabelMaxLength,
		"common#$defs.RenderPointSource.properties.signal.minLength":  1,
		"common#$defs.RenderPointSource.properties.signal.maxLength":  ContextFabricRenderLabelMaxLength,
		"common#$defs.RenderPointSource.properties.field.minLength":   1,
		"common#$defs.RenderPointSource.properties.field.maxLength":   ContextFabricRenderSourceFieldMaxLength,
		"common#$defs.RenderPointSource.properties.row_index.minimum": 0,
		// CHAOS-4413: claimed_facts_count/rows_count are not standalone
		// floor checks the generic probe can isolate -- validateCompleteness
		// enforces EXACT equality against len(claimed_facts) and the
		// summed row counts on the canonical result (never merely "not
		// negative"), and the projection's own validateCompleteness checks
		// non-negativity only because it cannot re-derive the unclamped
		// totals from its own budget-clamped key_facts. Mapped
		// declaratively, the same reasoning as the render-shape bounds
		// immediately above.
		"common#$defs.AnswerCompleteness.properties.claimed_facts_count.minimum": 0,
		"common#$defs.AnswerCompleteness.properties.rows_count.minimum":          0,
		"common#$defs.SubjectCandidate.properties.evidence_ref_ids.maxItems":     contextFabricWriteBounds.candidateEvidenceRefs,
		"common#$defs.CohortExclusion.properties.reason.maxLength":               contextFabricWriteBounds.cohortExclusionReasonLength,
		// Disproved as "schema-only" by boundProbes below: the validator
		// rejects a value one past each of these, so they are compared
		// numerically rather than excused (codex round-6 F1).
		"result#properties.result_id.maxLength":                     256,
		"result#properties.request_id.maxLength":                    256,
		"result#properties.question.maxLength":                      8000,
		"common#$defs.SubjectRef.properties.label.maxLength":        512,
		"common#$defs.SubjectRef.properties.canonical_id.maxLength": 256,
		// CHAOS-3900 W1: ContextFabricWindowClarification.Validate's own
		// bounds (validate_context_fabric_window.go) -- options is
		// required non-empty (minItems 1, "window clarification options
		// violate v1 bounds" on len==0) and capped at
		// contextFabricWindowClarificationMaxOptions.
		"common#$defs.WindowClarification.properties.options.maxItems": contextFabricWindowClarificationMaxOptions,
		"common#$defs.WindowClarification.properties.options.minItems": 1,
		// CHAOS-3900 P1: validate_context_fabric_structure.go's own
		// bounds. Every StructureNeeds offer list shares
		// contextFabricStructureNeedsMaxOptions; Missing is bounded by
		// the closed frame-member vocabulary's own size on both ends
		// (non-empty, at most one entry per member).
		"common#$defs.StructureNeeds.properties.kind_options.maxItems":      contextFabricStructureNeedsMaxOptions,
		"common#$defs.StructureNeeds.properties.anchor_options.maxItems":    contextFabricStructureNeedsMaxOptions,
		"common#$defs.StructureNeeds.properties.handle_options.maxItems":    contextFabricStructureNeedsMaxOptions,
		"common#$defs.StructureNeeds.properties.window_options.maxItems":    contextFabricStructureNeedsMaxOptions,
		"common#$defs.StructureNeeds.properties.accepted_grammars.maxItems": contextFabricStructureNeedsMaxOptions,
		// CHAOS-4012: candidate_options shares the SAME bound as every
		// other StructureNeeds offer list above.
		"common#$defs.StructureNeeds.properties.candidate_options.maxItems": contextFabricStructureNeedsMaxOptions,
		// CHAOS-4314: window_expand_options carries AT MOST ONE
		// recommendation (ContextFabricStructureNeeds.Validate's own
		// "window_expand_options violates v1 bounds" on len>1) --
		// deliberately NOT contextFabricStructureNeedsMaxOptions like every
		// sibling offer list above: this list can never grow past 1 by
		// construction (composeWindowExpandOption mints at most one).
		"common#$defs.StructureNeeds.properties.window_expand_options.maxItems": 1,
		"common#$defs.StructureNeeds.properties.missing.maxItems":               ContextFabricStructureNeedKindCount,
		"common#$defs.StructureNeeds.properties.missing.minItems":               1,
		"common#$defs.HandleOption.properties.value.minLength":                  1,
		"common#$defs.HandleOption.properties.value.maxLength":                  256,
		// matched_term_hash is a FIXED-length digest (min==max==24): not
		// probeable in either direction (a minimum probe one below 24 is
		// also one past the maximum, and vice versa), so mapped explicitly
		// rather than left for the probe to attempt and misreport.
		"common#$defs.AnchorOption.properties.matched_term_hash.minLength": 24,
		"common#$defs.AnchorOption.properties.matched_term_hash.maxLength": 24,
		// CHAOS-4042: AnchorOptionV2/StructureNeedsV2 share the exact same
		// Go-side bounds as their v1 counterparts above (identical wire
		// shape, only redemption meaning differs) -- see
		// ContextFabricAnchorOptionV2.Validate() and
		// ContextFabricStructureNeeds.Validate() (StructureNeedsV2 has no
		// separate Go type; the wire slice stays []ContextFabricAnchorOption
		// for both majors, so the SAME Validate() bounds this file already
		// proves for v1 apply -- these entries exist only because the JSON
		// Schema $defs are separate objects the probe walks independently).
		"common#$defs.AnchorOptionV2.properties.matched_term_hash.minLength":  24,
		"common#$defs.AnchorOptionV2.properties.matched_term_hash.maxLength":  24,
		"common#$defs.StructureNeedsV2.properties.kind_options.maxItems":      contextFabricStructureNeedsMaxOptions,
		"common#$defs.StructureNeedsV2.properties.anchor_options.maxItems":    contextFabricStructureNeedsMaxOptions,
		"common#$defs.StructureNeedsV2.properties.handle_options.maxItems":    contextFabricStructureNeedsMaxOptions,
		"common#$defs.StructureNeedsV2.properties.window_options.maxItems":    contextFabricStructureNeedsMaxOptions,
		"common#$defs.StructureNeedsV2.properties.accepted_grammars.maxItems": contextFabricStructureNeedsMaxOptions,
		"common#$defs.StructureNeedsV2.properties.candidate_options.maxItems": contextFabricStructureNeedsMaxOptions,
		// CHAOS-4314: mirrors StructureNeeds.window_expand_options.maxItems
		// above -- see that entry's own comment.
		"common#$defs.StructureNeedsV2.properties.window_expand_options.maxItems": 1,
		"common#$defs.StructureNeedsV2.properties.missing.maxItems":               ContextFabricStructureNeedKindCount,
		"common#$defs.StructureNeedsV2.properties.missing.minItems":               1,
		"common#$defs.HandleOption.properties.source_column.minLength":            1,
		"common#$defs.HandleOption.properties.source_column.maxLength":            128,
		"common#$defs.ConfirmedStructureEntry.properties.applied_value.minLength": 1,
		"common#$defs.ConfirmedStructureEntry.properties.applied_value.maxLength": 256,
		"common#$defs.StructureOfferSnapshotEntry.properties.rank.minimum":        0,
		// CHAOS-3972 P3: ContextFabricRequestedHandle.Validate's own
		// stringLengthBetween(h.Value, 1, 256) bound -- same reasoning
		// as HandleOption.value above (mapped explicitly, not probed).
		"common#$defs.RequestedHandle.properties.value.minLength": 1,
		"common#$defs.RequestedHandle.properties.value.maxLength": 256,
		// CHAOS-4867: InvestigationOptions was previously matched by
		// schemaOnlyBoundReason's "request-side shape" case, which excused
		// EVERY numeric bound on this shape as "bounded by the request
		// contract, not by result validation" -- a claim that is false for
		// this shape specifically: ContextFabricInvestigationOptions.Validate
		// (validate_context_fabric_request.go) numerically checks every one
		// of these six fields on the write path. That blanket exemption is
		// exactly how max_serialized_bytes.minimum went unchecked while a
		// same-named but different field, AnswerPlanBudget's own
		// max_serialized_bytes.minimum above, stayed mapped -- two fields
		// sharing a JSON name, one enumerated, one silently excused. Mapped
		// explicitly here instead, one entry per field the validator checks;
		// every value below is transcribed from the validator's own literals/
		// named constants, not chosen to make the schema agree with it.
		"common#$defs.InvestigationOptions.properties.max_subject_candidates.minimum": 1,
		"common#$defs.InvestigationOptions.properties.max_subject_candidates.maximum": 50,
		"common#$defs.InvestigationOptions.properties.max_cohort_members.minimum":     1,
		"common#$defs.InvestigationOptions.properties.max_cohort_members.maximum":     ContextFabricMaxCohortMembersLimit,
		"common#$defs.InvestigationOptions.properties.max_relationship_paths.minimum": 1,
		"common#$defs.InvestigationOptions.properties.max_relationship_paths.maximum": 250,
		"common#$defs.InvestigationOptions.properties.max_drivers.minimum":            1,
		"common#$defs.InvestigationOptions.properties.max_drivers.maximum":            50,
		"common#$defs.InvestigationOptions.properties.max_evidence_refs.minimum":      1,
		"common#$defs.InvestigationOptions.properties.max_evidence_refs.maximum":      500,
		"common#$defs.InvestigationOptions.properties.max_serialized_bytes.minimum":   ContextFabricSerializedBytesMin,
		"common#$defs.InvestigationOptions.properties.max_serialized_bytes.maximum":   ContextFabricSerializedBytesMax,
		// CHAOS-4867 (main's ruling, option A): the request-side substring
		// case that used to excuse InvestigationOptions above excused these
		// four sibling shapes with the exact same false claim. An audit
		// (schemaOnlyBoundReason's own doc comment) confirmed a Go Validate
		// numerically checks every one of these fourteen fields; every value
		// already agreed, so registering them (rather than deferring them)
		// carries no risk of silently changing a bound.
		"common#$defs.ConsumerInfo.properties.name.minLength":    1,
		"common#$defs.ConsumerInfo.properties.name.maxLength":    200,
		"common#$defs.ConsumerInfo.properties.surface.minLength": 1,
		"common#$defs.ConsumerInfo.properties.surface.maxLength": 200,
		// ContextFabricConversationTurn.Validate (validate_context_fabric_request.go)
		// trims the content before measuring it -- the length bound itself
		// is the same 1..12000 the schema states.
		"common#$defs.ConversationTurn.properties.content.minLength":              1,
		"common#$defs.ConversationTurn.properties.content.maxLength":              12000,
		"common#$defs.RequestedScope.properties.repository_slugs.maxItems":        200,
		"common#$defs.RequestedScope.properties.repository_slugs.items.minLength": 1,
		"common#$defs.RequestedScope.properties.repository_slugs.items.maxLength": 512,
		"common#$defs.RequestedScope.properties.subject_hints.maxItems":           50,
		// SubjectHint.id/label have no schema minLength (both optional,
		// zero-value-omitted the same way VersionSet.model_identity is in
		// asymmetricBounds above) -- only the ceiling is a shared bound.
		"common#$defs.SubjectHint.properties.id.maxLength":     256,
		"common#$defs.SubjectHint.properties.label.maxLength":  512,
		"common#$defs.SubjectHint.properties.source.minLength": 1,
		"common#$defs.SubjectHint.properties.source.maxLength": 64,
		// CHAOS-4867 (main's ruling, option A, full audit): the schema-only
		// classifier's remaining substring cases (opaque identifier, nested
		// shape, answer-surface shared helper, projection-batch ingest,
		// service-issued version, conditional restatement) are GONE -- every
		// path any of them used to match was individually audited against
		// its Go Validate (five parallel audits, cross-checked, zero gaps
		// against the population dump). These 283 entries are the ones a Go
		// Validate numerically checks; the values below already agree with
		// Go today. A genuine schema/Go value disagreement found during the
		// audit is NOT here -- see knownDisagreements below.
		"common#$defs.AcceptedGrammar.properties.pattern_id.maxLength":                            128,
		"common#$defs.AcceptedGrammar.properties.pattern_id.minLength":                            1,
		"common#$defs.AnchorBoundReceipt.properties.receipt_id.maxLength":                         256,
		"common#$defs.AnchorBoundReceipt.properties.receipt_id.minLength":                         8,
		"common#$defs.AnchorBoundReceipt.properties.result_id.maxLength":                          256,
		"common#$defs.AnchorBoundReceipt.properties.result_id.minLength":                          8,
		"common#$defs.AnchorOption.properties.canonical_id.maxLength":                             256,
		"common#$defs.AnchorOption.properties.canonical_id.minLength":                             1,
		"common#$defs.AnchorOption.properties.label.maxLength":                                    200,
		"common#$defs.AnchorOption.properties.label.minLength":                                    1,
		"common#$defs.AnchorOption.properties.option_id.maxLength":                                256,
		"common#$defs.AnchorOption.properties.option_id.minLength":                                1,
		"common#$defs.AnchorOption.properties.phrasing.maxLength":                                 200,
		"common#$defs.AnchorOption.properties.phrasing.minLength":                                 1,
		"common#$defs.AnchorOption.properties.prior_entry_id.maxLength":                           256,
		"common#$defs.AnchorOption.properties.prior_entry_id.minLength":                           1,
		"common#$defs.AnchorOption.properties.prior_version_id.maxLength":                         256,
		"common#$defs.AnchorOption.properties.prior_version_id.minLength":                         1,
		"common#$defs.AnchorOption.properties.receipt_id.maxLength":                               256,
		"common#$defs.AnchorOption.properties.receipt_id.minLength":                               8,
		"common#$defs.AnchorOptionV2.properties.canonical_id.maxLength":                           256,
		"common#$defs.AnchorOptionV2.properties.canonical_id.minLength":                           1,
		"common#$defs.AnchorOptionV2.properties.label.maxLength":                                  200,
		"common#$defs.AnchorOptionV2.properties.label.minLength":                                  1,
		"common#$defs.AnchorOptionV2.properties.option_id.maxLength":                              256,
		"common#$defs.AnchorOptionV2.properties.option_id.minLength":                              1,
		"common#$defs.AnchorOptionV2.properties.phrasing.maxLength":                               200,
		"common#$defs.AnchorOptionV2.properties.phrasing.minLength":                               1,
		"common#$defs.AnchorOptionV2.properties.prior_entry_id.maxLength":                         256,
		"common#$defs.AnchorOptionV2.properties.prior_entry_id.minLength":                         1,
		"common#$defs.AnchorOptionV2.properties.prior_version_id.maxLength":                       256,
		"common#$defs.AnchorOptionV2.properties.prior_version_id.minLength":                       1,
		"common#$defs.AnchorOptionV2.properties.receipt_id.maxLength":                             256,
		"common#$defs.AnchorOptionV2.properties.receipt_id.minLength":                             8,
		"common#$defs.AnswerPlan.properties.family_version.maxLength":                             64,
		"common#$defs.AnswerPlan.properties.family_version.minLength":                             1,
		"common#$defs.AuthorizationScope.anyOf.0.properties.repository_slugs.minItems":            1,
		"common#$defs.AuthorizationScope.anyOf.1.properties.project_ids.minItems":                 1,
		"common#$defs.AuthorizationScope.anyOf.2.properties.team_ids.minItems":                    1,
		"common#$defs.AuthorizationScope.properties.project_ids.items.maxLength":                  256,
		"common#$defs.AuthorizationScope.properties.project_ids.items.minLength":                  1,
		"common#$defs.AuthorizationScope.properties.project_ids.maxItems":                         200,
		"common#$defs.AuthorizationScope.properties.repository_slugs.items.maxLength":             512,
		"common#$defs.AuthorizationScope.properties.repository_slugs.items.minLength":             1,
		"common#$defs.AuthorizationScope.properties.repository_slugs.maxItems":                    200,
		"common#$defs.AuthorizationScope.properties.team_ids.items.maxLength":                     256,
		"common#$defs.AuthorizationScope.properties.team_ids.items.minLength":                     1,
		"common#$defs.AuthorizationScope.properties.team_ids.maxItems":                            200,
		"common#$defs.BoundSubjectReceipt.properties.receipt_id.maxLength":                        256,
		"common#$defs.BoundSubjectReceipt.properties.receipt_id.minLength":                        8,
		"common#$defs.BoundSubjectReceipt.properties.result_id.maxLength":                         256,
		"common#$defs.BoundSubjectReceipt.properties.result_id.minLength":                         8,
		"common#$defs.CandidateBoundReceipt.properties.receipt_id.maxLength":                      256,
		"common#$defs.CandidateBoundReceipt.properties.receipt_id.minLength":                      8,
		"common#$defs.CandidateBoundReceipt.properties.result_id.maxLength":                       256,
		"common#$defs.CandidateBoundReceipt.properties.result_id.minLength":                       8,
		"common#$defs.CandidateOption.properties.canonical_id.maxLength":                          256,
		"common#$defs.CandidateOption.properties.canonical_id.minLength":                          1,
		"common#$defs.CandidateOption.properties.label.maxLength":                                 200,
		"common#$defs.CandidateOption.properties.label.minLength":                                 1,
		"common#$defs.CandidateOption.properties.option_id.maxLength":                             256,
		"common#$defs.CandidateOption.properties.option_id.minLength":                             1,
		"common#$defs.CandidateOption.properties.phrasing.maxLength":                              200,
		"common#$defs.CandidateOption.properties.phrasing.minLength":                              1,
		"common#$defs.CandidateOption.properties.prior_entry_id.maxLength":                        256,
		"common#$defs.CandidateOption.properties.prior_entry_id.minLength":                        1,
		"common#$defs.CandidateOption.properties.prior_version_id.maxLength":                      256,
		"common#$defs.CandidateOption.properties.prior_version_id.minLength":                      1,
		"common#$defs.CandidateOption.properties.receipt_id.maxLength":                            256,
		"common#$defs.CandidateOption.properties.receipt_id.minLength":                            8,
		"common#$defs.ClaimedFact.properties.claim_id.maxLength":                                  256,
		"common#$defs.ClaimedFact.properties.claim_id.minLength":                                  8,
		"common#$defs.ClaimedFact.properties.rows.maxItems":                                       64,
		"common#$defs.ClaimedFact.properties.time_series_rows.maxItems":                           64,
		"common#$defs.ClaimedFactRow.properties.fields.maxProperties":                             32,
		"common#$defs.Cohort.properties.groups.maxItems":                                          250,
		"common#$defs.Cohort.properties.members.maxItems":                                         250,
		"common#$defs.CohortGroup.properties.member_canonical_ids.items.minLength":                1,
		"common#$defs.CohortMember.allOf.0.then.properties.drivers.maxItems":                      5,
		"common#$defs.CohortMember.allOf.0.then.properties.drivers.minItems":                      5,
		"common#$defs.CohortMember.allOf.1.then.properties.drivers.maxItems":                      4,
		"common#$defs.CohortMember.allOf.1.then.properties.drivers.minItems":                      3,
		"common#$defs.CohortMember.allOf.2.then.properties.drivers.maxItems":                      2,
		"common#$defs.CohortMember.properties.attention_rank.minimum":                             1,
		"common#$defs.CohortMember.properties.drivers.maxItems":                                   5,
		"common#$defs.CohortMember.properties.inclusion_reasons.minItems":                         1,
		"common#$defs.CohortMember.properties.missing_signals.maxItems":                           5,
		"common#$defs.CohortMember.properties.missing_signals.minItems":                           1,
		"common#$defs.CohortMember.properties.rank.minimum":                                       1,
		"common#$defs.CohortMember.properties.ranking_basis.items.maxLength":                      128,
		"common#$defs.CohortMember.properties.ranking_basis.items.minLength":                      1,
		"common#$defs.CohortMember.properties.ranking_basis.maxItems":                             16,
		"common#$defs.CohortMember.properties.score.maximum":                                      100,
		"common#$defs.CohortMember.properties.score.minimum":                                      0,
		"common#$defs.CohortMemberDriver.properties.source_claimed_fact_ids.items.maxLength":      256,
		"common#$defs.CohortMemberDriver.properties.source_claimed_fact_ids.maxItems":             250,
		"common#$defs.CohortMemberDriver.properties.threshold_labels.items.maxLength":             128,
		"common#$defs.CohortMemberDriver.properties.threshold_labels.items.minLength":             1,
		"common#$defs.CohortMemberDriver.properties.threshold_labels.maxItems":                    4,
		"common#$defs.ConfirmedStructureEntry.properties.prior_entry_id.maxLength":                256,
		"common#$defs.ConfirmedStructureEntry.properties.prior_entry_id.minLength":                1,
		"common#$defs.ConfirmedStructureEntry.properties.prior_result_id.maxLength":               256,
		"common#$defs.ConfirmedStructureEntry.properties.prior_result_id.minLength":               8,
		"common#$defs.ConfirmedStructureEntry.properties.prior_version_id.maxLength":              256,
		"common#$defs.ConfirmedStructureEntry.properties.prior_version_id.minLength":              1,
		"common#$defs.ConfirmedStructureEntry.properties.receipt_id.maxLength":                    256,
		"common#$defs.ConfirmedStructureEntry.properties.receipt_id.minLength":                    8,
		"common#$defs.ConsumerInfo.properties.version.maxLength":                                  200,
		"common#$defs.ConsumerInfo.properties.version.minLength":                                  1,
		"common#$defs.ContentProjection.properties.body.maxLength":                                100000,
		"common#$defs.ContentProjection.properties.content_digest.maxLength":                      256,
		"common#$defs.ContentProjection.properties.content_id.maxLength":                          256,
		"common#$defs.ContentProjection.properties.content_id.minLength":                          8,
		"common#$defs.ContentProjection.properties.evidence_ref_ids.items.maxLength":              256,
		"common#$defs.ContentProjection.properties.evidence_ref_ids.items.minLength":              8,
		"common#$defs.ContentProjection.properties.evidence_ref_ids.minItems":                     1,
		"common#$defs.ContentProjection.properties.source_version.maxLength":                      256,
		"common#$defs.ContentProjection.properties.source_version.minLength":                      1,
		"common#$defs.ConversationTurn.properties.turn_id.maxLength":                              256,
		"common#$defs.ConversationTurn.properties.turn_id.minLength":                              1,
		"common#$defs.Coverage.properties.details.maxItems":                                       100,
		"common#$defs.CoverageDetail.properties.detail_id.maxLength":                              64,
		"common#$defs.CoverageDetail.properties.detail_id.minLength":                              1,
		"common#$defs.CoverageDetail.properties.label.maxLength":                                  160,
		"common#$defs.CoverageDetail.properties.label.minLength":                                  1,
		"common#$defs.CoverageDetail.properties.phrasing.maxLength":                               400,
		"common#$defs.DriverJudgment.allOf.0.then.anyOf.0.properties.path_ids.minItems":           1,
		"common#$defs.DriverJudgment.allOf.0.then.anyOf.1.properties.evidence_ref_ids.minItems":   1,
		"common#$defs.DriverJudgment.properties.affected_subjects.minItems":                       1,
		"common#$defs.DriverJudgment.properties.claimed_fact_ids.items.maxLength":                 256,
		"common#$defs.DriverJudgment.properties.claimed_fact_ids.maxItems":                        250,
		"common#$defs.DriverJudgment.properties.confidence.maximum":                               1,
		"common#$defs.DriverJudgment.properties.confidence.minimum":                               0,
		"common#$defs.EntityProjection.properties.aliases.items.maxLength":                        512,
		"common#$defs.EntityProjection.properties.aliases.items.minLength":                        1,
		"common#$defs.EntityProjection.properties.aliases.maxItems":                               100,
		"common#$defs.EntityProjection.properties.evidence_ref_ids.items.maxLength":               256,
		"common#$defs.EntityProjection.properties.evidence_ref_ids.items.minLength":               8,
		"common#$defs.EntityProjection.properties.evidence_ref_ids.minItems":                      1,
		"common#$defs.EntityProjection.properties.previous_names.items.maxLength":                 512,
		"common#$defs.EntityProjection.properties.previous_names.items.minLength":                 1,
		"common#$defs.EntityProjection.properties.previous_names.maxItems":                        100,
		"common#$defs.EntityProjection.properties.properties.maxProperties":                       100,
		"common#$defs.EntityProjection.properties.provider_aliases.items.maxLength":               512,
		"common#$defs.EntityProjection.properties.provider_aliases.items.minLength":               1,
		"common#$defs.EntityProjection.properties.provider_aliases.maxItems":                      100,
		"common#$defs.EntityProjection.properties.provider_ids.additionalProperties.maxLength":    512,
		"common#$defs.EntityProjection.properties.provider_ids.additionalProperties.minLength":    1,
		"common#$defs.EntityProjection.properties.provider_ids.maxProperties":                     50,
		"common#$defs.EntityProjection.properties.source_version.maxLength":                       256,
		"common#$defs.EntityProjection.properties.source_version.minLength":                       1,
		"common#$defs.EpisodeProjection.properties.episode_id.maxLength":                          256,
		"common#$defs.EpisodeProjection.properties.episode_id.minLength":                          8,
		"common#$defs.EpisodeProjection.properties.evidence_ref_ids.items.maxLength":              256,
		"common#$defs.EpisodeProjection.properties.evidence_ref_ids.items.minLength":              8,
		"common#$defs.EpisodeProjection.properties.evidence_ref_ids.minItems":                     1,
		"common#$defs.EpisodeProjection.properties.goal.maxLength":                                4000,
		"common#$defs.EpisodeProjection.properties.goal.minLength":                                1,
		"common#$defs.EpisodeProjection.properties.outcome.maxLength":                             128,
		"common#$defs.EpisodeProjection.properties.outcome.minLength":                             1,
		"common#$defs.EpisodeProjection.properties.source_version.maxLength":                      256,
		"common#$defs.EpisodeProjection.properties.source_version.minLength":                      1,
		"common#$defs.EpisodeProjection.properties.summary.minLength":                             1,
		"common#$defs.Finding.properties.claimed_fact_ids.items.maxLength":                        256,
		"common#$defs.Finding.properties.claimed_fact_ids.maxItems":                               250,
		"common#$defs.Finding.properties.evidence_ref_ids.minItems":                               1,
		"common#$defs.Finding.properties.kind.minLength":                                          1,
		"common#$defs.HandleBoundReceipt.properties.receipt_id.maxLength":                         256,
		"common#$defs.HandleBoundReceipt.properties.receipt_id.minLength":                         8,
		"common#$defs.HandleBoundReceipt.properties.result_id.maxLength":                          256,
		"common#$defs.HandleBoundReceipt.properties.result_id.minLength":                          8,
		"common#$defs.HandleOption.properties.label.maxLength":                                    200,
		"common#$defs.HandleOption.properties.label.minLength":                                    1,
		"common#$defs.HandleOption.properties.option_id.maxLength":                                256,
		"common#$defs.HandleOption.properties.option_id.minLength":                                1,
		"common#$defs.HandleOption.properties.pattern_id.maxLength":                               128,
		"common#$defs.HandleOption.properties.pattern_id.minLength":                               1,
		"common#$defs.HandleOption.properties.phrasing.maxLength":                                 200,
		"common#$defs.HandleOption.properties.phrasing.minLength":                                 1,
		"common#$defs.HandleOption.properties.prior_entry_id.maxLength":                           256,
		"common#$defs.HandleOption.properties.prior_entry_id.minLength":                           1,
		"common#$defs.HandleOption.properties.prior_version_id.maxLength":                         256,
		"common#$defs.HandleOption.properties.prior_version_id.minLength":                         1,
		"common#$defs.HandleOption.properties.receipt_id.maxLength":                               256,
		"common#$defs.HandleOption.properties.receipt_id.minLength":                               8,
		"common#$defs.InterpretedQuestion.allOf.0.then.properties.clarification_reason.maxLength": 2000,
		"common#$defs.InterpretedQuestion.allOf.0.then.properties.clarification_reason.minLength": 1,
		"common#$defs.KindBoundReceipt.properties.receipt_id.maxLength":                           256,
		"common#$defs.KindBoundReceipt.properties.receipt_id.minLength":                           8,
		"common#$defs.KindBoundReceipt.properties.result_id.maxLength":                            256,
		"common#$defs.KindBoundReceipt.properties.result_id.minLength":                            8,
		"common#$defs.KindOption.properties.label.maxLength":                                      200,
		"common#$defs.KindOption.properties.label.minLength":                                      1,
		"common#$defs.KindOption.properties.option_id.maxLength":                                  256,
		"common#$defs.KindOption.properties.option_id.minLength":                                  1,
		"common#$defs.KindOption.properties.phrasing.maxLength":                                   200,
		"common#$defs.KindOption.properties.phrasing.minLength":                                   1,
		"common#$defs.KindOption.properties.prior_entry_id.maxLength":                             256,
		"common#$defs.KindOption.properties.prior_entry_id.minLength":                             1,
		"common#$defs.KindOption.properties.prior_version_id.maxLength":                           256,
		"common#$defs.KindOption.properties.prior_version_id.minLength":                           1,
		"common#$defs.KindOption.properties.receipt_id.maxLength":                                 256,
		"common#$defs.KindOption.properties.receipt_id.minLength":                                 8,
		"common#$defs.PriorSubjectReceiptDispositionEntry.properties.prior_result_id.maxLength":   256,
		"common#$defs.PriorSubjectReceiptDispositionEntry.properties.prior_result_id.minLength":   8,
		"common#$defs.PriorSubjectReceiptDispositionEntry.properties.receipt_id.maxLength":        256,
		"common#$defs.PriorSubjectReceiptDispositionEntry.properties.receipt_id.minLength":        8,
		"common#$defs.ProjectionTombstone.properties.canonical_id.maxLength":                      256,
		"common#$defs.ProjectionTombstone.properties.canonical_id.minLength":                      1,
		"common#$defs.ProjectionTombstone.properties.kind.minLength":                              1,
		"common#$defs.ProjectionTombstone.properties.reason.minLength":                            1,
		"common#$defs.ProjectionTombstone.properties.source_version.maxLength":                    256,
		"common#$defs.ProjectionTombstone.properties.source_version.minLength":                    1,
		"common#$defs.RelationshipEdge.properties.evidence_ref_ids.minItems":                      1,
		"common#$defs.RelationshipPath.properties.edges.minItems":                                 1,
		"common#$defs.RelationshipPath.properties.evidence_ref_ids.minItems":                      1,
		"common#$defs.RelationshipPath.properties.nodes.minItems":                                 2,
		"common#$defs.RelationshipProjection.properties.evidence_ref_ids.items.maxLength":         256,
		"common#$defs.RelationshipProjection.properties.evidence_ref_ids.items.minLength":         8,
		"common#$defs.RelationshipProjection.properties.evidence_ref_ids.minItems":                1,
		"common#$defs.RelationshipProjection.properties.properties.maxProperties":                 100,
		"common#$defs.RelationshipProjection.properties.relationship_id.maxLength":                256,
		"common#$defs.RelationshipProjection.properties.relationship_id.minLength":                8,
		"common#$defs.RelationshipProjection.properties.source_version.maxLength":                 256,
		"common#$defs.RelationshipProjection.properties.source_version.minLength":                 1,
		"common#$defs.RenderPoint.properties.label.maxLength":                                     256,
		"common#$defs.RenderPoint.properties.label.minLength":                                     1,
		"common#$defs.RenderPointSource.properties.claim_id.maxLength":                            256,
		"common#$defs.RenderPointSource.properties.claim_id.minLength":                            8,
		"common#$defs.RenderPointSource.properties.subject_canonical_id.maxLength":                256,
		"common#$defs.RenderPointSource.properties.subject_canonical_id.minLength":                1,
		"common#$defs.RenderSeries.properties.label.maxLength":                                    256,
		"common#$defs.RenderSeries.properties.label.minLength":                                    1,
		"common#$defs.RenderShape.properties.axis_label.maxLength":                                256,
		"common#$defs.RenderShape.properties.axis_label.minLength":                                1,
		"common#$defs.RenderShape.properties.shape_id.maxLength":                                  256,
		"common#$defs.RenderShape.properties.shape_id.minLength":                                  1,
		"common#$defs.RenderShape.properties.value_label.maxLength":                               256,
		"common#$defs.RenderShape.properties.value_label.minLength":                               1,
		"common#$defs.RequestedHandle.properties.pattern_id.maxLength":                            128,
		"common#$defs.RequestedHandle.properties.pattern_id.minLength":                            1,
		"common#$defs.RequestedScope.properties.project_ids.items.maxLength":                      256,
		"common#$defs.RequestedScope.properties.project_ids.items.minLength":                      1,
		"common#$defs.RequestedScope.properties.project_ids.maxItems":                             200,
		"common#$defs.RequestedScope.properties.team_ids.items.maxLength":                         256,
		"common#$defs.RequestedScope.properties.team_ids.items.minLength":                         1,
		"common#$defs.RequestedScope.properties.team_ids.maxItems":                                200,
		"common#$defs.ScalarValue.properties.string.maxLength":                                    4000,
		"common#$defs.StructureOfferSnapshotEntry.properties.offer_id.maxLength":                  256,
		"common#$defs.StructureOfferSnapshotEntry.properties.offer_id.minLength":                  1,
		"common#$defs.StructureOfferSnapshotEntry.properties.prior_entry_id.maxLength":            256,
		"common#$defs.StructureOfferSnapshotEntry.properties.prior_entry_id.minLength":            1,
		"common#$defs.StructureOfferSnapshotEntry.properties.prior_version_id.maxLength":          256,
		"common#$defs.StructureOfferSnapshotEntry.properties.prior_version_id.minLength":          1,
		"common#$defs.SubjectCandidate.properties.confidence.maximum":                             1,
		"common#$defs.SubjectCandidate.properties.confidence.minimum":                             0,
		"common#$defs.SubjectCandidate.properties.match_mechanisms.maxItems":                      6,
		"common#$defs.SubjectCandidate.properties.match_reasons.minItems":                         1,
		"common#$defs.SubjectResolution.properties.commit_decision_digests.maxItems":              250,
		"common#$defs.SubjectResolution.properties.prior_subject_receipt_dispositions.maxItems":   20,
		"common#$defs.WindowBoundReceipt.properties.receipt_id.maxLength":                         256,
		"common#$defs.WindowBoundReceipt.properties.receipt_id.minLength":                         8,
		"common#$defs.WindowBoundReceipt.properties.result_id.maxLength":                          256,
		"common#$defs.WindowBoundReceipt.properties.result_id.minLength":                          8,
		"common#$defs.WindowExpandOption.properties.candidate_label.maxLength":                    200,
		"common#$defs.WindowExpandOption.properties.candidate_label.minLength":                    1,
		"common#$defs.WindowExpandOption.properties.label.maxLength":                              200,
		"common#$defs.WindowExpandOption.properties.label.minLength":                              1,
		"common#$defs.WindowExpandOption.properties.option_id.maxLength":                          256,
		"common#$defs.WindowExpandOption.properties.option_id.minLength":                          1,
		"common#$defs.WindowExpandOption.properties.receipt_id.maxLength":                         256,
		"common#$defs.WindowExpandOption.properties.receipt_id.minLength":                         8,
		"common#$defs.WindowOption.properties.label.maxLength":                                    200,
		"common#$defs.WindowOption.properties.label.minLength":                                    1,
		"common#$defs.WindowOption.properties.option_id.maxLength":                                256,
		"common#$defs.WindowOption.properties.option_id.minLength":                                1,
		"common#$defs.WindowOption.properties.receipt_id.maxLength":                               256,
		"common#$defs.WindowOption.properties.receipt_id.minLength":                               8,
		"result#allOf.0.then.properties.direct_judgment.maxLength":                                4000,
		"result#allOf.0.then.properties.direct_judgment.minLength":                                1,
		"result#properties.evidence_ref_labels.additionalProperties.maxLength":                    160,
		"result#properties.evidence_ref_labels.additionalProperties.minLength":                    1,
		"result#properties.render_shapes.maxItems":                                                8,
	}

	discovered := schemaBounds(t, documents)
	if len(discovered) == 0 {
		t.Fatal("no schema bounds discovered; the enumeration is not working")
	}

	checked, proved, disagreed := 0, 0, 0
	exempted := make(map[string]bool, len(asymmetricBounds))
	disagreedSeen := make(map[string]bool, len(knownDisagreements))
	unmapped := make([]string, 0, len(discovered))
	for _, bound := range discovered {
		// PREFERRED: prove agreement behaviourally. A probe that accepts a
		// value AT the schema bound and rejects one past it has measured
		// Go's bound and shown it equals the schema's -- which is the
		// comparison, done without anyone transcribing a number (codex
		// round-8 F1). This is what makes the check derive from the
		// declaration rather than from a hand-maintained table.
		// A reviewed decision short-circuits the instrument. Consulted
		// BEFORE the probe, because the probe is exactly what an entry
		// here exists to overrule -- and because asymmetricBounds was
		// declared and never read until round 18, which nothing noticed
		// only because it was empty.
		if entry, exempt := asymmetricBounds[bound.path]; exempt {
			exempted[bound.path] = true
			if entry.why == "" {
				t.Errorf("%s is listed in asymmetricBounds with no reason; an unexplained exemption is indistinguishable from the defect", bound.path)
			}
			if entry.value != bound.value {
				t.Errorf("%s: asymmetricBounds records %d, the schema now says %d; the exemption was written against a different bound", bound.path, entry.value, bound.value)
			}
			continue
		}
		// A MINIMUM inverts the probe. "At the bound is accepted, one past
		// it is rejected" is the whole comparison, and for a minimum "past
		// it" means one BELOW, not one above (round-18 fix B). Probing a
		// minimum in the maximum direction measures nothing: a minLength
		// of 1 obviously accepts length 2, which reads as "Go accepts
		// beyond the schema" and is simply the wrong question.
		//
		// A minimum of 0 constrains nothing and has no value below it to
		// reject, so it is not probeable at all and falls through to the
		// declarative checks rather than manufacturing a proof.
		leaf := bound.path[strings.LastIndex(bound.path, ".")+1:]
		minimumSide := strings.HasPrefix(leaf, "min")
		probedValue := bound.value + 1
		if minimumSide {
			probedValue = bound.value - 1
		}
		if probe, probeable := genericProbe(bound.path); probeable && !(minimumSide && bound.value == 0) {
			atBound := probe.apply(bound.value)
			pastBound := probe.apply(probedValue)
			switch {
			case errors.Is(atBound, errProbeUnreachable) || errors.Is(pastBound, errProbeUnreachable):
				// UNREACHABLE IS NOT A REJECTION. A probe that cannot build
				// the control document returns an error indistinguishable
				// from a validator rejection, so treating it as one would
				// score "I could not test this" as "Go rejects N+1" -- the
				// same false green this file exists to prevent, one level up.
				//
				// It is a real case: fact_requirements cannot be driven past
				// the fact-kind vocabulary, because every entry needs a
				// distinct kind. Fall through to the declarative comparison
				// rather than claiming a proof the probe did not make.
			case atBound == nil && pastBound != nil:
				proved++
				continue
			case atBound == nil && pastBound == nil:
				// Go accepts beyond the schema: the service can emit a
				// document violating its own contract.
				direction := "but Go accepts"
				if minimumSide {
					direction = "as a minimum but Go accepts"
				}
				t.Errorf("%s: schema says %d %s %d; the service can emit a document that violates its own contract",
					bound.path, bound.value, direction, probedValue)
				continue
			case atBound != nil:
				// The probe cannot isolate this bound (a cross-field
				// invariant rejects the control). Fall through to the
				// declarative checks rather than claiming either result.
			}
		}
		// A KNOWN DISAGREEMENT is checked before goBoundsByPath because it
		// is the opposite claim: goBoundsByPath asserts the two sides
		// agree, this asserts they are known to disagree by a recorded
		// amount, on both sides, at once. Consulted before the probe stage
		// above would matter if either fell into "Go accepts beyond the
		// schema" -- confirmed by construction (see the CHAOS-4867 audit
		// notes) that none of these fourteen do; every one is either
		// unreachable to the generic probe or blocked by a cross-field
		// invariant, so it falls through here regardless of order.
		if entry, known := knownDisagreements[bound.path]; known {
			disagreedSeen[bound.path] = true
			disagreed++
			if bound.value != entry.schemaValue {
				t.Errorf("%s: knownDisagreements records the schema side as %d, the schema now says %d; the entry was written against a different bound (if this is a deliberate fix, update or remove the entry and say so)", bound.path, entry.schemaValue, bound.value)
			}
			continue
		}
		expected, mapped := goBoundsByPath[bound.path]
		if mapped {
			checked++
			if bound.value != expected {
				t.Errorf("%s: schema says %d, the Go write path enforces %d", bound.path, bound.value, expected)
			}
			continue
		}
		if reason := schemaOnlyBoundReason(bound.path); reason == "" {
			unmapped = append(unmapped, bound.path)
		}
	}
	sort.Strings(unmapped)
	if len(unmapped) > 0 {
		t.Errorf("%d schema bounds are neither proved by probe, mapped, nor classified as schema-only:\n  %s",
			len(unmapped), strings.Join(unmapped, "\n  "))
	}
	if proved == 0 {
		t.Fatal("no bound was proved behaviourally; the prober is not reaching anything")
	}
	// The denominator is reported explicitly: "classified the rest" hid how
	// large the residual bucket actually is, and that bucket is where the
	// round-9 fact_requirements drift was sitting.
	// The other side of the exemption list: an entry matching no
	// discovered bound describes nothing, and would quietly keep excusing
	// a path that no longer exists.
	for path := range asymmetricBounds {
		if !exempted[path] {
			t.Errorf("asymmetricBounds lists %q, which matches no schema bound; remove it rather than leaving an exemption that describes nothing", path)
		}
	}
	// Same discipline for knownDisagreements: an entry matching no
	// discovered bound is describing a disagreement that no longer exists
	// (the schema or the Go check moved, or the field was removed) and
	// must be updated or deleted, not left to silently stop checking
	// anything.
	for path := range knownDisagreements {
		if !disagreedSeen[path] {
			t.Errorf("knownDisagreements lists %q, which matches no schema bound; remove or update it rather than leaving a disagreement that describes nothing", path)
		}
	}
	t.Logf("%d schema bounds: %d proved behaviourally, %d compared declaratively, %d known disagreements, %d classified as schema-only",
		len(discovered), proved, checked, disagreed, len(discovered)-proved-checked-disagreed)
}

// chaos4867ProbeEvidenceRefIDs returns n distinct, valid evidence-ref-id
// strings (>=8 chars, trimmed, no "|") for driving boundedEvidenceRefs at
// an exact size.
func chaos4867ProbeEvidenceRefIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("chaos4867evref%08d", i)
	}
	return ids
}

// TestKnownDisagreementsGoSideStillMatchesRecordedValue is the Go-side half
// of the knownDisagreements proof (see that map's own doc comment above):
// the main walk in TestSchemaAndGoBoundsAgree pins the SCHEMA side (a
// mismatch between bound.value and entry.schemaValue fails there); this
// test pins the GO side, by exercising the actual Validate call site at
// the recorded goValue (or, where the value derives from a named constant
// rather than a literal, asserting the constant directly). A change to
// either side without updating the recorded pair fails one of the two
// tests by name -- that is the whole point of recording both.
func TestKnownDisagreementsGoSideStillMatchesRecordedValue(t *testing.T) {
	t.Parallel()

	// Root cause: uniqueTrimmedStrings (validate_context_fabric_helpers.go)
	// hardcodes stringLengthBetween(value, 1, maximum) -- covers three of
	// the fourteen disagreements (CohortMemberDriver, DriverJudgment,
	// Finding claimed/source_claimed_fact_ids item minLength).
	t.Run("uniqueTrimmedStrings floor is 1", func(t *testing.T) {
		if !uniqueTrimmedStrings([]string{"x"}, 256) {
			t.Fatal("a 1-character element was rejected; the recorded goValue=1 floor no longer holds")
		}
		if uniqueTrimmedStrings([]string{""}, 256) {
			t.Fatal("an empty element was accepted; sanity check on the floor probe itself failed")
		}
	})

	// Root cause: both derive from the closed structure-need-kind
	// vocabulary's size, not a magic literal.
	t.Run("confirmed_structure and structure_offer_snapshot bounds derive from the vocabulary", func(t *testing.T) {
		if ContextFabricStructureNeedKindCount != 5 {
			t.Fatalf("ContextFabricStructureNeedKindCount = %d; recorded goValue for confirmed_structure.maxItems is 5", ContextFabricStructureNeedKindCount)
		}
		if ContextFabricStructureNeedKindCount*contextFabricStructureNeedsMaxOptions != 100 {
			t.Fatalf("ContextFabricStructureNeedKindCount*contextFabricStructureNeedsMaxOptions = %d; recorded goValue for structure_offer_snapshot.maxItems is 100", ContextFabricStructureNeedKindCount*contextFabricStructureNeedsMaxOptions)
		}
	})

	t.Run("RelationshipProjection.evidence_ref_ids maxItems is 500", func(t *testing.T) {
		build := func(n int) ContextFabricRelationshipProjection {
			return ContextFabricRelationshipProjection{
				RelationshipID:  "chaos4867_relationship_probe",
				Type:            ContextFabricRelationshipRelatedTo,
				From:            ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "chaos4867_from", Label: "From"},
				To:              ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "chaos4867_to", Label: "To"},
				Derivation:      ContextFabricDerivationCanonicalStructured,
				EpistemicStatus: ContextFabricEpistemicObserved,
				Authorization:   ContextFabricAuthorizationScope{ProjectIDs: []string{"chaos4867_project"}},
				EvidenceRefIDs:  chaos4867ProbeEvidenceRefIDs(n),
				ObservedAt:      time.Now().UTC(),
				SourceVersion:   "chaos4867-probe-v1",
			}
		}
		if err := build(500).Validate(); err != nil {
			t.Fatalf("500 evidence refs rejected; recorded goValue is 500: %v", err)
		}
		if err := build(501).Validate(); err == nil {
			t.Fatal("501 evidence refs accepted; the recorded goValue=500 ceiling no longer holds")
		}
	})

	t.Run("EntityProjection.evidence_ref_ids maxItems is 500", func(t *testing.T) {
		build := func(n int) ContextFabricEntityProjection {
			return ContextFabricEntityProjection{
				Subject:        ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "chaos4867_entity_subject", Label: "Subject"},
				Authorization:  ContextFabricAuthorizationScope{ProjectIDs: []string{"chaos4867_project"}},
				EvidenceRefIDs: chaos4867ProbeEvidenceRefIDs(n),
				ObservedAt:     time.Now().UTC(),
				SourceVersion:  "chaos4867-probe-v1",
			}
		}
		if err := build(500).Validate(); err != nil {
			t.Fatalf("500 evidence refs rejected; recorded goValue is 500: %v", err)
		}
		if err := build(501).Validate(); err == nil {
			t.Fatal("501 evidence refs accepted; the recorded goValue=500 ceiling no longer holds")
		}
	})

	t.Run("ContentProjection body/content_digest/evidence_ref_ids", func(t *testing.T) {
		build := func(bodyLen, digestLen, evidenceCount int) ContextFabricContentProjection {
			return ContextFabricContentProjection{
				ContentID:      "chaos4867_content_probe",
				Subject:        ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "chaos4867_content_subject", Label: "Subject"},
				Title:          "Probe Title",
				Body:           strings.Repeat("x", bodyLen),
				ContentDigest:  strings.Repeat("d", digestLen),
				Authorization:  ContextFabricAuthorizationScope{ProjectIDs: []string{"chaos4867_project"}},
				EvidenceRefIDs: chaos4867ProbeEvidenceRefIDs(evidenceCount),
				ObservedAt:     time.Now().UTC(),
				SourceVersion:  "chaos4867-probe-v1",
				Untrusted:      true,
			}
		}
		if err := build(0, 8, 1).Validate(); err != nil {
			t.Fatalf("body length 0 (recorded goValue floor) rejected: %v", err)
		}
		if err := build(0, 7, 1).Validate(); err == nil {
			t.Fatal("content_digest length 7 accepted; recorded goValue floor for content_digest is 8")
		}
		if err := build(0, 8, 1).Validate(); err != nil {
			t.Fatalf("content_digest length 8 (recorded goValue floor) rejected: %v", err)
		}
		if err := build(0, 8, 500).Validate(); err != nil {
			t.Fatalf("500 evidence refs rejected; recorded goValue is 500: %v", err)
		}
		if err := build(0, 8, 501).Validate(); err == nil {
			t.Fatal("501 evidence refs accepted; the recorded goValue=500 ceiling no longer holds")
		}
	})

	t.Run("EpisodeProjection summary/evidence_ref_ids", func(t *testing.T) {
		started := time.Now().UTC()
		build := func(summaryLen, evidenceCount int) ContextFabricEpisodeProjection {
			return ContextFabricEpisodeProjection{
				EpisodeID:      "chaos4867_episode_probe",
				Subject:        ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "chaos4867_episode_subject", Label: "Subject"},
				Goal:           "Probe goal",
				Outcome:        "Probe outcome",
				Summary:        strings.Repeat("s", summaryLen),
				Authorization:  ContextFabricAuthorizationScope{ProjectIDs: []string{"chaos4867_project"}},
				EvidenceRefIDs: chaos4867ProbeEvidenceRefIDs(evidenceCount),
				StartedAt:      started,
				EndedAt:        started.Add(time.Hour),
				SourceVersion:  "chaos4867-probe-v1",
			}
		}
		if err := build(8000, 1).Validate(); err != nil {
			t.Fatalf("summary length 8000 (recorded goValue ceiling) rejected: %v", err)
		}
		if err := build(8001, 1).Validate(); err == nil {
			t.Fatal("summary length 8001 accepted; the recorded goValue=8000 ceiling no longer holds")
		}
		if err := build(100, 500).Validate(); err != nil {
			t.Fatalf("500 evidence refs rejected; recorded goValue is 500: %v", err)
		}
		if err := build(100, 501).Validate(); err == nil {
			t.Fatal("501 evidence refs accepted; the recorded goValue=500 ceiling no longer holds")
		}
	})

	t.Run("ProjectionTombstone kind/reason", func(t *testing.T) {
		build := func(kindLen, reasonLen int) ContextFabricProjectionTombstone {
			return ContextFabricProjectionTombstone{
				Kind:          strings.Repeat("k", kindLen),
				CanonicalID:   "chaos4867_tombstone_probe",
				Reason:        strings.Repeat("r", reasonLen),
				EffectiveAt:   time.Now().UTC(),
				SourceVersion: "chaos4867-probe-v1",
			}
		}
		if err := build(64, 1).Validate(); err != nil {
			t.Fatalf("kind length 64 (recorded goValue ceiling) rejected: %v", err)
		}
		if err := build(65, 1).Validate(); err == nil {
			t.Fatal("kind length 65 accepted; the recorded goValue=64 ceiling no longer holds")
		}
		if err := build(1, 2000).Validate(); err != nil {
			t.Fatalf("reason length 2000 (recorded goValue ceiling) rejected: %v", err)
		}
		if err := build(1, 2001).Validate(); err == nil {
			t.Fatal("reason length 2001 accepted; the recorded goValue=2000 ceiling no longer holds")
		}
	})

	// Cross-check: every path recorded in knownDisagreements above must be
	// exercised by one of the subtests here. A disagreement pinned on the
	// schema side but never re-derived on the Go side is only half proved.
	covered := map[string]bool{
		"common#$defs.CohortMemberDriver.properties.source_claimed_fact_ids.items.minLength": true,
		"common#$defs.DriverJudgment.properties.claimed_fact_ids.items.minLength":            true,
		"common#$defs.Finding.properties.claimed_fact_ids.items.minLength":                   true,
		"common#$defs.ContentProjection.properties.evidence_ref_ids.maxItems":                true,
		"common#$defs.EntityProjection.properties.evidence_ref_ids.maxItems":                 true,
		"common#$defs.EpisodeProjection.properties.evidence_ref_ids.maxItems":                true,
		"common#$defs.RelationshipProjection.properties.evidence_ref_ids.maxItems":           true,
		"common#$defs.ContentProjection.properties.body.minLength":                           true,
		"common#$defs.ContentProjection.properties.content_digest.minLength":                 true,
		"common#$defs.EpisodeProjection.properties.summary.maxLength":                        true,
		"common#$defs.ProjectionTombstone.properties.kind.maxLength":                         true,
		"common#$defs.ProjectionTombstone.properties.reason.maxLength":                       true,
		"result#properties.confirmed_structure.maxItems":                                     true,
		"result#properties.structure_offer_snapshot.maxItems":                                true,
	}
	for path := range knownDisagreements {
		if !covered[path] {
			t.Errorf("knownDisagreements[%q] has no Go-side re-derivation subtest in this file; add one or the Go side of this disagreement is unguarded", path)
		}
	}
	for path := range covered {
		if _, known := knownDisagreements[path]; !known {
			t.Errorf("this file's coverage map claims %q but knownDisagreements does not list it; remove the stale coverage entry", path)
		}
	}
}

// TestFactRequirementsBoundDerivesFromTheVocabulary pins the derivation
// itself (codex round-9 F1).
//
// fact_requirements is capped by the fact-kind vocabulary, because
// ContextFabricInterpretedQuestion.validate rejects a duplicate kind. That
// makes the count bound a CONSEQUENCE of the vocabulary, not an independent
// policy number -- so the published schema, the Go constant, and the enum
// must all move together. They did not: the schema said 50, Go said 64, and
// the vocabulary permitted 20.
//
// This is a declarative check by necessity. The behavioural prober cannot
// reach past the vocabulary to reject at N+1, and a probe that "proved" the
// bound by hitting the uniqueness rule instead would be measuring the wrong
// invariant -- which is exactly how the drift stayed green for nine rounds.
func TestFactRequirementsBoundDerivesFromTheVocabulary(t *testing.T) {
	documents := schemaDocuments(t)

	// The schema's own enum must be the vocabulary, in order.
	node := schemaNodeAt(t, documents, "common#$defs.FactRequirement.properties.kind")
	raw, ok := node["enum"].([]any)
	if !ok {
		t.Fatal("common#$defs.FactRequirement.properties.kind declares no enum")
	}
	vocabulary := ContextFabricFactKindVocabulary()
	published := make([]ContextFabricFactKind, 0, len(raw))
	for _, value := range raw {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("fact kind enum holds a non-string member %v", value)
		}
		published = append(published, ContextFabricFactKind(text))
	}
	if !slices.Equal(published, vocabulary[:]) {
		t.Errorf("the published fact-kind enum and the Go vocabulary disagree:\n  schema: %v\n  go:     %v", published, vocabulary)
	}

	// Every published kind must actually validate, and nothing else may.
	for _, kind := range published {
		if !validFactKind(kind) {
			t.Errorf("the schema publishes fact kind %q, which the validator rejects", kind)
		}
	}
	if validFactKind(ContextFabricFactKind("not_a_fact_kind")) {
		t.Error("validFactKind accepts a kind outside the closed vocabulary")
	}

	// And the count bound must be the vocabulary's size on both sides.
	if ContextFabricFactRequirementsMaxCount != ContextFabricFactKindCount {
		t.Errorf("ContextFabricFactRequirementsMaxCount is %d but the vocabulary holds %d kinds; a cap above the vocabulary can never be reached, and one below it silently forbids a legal interpretation",
			ContextFabricFactRequirementsMaxCount, ContextFabricFactKindCount)
	}
	bound := schemaNodeAt(t, documents, "common#$defs.InterpretedQuestion.properties.fact_requirements")
	value, ok := bound["maxItems"].(float64)
	if !ok {
		t.Fatal("fact_requirements declares no maxItems")
	}
	if int(value) != ContextFabricFactKindCount {
		t.Errorf("the schema caps fact_requirements at %d but only %d distinct kinds exist, so the contract promises a document the service always rejects",
			int(value), ContextFabricFactKindCount)
	}
}

type discoveredBound struct {
	path  string
	value int
}

// boundKeywords are the schema keywords that state a size the Go write path
// must agree with.
// "maximum" joins them for CHAOS-3746 round-17 finding 2. Enumerating
// only the collection and string keywords left every integer bound
// invisible: limitations_displaced shipped with a schema maximum of 250
// while the Go write path enforced 100, past a guard written precisely to
// catch that. A keyword this file does not know about is a bound nothing
// checks.
var boundKeywords = []string{"maxItems", "maxLength", "maxProperties", "maximum", "minItems", "minLength", "minimum"}

// schemaBounds enumerates every maxItems/maxLength in both canonical
// documents, as "<document>#<dotted path>.<keyword>".
func schemaBounds(t *testing.T, documents map[string]map[string]any) []discoveredBound {
	t.Helper()
	var found []discoveredBound
	var walk func(document string, node any, path string)
	walk = func(document string, node any, path string) {
		switch value := node.(type) {
		case map[string]any:
			// maxProperties counts too (codex round-9 F2). Enumerating only
			// maxItems/maxLength left every object-size bound invisible --
			// FactRequirement.parameters caps at 32 and nothing checked that
			// Go agreed, which is precisely the drift this file exists for.
			for _, keyword := range boundKeywords {
				if raw, ok := value[keyword].(float64); ok {
					found = append(found, discoveredBound{path: document + "#" + strings.TrimPrefix(path, ".") + "." + keyword, value: int(raw)})
				}
			}
			for key, child := range value {
				switch key {
				case "maxItems", "maxLength", "maxProperties", "maximum",
					"minItems", "minLength", "minimum",
					"description", "title", "$comment", "$schema", "$id":
					continue
				}
				walk(document, child, path+"."+key)
			}
		case []any:
			for i, child := range value {
				walk(document, child, path+"."+itoa(i))
			}
		}
	}
	for _, document := range []string{"result", "common"} {
		walk(document, documents[document], "")
	}
	sort.Slice(found, func(i, j int) bool { return found[i].path < found[j].path })
	return found
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// boundProbe tests one claim about a path by driving the field to a size
// and validating.
type boundProbe struct {
	apply func(size int) error
}

// shapeLocators maps a schema $defs name to a live instance of that shape
// inside a valid result document, so ONE generic prober can drive any bound
// on any of these shapes.
//
// A handful of locators instead of a bespoke closure per bound (codex
// round-8 F1): a new bound on an existing shape needs no new probe code,
// which is the difference between a mechanism and another hand-enumeration.
// Only a genuinely NEW shape requires an entry here.
var shapeLocators = map[string]func(*ContextFabricInvestigationResult) any{
	"SubjectRef":          func(r *ContextFabricInvestigationResult) any { return &r.SubjectResolution.Committed[0] },
	"SubjectCandidate":    func(r *ContextFabricInvestigationResult) any { return &r.SubjectResolution.Candidates[0] },
	"SubjectResolution":   func(r *ContextFabricInvestigationResult) any { return &r.SubjectResolution },
	"Cohort":              func(r *ContextFabricInvestigationResult) any { return r.Cohort },
	"CohortMember":        func(r *ContextFabricInvestigationResult) any { return &r.Cohort.Members[0] },
	"CohortExclusion":     func(r *ContextFabricInvestigationResult) any { return &r.Cohort.Exclusions[0] },
	"DriverJudgment":      func(r *ContextFabricInvestigationResult) any { return &r.Drivers[0] },
	"Finding":             func(r *ContextFabricInvestigationResult) any { return &r.RemainingWork[0] },
	"RelationshipPath":    func(r *ContextFabricInvestigationResult) any { return &r.Paths[0] },
	"RelationshipEdge":    func(r *ContextFabricInvestigationResult) any { return &r.Paths[0].Edges[0] },
	"ClaimedFact":         func(r *ContextFabricInvestigationResult) any { return &r.ClaimedFacts[0] },
	"SourceObservation":   func(r *ContextFabricInvestigationResult) any { return &r.Coverage.Sources[0] },
	"Coverage":            func(r *ContextFabricInvestigationResult) any { return &r.Coverage },
	"VersionSet":          func(r *ContextFabricInvestigationResult) any { return &r.Versions },
	"InterpretedQuestion": func(r *ContextFabricInvestigationResult) any { return &r.Interpretation },
	"FactRequirement":     func(r *ContextFabricInvestigationResult) any { return &r.Interpretation.FactRequirements[0] },
	"ScalarValue":         func(r *ContextFabricInvestigationResult) any { return &r.ClaimedFacts[0].Value },
}

// genericProbe builds a probe for any schema bound whose path names a shape
// in shapeLocators or the result root, navigating the Go document with the
// same property names the schema uses.
func genericProbe(path string) (boundProbe, bool) {
	document, rest, found := strings.Cut(path, "#")
	if !found {
		return boundProbe{}, false
	}
	keyword := rest[strings.LastIndex(rest, ".")+1:]
	if !slices.Contains(boundKeywords, keyword) {
		return boundProbe{}, false
	}
	rest = rest[:strings.LastIndex(rest, ".")]

	locate := func(r *ContextFabricInvestigationResult) any { return r }
	if document == "common" {
		trimmed, ok := strings.CutPrefix(rest, "$defs.")
		if !ok {
			return boundProbe{}, false
		}
		name, tail, _ := strings.Cut(trimmed, ".")
		locator, known := shapeLocators[name]
		if !known {
			return boundProbe{}, false
		}
		locate, rest = locator, tail
	}
	fieldPath := strings.ReplaceAll(rest, "properties.", "")
	fieldPath = strings.ReplaceAll(fieldPath, ".items", "[]")
	if fieldPath == "" {
		return boundProbe{}, false
	}
	return boundProbe{apply: func(size int) error {
		value := probeResult()
		if !driveField(reflect.ValueOf(locate(&value)), fieldPath, size, keyword) {
			return errProbeUnreachable
		}
		return value.Validate()
	}}, true
}

var errProbeUnreachable = fmt.Errorf("probe could not reach the field")

// driveField sets the named field to a value of the requested size: a
// string of that many runes, or a slice of that many unique entries.
func driveField(value reflect.Value, path string, size int, keyword string) bool {
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	segment, rest, _ := strings.Cut(path, ".")
	name, isElement := strings.CutSuffix(segment, "[]")
	field := fieldByJSONTag(value, name)
	if !field.IsValid() || !field.CanSet() {
		return false
	}
	if rest != "" {
		if field.Kind() == reflect.Slice {
			if field.Len() == 0 {
				return false
			}
			return driveField(field.Index(0), rest, size, keyword)
		}
		return driveField(field, rest, size, keyword)
	}
	// A field whose value is a CLOSED VOCABULARY cannot be driven to a
	// length at all: filler text of any size is rejected for not being a
	// member, so the probe would "reject" at both N and N+1 and measure
	// nothing (codex round-12). Type-checked, exactly like the fact
	// requirement case in uniquifyElement, so it cannot silently capture a
	// different shape's field that happens to be called "kind" -- Finding's
	// kind is the driver-category vocabulary, ClaimedFact's and
	// FactRequirement's are the fact-kind one.
	if rest == "" && value.Type() == reflect.TypeOf(ContextFabricFinding{}) && name == "kind" {
		return false
	}
	switch {
	case isElement && field.Kind() == reflect.Slice:
		if field.Len() == 0 {
			field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		}
		return driveScalar(field.Index(0), size)
	case field.Kind() == reflect.Map && keyword == "maxProperties":
		// Object-size bounds are driven by filling the map with distinct
		// keys (codex round-9 F2). Keys and values stay short so the probe
		// measures the PROPERTY COUNT and not a key/value length bound that
		// happens to sit on the same object.
		grown := reflect.MakeMapWithSize(field.Type(), size)
		for i := 0; i < size; i++ {
			key := reflect.ValueOf("p" + strconv.Itoa(i))
			value := reflect.ValueOf("probevalue")
			if !key.Type().AssignableTo(field.Type().Key()) || !value.Type().AssignableTo(field.Type().Elem()) {
				return false
			}
			grown.SetMapIndex(key, value)
		}
		field.Set(grown)
		return true
	case field.Kind() == reflect.Slice && keyword == "maxItems":
		grown := reflect.MakeSlice(field.Type(), size, size)
		for i := 0; i < size; i++ {
			if field.Len() > 0 {
				grown.Index(i).Set(field.Index(0))
			}
			if !uniquifyElement(grown.Index(i), i) {
				// The probe cannot build a valid element at this index, so
				// it cannot make a control document at this size. Reported
				// as unreachable, never as a rejection.
				return false
			}
		}
		field.Set(grown)
		return true
	default:
		return driveScalar(field, size)
	}
}

func driveScalar(value reflect.Value, size int) bool {
	if value.Kind() != reflect.String || !value.CanSet() {
		return false
	}
	value.SetString(strings.Repeat("x", size))
	return true
}

// uniquifyElement makes a duplicated slice element distinct, so a maxItems
// probe is rejected for LENGTH rather than for duplication. It reports
// whether a distinct element could be built at this index.
func uniquifyElement(value reflect.Value, index int) bool {
	suffix := strconv.Itoa(index)
	switch value.Kind() {
	case reflect.String:
		value.SetString("probevalue" + suffix)
	case reflect.Struct:
		// A fact requirement is made distinct by its KIND, not by an
		// identifier -- it has none (codex round-9 F1). Duplicating the same
		// requirement made the document fail the kind-uniqueness invariant at
		// BOTH N and N+1, so the length bound was never exercised and the
		// 50-vs-64 schema/Go drift was concealed behind a green probe.
		if value.Type() == reflect.TypeOf(ContextFabricFactRequirement{}) {
			if index >= ContextFabricFactKindCount {
				// Past the vocabulary there is no distinct kind left, so no
				// valid document of this size exists at all.
				return false
			}
			if field := fieldByJSONTag(value, "kind"); field.IsValid() && field.CanSet() {
				field.Set(reflect.ValueOf(ContextFabricFactKindVocabulary()[index]))
				return true
			}
			return false
		}
		for _, name := range []string{"canonical_id", "receipt_id", "driver_id", "finding_id", "path_id", "claim_id", "source"} {
			if field := fieldByJSONTag(value, name); field.IsValid() && field.Kind() == reflect.String && field.CanSet() {
				field.SetString("probevalue" + suffix)
				break
			}
		}
		if field := fieldByJSONTag(value, "rank"); field.IsValid() && field.CanSet() && field.Kind() == reflect.Int {
			field.SetInt(int64(index + 1))
		}
	}
	return true
}

func fieldByJSONTag(value reflect.Value, name string) reflect.Value {
	if value.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	for i := 0; i < value.NumField(); i++ {
		if strings.Split(value.Type().Field(i).Tag.Get("json"), ",")[0] == name {
			return value.Field(i)
		}
	}
	return reflect.Value{}
}

func probeResult() ContextFabricInvestigationResult {
	return closureResult()
}

// schemaOnlyBoundReason classifies a schema bound the Go validator does not
// numerically enforce, returning why. An empty return means unclassified,
// which fails the test.
//
// These are genuine: opaque identifiers, timestamps, enums, and structural
// wrappers are constrained by the schema alone, and duplicating their
// numbers in Go would create a second source of truth to drift against --
// the very problem this file exists to prevent.
// requestSideDeferredBounds lists exact schema paths under request-side
// shapes where a Go Validate numerically checks the field but this file
// does not map it declaratively into goBoundsByPath -- a deliberate,
// evidenced, TEMPORARY exemption, never a substring guess. It is empty
// today: the audit that built this mechanism (ruled by main 2026-09-02)
// found fourteen such paths -- ConsumerInfo, ConversationTurn,
// RequestedScope, SubjectHint -- and every one turned out cheap enough to
// register directly in goBoundsByPath instead (see that map, CHAOS-4867
// entries), so none needed to sit here. The mechanism stays: a path
// legitimately blocked on a follow-up (an entangled Validate, say, the
// same reason several $defs-nested entries in goBoundsByPath are mapped
// declaratively rather than probed) belongs here, cited the same way,
// rather than added back to a name-substring case. A path here that stops
// matching any discovered schema bound describes nothing and should be
// deleted, not left behind.
var requestSideDeferredBounds = map[string]string{}

// schemaOnlyBounds lists the paths a Go Validate genuinely does NOT
// numerically check -- confirmed by POSITIVE CONTROL, not asserted: each
// reason cites the site where Go READS the field (proving the search
// actually reached it) and the absence of a length/count check there. A
// disposition with no reading site is "unreachable in Go", a different and
// stronger claim this map does not make (main's ruling, 2026-09-02).
//
// This is the entire population: CHAOS-4867's full audit (five parallel
// passes over the 301 paths that used to fall through the old substring
// classifier below, cross-checked, zero gaps against the walk's own
// count) found these four and NO others. Every other path the substring
// cases used to excuse is either Go-checked (registered in goBoundsByPath)
// or a genuine schema/Go value disagreement (knownDisagreements, above).
var schemaOnlyBounds = map[string]string{
	"common#$defs.CohortGroup.properties.member_canonical_ids.items.maxLength":          "Go reads every member id (context_fabric_grouped_cohort.go:112 `for _, id := range g.MemberCanonicalIDs`) but only checks it non-empty and unique (:114-120); it never measures length against 512",
	"common#$defs.CohortGroup.properties.member_canonical_ids.maxItems":                 "Go reads len(g.MemberCanonicalIDs) at context_fabric_grouped_cohort.go:107 (`g.Total < len(...)`) and :119 (loop bound) but never compares the count itself to 250 -- the full Validate method (:96-123) has no such check",
	"common#$defs.CohortMemberDriver.allOf.6.then.properties.threshold_labels.maxItems": "Go reads d.ThresholdLabels at validate_context_fabric_result.go:738 (`len(d.ThresholdLabels) > bounds.cohortMemberDriverThresholdLabels`, a real cap of 4, not 0) and :741 (`strings.HasPrefix(label, d.Signal+\".\")` per label); this allOf branch's maxItems:0 is a structural CONSEQUENCE of the prefix check for a non-investment_mix signal (no label in the closed vocabulary can match a non-investment_mix prefix), not something Go compares to 0 directly",
	"result#properties.evidence_ref_labels.maxProperties":                               "Go reads len(r.EvidenceRefLabels) at context_fabric_coverage_detail.go:483 but requires EXACT equality with len(closure), not an independent ceiling -- 8192 is never compared against",
}

func schemaOnlyBoundReason(path string) string {
	// CHAOS-4867 (main's ruling, 2026-09-02, full population audit): this
	// function used to classify by NAME SUBSTRING -- eight cases, each
	// excusing every path containing a fragment like "_id." or "version."
	// with one blanket claim, none of it verified per path. That is
	// exactly the defect class this whole file exists to close: the
	// InvestigationOptions case excused a Go-checked bound this way, and
	// the audit that followed found the SAME failure in every other
	// substring case here too. All eight are gone. What replaces them:
	// requestSideDeferredBounds (a mechanism, currently empty) and
	// schemaOnlyBounds (below) are the ONLY schema-only exemptions, one
	// entry per exact path, each with a positive-control citation. A path
	// with no entry in either map, and no registration in goBoundsByPath
	// or knownDisagreements above, fails the test by name -- there is no
	// remaining way for an unaudited bound to pass silently.
	if reason, deferred := requestSideDeferredBounds[path]; deferred {
		return reason
	}
	return schemaOnlyBounds[path]
}

// TestFactKindVocabularyCannotBeMutatedByCallers closes codex round-10 F2.
//
// The vocabulary was an exported array VAR. An array var's elements are
// assignable, so any importing package could write one -- and the two
// consumers read it differently: validFactKind consults it live on every
// call, while the interpretation prompt renders it once at init. A single
// in-process write therefore desynchronized them, leaving the validator
// accepting a kind the prompt never advertised and the published schema does
// not contain. Demonstrated before the fix: assigning to element 0 made
// ContextFabricInterpretedQuestion.Validate accept "forged_kind" while the
// rendered prompt still listed "identity".
//
// The backing array is now unexported and reached only through
// ContextFabricFactKindVocabulary, which returns an ARRAY -- copied on
// return. The absence of a writable path is a COMPILE-TIME property, not a
// runtime one, so it cannot be red-tested from outside this package: the
// pre-fix expression `contractsv1.ContextFabricFactKinds[0] = x` no longer
// compiles because the identifier does not exist, and no exported symbol
// yields an alias to the backing array. What this test can and does check is
// the other half of that guarantee -- that the accessor hands back a copy
// rather than a window onto the declaration.
func TestFactKindVocabularyCannotBeMutatedByCallers(t *testing.T) {
	const forged = ContextFabricFactKind("forged_kind")

	vocabulary := ContextFabricFactKindVocabulary()
	if len(vocabulary) == 0 {
		t.Fatal("the vocabulary is empty")
	}
	original := vocabulary[0]

	// Write to the returned value the way a caller ranging over it might.
	vocabulary[0] = forged

	if validFactKind(forged) {
		t.Error("mutating the value returned by ContextFabricFactKindVocabulary changed what the validator accepts; the accessor is handing out an alias, not a copy")
	}
	if !validFactKind(original) {
		t.Errorf("mutating the returned value removed %q from the accepted set; the accessor is handing out an alias, not a copy", original)
	}
	if fresh := ContextFabricFactKindVocabulary(); fresh[0] != original {
		t.Errorf("a second call returned the mutated vocabulary (%q); the copy is not fresh per call", fresh[0])
	}

	// And the derived count stays tied to the declaration.
	if ContextFabricFactKindCount != len(ContextFabricFactKindVocabulary()) {
		t.Errorf("ContextFabricFactKindCount is %d but the vocabulary holds %d", ContextFabricFactKindCount, len(ContextFabricFactKindVocabulary()))
	}
}

// TestWindowShapeSchemaAndGoValidateAgree closes the CHAOS-3900 W1
// window-SHAPE class TestSchemaAndGoBoundsAgree above does not reach: that
// test enumerates numeric maxItems/maxLength/etc keywords, but the window
// defect class codex round 6 opened and round 7 reopened twice more is
// STRUCTURAL -- an anyOf/if/then combination that is laxer than
// validate_context_fabric_window.go's own Validate()/validate() methods.
// Two hand-audit passes (round 6's own fix, then round 7's re-review of
// that fix) each missed at least one instance of the same shape, which is
// exactly the pattern the house rule ("after the second boundary defect you
// enforce the invariant, not the instance") exists for: this table is that
// invariant. A future schema edit that reopens ANY of these gaps fails this
// test instead of waiting for a third codex round to notice.
//
// Every case drives BOTH sides independently -- the Go method the write
// path actually calls, and contractcheck.ValidateSerialized against the
// canonical schema wrapping the shape in an otherwise-valid document -- and
// asserts each against the test's own stated expectation, not merely
// against each other. Asserting only mutual agreement would pass if both
// sides regressed together in the same direction; asserting each against a
// known-correct expectation is what actually catches that.
func TestWindowShapeSchemaAndGoValidateAgree(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	assertParity := func(t *testing.T, wantValid bool, goErr, schemaErr error) {
		t.Helper()
		if goValid := goErr == nil; goValid != wantValid {
			t.Errorf("Go verdict: valid=%v (err=%v), want valid=%v", goValid, goErr, wantValid)
		}
		if schemaValid := schemaErr == nil; schemaValid != wantValid {
			t.Errorf("schema verdict: valid=%v (err=%v), want valid=%v", schemaValid, schemaErr, wantValid)
		}
	}

	// --- RequestedEvidenceWindow, via request.time_context.evidence_window ---
	requestedCases := []struct {
		name      string
		window    ContextFabricRequestedEvidenceWindow
		wantValid bool
	}{
		{"relative_id + start, no end (codex round 7 F1)", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowTrailing90D, Start: &start}, false},
		{"relative_id + end, no start (codex round 7 F1)", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowTrailing90D, End: &end}, false},
		{"relative_id alone", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowTrailing90D}, true},
		{"explicit bounds alone, no relative_id", ContextFabricRequestedEvidenceWindow{Start: &start, End: &end}, true},
		{"all_time alone", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowAllTime}, true},
		{"all_time with bounds", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowAllTime, Start: &start, End: &end}, false},
	}
	for _, tc := range requestedCases {
		t.Run("RequestedEvidenceWindow/"+tc.name, func(t *testing.T) {
			goErr := tc.window.validate()
			request := validContextFabricContractRequest()
			request.TimeContext.EvidenceWindow = &tc.window
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_fabric_investigation_request.v1.schema.json", encoded)
			assertParity(t, tc.wantValid, goErr, schemaErr)
		})
	}

	// --- EffectiveEvidenceWindow, via result.effective_evidence_window ---
	effectiveCases := []struct {
		name      string
		window    ContextFabricEffectiveEvidenceWindow
		wantValid bool
	}{
		{"relative_id + start, no end (codex round 7 F1)", ContextFabricEffectiveEvidenceWindow{Provenance: ContextFabricWindowInferredDefault, RelativeID: ContextFabricRelativeWindowTrailing30D, Start: &start}, false},
		{"relative_id + end, no start (codex round 7 F1)", ContextFabricEffectiveEvidenceWindow{Provenance: ContextFabricWindowInferredDefault, RelativeID: ContextFabricRelativeWindowTrailing30D, End: &end}, false},
		{"relative_id alone", ContextFabricEffectiveEvidenceWindow{Provenance: ContextFabricWindowInferredDefault, RelativeID: ContextFabricRelativeWindowTrailing30D}, true},
	}
	for _, tc := range effectiveCases {
		t.Run("EffectiveEvidenceWindow/"+tc.name, func(t *testing.T) {
			goErr := tc.window.validate()
			result := validContextFabricContractResult()
			result.EffectiveEvidenceWindow = &tc.window
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", encoded)
			assertParity(t, tc.wantValid, goErr, schemaErr)
		})
	}

	// --- WindowOption, via result.window_clarification.options[0] ---
	baseOption := ContextFabricWindowOption{ReceiptID: "winr_confirm00001", OptionID: "opt_1", Label: "a window option"}
	optionCases := []struct {
		name      string
		option    ContextFabricWindowOption
		wantValid bool
	}{
		{"neither relative_id nor bounds (codex round 7 F2)", baseOption, false},
		{"all_time with bounds (codex round 7 F2)", withOption(baseOption, func(o *ContextFabricWindowOption) {
			o.RelativeID = ContextFabricRelativeWindowAllTime
			o.Start, o.End = &start, &end
		}), false},
		{"relative_id + start, no end (codex round 7 F1/F2)", withOption(baseOption, func(o *ContextFabricWindowOption) {
			o.RelativeID = ContextFabricRelativeWindowTrailing30D
			o.Start = &start
		}), false},
		{"relative_id + end, no start (codex round 7 F1/F2)", withOption(baseOption, func(o *ContextFabricWindowOption) {
			o.RelativeID = ContextFabricRelativeWindowTrailing30D
			o.End = &end
		}), false},
		{"relative_id + both bounds (valid control)", withOption(baseOption, func(o *ContextFabricWindowOption) {
			o.RelativeID = ContextFabricRelativeWindowTrailing30D
			o.Start, o.End = &start, &end
		}), true},
		{"all_time alone (valid control)", withOption(baseOption, func(o *ContextFabricWindowOption) { o.RelativeID = ContextFabricRelativeWindowAllTime }), true},
		{"no relative_id, both bounds (valid control)", withOption(baseOption, func(o *ContextFabricWindowOption) { o.Start, o.End = &start, &end }), true},
	}
	for _, tc := range optionCases {
		t.Run("WindowOption/"+tc.name, func(t *testing.T) {
			goErr := tc.option.Validate()
			result := validContextFabricContractResult()
			result.WindowClarification = &ContextFabricWindowClarification{Options: []ContextFabricWindowOption{tc.option}}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", encoded)
			assertParity(t, tc.wantValid, goErr, schemaErr)
		})
	}

	// --- WindowClarification per-field uniqueness: the DOCUMENTED RESIDUAL
	// asymmetry (codex round 7 F3/F5), not a closed gap. Standard JSON
	// Schema (draft 2020-12) has no keyword for uniqueness on a derived
	// subset of fields, so uniqueItems (whole-object equality) cannot
	// reject two options that differ only in label -- see WindowClarification's
	// own schema description for the full explanation. This is pinned as an
	// EXPECTED disagreement, not silently left untested: if a future
	// schema-validator upgrade ever makes this expressible, tightening the
	// schema and flipping wantSchemaValid to false here (matching every
	// other case's shared expectation) is the signal to do it.
	uniquenessCases := []struct {
		name            string
		options         []ContextFabricWindowOption
		wantGoValid     bool
		wantSchemaValid bool
	}{
		{
			name: "duplicate receipt_id, differing label",
			options: []ContextFabricWindowOption{
				withOption(baseOption, func(o *ContextFabricWindowOption) {
					o.RelativeID = ContextFabricRelativeWindowTrailing30D
					o.Start, o.End = &start, &end
				}),
				withOption(baseOption, func(o *ContextFabricWindowOption) {
					o.OptionID, o.Label = "opt_2", "a differently-labeled window option"
					o.RelativeID = ContextFabricRelativeWindowTrailing90D
					o.Start, o.End = &start, &end
				}),
			},
			wantGoValid:     false,
			wantSchemaValid: true,
		},
		{
			name: "duplicate option_id, differing label",
			options: []ContextFabricWindowOption{
				withOption(baseOption, func(o *ContextFabricWindowOption) {
					o.RelativeID = ContextFabricRelativeWindowTrailing30D
					o.Start, o.End = &start, &end
				}),
				withOption(baseOption, func(o *ContextFabricWindowOption) {
					o.ReceiptID, o.Label = "winr_confirm00002", "a differently-labeled window option"
					o.RelativeID = ContextFabricRelativeWindowTrailing90D
					o.Start, o.End = &start, &end
				}),
			},
			wantGoValid:     false,
			wantSchemaValid: true,
		},
	}
	for _, tc := range uniquenessCases {
		t.Run("WindowClarification/"+tc.name, func(t *testing.T) {
			clarification := ContextFabricWindowClarification{Options: tc.options}
			goErr := clarification.Validate()
			if goValid := goErr == nil; goValid != tc.wantGoValid {
				t.Errorf("Go verdict: valid=%v (err=%v), want valid=%v", goValid, goErr, tc.wantGoValid)
			}
			result := validContextFabricContractResult()
			result.WindowClarification = &clarification
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", encoded)
			if schemaValid := schemaErr == nil; schemaValid != tc.wantSchemaValid {
				t.Errorf("schema verdict: valid=%v (err=%v), want valid=%v -- if this now differs, standard JSON Schema may have gained a way to express per-field uniqueness; see this test's own doc comment", schemaValid, schemaErr, tc.wantSchemaValid)
			}
		})
	}
}

// withOption returns a copy of base with mutate applied, so table entries
// above can start from one shared, already-valid receipt_id/option_id/label
// triple and vary only the window shape under test.
func withOption(base ContextFabricWindowOption, mutate func(*ContextFabricWindowOption)) ContextFabricWindowOption {
	mutate(&base)
	return base
}
