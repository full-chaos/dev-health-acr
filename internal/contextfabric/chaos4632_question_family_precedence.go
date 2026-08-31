package contextfabric

import "strings"

// CHAOS-4632 §4.2: the precedence table. SHADOW ONLY -- see
// chaos4632_question_family_vocab.go's package-level note.
//
// THE ONE PROPERTY THIS FILE EXISTS FOR: structure signals are evaluated
// ABOVE Shape, and Shape only breaks ties among the families those signals
// leave open.
//
// Round 1 of the design's adversarial review showed why the original
// ordering (Shape first) was wrong, using the six replicate captures in
// ~/.cache/acr-kiac-askdev/results/triage-*.json (kiac/dh_0830, REAL
// data). Across two questions and six replicates, Shape took THREE
// distinct values, and SubjectTerms was absent entirely in one replicate:
//
//	Q-A typo r1   discovered_cohort   ["each team","project statuses"]
//	Q-A typo r2   explicit_cohort     ["each team","project"]
//	Q-A clean r1  discovered_cohort   ["each team"]
//	Q-A clean r2  discovered_cohort   null
//	Q-B r1        single_subject      ["fullchaos team"]
//	Q-B r2        explicit_cohort     ["fullchaos team"]
//
// Shape-first routed Q-A's explicit_cohort replicate to explicit_comparison
// and Q-B's single_subject replicate to subject_status -- both real
// contradictions with the design's own §7 walkthrough, and both caused by
// letting the least stable field decide first.
//
// WHAT THIS TABLE IS VALIDATED AGAINST, STATED PRECISELY, because an
// earlier revision of the design overclaimed here and round 2 caught it:
//
//   - The STRUCTURAL property is real and checkable on those captures: the
//     table does not read Shape until rows 4-6, so the three Shape values
//     the captures exhibit cannot by themselves split one question across
//     families. That is the property the Shape-first table lacked, and
//     TestPrecedenceIsStableAcrossTheCapturedShapeValues pins it.
//
//   - The ROUTING property is CONDITIONAL AND UNMEASURED. GroupKind and
//     ScopeAnchorTerm do not exist on today's wire, so they are null in
//     all six captures. Applying this table literally to the captures as
//     they stand routes Q-A's discovered samples to
//     discovered_cohort_ranking, Q-B r1 to subject_investigation, and the
//     explicit-cohort samples to unclassified. THE CAPTURES CANNOT
//     VALIDATE ROWS 1 AND 2. They can only show that rows 4-6 would
//     mis-route without them. Saying "checked against ground truth, this
//     table is stable" would be circular -- it would assume the very
//     emission this slice exists to measure.
//
// That is why this slice's gate is LABELLED SEMANTIC CORRECTNESS on a
// hand-labelled set INCLUDING NEGATIVE CASES, not emission rate. Row 1
// fires on GroupKind ALONE: a model that spuriously emits GroupKind on a
// plain single-subject status question sends that question to a grouped
// cohort, and an emission-rate gate scores that 100%.

// FamilyPrecedenceRow names WHICH row of the §4.2 table decided a sample's
// family. Closed vocabulary, telemetry-safe: it carries no question text
// and no subject identifier, only which rule fired.
//
// This exists because "the family was X" is not a diagnosable statement on
// its own -- AGENTS.md's CANONICAL ARCHITECTURE bar is that a defect must
// be diagnosable from the run's own completed artifacts, and two samples
// reaching the same family through different rows are different states
// worth telling apart.
type FamilyPrecedenceRow string

const (
	// FamilyPrecedenceRowGroupKind is row 1: GroupKind set.
	FamilyPrecedenceRowGroupKind FamilyPrecedenceRow = "group_kind_set"
	// FamilyPrecedenceRowScopeAnchor is row 2: ScopeAnchorTerm set AND the
	// question asks about a different kind than the anchor's.
	FamilyPrecedenceRowScopeAnchor FamilyPrecedenceRow = "scope_anchor_asymmetry"
	// FamilyPrecedenceRowComparison is row 3: non-empty ComparisonTerms,
	// or >=2 subject terms naming distinct subjects.
	FamilyPrecedenceRowComparison FamilyPrecedenceRow = "comparison_terms"
	// FamilyPrecedenceRowCohortShape is row 4: Shape in {discovered_cohort,
	// open}. The FIRST row that reads Shape at all.
	FamilyPrecedenceRowCohortShape FamilyPrecedenceRow = "cohort_shape"
	// FamilyPrecedenceRowSingleSubject is rows 5+6, merged by decision D1
	// (§10): Shape == single_subject. D1 merged subject_status and
	// subject_drivers into subject_investigation precisely because rows 5
	// and 6 had identical conditions -- row 5's sketched discriminator was
	// RequestedJudgment, free-text model output, and keying on it would be
	// exactly the heuristic this design forbids.
	FamilyPrecedenceRowSingleSubject FamilyPrecedenceRow = "single_subject_shape"
	// FamilyPrecedenceRowNone is row 7: anything else -> unclassified.
	// Refuse to guess. This is NOT an error and NOT a downgrade.
	FamilyPrecedenceRowNone FamilyPrecedenceRow = "no_row_matched"
)

// FamilyIncompatibilityReason names why a sample's OWN model-picked family
// was not the family the precedence table produced. Closed vocabulary.
//
// This is the field the design's round-2 review added after finding that
// the original telemetry event recorded only the outcome, so a downgraded
// decision could not be diagnosed from the run's own artifacts. Empty when
// the model's pick and the table agreed, or when the model picked nothing.
type FamilyIncompatibilityReason string

const (
	// FamilyIncompatibilityUnrecognized: the model emitted a family name
	// outside the closed vocabulary, and SanitizeQuestionFamily discarded
	// it.
	FamilyIncompatibilityUnrecognized FamilyIncompatibilityReason = "unrecognized_family"
	// FamilyIncompatibilityStructuralMismatch: the model's pick is a
	// vocabulary member, but this sample's own structure signals route to
	// a different family.
	FamilyIncompatibilityStructuralMismatch FamilyIncompatibilityReason = "structural_mismatch"
	// FamilyIncompatibilityUnreachable: the model picked a family that is
	// declared but deliberately unreachable in this slice (trend,
	// investment_allocation) -- see UnreachableQuestionFamilies.
	FamilyIncompatibilityUnreachable FamilyIncompatibilityReason = "declared_unreachable"
)

// FamilySample is ONE interpret sample's structure signals, as the
// precedence table reads them. It is deliberately a small value type over
// signals only -- never the whole InterpretedQuestion -- so the table's
// inputs are exactly enumerable and a table-driven test can construct
// every row without a model, a graph, or a database.
type FamilySample struct {
	// Shape is this sample's own InvestigationShape. Read ONLY by rows 4
	// and 5.
	Shape InvestigationShape
	// SubjectTerms is this sample's own subject terms. Row 3 counts
	// DISTINCT non-empty terms.
	SubjectTerms []string
	// ComparisonTerms is this sample's own comparison terms. Row 3 fires
	// on any non-empty entry.
	ComparisonTerms []string
	// GroupKind is the sanitized (closed-vocabulary) grouping kind, empty
	// when the model emitted none or emitted something outside the
	// registry. Row 1 fires on this ALONE.
	GroupKind SubjectKind
	// ScopeAnchorTerm is the sanitized scope anchor -- a RETRIEVAL
	// POINTER, never a value (see SanitizeScopeAnchorTerm). Row 2 reads
	// only whether it is set and how it relates to ExpectedKinds; nothing
	// anywhere branches on its text.
	ScopeAnchorTerm string
	// ScopeAnchorKind is the kind the anchor itself names, when the model
	// stated one. Row 2's asymmetry test is "the question asks about a
	// DIFFERENT kind than the anchor's" -- with no anchor kind stated,
	// the asymmetry cannot be established and row 2 does not fire, which
	// is the refuse-to-guess side of the rule.
	ScopeAnchorKind SubjectKind
	// RequestedKind is the kind the question asks ABOUT, when one is
	// determinable. Row 2's other half.
	RequestedKind SubjectKind
	// ModelFamily is this sample's own sanitized family pick, empty when
	// the model emitted none or emitted an unrecognized name.
	ModelFamily QuestionFamily
	// ModelFamilyUnrecognized records that the model DID emit a family
	// name and it was outside the vocabulary -- distinct from emitting
	// nothing, and the distinction is the whole point of the
	// FamilyIncompatibilityUnrecognized reason.
	ModelFamilyUnrecognized bool
}

// FamilySampleOutcome is the precedence table's verdict for ONE sample.
type FamilySampleOutcome struct {
	// Family is the family this sample resolves to. Never empty: the
	// table is TOTAL, and its last row is unclassified.
	Family QuestionFamily
	// Row names which rule fired.
	Row FamilyPrecedenceRow
	// AttemptedFamily is the model's own pick for this sample, empty when
	// it emitted none or an unrecognized one.
	AttemptedFamily QuestionFamily
	// IncompatibilityReason is why AttemptedFamily was not used. Empty
	// when it was used, or when there was nothing to use.
	IncompatibilityReason FamilyIncompatibilityReason
	// Downgraded is true iff the model asserted a family that this
	// sample's own structure signals did not support -- the same counted
	// "divergence is a telemetry event" concept WindowClassOutcome.
	// Downgraded carries for CHAOS-3900.
	Downgraded bool
}

// UnreachableQuestionFamilies are declared in the vocabulary (§3) but
// deliberately NOT reachable from the precedence table in this slice.
//
// trend needs a fact declared time_series to plan against (§5, slice S3)
// and investment_allocation needs a declared breakdown table plus a
// producer to render it. Declaring a family with no reachable path is
// DELIBERATE and has a precedent in this very repository: CHAOS-4415 slice
// 1 declared seven ContextFabricRenderKind members without producers, for
// the same reason -- the alternative is widening a closed enum later,
// underneath consumers who have already pinned it.
//
// A model pick naming one of these is treated as an incompatibility with
// its own reason (FamilyIncompatibilityUnreachable) rather than being
// silently ignored, so "the model wanted trend and could not have it" is a
// countable state that tells S3 whether the demand is real.
func UnreachableQuestionFamilies() []QuestionFamily {
	return []QuestionFamily{QuestionFamilyTrend, QuestionFamilyInvestmentAllocation}
}

func familyIsUnreachable(family QuestionFamily) bool {
	for _, member := range UnreachableQuestionFamilies() {
		if member == family {
			return true
		}
	}
	return false
}

// ResolveFamilyForSample applies the §4.2 precedence table to ONE sample,
// top to bottom, first match wins, and reports what the model had wanted
// instead.
//
// The table is TOTAL: every input reaches a row, and the last row is
// unclassified. There is no error return and no "could not decide" state
// distinct from unclassified, because unclassified IS the refuse-to-guess
// answer and today's unchanged behaviour.
func ResolveFamilyForSample(sample FamilySample) FamilySampleOutcome {
	family, row := precedenceFamily(sample)
	outcome := FamilySampleOutcome{Family: family, Row: row}

	switch {
	case sample.ModelFamilyUnrecognized:
		// The model emitted something; it was not a vocabulary member.
		// AttemptedFamily stays empty because there is no vocabulary
		// member to name -- recording the raw string would put model text
		// into a closed-vocabulary telemetry field.
		outcome.IncompatibilityReason = FamilyIncompatibilityUnrecognized
		outcome.Downgraded = true
	case sample.ModelFamily == "":
		// The model made no pick at all. Not a downgrade: there was
		// nothing to override. The table simply decided on its own.
	case familyIsUnreachable(sample.ModelFamily):
		outcome.AttemptedFamily = sample.ModelFamily
		outcome.IncompatibilityReason = FamilyIncompatibilityUnreachable
		outcome.Downgraded = true
	case sample.ModelFamily != family:
		outcome.AttemptedFamily = sample.ModelFamily
		outcome.IncompatibilityReason = FamilyIncompatibilityStructuralMismatch
		outcome.Downgraded = true
	default:
		// Model pick and table agree. Recorded as the attempted family so
		// an operator can distinguish "the model agreed" from "the model
		// was silent" -- both produce the same Family, and only this
		// field tells them apart.
		outcome.AttemptedFamily = sample.ModelFamily
	}
	return outcome
}

// precedenceFamily is the §4.2 table itself, and nothing else. Kept
// separate from the downgrade bookkeeping above so a test can assert the
// ROUTING in isolation from the model's pick -- the two are independent
// properties and conflating them is how a table-driven test ends up
// unable to fail.
func precedenceFamily(sample FamilySample) (QuestionFamily, FamilyPrecedenceRow) {
	// Row 1 -- GroupKind set. Structure signal, read before Shape.
	if sample.GroupKind != "" {
		return QuestionFamilyGroupedCohortStatus, FamilyPrecedenceRowGroupKind
	}
	// Row 2 -- ScopeAnchorTerm set AND the question asks about a DIFFERENT
	// kind than the anchor's. Both halves are required. An anchor with no
	// stated kind, or an anchor whose kind matches what is being asked
	// about ("the fullchaos team's team"), does not establish the
	// asymmetry, and this row declines rather than guessing -- the same
	// refuse-to-guess discipline row 7 encodes.
	if sample.ScopeAnchorTerm != "" && sample.ScopeAnchorKind != "" &&
		sample.RequestedKind != "" && sample.ScopeAnchorKind != sample.RequestedKind {
		return QuestionFamilyScopedCohortStatus, FamilyPrecedenceRowScopeAnchor
	}
	// Row 3 -- explicit comparison. Either an explicit comparison term, or
	// >=2 DISTINCT subject terms. Distinctness matters: the captures show
	// a replicate emitting ["each team","project statuses"] for a question
	// that is a grouped cohort, not a two-sided comparison, so counting
	// raw slice length would fire this row on duplicates and near-
	// duplicates of one term.
	if len(nonEmptyTerms(sample.ComparisonTerms)) > 0 || len(distinctTerms(sample.SubjectTerms)) >= 2 {
		return QuestionFamilyExplicitComparison, FamilyPrecedenceRowComparison
	}
	// Row 4 -- the FIRST row that reads Shape.
	if sample.Shape == ShapeDiscoveredCohort || sample.Shape == ShapeOpen {
		return QuestionFamilyDiscoveredCohortRanking, FamilyPrecedenceRowCohortShape
	}
	// Rows 5+6, merged by decision D1.
	if sample.Shape == ShapeSingleSubject {
		return QuestionFamilySubjectInvestigation, FamilyPrecedenceRowSingleSubject
	}
	// Row 7 -- anything else. Note that ShapeExplicitCohort with fewer
	// than two distinct subject terms lands HERE, not in a cohort family:
	// an explicit cohort whose members were never named is not something
	// this table can route, and unclassified is the honest answer.
	return QuestionFamilyUnclassified, FamilyPrecedenceRowNone
}

func nonEmptyTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		if trimmed := strings.TrimSpace(term); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// distinctTerms folds case and whitespace before counting, so
// ["Project","project "] is ONE term. It deliberately does no stemming and
// no synonym folding -- the same narrowness CanonicalizeQuestion's own doc
// comment defends for the reuse key, and for the same reason: widening it
// would be a correctness change to which family a question gets, not a
// cosmetic one.
func distinctTerms(terms []string) []string {
	seen := make(map[string]struct{}, len(terms))
	out := make([]string, 0, len(terms))
	for _, term := range nonEmptyTerms(terms) {
		key := strings.ToLower(term)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, term)
	}
	return out
}
