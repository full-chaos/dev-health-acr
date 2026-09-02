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
		{Field: "Interpretation", Why: "RequestedJudgment 1..256; SubjectTerms/ComparisonTerms 50 each at 512 runes; FactRequirements 0..ContextFabricFactRequirementsMaxCount",
			Min: func(r *ContextFabricInvestigationResult) { r.Interpretation = minimalInterpretation() },
			Max: func(r *ContextFabricInvestigationResult) { r.Interpretation = maximalInterpretation() },
			PastMax: func(r *ContextFabricInvestigationResult) {
				i := maximalInterpretation()
				i.RequestedJudgment = escaped(ContextFabricRequestedJudgmentMaxLength + 1)
				r.Interpretation = i
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
		{Field: "CurrentState", Why: "boundedText 1..ContextFabricCurrentStateMaxLength",
			Min: func(r *ContextFabricInvestigationResult) { r.CurrentState = oneRune },
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
		{Field: "Paths", Why: "relationship paths; excluded from the item budget by the 4523 rule but present in the document",
			Min: func(r *ContextFabricInvestigationResult) { r.Paths = []ContextFabricRelationshipPath{} },
			Max: func(r *ContextFabricInvestigationResult) { r.Paths = []ContextFabricRelationshipPath{} }},
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
		{Field: "Completeness", Why: "a census of the document; its counts must equal what the document carries, so it is derived rather than bounded",
			Min: deriveCompleteness,
			Max: deriveCompleteness},

		// --- optional pointers and collections: absent is the byte minimum ---
		{Field: "AnswerPlan", Why: "optional pointer, so absent is the byte minimum; when present it carries every fact kind and up to ContextFabricPlanNarrowingMaxCount narrowing steps",
			Min:     func(r *ContextFabricInvestigationResult) { r.AnswerPlan = nil },
			Max:     func(r *ContextFabricInvestigationResult) { r.AnswerPlan = maximalPlan() },
			PastMax: func(r *ContextFabricInvestigationResult) { r.AnswerPlan = pastMaxPlan() }},
		{Field: "Cohort", Why: "optional pointer; absent is the byte minimum",
			Min: func(r *ContextFabricInvestigationResult) { r.Cohort = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.Cohort = nil }},
		{Field: "EvidenceRefLabels", Why: "optional map, composed after synthesis; absent is the byte minimum",
			Min: func(r *ContextFabricInvestigationResult) { r.EvidenceRefLabels = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.EvidenceRefLabels = nil }},
		{Field: "Temporal", Why: "optional pointer",
			Min: func(r *ContextFabricInvestigationResult) { r.Temporal = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.Temporal = nil }},
		{Field: "EffectiveEvidenceWindow", Why: "optional pointer",
			Min: func(r *ContextFabricInvestigationResult) { r.EffectiveEvidenceWindow = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.EffectiveEvidenceWindow = nil }},
		{Field: "WindowClarification", Why: "optional pointer",
			Min: func(r *ContextFabricInvestigationResult) { r.WindowClarification = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.WindowClarification = nil }},
		{Field: "StructureNeeds", Why: "optional pointer",
			Min: func(r *ContextFabricInvestigationResult) { r.StructureNeeds = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.StructureNeeds = nil }},
		{Field: "ConfirmedStructure", Why: "optional slice; absent is the byte minimum",
			Min: func(r *ContextFabricInvestigationResult) { r.ConfirmedStructure = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.ConfirmedStructure = nil }},
		{Field: "StructureOfferSnapshot", Why: "optional slice",
			Min: func(r *ContextFabricInvestigationResult) { r.StructureOfferSnapshot = nil },
			Max: func(r *ContextFabricInvestigationResult) { r.StructureOfferSnapshot = nil }},
		{Field: "RenderShapes", Why: "optional slice, authorized by the plan",
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
		if b.Field == completenessField {
			pick(b)(&r)
		}
	}
	return r
}

func TestIrreducibleAndMaximalFixturesAreValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		pick func(answerBound) func(*ContextFabricInvestigationResult)
	}{
		{"irreducible", func(b answerBound) func(*ContextFabricInvestigationResult) { return b.Min }},
		{"maximal", func(b answerBound) func(*ContextFabricInvestigationResult) { return b.Max }},
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
			if err := r.Validate(); err == nil {
				t.Fatalf("stepping %s past its bound must be rejected, but Validate accepted it", b.Field)
			}
		})
	}
}

const completenessField = "Completeness"

// deriveCompleteness recomputes the census from the document itself, matching
// validateCompleteness exactly: the status, the terminal reason the document's
// own disclosures imply, the claimed-fact count, and the summed row count.
func deriveCompleteness(r *ContextFabricInvestigationResult) {
	rows := 0
	for _, fact := range r.ClaimedFacts {
		rows += len(fact.Rows)
	}
	r.Completeness = ContextFabricAnswerCompleteness{
		TerminalStatus:    r.Status,
		TerminalReason:    expectedTerminalReason(*r),
		ClaimedFactsCount: len(r.ClaimedFacts),
		RowsCount:         rows,
	}
}
