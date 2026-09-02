package v1

import (
	"strings"
	"time"
)

// The measured answer fixtures, built BY CONSTRUCTION from one table of the
// validator's own limits.
//
// WHY THIS EXISTS. A hand-built fixture measured the minimum answer size four
// times and was wrong four times: it padded a rune-counted field with ASCII; it
// omitted the interpretation's terms entirely; it carried a live clock, so the
// same document measured differently between runs; and it inherited two
// non-minimal fields from a fixture written for another purpose. Every one was
// an INHERITANCE or an OMISSION. Not one was a wrong calculation.
//
// That is the signature of a hand-built primitive: each fix corrects the
// instance and leaves the class intact, because nothing forces the fixture to
// account for a field nobody thought about. So the fixtures are no longer
// written; they are DERIVED from a table, and a field missing from that table
// is a test failure rather than a silent default.
//
// The table is the single source for both documents:
//
//	irreducible — every field at the SMALLEST value its validator accepts
//	maximal     — every field at the LARGEST value its validator accepts
//
// and the proof is per FIELD, not per byte: for every entry, stepping past the
// bound must make the validator reject. A byte total that nobody can attribute
// to a field is not evidence; a field whose limit is proven by the validator is.

// answerBound is one field of ContextFabricInvestigationResult and the limits
// its own validator places on it.
type answerBound struct {
	// Field is the Go field name. The reflection guard requires every field
	// of the result struct to appear here exactly once -- that is what makes
	// an omission impossible rather than merely unlikely.
	Field string
	// Why names the validator clause or constant that fixes the limits, so a
	// reader can check the entry against the code rather than trusting it.
	Why string
	// Min sets the field to the SMALLEST legal value, measured in serialized
	// bytes rather than in count -- for a bool that is `true`, which encodes
	// one byte shorter than `false`.
	Min func(r *ContextFabricInvestigationResult)
	// Max sets the field to the LARGEST legal value.
	Max func(r *ContextFabricInvestigationResult)
	// PastMax steps one increment beyond the upper bound. Validate MUST
	// reject the result afterwards. Nil means the field has no upper bound a
	// test can breach (a fixed enum, a bool, a required identifier), and the
	// guard requires a reason in Why rather than accepting a bare nil.
	PastMax func(r *ContextFabricInvestigationResult)
}

// fixedAnswerInstant pins the one input that is otherwise a live clock. A zero
// fractional second is deliberate: Go marshals time as RFC3339Nano, which drops
// trailing zeros, so a zero fraction is both the SHORTEST encoding and a stable
// one. A fixture with time.Now() in it measured 22 to 30 bytes differently
// between runs, passed locally four times, and failed in CI.
var fixedAnswerInstant = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

// minimalSubject is the smallest legal subject reference: a valid kind and
// one-rune identifiers.
func minimalSubject() ContextFabricSubjectRef {
	return ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "p", Label: "l"}
}

// escaped returns n copies of the rune that costs the MOST serialized bytes.
// It is not the widest UTF-8 encoding: Go's JSON encoder escapes "<" to a
// six-byte \uXXXX form, against four bytes for a non-BMP emoji raw. Bounds in
// this contract are counted in RUNES, so a maximal field's byte cost is its
// rune bound times this factor. answer_bound_fixtures_test.go proves the choice
// by comparing candidates rather than asserting it.
func escaped(n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = '<'
	}
	return string(out)
}

// oneRune is the smallest value any 1..N bounded text field may hold.
// The result validator spells these as literals rather than reusing
// ContextFabricModelMintedIDMaxLength, so the fixtures must follow the
// literals or they would be measuring a bound nothing enforces.
const (
	resultIDMinRunes = 8
	resultIDMaxRunes = 256
)

const oneRune = "q"

// minimalFinding / maximalFinding bound the three Finding-shaped collections
// (RemainingWork, ReadinessGaps, Conflicts), which share one validator.
// minimalFinding uses ContextFabricModelMintedIDMinLength for the id, not a
// single rune: a 1-rune id would make every PastMax proof that carries a
// minimal finding reject on the ID bound instead of the count bound it means
// to breach -- passing for the wrong reason.
func minimalFinding() ContextFabricFinding {
	return ContextFabricFinding{
		FindingID:      strings.Repeat(oneRune, ContextFabricModelMintedIDMinLength),
		Kind:           "narrative",
		Summary:        oneRune,
		Subjects:       []ContextFabricSubjectRef{minimalSubject()},
		EvidenceRefIDs: []string{},
	}
}

func maximalFinding() ContextFabricFinding {
	// uniqueSubjects rejects repeats, so the maximal finding needs DISTINCT
	// subjects -- 250 copies of the same ref is not a maximal finding, it is
	// an invalid one.
	subjects := make([]ContextFabricSubjectRef, ContextFabricFindingSubjectsMaxCount)
	for i := range subjects {
		subjects[i] = distinctSubject(i)
	}
	return ContextFabricFinding{
		FindingID: escaped(ContextFabricModelMintedIDMaxLength),
		Kind:      string(maximalDriverCategory),
		Summary:   escaped(ContextFabricFindingSummaryMaxLength),
		Subjects:  subjects,
		// boundedEvidenceRefs rejects a NIL slice even in its optional
		// (required=false) mode, so nil and empty are NOT interchangeable
		// here; the maximal carries the nested bound's worth of refs.
		EvidenceRefIDs: repeatEvidenceRefs(ContextFabricNestedEvidenceRefIDsMaxCount, ContextFabricEvidenceRefIDMaxLength),
		ClaimedFactIDs: []string{maximalClaimedFactID},
	}
}

// repeatFinding builds a collection of n findings with distinct ids, since the
// validator requires unique identifiers.
func repeatFindings(n int, build func() ContextFabricFinding) []ContextFabricFinding {
	out := make([]ContextFabricFinding, 0, n)
	for i := 0; i < n; i++ {
		f := build()
		f.FindingID = uniqueID("f", i, len(f.FindingID))
		out = append(out, f)
	}
	return out
}

// uniqueID produces a distinct identifier of the requested rune length,
// padding with the worst-case rune so a maximal collection is maximal in bytes
// as well as in count.
func uniqueID(prefix string, i, runes int) string {
	suffix := prefix + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
	if runes <= len(suffix) {
		return suffix[:max(1, runes)]
	}
	return escaped(runes-len(suffix)) + suffix
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func repeatStrings(count, runes int) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, uniqueID("s", i, runes))
	}
	return out
}

func minimalInterpretation() ContextFabricInterpretedQuestion {
	return ContextFabricInterpretedQuestion{
		Shape:             ContextFabricShapeOpen,
		RequestedJudgment: oneRune,
		TimeContext:       ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent},
	}
}

func maximalInterpretation() ContextFabricInterpretedQuestion {
	q := minimalInterpretation()
	q.RequestedJudgment = escaped(ContextFabricRequestedJudgmentMaxLength)
	q.SubjectTerms = repeatStrings(ContextFabricSubjectTermsMaxCount, ContextFabricSubjectOrComparisonTermMaxLength)
	q.ComparisonTerms = repeatStrings(ContextFabricComparisonTermsMaxCount, ContextFabricSubjectOrComparisonTermMaxLength)
	q.ClarificationReason = escaped(ContextFabricClarificationReasonMaxLength)
	return q
}

func repeatDrivers(n int) []ContextFabricDriverJudgment {
	out := make([]ContextFabricDriverJudgment, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ContextFabricDriverJudgment{
			DriverID:         uniqueID("d", i, 16),
			Standing:         ContextFabricDriverPrincipal,
			Category:         string(maximalDriverCategory),
			Title:            oneRune,
			Summary:          oneRune,
			AffectedSubjects: []ContextFabricSubjectRef{minimalSubject()},
			EvidenceRefIDs:   repeatEvidenceRefs(1, 8),
			ClaimedFactIDs:   []string{maximalClaimedFactID},
			Derivation:       ContextFabricDerivationCanonicalStructured,
			EpistemicStatus:  ContextFabricEpistemicObserved,
			Confidence:       0.9,
			Current:          true,
		})
	}
	return out
}

// maximalDriverCategory is a category whose ContextFabricDriverCategoryRequiresClaimedFact
// entry exists, so the maximal driver must ALSO carry a ClaimedFactID that
// resolves to a fact of the matching kind. Picking a requiring category on
// purpose keeps the fixture honest: it exercises the strongest closure rule
// rather than dodging it with a category that demands nothing.
// maximalDriverCategory is the LONGEST member of the closed driver-category
// vocabulary, derived rather than hardcoded so a future vocabulary entry
// widens the fixture automatically. Finding.Kind is governed by this same
// vocabulary (validate_context_fabric_result.go:912), which means
// ContextFabricFindingKindMaxLength (128) is NOT reachable by any valid
// document -- the vocabulary, not the length bound, is what caps that field.
var maximalDriverCategory = longestDriverCategory()

func longestDriverCategory() ContextFabricDriverCategory {
	vocab := ContextFabricDriverCategoryVocabulary()
	longest := vocab[0]
	for _, c := range vocab {
		if len(c) > len(longest) {
			longest = c
		}
	}
	return longest
}

var maximalDriverFactKind, _ = ContextFabricDriverCategoryRequiresClaimedFact(maximalDriverCategory)

func claimedFactID(i int) string { return uniqueID("c", i, 16) }

var maximalClaimedFactID = claimedFactID(0)

func repeatClaimedFacts(n int) []ContextFabricClaimedFact {
	value := oneRune
	out := make([]ContextFabricClaimedFact, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ContextFabricClaimedFact{
			ClaimID: claimedFactID(i),
			Kind:    maximalDriverFactKind,
			Subject: minimalSubject(),
			Field:   oneRune,
			Value:   ContextFabricScalarValue{String: &value},
		})
	}
	return out
}

func minimalResolution() ContextFabricSubjectResolution {
	return ContextFabricSubjectResolution{
		Candidates: []ContextFabricSubjectCandidate{},
		Committed:  []ContextFabricSubjectRef{minimalSubject()},
	}
}

func minimalCoverage() ContextFabricCoverage {
	return ContextFabricCoverage{Sources: []ContextFabricSourceObservation{}, DegradedReasons: []string{}}
}

func minimalVersions() ContextFabricVersionSet {
	return ContextFabricVersionSet{
		ServiceVersion: oneRune, ContractVersion: ContextFabricInvestigationResultSchema, Backend: oneRune,
		ProjectionVersion: oneRune, QueryVersion: oneRune, InterpretationVersion: oneRune,
		SynthesisVersion: oneRune, CanonicalServiceVersion: oneRune,
	}
}

func maximalVersions() ContextFabricVersionSet {
	pad := escaped(256)
	v := minimalVersions()
	v.ServiceVersion, v.Backend, v.ProjectionVersion = pad, pad, pad
	v.QueryVersion, v.InterpretationVersion, v.SynthesisVersion, v.CanonicalServiceVersion = pad, pad, pad, pad
	v.BackendVersion = pad
	return v
}

func maximalPlan() *ContextFabricAnswerPlan {
	plan := ContextFabricAnswerPlan{
		Family:        ContextFabricQuestionFamilySubjectInvestigation,
		FamilySource:  ContextFabricQuestionFamilySourceStructurePrecedence,
		FamilyVersion: escaped(64),
		Budget:        ContextFabricAnswerPlanBudget{MaxItems: 30, MaxSerializedBytes: 1 << 20},
	}
	kinds := ContextFabricFactKindVocabulary()
	plan.FactKinds = append(plan.FactKinds, kinds[:]...)
	for i := 0; i < ContextFabricPlanNarrowingMaxCount; i++ {
		plan.Narrowing = append(plan.Narrowing, ContextFabricPlanNarrowing{
			Stage: ContextFabricPlanNarrowingAssembledResult, Basis: ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: 50, After: 10, Overrun: ContextFabricBudgetOverrunItems,
		})
	}
	return &plan
}

// maximalLimitations fills the list to its count bound while satisfying the
// coupling that makes a nonzero LimitationsDisplaced legal: one slot must hold
// a service-authored limitation, which is shorter than the per-entry maximum.
func maximalLimitations() []string {
	out := []string{ContextFabricServiceAuthoredLimitations()[0]}
	out = append(out, repeatStrings(ContextFabricLimitationsMaxCount-1, ContextFabricLimitationMaxLength)...)
	return out
}

// repeatEvidenceRefs builds distinct evidence ids. Drivers REQUIRE at least one
// (boundedEvidenceRefs(..., true) at validate_context_fabric_result.go:865),
// which is why no driver -- not even in the irreducible direction -- can carry
// an empty list.
func repeatEvidenceRefs(count, runes int) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, uniqueID("e", i, runes))
	}
	return out
}

// distinctSubject yields refs that differ in canonical id, which uniqueSubjects
// requires of every subject list.
func distinctSubject(i int) ContextFabricSubjectRef {
	ref := minimalSubject()
	ref.CanonicalID = uniqueID("sub", i, ContextFabricModelMintedIDMinLength)
	return ref
}

// --- the six formerly under-specified fields, closed with real maxima ---

const (
	// Literal bounds the validators spell inline rather than via a named
	// constant, mirrored here so the fixture tracks what is ENFORCED.
	subjectCandidatesMaxCount = 50  // validate_context_fabric_result.go:54
	committedSubjectsMaxCount = 250 // same predicate
	coverageEntriesMaxCount   = 100 // contextFabricWriteBounds.coverageEntries
	sourceNameMaxRunes        = 128 // validate_context_fabric_result.go:970
	watermarkMaxRunes         = 512 // same predicate
	matchedTermsMaxCount      = 32  // contextFabricWriteBounds.matchedTerms
	matchReasonsMaxCount      = 32  // contextFabricWriteBounds.matchReasons
	matchReasonMaxRunes       = 1000
)

func maximalCandidate(i int) ContextFabricSubjectCandidate {
	return ContextFabricSubjectCandidate{
		ReceiptID:    uniqueID("rcpt", i, 32),
		State:        ContextFabricResolutionCommitted,
		Confidence:   1,
		Subject:      distinctSubject(i),
		MatchedTerms: repeatStrings(matchedTermsMaxCount, ContextFabricSubjectOrComparisonTermMaxLength),
		MatchReasons: repeatStrings(matchReasonsMaxCount, matchReasonMaxRunes),
	}
}

func maximalResolution() ContextFabricSubjectResolution {
	candidates := make([]ContextFabricSubjectCandidate, subjectCandidatesMaxCount)
	for i := range candidates {
		candidates[i] = maximalCandidate(i)
	}
	committed := make([]ContextFabricSubjectRef, committedSubjectsMaxCount)
	for i := range committed {
		committed[i] = distinctSubject(i)
	}
	return ContextFabricSubjectResolution{Candidates: candidates, Committed: committed}
}

// maximalSource keeps State available on purpose: a non-available source
// REQUIRES a reason, so state and reason are coupled. Available plus a
// max-length reason is the larger document, and it is legal.
func maximalSource(i int) ContextFabricSourceObservation {
	return ContextFabricSourceObservation{
		Source:     uniqueID("src", i, sourceNameMaxRunes),
		State:      ContextFabricSourceAvailable,
		Watermark:  escaped(watermarkMaxRunes),
		Reason:     escaped(ContextFabricSourceObservationReasonMaxLength),
		Label:      escaped(ContextFabricCoverageDetailLabelMaxLength),
		StateLabel: escaped(ContextFabricCoverageDetailLabelMaxLength),
	}
}

func maximalCoverage() ContextFabricCoverage {
	sources := make([]ContextFabricSourceObservation, coverageEntriesMaxCount)
	for i := range sources {
		sources[i] = maximalSource(i)
	}
	return ContextFabricCoverage{
		Sources:         sources,
		DegradedReasons: repeatStrings(coverageEntriesMaxCount, ContextFabricCoverageDegradedReasonMaxLength),
	}
}

// pastMaxPlan breaches ContextFabricPlanNarrowingMaxCount, the plan's own
// count bound, by exactly one step.
func pastMaxPlan() *ContextFabricAnswerPlan {
	plan := maximalPlan()
	plan.Narrowing = append(plan.Narrowing, plan.Narrowing[0])
	return plan
}
