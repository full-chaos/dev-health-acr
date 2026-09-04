package v1

import (
	"fmt"
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
// answerBreach is one extra proof for a field whose bound is COMPOSITE. Round 1
// finding 2: a single PastMax on Interpretation tested only RequestedJudgment,
// so the entry's other claimed bounds (term counts, fact requirements,
// clarification reason) had no proof at all.
type answerBreach struct {
	Name   string
	Mutate func(*ContextFabricInvestigationResult)
	Expect string
}

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
	// Breaches are additional per-inner-bound proofs, run with the same
	// reason-and-attribution oracle as PastMax.
	Breaches []answerBreach
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
		// boundedEvidenceRefs(values, max, allowEmpty): the third argument
		// is allowEmpty, NOT "required". Findings pass false, so a finding
		// must carry at least one ref -- nil AND empty are both rejected.
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
		// A NIL slice marshals to `null` (4 bytes); a non-nil empty one to
		// `[]` (2 bytes). nil is the larger encoding, not the smaller.
		FactRequirements: []ContextFabricFactRequirement{},
	}
}

func maximalInterpretation() ContextFabricInterpretedQuestion {
	q := minimalInterpretation()
	q.RequestedJudgment = escaped(ContextFabricRequestedJudgmentMaxLength)
	q.SubjectTerms = repeatStrings(ContextFabricSubjectTermsMaxCount, ContextFabricSubjectOrComparisonTermMaxLength)
	q.ComparisonTerms = repeatStrings(ContextFabricComparisonTermsMaxCount, ContextFabricSubjectOrComparisonTermMaxLength)
	q.ClarificationReason = escaped(ContextFabricClarificationReasonMaxLength)
	// Round 1 finding 2: this was EMPTY while the table entry claimed its
	// 0..ContextFabricFactRequirementsMaxCount bound. The bound equals the
	// fact-kind vocabulary size, and each requirement must name a distinct
	// kind, so the vocabulary is what fills it.
	kinds := ContextFabricFactKindVocabulary()
	for i, kind := range kinds {
		params := map[string]string{}
		for k := 0; k < factRequirementParametersMaxCount; k++ {
			params[uniqueID("p", k, 16)] = escaped(256)
		}
		q.FactRequirements = append(q.FactRequirements, ContextFabricFactRequirement{
			Kind:       kind,
			Subjects:   distinctSubjects(ContextFabricFindingSubjectsMaxCount, i*ContextFabricFindingSubjectsMaxCount),
			Parameters: params,
		})
	}
	return q
}

func repeatDrivers(n int) []ContextFabricDriverJudgment {
	out := make([]ContextFabricDriverJudgment, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ContextFabricDriverJudgment{
			DriverID:         uniqueID("d", i, ContextFabricModelMintedIDMaxLength),
			Standing:         ContextFabricDriverPrincipal,
			Category:         string(maximalDriverCategory),
			Title:            escaped(ContextFabricDriverTitleMaxLength),
			Summary:          escaped(ContextFabricDriverSummaryMaxLength),
			Qualification:    escaped(ContextFabricDriverQualificationMaxLength),
			AffectedSubjects: distinctSubjects(ContextFabricDriverAffectedSubjectsMaxCount, i*ContextFabricDriverAffectedSubjectsMaxCount),
			PathIDs:          repeatStrings(ContextFabricDriverPathIDsMaxCount, ContextFabricIdentifierRefMaxLength),
			EvidenceRefIDs:   repeatEvidenceRefs(ContextFabricNestedEvidenceRefIDsMaxCount, ContextFabricEvidenceRefIDMaxLength),
			// Every cited id must RESOLVE in the claimed-fact map, so this
			// cannot simply be padded to ContextFabricDriverClaimedFactIDsMaxCount:
			// it is capped by how many facts the document actually carries.
			ClaimedFactIDs:  claimedFactIDs(minInt(ContextFabricDriverClaimedFactIDsMaxCount, ContextFabricClaimedFactsMaxCount)),
			Derivation:      ContextFabricDerivationCanonicalStructured,
			EpistemicStatus: ContextFabricEpistemicObserved,
			Confidence:      0.9,
			Current:         true,
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

func claimedFactID(i int) string { return uniqueID("c", i, ContextFabricModelMintedIDMaxLength) }

var maximalClaimedFactID = claimedFactID(0)

// repeatClaimedFacts maximizes each fact's SCALAR fields. Rows are left empty
// on purpose and are a declared lower-bound axis, like path edges: the combined
// Rows/TimeSeriesRows cap applies only when BOTH tables are non-empty, so a
// single-table fact is governed by its own ~8.45M content-byte bound instead --
// 250 facts at that size is ~2GB, which cannot be marshaled in memory.
func repeatClaimedFacts(n int) []ContextFabricClaimedFact {
	value := oneRune
	out := make([]ContextFabricClaimedFact, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ContextFabricClaimedFact{
			ClaimID: claimedFactID(i),
			Kind:    maximalDriverFactKind,
			Subject: minimalSubject(),
			Field:   escaped(ContextFabricClaimedFieldMaxLength),
			Value:   ContextFabricScalarValue{String: &value},
		})
	}
	return out
}

// minimalResolution commits NOTHING. Round 1 finding 1: the validator requires
// only that both slices be non-nil, so a committed subject is 49 bytes the
// irreducible answer does not have to carry. An answer with no committed
// subject is still a valid answer.
func minimalResolution() ContextFabricSubjectResolution {
	return ContextFabricSubjectResolution{
		Candidates: []ContextFabricSubjectCandidate{},
		Committed:  []ContextFabricSubjectRef{},
	}
}

// minimalCoverage sets Partial TRUE: `true` is one byte shorter than `false`,
// so the zero value is not the byte minimum. Same rule as Reused. The recursive
// encoding sweep found this one; round 2 did not name it.
func minimalCoverage() ContextFabricCoverage {
	return ContextFabricCoverage{
		Sources:         []ContextFabricSourceObservation{},
		DegradedReasons: []string{},
		Partial:         true,
	}
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
	// ModelIdentity has its OWN bound, wider than the version bound the
	// other fields share.
	v.ModelIdentity = escaped(ContextFabricModelIdentityMaxLength)
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
	plan.Requirements = maximalPlanRequirements()
	return &plan
}

// maximalPlanRequirements fills the plan's requirement array to its count
// bound with rows that are each individually maximal.
//
// IDENTITIES MUST BE DISTINCT -- ValidateContextFabricPlanRequirements rejects
// a repeat, because a duplicate would make the join to the outcome rows
// ambiguous -- so the rows cannot simply be one row repeated. They are
// generated by walking the (obligation, role, subject kind) product, which has
// 13 x 4 x 15 = 780 members against a bound of 200, so the walk cannot run out
// and cannot collide.
//
// EVERY ROW IS A COMPUTATION, and that is a byte choice rather than a
// semantic one: the three server arms are mutually exclusive, and the computed
// arm is the larger of them (a step, an execution, an input class AND the full
// input-kind list, against a read's kind list alone). Which arm is saturated
// is disclosed in unsaturatedByDesign, because saturating one necessarily
// leaves the others empty -- that is the invariant, not a gap in this builder.
func maximalPlanRequirements() []ContextFabricPlanRequirement {
	kinds := ContextFabricFactKindVocabulary()
	obligations := ContextFabricAnswerObligationVocabulary()
	roles := ContextFabricSubjectRoleVocabulary()
	subjects := ContextFabricSubjectKindVocabulary()

	out := make([]ContextFabricPlanRequirement, 0, ContextFabricPlanRequirementsMaxCount)
	for index := 0; len(out) < ContextFabricPlanRequirementsMaxCount; index++ {
		// Vary the SLOWEST-changing coordinate last so the first rows differ
		// in subject kind: the saturation probe reads element 0 only, and a
		// walk that changed obligation first would make every early row share
		// one obligation without changing what the probe sees.
		obligation := obligations[(index/(len(roles)*len(subjects)))%len(obligations)]
		role := roles[(index/len(subjects))%len(roles)]
		subject := subjects[index%len(subjects)]
		row := ContextFabricPlanRequirement{
			Obligation: obligation,
			Role:       role,
			Subject:    subject,
			Kind:       contextFabricObligationKindComputed,
			// The longest members of their vocabularies, so the row is at
			// its byte maximum rather than merely valid.
			Step:           "membership_cardinality",
			StepExecution:  "server_executed",
			InputClass:     contextFabricComputedInputFactKinds,
			InputFactKinds: append([]ContextFabricFactKind{}, kinds[:]...),
			Scope:          "single_subject",
			Quantifier:     "corroborated",
		}
		row.Requirement = row.Obligation + "/" + row.Role + "/" + string(row.Subject)
		out = append(out, row)
	}
	return out
}

// maximalRefinements fills one outcome row's refinement chain to its bound.
//
// The chain must RECONCILE with the row it sits on: it starts at Declared,
// ends at Served, and each step continues the previous one. A builder that
// produced a plausible-looking list without that arithmetic would be building
// a fixture the validator rejects, which is how a bound fixture stops
// measuring the bound and starts measuring itself.
func maximalRefinements(declared, served int) []ContextFabricRequirementRefinement {
	steps := ContextFabricRequirementRefinementMaxCount
	if declared-served < steps {
		// Each step must strictly reduce, so a chain longer than the total
		// reduction cannot be built. Say so rather than emitting a shorter
		// chain that silently fails to reach the bound.
		return nil
	}
	stages := ContextFabricOutcomeStageVocabulary()
	out := make([]ContextFabricRequirementRefinement, 0, steps)
	before := declared
	for i := 0; i < steps; i++ {
		after := before - 1
		if i == steps-1 {
			after = served
		}
		out = append(out, ContextFabricRequirementRefinement{
			Stage:  stages[i%len(stages)],
			Basis:  ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: before,
			After:  after,
		})
		before = after
	}
	return out
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
		ReceiptID: uniqueID("rcpt", i, resultIDMaxRunes),
		// Candidates have their OWN evidence-ref bound (contextFabricWriteBounds.candidateEvidenceRefs = 100),
		// not the nested bound the rest of the document uses.
		EvidenceRefIDs: repeatEvidenceRefs(candidateEvidenceRefsMaxCount, ContextFabricEvidenceRefIDMaxLength),
		State:          ContextFabricResolutionCommitted,
		Confidence:     1,
		Subject:        distinctSubject(i),
		MatchedTerms:   repeatStrings(matchedTermsMaxCount, ContextFabricSubjectOrComparisonTermMaxLength),
		MatchReasons:   repeatStrings(matchReasonsMaxCount, matchReasonMaxRunes),
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
	return ContextFabricSubjectResolution{
		Candidates:          candidates,
		Committed:           committed,
		ClarificationPrompt: escaped(clarificationPromptMaxRunes),
	}
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

// --- the ten optional composites ---

const (
	pathsMaxCount              = 250 // validate_context_fabric_result.go:1381
	pathNodesMaxCount          = contextFabricRelationshipPathMaxNodes
	pathWhyRelevantMaxRunes    = 2000 // contextFabricWriteBounds.pathWhyRelevantLength
	pathEvidenceRefsMaxCount   = 200  // contextFabricWriteBounds.pathEvidenceRefs
	cohortMembersMaxCount      = 250  // validate_context_fabric_result.go:160
	cohortRationaleMaxRunes    = 4000
	inclusionReasonsMaxCount   = 32   // contextFabricWriteBounds.cohortInclusionReasons
	inclusionReasonMaxRunes    = 1000 // contextFabricWriteBounds.cohortInclusionReasonLength
	memberEvidenceRefsMaxCount = 100  // contextFabricWriteBounds.memberEvidenceRefs
)

// maximalPath builds one path at every bound. Edges are NOT free: the
// validator requires len(Edges) == len(Nodes)-1 AND edge i to run exactly
// from Nodes[i] to Nodes[i+1], so the edge list is determined by the node
// list rather than independently maximizable.
func maximalPath(index int) ContextFabricRelationshipPath {
	nodes := make([]ContextFabricSubjectRef, pathNodesMaxCount)
	for i := range nodes {
		nodes[i] = distinctSubject(index*pathNodesMaxCount + i)
	}
	edges := make([]ContextFabricRelationshipEdge, 0, len(nodes)-1)
	for i := 0; i < len(nodes)-1; i++ {
		edges = append(edges, ContextFabricRelationshipEdge{
			Type:            ContextFabricRelationshipCorrelatedWithIncident,
			From:            nodes[i],
			To:              nodes[i+1],
			Derivation:      ContextFabricDerivationCanonicalStructured,
			EpistemicStatus: ContextFabricEpistemicObserved,
			// Edges pass allowEmpty=false, so each needs >=1 ref. This is
			// deliberately the MINIMUM legal count, not the bound of 100:
			// 250 paths x 50 edges x 100 refs x 256 runes is ~320M runes
			// of ids alone, which no process can marshal. The contract
			// permits a document that cannot be built in memory -- which
			// is itself the strongest form of the finding that no static
			// byte constant can bound an answer. The recorded maximal is
			// therefore a LOWER bound on this axis.
			EvidenceRefIDs: repeatEvidenceRefs(1, resultIDMinRunes),
		})
	}
	return ContextFabricRelationshipPath{
		PathID:         uniqueID("path", index, resultIDMaxRunes),
		Nodes:          nodes,
		Edges:          edges,
		WhyRelevant:    escaped(pathWhyRelevantMaxRunes),
		EvidenceRefIDs: repeatEvidenceRefs(pathEvidenceRefsMaxCount, ContextFabricEvidenceRefIDMaxLength),
	}
}

func maximalPaths() []ContextFabricRelationshipPath {
	out := make([]ContextFabricRelationshipPath, pathsMaxCount)
	for i := range out {
		out[i] = maximalPath(i)
	}
	return out
}

// maximalCohortMember keeps RankingComputed FALSE on purpose. The contract
// makes that a structural fork, not a shortcut: when RankingComputed is
// false, Score, AttentionRank, DataCompleteness, RankingBasis, Drivers,
// Outcome and MissingSignals must ALL be absent or zero
// (validate_context_fabric_result.go:476). The ranked variant is therefore a
// different document shape with its own chain of interlocking invariants
// (Score iff Outcome is qualified/provisional; MissingSignals empty iff
// qualified; Drivers exactly the family-name subset of RankingBasis), so the
// cohort's contribution here is a LOWER bound on a ranked cohort's.
func maximalCohortMember(i int) ContextFabricCohortMember {
	return ContextFabricCohortMember{
		Subject:          distinctSubject(i),
		Rank:             i + 1,
		InclusionReasons: repeatStrings(inclusionReasonsMaxCount, inclusionReasonMaxRunes),
		EvidenceRefIDs:   repeatEvidenceRefs(memberEvidenceRefsMaxCount, ContextFabricEvidenceRefIDMaxLength),
	}
}

func maximalCohort() *ContextFabricCohort {
	members := make([]ContextFabricCohortMember, cohortMembersMaxCount)
	for i := range members {
		members[i] = maximalCohortMember(i)
	}
	return &ContextFabricCohort{
		// Kind must equal EVERY member subject kind (validate_context_fabric_result.go:175),
		// so the cohort kind is chosen to match distinctSubject, not the other way round.
		Kind:      ContextFabricSubjectProject,
		Members:   members,
		Rationale: escaped(cohortRationaleMaxRunes),
		Complete:  true,
	}
}

// maximalTemporal is COUPLED to the interpretation: Requested must equal
// Interpretation.TimeContext exactly, so this takes the context rather than
// inventing one.
func maximalTemporal(ctx ContextFabricTimeContext) *ContextFabricTemporalLabel {
	return &ContextFabricTemporalLabel{
		Requested:        ctx,
		Effective:        ctx,
		Grain:            ContextFabricGrainDay,
		CoverageComplete: true,
	}
}

// deriveEvidenceRefLabels is the SECOND derived field, alongside
// Completeness: the label map must have exactly one entry per member of the
// result's own evidence-ref closure, so it cannot be built in table order
// either.
func deriveEvidenceRefLabels(r *ContextFabricInvestigationResult, label string) {
	closure := ContextFabricEvidenceRefClosure(*r)
	if len(closure) == 0 {
		r.EvidenceRefLabels = nil
		return
	}
	labels := make(map[string]string, len(closure))
	for ref := range closure {
		labels[ref] = label
	}
	r.EvidenceRefLabels = labels
}

// maximalConfirmedStructure carries ONE entry per structure need kind, which
// is the bound: the validator rejects a duplicate member, so the vocabulary's
// own size caps this list rather than a separate count.
//
// Source is "receipt" on purpose -- it is the only source that carries BOTH a
// prior result id and a receipt id. "carried" forbids the receipt id and
// every other source forbids both, so receipt is the widest legal entry.
func maximalConfirmedStructure() []ContextFabricConfirmedStructureEntry {
	out := make([]ContextFabricConfirmedStructureEntry, 0, ContextFabricStructureNeedKindCount)
	for i, kind := range contextFabricStructureNeedKinds {
		out = append(out, ContextFabricConfirmedStructureEntry{
			Member:         kind,
			AppliedValue:   escaped(256),
			Source:         ContextFabricStructureSourceReceipt,
			PriorResultID:  uniqueID("prior", i, resultIDMaxRunes),
			ReceiptID:      uniqueID("rcpt", i, resultIDMaxRunes),
			OfferSource:    ContextFabricStructureOfferEngine,
			PriorVersionID: escaped(256),
			PriorEntryID:   escaped(256),
			Provenance:     ContextFabricStructureClarificationConfirmed,
			Disposition:    ContextFabricStructureDispositionApplied,
		})
	}
	return out
}

// maximalOfferSnapshot is bounded per member, not by a flat cap: the
// vocabulary's size times each member's own mint-time offer cap.
func maximalOfferSnapshot() []ContextFabricStructureOfferSnapshotEntry {
	out := make([]ContextFabricStructureOfferSnapshotEntry, 0, ContextFabricStructureNeedKindCount*contextFabricStructureNeedsMaxOptions)
	for _, kind := range contextFabricStructureNeedKinds {
		for rank := 0; rank < contextFabricStructureNeedsMaxOptions; rank++ {
			out = append(out, ContextFabricStructureOfferSnapshotEntry{
				Member:         kind,
				OfferID:        escaped(256),
				Rank:           rank,
				OfferSource:    ContextFabricStructureOfferPrior,
				PriorVersionID: escaped(256),
				PriorEntryID:   escaped(256),
			})
		}
	}
	return out
}

// nonAllTimeWindowID returns a relative window id that is NOT the all_time
// sentinel. all_time is special-cased: it must NOT carry explicit bounds,
// so it cannot be used to build the widest option.
func nonAllTimeWindowID() ContextFabricRelativeWindowID {
	for _, id := range contextFabricRelativeWindowIDs {
		if id != ContextFabricRelativeWindowAllTime {
			return id
		}
	}
	return ContextFabricRelativeWindowAllTime
}

func windowBounds() (*time.Time, *time.Time) {
	start := fixedAnswerInstant.Add(-24 * time.Hour)
	end := fixedAnswerInstant
	return &start, &end
}

// maximalWindowClarification fills the option list to its bound. Receipt ids
// must carry the winr_ namespace prefix, and both receipt and option ids must
// be unique within the result.
func maximalWindowClarification() *ContextFabricWindowClarification {
	start, end := windowBounds()
	options := make([]ContextFabricWindowOption, 0, contextFabricWindowClarificationMaxOptions)
	for i := 0; i < contextFabricWindowClarificationMaxOptions; i++ {
		options = append(options, ContextFabricWindowOption{
			ReceiptID: ContextFabricWindowOptionReceiptPrefix + uniqueID("w", i, resultIDMaxRunes-len(ContextFabricWindowOptionReceiptPrefix)),
			// OptionID must be UNIQUE within the result as well as bounded,
			// so it cannot simply be padded to the maximum like a free string.
			OptionID:   uniqueID("opt", i, 256),
			Label:      escaped(200),
			RelativeID: nonAllTimeWindowID(),
			Start:      start,
			End:        end,
		})
	}
	return &ContextFabricWindowClarification{Options: options}
}

func maximalEffectiveWindow() *ContextFabricEffectiveEvidenceWindow {
	start, end := windowBounds()
	return &ContextFabricEffectiveEvidenceWindow{
		Start:      start,
		End:        end,
		RelativeID: nonAllTimeWindowID(),
		Provenance: ContextFabricWindowClarificationConfirmed,
	}
}

// maximalStructureNeeds fills Missing and every offer list to its own bound.
// The option ids are drawn from ONE counter because the validator requires
// receipt and option ids to be unique ACROSS every offer list, not merely
// within one -- six lists that each looked internally consistent would still
// be rejected.
func maximalStructureNeeds() *ContextFabricStructureNeeds {
	// Each offer type carries its OWN receipt namespace prefix -- kindr_,
	// ancr_, handr_, candr_ -- so a single shared receipt format is not
	// legal even though the ids share one uniqueness space.
	next := 0
	ids := func(prefix string) (string, string) {
		next++
		return prefix + uniqueID("r", next, resultIDMaxRunes-len(prefix)), uniqueID("so", next, resultIDMaxRunes)
	}
	needs := &ContextFabricStructureNeeds{}
	needs.Missing = append(needs.Missing, contextFabricStructureNeedKinds[:]...)
	for i := 0; i < contextFabricStructureNeedsMaxOptions; i++ {
		rid, oid := ids(ContextFabricKindOptionReceiptPrefix)
		needs.KindOptions = append(needs.KindOptions, ContextFabricKindOption{
			ReceiptID: rid, OptionID: oid, Label: escaped(200),
			Kind: ContextFabricSubjectProject, OfferSource: ContextFabricStructureOfferEngine,
			PriorVersionID: escaped(256), PriorEntryID: escaped(256), Phrasing: escaped(200),
		})
		rid, oid = ids(ContextFabricAnchorOptionReceiptPrefix)
		needs.AnchorOptions = append(needs.AnchorOptions, ContextFabricAnchorOption{
			ReceiptID: rid, OptionID: oid, Label: escaped(200),
			Kind: ContextFabricSubjectProject, CanonicalID: uniqueID("anc", i, ContextFabricSubjectRefCanonicalIDMaxLength),
			// MatchedTermHash is a fixed-shape 24-character lowercase hex
			// digest, not a free string: it has no maximum to pad to.
			MatchedTermHash: fmt.Sprintf("%024x", i),
			OfferSource:     ContextFabricStructureOfferEngine,
			PriorVersionID:  escaped(256), PriorEntryID: escaped(256), Phrasing: escaped(200),
		})
		rid, oid = ids(ContextFabricHandleOptionReceiptPrefix)
		needs.HandleOptions = append(needs.HandleOptions, ContextFabricHandleOption{
			ReceiptID: rid, OptionID: oid, Label: escaped(200),
			Kind: ContextFabricSubjectProject, PatternID: uniqueID("pat", i, 128),
			Value: escaped(256), SourceColumn: escaped(128), OfferSource: ContextFabricStructureOfferEngine,
			PriorVersionID: escaped(256), PriorEntryID: escaped(256), Phrasing: escaped(200),
		})
		rid, oid = ids(ContextFabricCandidateOptionReceiptPrefix)
		needs.CandidateOptions = append(needs.CandidateOptions, ContextFabricCandidateOption{
			ReceiptID: rid, OptionID: oid, Label: escaped(200),
			Kind: ContextFabricSubjectProject, CanonicalID: uniqueID("cnd", i, ContextFabricSubjectRefCanonicalIDMaxLength),
			OfferSource:    ContextFabricStructureOfferEngine,
			PriorVersionID: escaped(256), PriorEntryID: escaped(256), Phrasing: escaped(200),
		})
	}
	return needs
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// distinctSubjects builds n subject refs with distinct canonical ids, offset so
// that separate collections do not collide when uniqueness spans them.
func distinctSubjects(n, offset int) []ContextFabricSubjectRef {
	out := make([]ContextFabricSubjectRef, n)
	for i := range out {
		out[i] = distinctSubject(offset + i)
	}
	return out
}

// claimedFactIDs names the first n claimed facts the maximal document carries,
// so every driver citation resolves.
func claimedFactIDs(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, claimedFactID(i))
	}
	return out
}

const (
	// Bounds the saturation probe proved reachable but that no named constant
	// exposes: both are literals inside the validators.
	factRequirementParametersMaxCount = 32
	clarificationPromptMaxRunes       = 2000 // validate_context_fabric_result.go:108
	candidateEvidenceRefsMaxCount     = 100  // contextFabricWriteBounds.candidateEvidenceRefs
)
