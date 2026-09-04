package v1

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// answerBoundTable is the single source for both measured fixtures.
//
// It starts deliberately incomplete: TestEveryResultFieldIsInTheBoundTable
// enumerates ContextFabricInvestigationResult by reflection and fails naming
// every field not listed here. That is the point -- the table is not something
// a person remembers to update, it is something the build refuses to let them
// forget. A field added to the result struct tomorrow fails this test until
// somebody states what its smallest and largest legal values are.
func answerBoundTable() []answerBound {
	return []answerBound{
		// --- envelope identity: fixed or externally supplied, no breachable bound ---
		{Field: "SchemaVersion", Why: "must equal ContextFabricInvestigationResultSchema exactly; a fixed identifier has no length to breach",
			Min: func(r *ContextFabricInvestigationResult) { r.SchemaVersion = ContextFabricInvestigationResultSchema },
			Max: func(r *ContextFabricInvestigationResult) { r.SchemaVersion = ContextFabricInvestigationResultSchema }},
		{Field: "ResultID", Why: "stringLengthBetween(id, 8, 256) -- the result validator uses LITERAL 8..256, NOT ContextFabricModelMintedIDMaxLength; the floor of 8 is why an irreducible answer cannot carry one-rune ids",
			Min: func(r *ContextFabricInvestigationResult) { r.ResultID = strings.Repeat(oneRune, resultIDMinRunes) },
			Max: func(r *ContextFabricInvestigationResult) { r.ResultID = escaped(resultIDMaxRunes) },
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.ResultID = escaped(resultIDMaxRunes + 1)
			}},
		{Field: "RequestID", Why: "same literal 8..256 bound as ResultID, asserted in the same validator predicate",
			Min: func(r *ContextFabricInvestigationResult) { r.RequestID = strings.Repeat(oneRune, resultIDMinRunes) },
			Max: func(r *ContextFabricInvestigationResult) { r.RequestID = escaped(resultIDMaxRunes) },
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.RequestID = escaped(resultIDMaxRunes + 1)
			}},
		{Field: "GeneratedAt", Why: "a timestamp has no length bound; pinned to a zero fractional second, which is both the shortest RFC3339Nano encoding and a stable one",
			Min: func(r *ContextFabricInvestigationResult) { r.GeneratedAt = fixedAnswerInstant },
			Max: func(r *ContextFabricInvestigationResult) { r.GeneratedAt = fixedAnswerInstant }},
		{Field: "Status", Why: "closed vocabulary; the shortest and longest members are the bounds, and a non-member is a different test's concern",
			Min: func(r *ContextFabricInvestigationResult) { r.Status = ContextFabricInvestigationComplete },
			Max: func(r *ContextFabricInvestigationResult) { r.Status = ContextFabricInvestigationComplete }},
		{Field: "Reused", Why: "bool; `true` encodes ONE BYTE SHORTER than `false`, so the byte-minimal value is true -- minimal means smallest serialized, not smallest-looking",
			Min: func(r *ContextFabricInvestigationResult) { r.Reused = true },
			Max: func(r *ContextFabricInvestigationResult) { r.Reused = false }},

		// --- the two request-shaped fields that dominate the maximal document ---
		{Field: "Question", Why: "rawBoundedText 1..8000 RUNES (validate_context_fabric_request.go); the worst-case rune costs 6 serialized bytes",
			Min:     func(r *ContextFabricInvestigationResult) { r.Question = oneRune },
			Max:     func(r *ContextFabricInvestigationResult) { r.Question = escaped(8000) },
			PastMax: func(r *ContextFabricInvestigationResult) { r.Question = escaped(8001) }},
		{Field: "Interpretation", Why: "COMPOSITE: RequestedJudgment 1..256; SubjectTerms/ComparisonTerms 50 each at 512 runes; FactRequirements 0..ContextFabricFactRequirementsMaxCount (which equals the fact-kind vocabulary size); ClarificationReason 0..2000. One PastMax cannot prove five bounds, so each inner bound has its own breach below",
			Min: func(r *ContextFabricInvestigationResult) { r.Interpretation = minimalInterpretation() },
			Max: func(r *ContextFabricInvestigationResult) { r.Interpretation = maximalInterpretation() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				i := maximalInterpretation()
				i.RequestedJudgment = escaped(ContextFabricRequestedJudgmentMaxLength + 1)
				r.Interpretation = i
			},
			Breaches: []answerBreach{
				{Name: "SubjectTerms count", Expect: "interpreted question violates v1 bounds",
					Mutate: func(r *ContextFabricInvestigationResult) {
						i := maximalInterpretation()
						i.SubjectTerms = repeatStrings(ContextFabricSubjectTermsMaxCount+1, 8)
						r.Interpretation = i
					}},
				{Name: "ComparisonTerms count", Expect: "interpreted question violates v1 bounds",
					Mutate: func(r *ContextFabricInvestigationResult) {
						i := maximalInterpretation()
						i.ComparisonTerms = repeatStrings(ContextFabricComparisonTermsMaxCount+1, 8)
						r.Interpretation = i
					}},
				{Name: "FactRequirements count", Expect: "interpreted question violates v1 bounds",
					Mutate: func(r *ContextFabricInvestigationResult) {
						i := maximalInterpretation()
						i.FactRequirements = append(i.FactRequirements, i.FactRequirements[0])
						r.Interpretation = i
					}},
				{Name: "ClarificationReason length", Expect: "interpreted question violates v1 bounds",
					Mutate: func(r *ContextFabricInvestigationResult) {
						i := maximalInterpretation()
						i.ClarificationReason = escaped(ContextFabricClarificationReasonMaxLength + 1)
						r.Interpretation = i
					}},
			}},

		// --- answer content ---
		{Field: "DirectJudgment", Why: "boundedText 1..ContextFabricDirectJudgmentMaxLength",
			Min: func(r *ContextFabricInvestigationResult) { r.DirectJudgment = oneRune },
			Max: func(r *ContextFabricInvestigationResult) {
				r.DirectJudgment = escaped(ContextFabricDirectJudgmentMaxLength)
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.DirectJudgment = escaped(ContextFabricDirectJudgmentMaxLength + 1)
			}},
		{Field: "CurrentState", Why: "boundedText 1..ContextFabricCurrentStateMaxLength WHEN PRESENT, but the field is optional -- only DirectJudgment is required for an answer-capable status, so the empty string is the true minimum (round 1 finding 1)",
			Min: func(r *ContextFabricInvestigationResult) { r.CurrentState = "" },
			Max: func(r *ContextFabricInvestigationResult) {
				r.CurrentState = escaped(ContextFabricCurrentStateMaxLength)
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.CurrentState = escaped(ContextFabricCurrentStateMaxLength + 1)
			}},
		{Field: "DeterministicAnswer", Why: "boundedText 1..ContextFabricDeterministicAnswerMaxLength",
			Min: func(r *ContextFabricInvestigationResult) { r.DeterministicAnswer = oneRune },
			Max: func(r *ContextFabricInvestigationResult) {
				r.DeterministicAnswer = escaped(ContextFabricDeterministicAnswerMaxLength)
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.DeterministicAnswer = escaped(ContextFabricDeterministicAnswerMaxLength + 1)
			}},
		{Field: "StrongestPressures", Why: "ContextFabricStrongestPressuresMaxCount entries of ContextFabricStrongestPressureMaxLength runes",
			Min: func(r *ContextFabricInvestigationResult) { r.StrongestPressures = []string{} },
			Max: func(r *ContextFabricInvestigationResult) {
				r.StrongestPressures = repeatStrings(ContextFabricStrongestPressuresMaxCount, ContextFabricStrongestPressureMaxLength)
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.StrongestPressures = repeatStrings(ContextFabricStrongestPressuresMaxCount+1, 1)
			}},
		{Field: "Limitations", Why: "ContextFabricLimitationsMaxCount entries of ContextFabricLimitationMaxLength runes. COUPLED: a nonzero LimitationsDisplaced additionally requires ONE entry to be service-authored, so the maximal list spends a slot on a short fixed string",
			Min: func(r *ContextFabricInvestigationResult) { r.Limitations = []string{} },
			Max: func(r *ContextFabricInvestigationResult) {
				r.Limitations = maximalLimitations()
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.Limitations = repeatStrings(ContextFabricLimitationsMaxCount+1, 1)
			}},
		{Field: "Warnings", Why: "ContextFabricWarningsMaxCount entries of ContextFabricWarningMaxLength runes",
			Min: func(r *ContextFabricInvestigationResult) { r.Warnings = []string{} },
			Max: func(r *ContextFabricInvestigationResult) {
				r.Warnings = repeatStrings(ContextFabricWarningsMaxCount, ContextFabricWarningMaxLength)
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.Warnings = repeatStrings(ContextFabricWarningsMaxCount+1, 1)
			}},
		{Field: "LimitationsDisplaced", Why: "non-negative; a nonzero value is legal ONLY when the list is full AND carries a service-authored limitation (validate_context_fabric_result.go:1433), so this field's maximum is not independent of Limitations",
			Min: func(r *ContextFabricInvestigationResult) { r.LimitationsDisplaced = 0 },
			Max: func(r *ContextFabricInvestigationResult) { r.LimitationsDisplaced = ContextFabricLimitationsMaxCount },
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.LimitationsDisplaced = ContextFabricLimitationsMaxCount + 1
			}},

		// --- charged collections ---
		{Field: "Drivers", Why: "ContextFabricDriversMaxCount; each driver's own fields bounded by the driver validator",
			Min:     func(r *ContextFabricInvestigationResult) { r.Drivers = []ContextFabricDriverJudgment{} },
			Max:     func(r *ContextFabricInvestigationResult) { r.Drivers = repeatDrivers(ContextFabricDriversMaxCount) },
			PastMax: func(r *ContextFabricInvestigationResult) { r.Drivers = repeatDrivers(ContextFabricDriversMaxCount + 1) }},
		{Field: "RemainingWork", Why: "ContextFabricRemainingWorkMaxCount findings",
			Min: func(r *ContextFabricInvestigationResult) { r.RemainingWork = []ContextFabricFinding{} },
			Max: func(r *ContextFabricInvestigationResult) {
				r.RemainingWork = repeatFindings(ContextFabricRemainingWorkMaxCount, maximalFinding)
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.RemainingWork = repeatFindings(ContextFabricRemainingWorkMaxCount+1, minimalFinding)
			}},
		{Field: "ReadinessGaps", Why: "ContextFabricReadinessGapsMaxCount findings",
			Min: func(r *ContextFabricInvestigationResult) { r.ReadinessGaps = []ContextFabricFinding{} },
			Max: func(r *ContextFabricInvestigationResult) {
				r.ReadinessGaps = repeatFindings(ContextFabricReadinessGapsMaxCount, maximalFinding)
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.ReadinessGaps = repeatFindings(ContextFabricReadinessGapsMaxCount+1, minimalFinding)
			}},
		{Field: "Conflicts", Why: "ContextFabricConflictsMaxCount findings",
			Min: func(r *ContextFabricInvestigationResult) { r.Conflicts = []ContextFabricFinding{} },
			Max: func(r *ContextFabricInvestigationResult) {
				r.Conflicts = repeatFindings(ContextFabricConflictsMaxCount, maximalFinding)
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.Conflicts = repeatFindings(ContextFabricConflictsMaxCount+1, minimalFinding)
			}},
		{Field: "ClaimedFacts", Why: "0..ContextFabricClaimedFactsMaxCount, ids unique. A NIL slice is rejected outright (validate_context_fabric_helpers.go:173), so the minimum is an EMPTY slice, not an absent one. COUPLED: combined fact content may not exceed ContextFabricClaimedFactCombinedContentBytesMax, which is why the maximal carries many small facts rather than few large ones",
			Min: func(r *ContextFabricInvestigationResult) { r.ClaimedFacts = []ContextFabricClaimedFact{} },
			Max: func(r *ContextFabricInvestigationResult) {
				r.ClaimedFacts = repeatClaimedFacts(ContextFabricClaimedFactsMaxCount)
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.ClaimedFacts = repeatClaimedFacts(ContextFabricClaimedFactsMaxCount + 1)
			}},
		{Field: "Paths", Why: "must be NON-NIL and 0..250; each path 2..51 unique nodes with WhyRelevant at 2000 runes and 200 evidence refs. COUPLED: len(Edges) must equal len(Nodes)-1 AND edge i must run exactly Nodes[i]->Nodes[i+1], so the edge list is determined by the nodes, not independently maximizable. Excluded from the ITEM budget by the 4523 rule, but it is bytes on the wire",
			Min: func(r *ContextFabricInvestigationResult) { r.Paths = []ContextFabricRelationshipPath{} },
			Max: func(r *ContextFabricInvestigationResult) { r.Paths = maximalPaths() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				paths := maximalPaths()
				r.Paths = append(paths, maximalPath(pathsMaxCount))
			}},
		{Field: "EvidenceRefIDs", Why: "ContextFabricEvidenceRefIDsMaxCount ids of ContextFabricEvidenceRefIDMaxLength runes",
			Min: func(r *ContextFabricInvestigationResult) { r.EvidenceRefIDs = []string{} },
			Max: func(r *ContextFabricInvestigationResult) {
				r.EvidenceRefIDs = repeatEvidenceRefs(ContextFabricEvidenceRefIDsMaxCount, ContextFabricEvidenceRefIDMaxLength)
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				r.EvidenceRefIDs = repeatEvidenceRefs(ContextFabricEvidenceRefIDsMaxCount+1, 8)
			}},

		// --- nested structures ---
		{Field: "SubjectResolution", Why: "Candidates 0..50 and Committed 0..250 (both LITERALS in validate_context_fabric_result.go:54, not named constants); both slices must be non-nil, candidate receipt ids unique, committed subjects unique",
			Min: func(r *ContextFabricInvestigationResult) { r.SubjectResolution = minimalResolution() },
			Max: func(r *ContextFabricInvestigationResult) { r.SubjectResolution = maximalResolution() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				res := maximalResolution()
				res.Committed = append(res.Committed, distinctSubject(committedSubjectsMaxCount))
				r.SubjectResolution = res
			}},
		{Field: "Coverage", Why: "Sources must be non-nil and 0..100; DegradedReasons 0..100 at ContextFabricCoverageDegradedReasonMaxLength, unique and trimmed. COUPLED: a source whose State is not available REQUIRES a reason, so state and reason cannot be maximized independently",
			Min: func(r *ContextFabricInvestigationResult) { r.Coverage = minimalCoverage() },
			Max: func(r *ContextFabricInvestigationResult) { r.Coverage = maximalCoverage() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				cov := maximalCoverage()
				cov.DegradedReasons = repeatStrings(coverageEntriesMaxCount+1, 8)
				r.Coverage = cov
			}},
		{Field: "Versions", Why: "eight required version strings at validVersion 1..256, plus an optional BackendVersion; every one is bounded, so any of them stepping past 256 must reject",
			Min: func(r *ContextFabricInvestigationResult) { r.Versions = minimalVersions() },
			Max: func(r *ContextFabricInvestigationResult) { r.Versions = maximalVersions() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				v := maximalVersions()
				v.ServiceVersion = escaped(257)
				r.Versions = v
			}},
		{Field: "Completeness", Why: "PART census, PART bounded array. The four counts are derived -- they must equal what the document carries -- but `outcomes` is a genuinely bounded list (ContextFabricPlanRequirementOutcomeMaxCount, validateAnswerOutcomes), so the two sides of this entry are NOT the same function. They were, when the block held only the census; leaving them so would have hidden a bounded array inside a field the table calls derived, and the maximal fixture would no longer have measured the maximal answer",
			Min:     deriveCompletenessMinimal,
			Max:     deriveCompletenessMaximal,
			PastMax: deriveCompletenessPastMax},

		// --- optional pointers and collections: absent is the byte minimum ---
		{Field: "AnswerPlan", Why: "optional pointer, so absent is the byte minimum; when present it carries every fact kind and up to ContextFabricPlanNarrowingMaxCount narrowing steps",
			Min:     func(r *ContextFabricInvestigationResult) { r.AnswerPlan = nil },
			Max:     func(r *ContextFabricInvestigationResult) { r.AnswerPlan = maximalPlan() },
			PastMax: func(r *ContextFabricInvestigationResult) { r.AnswerPlan = pastMaxPlan() }},
		{Field: "Cohort", Why: "optional pointer; absent is the byte minimum. Kind must be team or project, Members non-nil and 0..250, Rationale 1..4000, and Complete/Truncated are mutually exclusive. COUPLED: a member with RankingComputed false must carry NO ranking fields at all, so the ranked member variant is a different shape -- see maximalCohortMember; this max is a lower bound on a ranked cohort",
			Min: func(r *ContextFabricInvestigationResult) { r.Cohort = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.Cohort = maximalCohort() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				c := maximalCohort()
				c.Members = append(c.Members, maximalCohortMember(cohortMembersMaxCount))
				r.Cohort = c
			}},
		{Field: "EvidenceRefLabels", Why: "DERIVED, like Completeness: the map must hold exactly one entry per member of the result's own evidence-ref closure, each label trimmed and 1..ContextFabricCoverageDetailLabelMaxLength. It is therefore applied in the second pass, once every evidence ref on the document is final",
			Min: func(r *ContextFabricInvestigationResult) { r.EvidenceRefLabels = nil },
			Max: func(r *ContextFabricInvestigationResult) {
				deriveEvidenceRefLabels(r, escaped(ContextFabricCoverageDetailLabelMaxLength))
			},
			PastMax: func(r *ContextFabricInvestigationResult) {
				deriveEvidenceRefLabels(r, escaped(ContextFabricCoverageDetailLabelMaxLength))
				// Replace a key rather than ADD one: adding changes the map
				// SIZE, and the count check fires before the membership
				// check, so the proof would pass on the wrong predicate.
				for ref := range r.EvidenceRefLabels {
					delete(r.EvidenceRefLabels, ref)
					break
				}
				r.EvidenceRefLabels["cf_not_on_this_result"] = "x"
			}},
		{Field: "Temporal", Why: "MUTUALLY EXCLUSIVE with EffectiveEvidenceWindow, which is the real finding here: a temporal label is legal ONLY on a non-current axis, and an effective evidence window is legal ONLY on the current axis, so no valid document can carry both. The maximal interprets on the current axis and therefore cannot carry a temporal label at all -- structurally unreachable, not merely omitted. The PastMax proof is the coupling itself: attaching a label on the current axis rejects",
			Min:     func(r *ContextFabricInvestigationResult) { r.Temporal = nil },
			Max:     func(r *ContextFabricInvestigationResult) { r.Temporal = nil },
			PastMax: func(r *ContextFabricInvestigationResult) { r.Temporal = maximalTemporal(r.Interpretation.TimeContext) }},
		{Field: "EffectiveEvidenceWindow", Why: "optional pointer, legal ONLY on the current time axis -- the mirror of Temporal's rule, and the reason the two can never appear on the same document. COUPLED: the all_time sentinel must NOT carry explicit bounds, so the widest option uses a non-all_time relative id",
			Min: func(r *ContextFabricInvestigationResult) { r.EffectiveEvidenceWindow = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.EffectiveEvidenceWindow = maximalEffectiveWindow() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				w := maximalEffectiveWindow()
				w.RelativeID = ContextFabricRelativeWindowAllTime
				r.EffectiveEvidenceWindow = w
			}},
		{Field: "WindowClarification", Why: "optional pointer, but a PRESENT one may not be empty: options are 1..contextFabricWindowClarificationMaxOptions, receipt ids must carry the winr_ namespace prefix, and both receipt and option ids are unique within the result",
			Min: func(r *ContextFabricInvestigationResult) { r.WindowClarification = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.WindowClarification = maximalWindowClarification() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				c := maximalWindowClarification()
				c.Options = append(c.Options, c.Options[0])
				r.WindowClarification = c
			}},
		{Field: "StructureNeeds", Why: "optional pointer, but a PRESENT one must name at least one missing member: Missing is 1..ContextFabricStructureNeedKindCount and unique. Each of the six offer lists is capped at contextFabricStructureNeedsMaxOptions, and receipt/option ids must be unique ACROSS every list, not merely within one",
			Min: func(r *ContextFabricInvestigationResult) { r.StructureNeeds = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.StructureNeeds = maximalStructureNeeds() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				n := maximalStructureNeeds()
				n.KindOptions = append(n.KindOptions, n.KindOptions[0])
				r.StructureNeeds = n
			}},
		{Field: "ConfirmedStructure", Why: "0..ContextFabricStructureNeedKindCount with ONE entry per member -- the closed need-kind vocabulary caps this list, not a separate count. COUPLED: only source=receipt carries both a prior result id and a receipt id (carried forbids the receipt, every other source forbids both), so receipt is the widest legal entry",
			Min: func(r *ContextFabricInvestigationResult) { r.ConfirmedStructure = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.ConfirmedStructure = maximalConfirmedStructure() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				entries := maximalConfirmedStructure()
				r.ConfirmedStructure = append(entries, entries[0])
			}},
		{Field: "StructureOfferSnapshot", Why: "bounded PER MEMBER rather than by a flat cap: ContextFabricStructureNeedKindCount times each member's own mint-time offer cap (contextFabricStructureNeedsMaxOptions)",
			Min: func(r *ContextFabricInvestigationResult) { r.StructureOfferSnapshot = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.StructureOfferSnapshot = maximalOfferSnapshot() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				entries := maximalOfferSnapshot()
				r.StructureOfferSnapshot = append(entries, entries[0])
			}},
		{Field: "RenderShapes", Why: "THIRD DECLARED LOWER-BOUND AXIS, not a structural skip -- round 1 finding 3 corrected this. The contract DOES bound the list: ContextFabricRenderShapesMaxCount at validate_context_fabric_render_shapes.go:127. A valid result can carry shapes when its claimed facts provide numeric rows or its cohort is ranked and the plan authorizes the kind. This fixture carries neither (unranked cohort, no fact rows), so no shape here could resolve its plotted points and the empty list is the only value THIS document can hold. The bound is reachable; this construction just does not reach it",
			Min: func(r *ContextFabricInvestigationResult) { r.RenderShapes = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.RenderShapes = nil }},
	}
}

// TestEveryResultFieldIsInTheBoundTable is the guard that makes an omission
// impossible rather than unlikely.
//
// The four defects this whole file replaces were omissions and inheritances --
// a field the fixture never set, or set from a fixture built for another
// purpose. Reading the fixture could not reveal them, because what was wrong
// was what the fixture did NOT say. Enumerating the struct can.
func TestEveryResultFieldIsInTheBoundTable(t *testing.T) {
	t.Parallel()

	listed := map[string]int{}
	for _, bound := range answerBoundTable() {
		listed[bound.Field]++
	}

	resultType := reflect.TypeOf(ContextFabricInvestigationResult{})
	var missing []string
	for i := 0; i < resultType.NumField(); i++ {
		name := resultType.Field(i).Name
		switch listed[name] {
		case 0:
			missing = append(missing, name)
		case 1:
		default:
			t.Errorf("%s appears %d times in the bound table; each field is stated once", name, listed[name])
		}
	}
	for name := range listed {
		if _, ok := resultType.FieldByName(name); !ok {
			t.Errorf("the bound table names %q, which is not a field of the result: the table has drifted from the struct", name)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("%d of %d result fields are absent from the bound table:\n  %s\n\n"+
			"Every field must state its smallest and largest legal value, with the validator clause that fixes them. "+
			"A field with no bound to breach still gets an entry, with PastMax nil and the reason in Why -- an\n"+
			"exemption list is the shape this file exists to remove.",
			len(missing), resultType.NumField(), strings.Join(missing, "\n  "))
	}
}

// buildFromTable constructs a result by applying one side of every bound in the
// table. Because the table is the single source of truth and
// TestEveryResultFieldIsInTheBoundTable proves it names every field, neither
// fixture can silently omit a field: a new field on the struct fails the guard
// before it can reach these builders.
func buildFromTable(t *testing.T, pick func(answerBound) func(*ContextFabricInvestigationResult)) ContextFabricInvestigationResult {
	t.Helper()
	var r ContextFabricInvestigationResult
	table := answerBoundTable()
	for _, b := range table {
		apply := pick(b)
		if apply == nil {
			t.Fatalf("bound %q has no builder for this side", b.Field)
		}
		apply(&r)
	}
	// Completeness is a CENSUS of the finished document, not an independent
	// field: its counts must equal what the other fields ended up carrying.
	// A census cannot be built in table order, because entries applied after
	// it would change the very numbers it reports -- so it gets a second
	// pass, once every other field is final. It stays IN the table (and so
	// stays covered by the field guard); only its timing is special.
	for _, b := range table {
		if isDerivedField(b.Field) {
			pick(b)(&r)
		}
	}
	return r
}

// irreducibleAnswerBytes is the measured size of the smallest document the
// validators accept, built from answerBoundTable() with every field at its Min.
//
// It is ASSERTED, not logged. Round 1 finding 1: the earlier test only printed
// this number, so a Min that was not actually minimal could not fail anything --
// and two of them were not. The contract constant PR-a pins is derived from
// this value, so a silent drift here would ship a wrong floor.
// 1001 -> 1023 (+22), S7c: the completeness block gained a required `state`,
// so the smallest valid answer carries `"state":"not_derived"`. The outcome
// set contributes nothing here -- it is omitempty and the irreducible answer
// has no rows.
const irreducibleAnswerBytes = 1023

// maximalAnswerBytes is the largest document this table can construct. It is a
// LOWER bound on the true maximum -- see the lower-bound axes named in the
// table entries -- which is all the no-static-constant conclusion needs.
// 520841850 -> 521187082 (+345232), S7c: the maximal answer now carries the
// outcome set at its declared ceiling -- 200 rows, each with the identity at
// its maximum LEGAL encoding (the longest obligation, then two escaped
// segments filling the 256-character bound) and all three cause vocabularies
// present at their longest member.
const maximalAnswerBytes = 521187082

func TestIrreducibleAndMaximalFixturesAreValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
		pick func(answerBound) func(*ContextFabricInvestigationResult)
	}{
		{"irreducible", irreducibleAnswerBytes, func(b answerBound) func(*ContextFabricInvestigationResult) { return b.Min }},
		{"maximal", maximalAnswerBytes, func(b answerBound) func(*ContextFabricInvestigationResult) { return b.Max }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := buildFromTable(t, tc.pick)
			if err := r.Validate(); err != nil {
				t.Fatalf("%s fixture must be valid, got: %v", tc.name, err)
			}
			encoded, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			t.Logf("%s fixture: %d serialized bytes", tc.name, len(encoded))
			if len(encoded) != tc.want {
				t.Fatalf("%s fixture is %d bytes, pinned at %d. If a bound in the table changed on purpose, update the pin in the same commit and say so; if not, a Min or Max just drifted.",
					tc.name, len(encoded), tc.want)
			}
		})
	}
}

// TestEveryBoundIsBreachable is the per-field proof the ruling asks for: for each
// field that carries a breachable limit, stepping ONE past it makes the validator
// reject. A nil PastMax is not an exemption -- the table entry must say in Why
// why no bound is breachable, and this test asserts that reason is written down.
func TestEveryBoundIsBreachable(t *testing.T) {
	for _, b := range answerBoundTable() {
		t.Run(b.Field, func(t *testing.T) {
			if b.PastMax == nil {
				if strings.TrimSpace(b.Why) == "" {
					t.Fatalf("%s has no PastMax and no written reason", b.Field)
				}
				t.Skipf("no breachable bound: %s", b.Why)
			}
			r := buildFromTable(t, func(x answerBound) func(*ContextFabricInvestigationResult) { return x.Max })
			if err := r.Validate(); err != nil {
				t.Fatalf("baseline maximal fixture must be valid first, got: %v", err)
			}
			b.PastMax(&r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("stepping %s past its bound must be rejected, but Validate accepted it", b.Field)
			}

			// Oracle part 1 -- THE REASON. Round 1 finding 4: accepting any
			// non-nil error proves only that something rejected, not that
			// THIS bound did. A mutation caught by an unrelated closure,
			// enum or coupling rule would have passed the old check.
			want := expectedRejection[b.Field]
			if want == "" {
				t.Fatalf("%s has no expected rejection predicate", b.Field)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s rejected for the WRONG reason:\n  got:  %v\n  want a message containing: %q",
					b.Field, err, want)
			}

			// Oracle part 2 -- ATTRIBUTION. Several validator predicates are
			// compound: one message covers a whole group of fields, so the
			// message alone cannot say WHICH clause fired. Restoring only
			// this field's Max must make the document valid again. If it
			// does not, the rejection was not attributable to this field.
			restore := b.Max
			restore(&r)
			for _, other := range answerBoundTable() {
				if isDerivedField(other.Field) {
					other.Max(&r)
				}
			}
			if err := r.Validate(); err != nil {
				t.Fatalf("restoring %s's Max must make the document valid again, so the rejection is attributable to %s alone; got: %v",
					b.Field, b.Field, err)
			}

			// Composite bounds get one proof per inner bound.
			for _, br := range b.Breaches {
				t.Run(br.Name, func(t *testing.T) {
					r := buildFromTable(t, func(x answerBound) func(*ContextFabricInvestigationResult) { return x.Max })
					br.Mutate(&r)
					err := r.Validate()
					if err == nil {
						t.Fatalf("breaching %s must be rejected, but Validate accepted it", br.Name)
					}
					if !strings.Contains(err.Error(), br.Expect) {
						t.Fatalf("%s rejected for the WRONG reason:\n  got:  %v\n  want a message containing: %q",
							br.Name, err, br.Expect)
					}
					b.Max(&r)
					for _, other := range answerBoundTable() {
						if isDerivedField(other.Field) {
							other.Max(&r)
						}
					}
					if err := r.Validate(); err != nil {
						t.Fatalf("restoring %s's Max must make the document valid again; got: %v", b.Field, err)
					}
				})
			}
		})
	}
}

// Two fields are DERIVED rather than bounded: each is a function of the rest
// of the finished document, so neither can be built in table order. They stay
// in the table (and so stay covered by the field guard); only their timing
// differs.
func isDerivedField(field string) bool {
	return field == "Completeness" || field == "EvidenceRefLabels"
}

// deriveCompleteness recomputes the census from the document itself, matching
// validateCompleteness exactly: the status, the terminal reason the document's
// own disclosures imply, the claimed-fact count, and the summed row count.
func deriveCompleteness(r *ContextFabricInvestigationResult, outcomes []ContextFabricPlanRequirementOutcomeRow) {
	rows := 0
	for _, fact := range r.ClaimedFacts {
		rows += len(fact.Rows)
	}
	r.Completeness = ContextFabricAnswerCompleteness{
		TerminalStatus:    r.Status,
		TerminalReason:    expectedTerminalReason(*r),
		ClaimedFactsCount: len(r.ClaimedFacts),
		RowsCount:         rows,
		Outcomes:          outcomes,
		// DERIVED from the rows just placed, by the same function the
		// validator runs. Writing a literal here would make the fixture
		// its own authority for a value the validator recomputes, which is
		// how a fixture and the thing it is a fixture for drift apart.
		State: DeriveContextFabricAnswerCompletenessState(outcomes),
	}
}

// deriveCompletenessMinimal is the smallest legal block: the census, and NO
// outcome rows. `outcomes` is omitempty, so an empty set costs nothing; the
// whole of this block's contribution to the irreducible answer is the
// required `state`.
func deriveCompletenessMinimal(r *ContextFabricInvestigationResult) {
	deriveCompleteness(r, nil)
}

// deriveCompletenessMaximal fills the outcome set to its declared ceiling,
// every string at its own maximum and every optional cause present.
//
// The rows are NARROWED rather than unavailable, and that is a change from
// this fixture's first revision. Unavailable WAS the largest legal row while
// the only optional content was the three cause vocabularies, which a
// narrowed row may carry too. Once a row could also carry a refinement chain
// it stopped being so: a chain is only legal on a row that lost something and
// only reconciles against a real reduction, so an unavailable row with
// served=declared=0 can hold no refinements at all. A narrowed row carries
// every cause AND the full chain, which strictly dominates.
func deriveCompletenessMaximal(r *ContextFabricInvestigationResult) {
	deriveCompleteness(r, maximalOutcomeRows(ContextFabricPlanRequirementOutcomeMaxCount))
}

// deriveCompletenessPastMax steps one row beyond the ceiling. Validate must
// reject it.
func deriveCompletenessPastMax(r *ContextFabricInvestigationResult) {
	deriveCompleteness(r, maximalOutcomeRows(ContextFabricPlanRequirementOutcomeMaxCount+1))
}

// maximalOutcomeRows builds n rows at their largest legal encoding.
func maximalOutcomeRows(n int) []ContextFabricPlanRequirementOutcomeRow {
	rows := make([]ContextFabricPlanRequirementOutcomeRow, 0, n)
	// The longest members of each closed vocabulary, so the fixture measures
	// the worst case rather than the first case.
	longestCoverage := ContextFabricCoverageDetailCode("")
	for _, code := range ContextFabricCoverageDetailCodeVocabulary() {
		if len(code) > len(longestCoverage) {
			longestCoverage = code
		}
	}
	longestBasis := ContextFabricNarrowingBasis("")
	for _, basis := range ContextFabricNarrowingBasisVocabulary() {
		if len(basis) > len(longestBasis) {
			longestBasis = basis
		}
	}
	// The longest obligation the mirrored vocabulary holds, so the identity
	// below is both maximal AND legal: the validator requires the identity's
	// first segment to equal the row's own obligation.
	longestObligation := ""
	for _, obligation := range ContextFabricAnswerObligationVocabulary() {
		if len(obligation) > len(longestObligation) {
			longestObligation = obligation
		}
	}
	// obligation + "/" + role + "/" + kind, filling the identity bound
	// exactly. Split evenly; the remainder goes to the first segment.
	remaining := ContextFabricRequirementIdentityMaxLength - len(longestObligation) - 2
	role, kind := remaining-remaining/2, remaining/2
	identity := longestObligation + "/" + escaped(role) + "/" + escaped(kind)
	for index := 0; index < n; index++ {
		rows = append(rows, ContextFabricPlanRequirementOutcomeRow{
			Stage:          ContextFabricOutcomeStageAssembledResult,
			Requirement:    identity,
			Obligation:     longestObligation,
			Outcome:        ContextFabricRequirementNarrowed,
			Impact:         ContextFabricAnswerImpactDimension,
			CauseOverrun:   ContextFabricBudgetOverrunBytes,
			CauseCoverage:  longestCoverage,
			CauseNarrowing: longestBasis,
			CauseObserved:  true,
			// Declared must exceed Served by at least the chain length,
			// because every refinement strictly reduces. These are the
			// smallest numbers that admit a full-length chain; the counts
			// themselves are integers with no length bound, so nothing is
			// lost by keeping them small.
			Served:      1,
			Declared:    1 + ContextFabricRequirementRefinementMaxCount,
			Refinements: maximalRefinements(1+ContextFabricRequirementRefinementMaxCount, 1),
		})
	}
	return rows
}

// expectedRejection is the oracle finding 4 of round 1 said was missing: a
// mutation must be rejected BY ITS OWN BOUND, not by some unrelated closure,
// enum or coupling rule. Each entry is the fragment the validator emits for
// that bound. Several validator predicates are COMPOUND (one message covers a
// whole group of fields), which is why the message check alone is not enough
// and TestEveryBoundIsBreachable also runs an attribution check.
var expectedRejection = map[string]string{
	"ResultID":                "result identity or status violates v1 bounds",
	"RequestID":               "result identity or status violates v1 bounds",
	"Question":                "result identity or status violates v1 bounds",
	"Interpretation":          "interpreted question violates v1 bounds",
	"DirectJudgment":          "result answer fields violate v1 bounds",
	"CurrentState":            "result answer fields violate v1 bounds",
	"DeterministicAnswer":     "result answer fields violate v1 bounds",
	"StrongestPressures":      "result answer fields violate v1 bounds",
	"Limitations":             "result answer fields violate v1 bounds",
	"Warnings":                "result answer fields violate v1 bounds",
	"LimitationsDisplaced":    "result displaced-limitation count violates v1 bounds",
	"Drivers":                 "result answer fields violate v1 bounds",
	"RemainingWork":           "result answer fields violate v1 bounds",
	"ReadinessGaps":           "result answer fields violate v1 bounds",
	"Conflicts":               "result answer fields violate v1 bounds",
	"ClaimedFacts":            "claimed facts violate v1 bounds",
	"Paths":                   "result answer fields violate v1 bounds",
	"EvidenceRefIDs":          "result answer fields violate v1 bounds",
	"SubjectResolution":       "subject resolution arrays violate v1 bounds",
	"Coverage":                "coverage violates v1 bounds",
	"Versions":                "version metadata violates v1 bounds",
	"AnswerPlan":              "narrowing steps",
	"Cohort":                  "cohort violates v1 bounds",
	"Completeness":            "outcomes exceeds v1 bounds",
	"EvidenceRefLabels":       "names no evidence ref on the result",
	"Temporal":                "temporal label is only meaningful",
	"EffectiveEvidenceWindow": "all_time must not carry explicit bounds",
	"WindowClarification":     "window clarification options violate v1 bounds",
	"StructureNeeds":          "structure needs offer lists violate v1 bounds",
	"ConfirmedStructure":      "confirmed_structure exceeds v1 bounds",
	"StructureOfferSnapshot":  "structure_offer_snapshot exceeds v1 bounds",
}

// TestEveryPastMaxHasAnExpectedRejection keeps the oracle honest: a new
// PastMax with no expected predicate cannot silently fall back to "any error
// will do", which is exactly what round 1 caught.
func TestEveryPastMaxHasAnExpectedRejection(t *testing.T) {
	for _, b := range answerBoundTable() {
		if b.PastMax == nil {
			continue
		}
		if _, ok := expectedRejection[b.Field]; !ok {
			t.Errorf("%s has a PastMax but no expected rejection predicate", b.Field)
		}
	}
	for field := range expectedRejection {
		found := false
		for _, b := range answerBoundTable() {
			if b.Field == field && b.PastMax != nil {
				found = true
			}
		}
		if !found {
			t.Errorf("expectedRejection names %q, which has no PastMax", field)
		}
	}
}

// encodingCandidates returns the legal alternative ENCODINGS of one value.
// Round 2 finding 1: byte cost depends on encoding, not only on value size. A
// nil slice or map marshals to `null` (4 bytes); a non-nil empty one to `[]` or
// `{}` (2 bytes), so nil is 2 bytes WORSE. `Reused` is the same rule from the
// other side: `true` is a byte shorter than `false`, so the zero value is not
// the minimum there either.
func encodingCandidates(v reflect.Value) map[string]func(reflect.Value) {
	out := map[string]func(reflect.Value){}
	switch v.Kind() {
	case reflect.Slice:
		out["nil slice"] = func(x reflect.Value) { x.Set(reflect.Zero(x.Type())) }
		out["empty non-nil slice"] = func(x reflect.Value) { x.Set(reflect.MakeSlice(x.Type(), 0, 0)) }
	case reflect.Map:
		out["nil map"] = func(x reflect.Value) { x.Set(reflect.Zero(x.Type())) }
		out["empty non-nil map"] = func(x reflect.Value) { x.Set(reflect.MakeMap(x.Type())) }
	case reflect.Bool:
		out["false"] = func(x reflect.Value) { x.SetBool(false) }
		out["true"] = func(x reflect.Value) { x.SetBool(true) }
	}
	return out
}

// encodingProbePaths walks the result RECURSIVELY and returns an index path for
// every field whose encoding could be chosen differently. Round 2 finding 1 was
// invisible to a top-level-only sweep: FactRequirements lives inside
// Interpretation, so a walk over the 36 result fields could never reach it.
func encodingProbePaths(t reflect.Type, prefix []int, name string, depth int) map[string][]int {
	out := map[string][]int{}
	if depth > 4 || t.Kind() != reflect.Struct {
		return out
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		path := append(append([]int{}, prefix...), i)
		full := name + "." + f.Name
		switch f.Type.Kind() {
		case reflect.Slice, reflect.Map, reflect.Bool:
			out[full] = path
		case reflect.Struct:
			for k, v := range encodingProbePaths(f.Type, path, full, depth+1) {
				out[k] = v
			}
		case reflect.Ptr:
			if f.Type.Elem().Kind() == reflect.Struct {
				for k, v := range encodingProbePaths(f.Type.Elem(), path, full, depth+1) {
					out[k] = v
				}
			}
		}
	}
	return out
}

func fieldByPath(root reflect.Value, path []int) (reflect.Value, bool) {
	v := root
	for _, i := range path {
		for v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct || i >= v.NumField() {
			return reflect.Value{}, false
		}
		v = v.Field(i)
	}
	return v, v.IsValid() && v.CanSet()
}

// TestIrreducibleUsesTheByteMinimalEncoding asserts, for every encodable field
// reachable anywhere in the result, that the table's choice is the byte-minimum
// among the encodings that VALIDATE.
func TestIrreducibleUsesTheByteMinimalEncoding(t *testing.T) {
	base := buildFromTable(t, func(x answerBound) func(*ContextFabricInvestigationResult) { return x.Min })
	if err := base.Validate(); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	chosen, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	paths := encodingProbePaths(reflect.TypeOf(base), nil, "result", 0)
	t.Logf("probing %d encodable fields reachable in the result", len(paths))

	for name, path := range paths {
		r := buildFromTable(t, func(x answerBound) func(*ContextFabricInvestigationResult) { return x.Min })
		v, ok := fieldByPath(reflect.ValueOf(&r).Elem(), path)
		if !ok {
			continue
		}
		for cname, apply := range encodingCandidates(v) {
			probe := buildFromTable(t, func(x answerBound) func(*ContextFabricInvestigationResult) { return x.Min })
			pv, ok := fieldByPath(reflect.ValueOf(&probe).Elem(), path)
			if !ok {
				continue
			}
			apply(pv)
			for _, d := range answerBoundTable() {
				if isDerivedField(d.Field) {
					d.Min(&probe)
				}
			}
			if err := probe.Validate(); err != nil {
				continue
			}
			encoded, err := json.Marshal(probe)
			if err != nil {
				continue
			}
			if len(encoded) < len(chosen) {
				t.Errorf("%s: fixture encodes at %d bytes, but %q also validates at %d (%d smaller)",
					name, len(chosen), cname, len(encoded), len(chosen)-len(encoded))
			}
		}
	}
}

// --- recursive saturation: round 2 findings 2 and 3, closed as a class ---
//
// The reflection guard covers the 36 TOP-LEVEL result fields. That is exactly
// why two review rounds kept finding inner fields left at minima inside a
// collection whose COUNT was maximised: nothing walked into the elements.
//
// The result type has 432 reachable field paths. Hand-writing a table entry for
// each would be churn, not proof. This instead asks every reachable field the
// question the entry would have asked: CAN THIS STILL GROW? A field that can be
// made larger while the document stays valid is not at its bound, and is named.
//
// Limits of the probe, stated rather than implied: it grows strings, []string,
// map[string]string and ints, where a larger legal value can be synthesised
// generically. It does NOT grow slices of structs, because a valid element
// cannot be built without that type's own construction rules. Those stay
// covered by the table's count bounds, or are disclosed as lower-bound axes.
func growValue(v reflect.Value, seed int) bool {
	if !v.CanSet() {
		return false
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + oneRune)
		return true
	case reflect.Int:
		v.SetInt(v.Int() + 1)
		return true
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return false
		}
		// The element may be a NAMED string type (ContextFabricStructureNeedKind
		// and friends): Kind() is String but the concrete type differs, so a
		// bare string is not assignable and reflect.Append panics.
		v.Set(reflect.Append(v, reflect.ValueOf(uniqueID("grow", seed, 12)).Convert(v.Type().Elem())))
		return true
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || v.Type().Elem().Kind() != reflect.String {
			return false
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
		v.SetMapIndex(
			reflect.ValueOf(uniqueID("k", seed, 8)).Convert(v.Type().Key()),
			reflect.ValueOf(oneRune).Convert(v.Type().Elem()))
		return true
	}
	return false
}

// boundPathStep addresses one hop: a struct field, or one element of a slice.
type boundPathStep struct {
	Field   int
	Index   int
	IsIndex bool
}

func saturationPaths(v reflect.Value, prefix []boundPathStep, name string, depth int, out map[string][]boundPathStep) {
	if depth > saturationProbeMaxDepth || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Ptr:
		if !v.IsNil() {
			saturationPaths(v.Elem(), prefix, name, depth, out)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.PkgPath != "" {
				continue
			}
			saturationPaths(v.Field(i), append(append([]boundPathStep{}, prefix...), boundPathStep{Field: i}),
				name+"."+f.Name, depth+1, out)
		}
	case reflect.Slice:
		out[name] = prefix
		if v.Type().Elem().Kind() != reflect.String && v.Len() > 0 {
			// Probe element 0 only: every element comes from one builder, so
			// an unsaturated element 0 means every element is unsaturated.
			saturationPaths(v.Index(0), append(append([]boundPathStep{}, prefix...), boundPathStep{Index: 0, IsIndex: true}),
				name+"[0]", depth+1, out)
		}
	default:
		out[name] = prefix
	}
}

func navigateBoundPath(root reflect.Value, path []boundPathStep) (reflect.Value, bool) {
	v := root
	for _, s := range path {
		for v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		if s.IsIndex {
			if v.Kind() != reflect.Slice || s.Index >= v.Len() {
				return reflect.Value{}, false
			}
			v = v.Index(s.Index)
			continue
		}
		if v.Kind() != reflect.Struct || s.Field >= v.NumField() {
			return reflect.Value{}, false
		}
		v = v.Field(s.Field)
	}
	return v, v.IsValid()
}

// saturationProbeMaxDepth is a DISCLOSED limit of the probe, not an incidental
// constant. Round 3 found the earlier value of 4 stopping the walk short of
// result.RemainingWork[0].Subjects[0].CanonicalID, which sits at depth 5: the
// probe reported success over a region it had never visited, which is the same
// failure class as an oracle that accepts any error -- it passes without having
// looked. The cap still exists (the type graph is cyclic through subject refs
// and would not terminate otherwise), so the depth it stops at is stated here,
// in the probe's log line, and in the PR body.
const saturationProbeMaxDepth = 8

func TestMaximalIsSaturated(t *testing.T) {
	if testing.Short() {
		t.Skip("rebuilds the ~520MB maximal fixture once per probed field (~90s); `make contract-test` runs it without -short")
	}
	if raceDetectorEnabled {
		// Race instrumentation makes each rebuild about an order of magnitude
		// slower, which overruns the package timeout. The probe finds no data
		// races to detect -- it is single-goroutine -- so running it here buys
		// nothing and costs the whole shard.
		t.Skip("skipped under -race: 270 rebuilds of a ~520MB fixture overruns the package timeout, and the probe is single-goroutine so -race has nothing to find")
	}
	base := buildFromTable(t, func(x answerBound) func(*ContextFabricInvestigationResult) { return x.Max })
	if err := base.Validate(); err != nil {
		t.Fatalf("baseline maximal must be valid: %v", err)
	}
	paths := map[string][]boundPathStep{}
	saturationPaths(reflect.ValueOf(base), nil, "result", 0, paths)

	names := make([]string, 0, len(paths))
	for n := range paths {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("probing %d reachable fields for further growth (walk depth capped at %d)", len(names), saturationProbeMaxDepth)
	seen := map[string]bool{}

	for seed, name := range names {
		r := buildFromTable(t, func(x answerBound) func(*ContextFabricInvestigationResult) { return x.Max })
		// A DERIVED field cannot be probed this way: growing it and then
		// re-deriving simply undoes the mutation, so the document validates
		// and the field looks unsaturated when nothing actually changed.
		if isDerivedTopLevel(name) {
			continue
		}
		v, ok := navigateBoundPath(reflect.ValueOf(&r).Elem(), paths[name])
		if !ok || !growValue(v, seed+1) {
			continue
		}
		for _, d := range answerBoundTable() {
			if isDerivedField(d.Field) {
				d.Max(&r)
			}
		}
		if err := r.Validate(); err != nil {
			continue
		}
		if reason, disclosed := unsaturatedByDesign[name]; disclosed {
			seen[name] = true
			t.Logf("lower-bound axis: %s -- %s", name, reason)
			continue
		}
		t.Errorf("NOT SATURATED: %s can still grow and the document stays valid, and it is not a disclosed lower-bound axis", name)
	}

	// The other direction, which is what keeps the disclosure list honest: an
	// entry that is no longer unsaturated is STALE and must be deleted, or the
	// list slowly becomes a place to hide fields that were since fixed.
	for name, reason := range unsaturatedByDesign {
		if !seen[name] {
			t.Errorf("STALE DISCLOSURE: %s is listed as a lower-bound axis (%q) but the probe no longer finds it unsaturated -- delete the entry", name, reason)
		}
	}
}

// isDerivedTopLevel reports whether a probe path sits under a derived field.
// Completeness and EvidenceRefLabels are functions of the rest of the document,
// so the saturation probe cannot mutate them: it grows the value, the derived
// pass recomputes it, and the document validates unchanged. Reporting those as
// "not saturated" would be an artifact of the probe, not a defect in the table.
func isDerivedTopLevel(path string) bool {
	for _, b := range answerBoundTable() {
		if isDerivedField(b.Field) && strings.HasPrefix(path, "result."+b.Field) {
			return true
		}
	}
	return false
}

// unsaturatedByDesign names every field the maximal fixture deliberately leaves
// below its bound, with the reason. The saturation probe FAILS on anything
// unsaturated that is not listed here, and fails on a listed entry that has
// become saturated, so this list cannot rot in either direction.
// sharedSubjectWidth is one reason shared by every subject reference in the
// document. distinctSubject builds them all, so widening it to the validator's
// 256-rune canonical id and 512-rune label multiplies across roughly 187,500
// subjects inside findings alone -- about a gigabyte, which cannot be marshaled
// here. Round 3: these were invisible to the probe at depth 4, so they were
// neither fixed nor disclosed. The disclosure was accurate about the fields it
// named and silently incomplete about the rest.
const sharedSubjectWidth = "shared subject-ref width: distinctSubject builds every subject in the document; widening it adds ~1GB across ~187,500 subjects"

var unsaturatedByDesign = map[string]string{
	"result.Cohort.Members[0].Subject.CanonicalID":                      sharedSubjectWidth,
	"result.Cohort.Members[0].Subject.Label":                            sharedSubjectWidth,
	"result.Conflicts[0].Subjects[0].CanonicalID":                       sharedSubjectWidth,
	"result.Conflicts[0].Subjects[0].Label":                             sharedSubjectWidth,
	"result.Drivers[0].AffectedSubjects[0].CanonicalID":                 sharedSubjectWidth,
	"result.Drivers[0].AffectedSubjects[0].Label":                       sharedSubjectWidth,
	"result.Interpretation.FactRequirements[0].Subjects[0].CanonicalID": sharedSubjectWidth,
	"result.Interpretation.FactRequirements[0].Subjects[0].Label":       sharedSubjectWidth,
	"result.ReadinessGaps[0].Subjects[0].CanonicalID":                   sharedSubjectWidth,
	"result.ReadinessGaps[0].Subjects[0].Label":                         sharedSubjectWidth,
	"result.RemainingWork[0].Subjects[0].CanonicalID":                   sharedSubjectWidth,
	"result.RemainingWork[0].Subjects[0].Label":                         sharedSubjectWidth,
	"result.SubjectResolution.Candidates[0].Subject.CanonicalID":        sharedSubjectWidth,
	"result.SubjectResolution.Candidates[0].Subject.Label":              sharedSubjectWidth,
	"result.Paths[0].Edges[0].EvidenceRefIDs":                           "path-edge evidence refs: 250 paths x 50 edges x 100 refs x 256 runes is ~320M runes of ids alone, which cannot be marshaled here. Disclosed in maximalPath's comment since it was written; round 3 showed the comment was not ENFORCED, because the probe never walked deep enough to reach it",
	"result.AnswerPlan.Budget.MaxItems":                                 "int with no upper bound in the contract: there is no maximum to sit at",
	"result.AnswerPlan.Budget.MaxMembers":                               "int with no upper bound in the contract",
	"result.AnswerPlan.Budget.SynthesisHeadroom":                        "int with no upper bound in the contract",
	"result.AnswerPlan.Narrowing[0].Before":                             "int with no upper bound in the contract",
	"result.AnswerPlan.Narrowing[0].After":                              "int with no upper bound in the contract",
	"result.StructureOfferSnapshot[0].Rank":                             "int bounded only from below (>= 0)",
	"result.ClaimedFacts[0].Subject.CanonicalID":                        "subject-ref width: distinctSubject is shared by every subject in the document, including ~187,500 inside findings; widening it to 256 runes adds roughly a gigabyte and cannot be marshaled here",
	"result.ClaimedFacts[0].Subject.Label":                              "subject-ref width, same shared builder",
	"result.SubjectResolution.Committed[0].CanonicalID":                 "subject-ref width, same shared builder",
	"result.SubjectResolution.Committed[0].Label":                       "subject-ref width, same shared builder",
	"result.Versions.ContractVersion":                                   "must stay the real schema identifier to mean anything; the validator bounds only its length, so a padded value would validate while making the document nonsense",
}

// TestDisclosedLowerBoundAxesAreNamed only reports the count. The ENFORCEMENT
// of staleness lives in TestMaximalIsSaturated, which is the only place that
// knows which disclosed axes the probe actually reached.
func TestDisclosedLowerBoundAxesAreNamed(t *testing.T) {
	t.Logf("%d disclosed lower-bound axes", len(unsaturatedByDesign))
}
