package v1

import (
	"strconv"
	"strings"
	"testing"
)

// boundaryCase is one cell of the AT-THE-MAXIMUM half of the input domain.
//
// The clause domain table next door feeds every bound its boundary+1 value
// and asserts the clause that fires. That half alone cannot see a guard
// narrowed from `>` to `>=` (or widened from `<` to `<=`): the over-the-line
// value rejects under both. A mutation battery arm proved exactly that on
// the affected_subjects count — `>` became `>=` and no test in the suite
// noticed, because nothing ever fed a value sitting EXACTLY on a maximum.
//
// So each cell here sets one field to exactly its documented bound and
// asserts that the clause for THAT field does not fire. Stating it as "this
// clause must not fire" rather than "the struct must validate" is what makes
// the rule expressible for every bound: a finding kind exactly at its
// maximum LENGTH cannot also be a member of the closed category vocabulary,
// so that cell has a legitimate later clause and still pins its own.
type boundaryCase struct {
	name string
	// notClause must NOT be the reported clause: the value is legal at the
	// boundary, so the guard for this field must let it through.
	notClause ContextFabricRejectedClause
	// alsoValid is true when the whole value stays legal at the boundary, so
	// the cell can make the stronger assertion (Validate() == nil and the
	// traversal reports `none`). False where a different clause legitimately
	// fires afterwards.
	alsoValid bool
	driver    func(ContextFabricDriverJudgment) ContextFabricDriverJudgment
	claim     func(ContextFabricClaimedFact) ContextFabricClaimedFact
	finding   func(ContextFabricFinding) ContextFabricFinding
}

func evidenceRefsOfSize(n int) []string {
	refs := make([]string, n)
	for i := range refs {
		// Each ref must clear the 8-character item minimum, stay unique, be
		// exactly trimmed and carry no '|' separator.
		refs[i] = "evidence_" + strings.Repeat("a", i%16) + "_" + strconv.Itoa(i)
	}
	return refs
}

func boundaryCases() []boundaryCase {
	return []boundaryCase{
		// --- driver: lengths ---
		{name: "driver_id exactly at maximum", notClause: ContextFabricClauseDriverIDLength, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.DriverID = strings.Repeat("d", ContextFabricModelMintedIDMaxLength)
				return d
			}},
		{name: "title exactly at maximum", notClause: ContextFabricClauseDriverTitle, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Title = strings.Repeat("a", ContextFabricDriverTitleMaxLength)
				return d
			}},
		{name: "summary exactly at maximum", notClause: ContextFabricClauseDriverSummary, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Summary = strings.Repeat("a", ContextFabricDriverSummaryMaxLength)
				return d
			}},
		{name: "qualification exactly at maximum", notClause: ContextFabricClauseDriverQualificationSize, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Qualification = strings.Repeat("a", ContextFabricDriverQualificationMaxLength)
				return d
			}},
		// --- driver: counts, both ends ---
		{name: "affected_subjects exactly at minimum", notClause: ContextFabricClauseDriverAffectedSubjectsBelowMinimum, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.AffectedSubjects = subjectRefsOfSize(ContextFabricDriverAffectedSubjectsMinCount)
				return d
			}},
		{name: "affected_subjects exactly at maximum", notClause: ContextFabricClauseDriverAffectedSubjectsAboveMaximum, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.AffectedSubjects = subjectRefsOfSize(ContextFabricDriverAffectedSubjectsMaxCount)
				return d
			}},
		{name: "path_ids exactly at maximum", notClause: ContextFabricClauseDriverPathIDsAboveMaximum, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.PathIDs = trimmedStringsOfSize(ContextFabricDriverPathIDsMaxCount, "p")
				return d
			}},
		{name: "evidence_ref_ids exactly at maximum", notClause: ContextFabricClauseDriverEvidenceRefIDsShape, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.EvidenceRefIDs = evidenceRefsOfSize(contextFabricWriteBounds.nestedEvidenceRefs)
				return d
			}},
		{name: "claimed_fact_ids exactly at maximum", notClause: ContextFabricClauseDriverClaimedFactIDsAboveMaximum, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.ClaimedFactIDs = trimmedStringsOfSize(ContextFabricDriverClaimedFactIDsMaxCount, "c")
				return d
			}},
		// --- driver: item lengths inside the shared helpers ---
		{name: "path_id item exactly at maximum", notClause: ContextFabricClauseDriverPathIDsShape, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.PathIDs = []string{strings.Repeat("p", ContextFabricIdentifierRefMaxLength)}
				return d
			}},
		{name: "claimed_fact_id item exactly at maximum", notClause: ContextFabricClauseDriverClaimedFactIDsShape, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.ClaimedFactIDs = []string{strings.Repeat("c", ContextFabricIdentifierRefMaxLength)}
				return d
			}},
		{name: "affected subject canonical_id exactly at maximum", notClause: ContextFabricClauseDriverAffectedSubjectsShape, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.AffectedSubjects = []ContextFabricSubjectRef{{
					Kind: ContextFabricSubjectProject, Label: "P",
					CanonicalID: strings.Repeat("p", ContextFabricSubjectRefCanonicalIDMaxLength),
				}}
				return d
			}},
		{name: "affected subject label exactly at maximum", notClause: ContextFabricClauseDriverAffectedSubjectsShape, alsoValid: true,
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.AffectedSubjects = []ContextFabricSubjectRef{{
					Kind: ContextFabricSubjectProject, CanonicalID: "project_1",
					Label: strings.Repeat("p", ContextFabricSubjectRefLabelMaxLength),
				}}
				return d
			}},
		// --- claimed fact ---
		{name: "claim_id exactly at maximum", notClause: ContextFabricClauseClaimIDLength, alsoValid: true,
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.ClaimID = strings.Repeat("c", ContextFabricModelMintedIDMaxLength)
				return c
			}},
		{name: "field exactly at maximum", notClause: ContextFabricClauseClaimFieldLength, alsoValid: true,
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Field = strings.Repeat("f", ContextFabricClaimedFieldMaxLength)
				return c
			}},
		{name: "string value exactly at maximum", notClause: ContextFabricClauseClaimValueShape, alsoValid: true,
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				value := strings.Repeat("v", ContextFabricClaimedFactValueMaxLength)
				c.Value = ContextFabricScalarValue{String: &value}
				return c
			}},
		{name: "claim subject canonical_id exactly at maximum", notClause: ContextFabricClauseClaimSubjectShape, alsoValid: true,
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Subject.CanonicalID = strings.Repeat("p", ContextFabricSubjectRefCanonicalIDMaxLength)
				return c
			}},
		{name: "claim subject label exactly at maximum", notClause: ContextFabricClauseClaimSubjectShape, alsoValid: true,
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Subject.Label = strings.Repeat("p", ContextFabricSubjectRefLabelMaxLength)
				return c
			}},
		{name: "rows exactly at the row maximum", notClause: ContextFabricClauseClaimRowsShape, alsoValid: true,
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Rows = rowsOfSize(ContextFabricClaimedFactMaxRows, 1)
				return c
			}},
		{name: "one row exactly at the field maximum", notClause: ContextFabricClauseClaimRowsShape, alsoValid: true,
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Rows = rowsOfSize(1, ContextFabricClaimedFactRowMaxFields)
				return c
			}},
		{name: "rows plus time series rows exactly at the combined cell maximum", notClause: ContextFabricClauseClaimRowsCombinedShape, alsoValid: true,
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				// Both arrays non-empty, so the combined rule is actually
				// reached (it returns early when either is empty), summing to
				// exactly the cap: 63x32 + 1x32 = 2048.
				c.Rows = rowsOfSize(ContextFabricClaimedFactMaxRows-1, ContextFabricClaimedFactRowMaxFields)
				c.TimeSeriesRows = rowsOfSize(1, ContextFabricClaimedFactRowMaxFields)
				return c
			}},
		// --- finding ---
		{name: "finding_id exactly at maximum", notClause: ContextFabricClauseFindingIDLength, alsoValid: true,
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.FindingID = strings.Repeat("f", ContextFabricModelMintedIDMaxLength)
				return f
			}},
		{name: "finding kind exactly at maximum length", notClause: ContextFabricClauseFindingKindLength, alsoValid: false,
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				// alsoValid=false and deliberately so: a kind long enough to
				// sit on the length bound cannot also be a member of the
				// closed category vocabulary, so the vocabulary clause fires
				// afterwards. This cell still pins its own guard — the LENGTH
				// clause must not be the one reported.
				f.Kind = strings.Repeat("k", ContextFabricFindingKindMaxLength)
				return f
			}},
		{name: "finding summary exactly at maximum", notClause: ContextFabricClauseFindingSummaryLength, alsoValid: true,
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.Summary = strings.Repeat("s", ContextFabricFindingSummaryMaxLength)
				return f
			}},
		{name: "finding subjects exactly at maximum", notClause: ContextFabricClauseFindingSubjectsAboveMaximum, alsoValid: true,
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.Subjects = subjectRefsOfSize(ContextFabricFindingSubjectsMaxCount)
				return f
			}},
		{name: "finding claimed_fact_ids exactly at maximum", notClause: ContextFabricClauseFindingClaimedFactIDsAboveMaximum, alsoValid: true,
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.ClaimedFactIDs = trimmedStringsOfSize(ContextFabricDriverClaimedFactIDsMaxCount, "c")
				return f
			}},
		{name: "finding evidence_ref_ids exactly at maximum", notClause: ContextFabricClauseFindingEvidenceRefIDsShape, alsoValid: true,
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.EvidenceRefIDs = evidenceRefsOfSize(contextFabricWriteBounds.nestedEvidenceRefs)
				return f
			}},
	}
}

// TestDiagnoseContextFabricClauseDomainAcceptsValuesExactlyAtTheirBound
// executes the at-the-boundary half of the domain.
//
// It is the class fix for one surviving mutation arm, not a fix for the one
// field the arm reported: every count and length bound in all three
// traversals gets a cell, because a `>`-to-`>=` narrowing is invisible to a
// boundary+1-only suite wherever it is applied, and the surviving arm proved
// the suite had that shape everywhere.
func TestDiagnoseContextFabricClauseDomainAcceptsValuesExactlyAtTheirBound(t *testing.T) {
	for _, testCase := range boundaryCases() {
		t.Run(testCase.name, func(t *testing.T) {
			var (
				clause      ContextFabricRejectedClause
				validateErr error
			)
			switch {
			case testCase.driver != nil:
				driver := testCase.driver(validDiagnosisDriverJudgment())
				validateErr = driver.Validate()
				clause, _, _ = DiagnoseContextFabricDriverJudgmentClause(driver)
			case testCase.claim != nil:
				claim := testCase.claim(validDiagnosisClaimedFact())
				validateErr = claim.Validate()
				clause, _, _ = DiagnoseContextFabricClaimedFactClause(claim)
			case testCase.finding != nil:
				finding := testCase.finding(validDiagnosisFinding())
				validateErr = finding.Validate()
				clause, _, _ = DiagnoseContextFabricFindingClause(finding)
			default:
				t.Fatalf("case %q sets no mutator", testCase.name)
			}

			if clause == testCase.notClause {
				t.Fatalf("clause = %q for a value sitting EXACTLY on its bound -- the guard rejects the boundary itself (Validate() said: %v)", clause, validateErr)
			}
			if testCase.alsoValid {
				if validateErr != nil {
					t.Fatalf("Validate() = %v, want nil -- the value is legal at its boundary", validateErr)
				}
				if clause != ContextFabricClauseNone {
					t.Fatalf("clause = %q, want %q -- the value validates, so no clause may be reported", clause, ContextFabricClauseNone)
				}
			}
		})
	}
}

// TestBoundaryCasesCoverEveryCountAndLengthClause fails if a count- or
// length-shaped clause has no at-the-boundary cell. Enumerated from the
// vocabulary's own producer, so a new bound cannot be added with only its
// over-the-line half covered — which is the exact shape the surviving arm
// found.
func TestBoundaryCasesCoverEveryCountAndLengthClause(t *testing.T) {
	covered := make(map[ContextFabricRejectedClause]struct{}, len(boundaryCases()))
	for _, testCase := range boundaryCases() {
		covered[testCase.notClause] = struct{}{}
	}
	for _, clause := range ContextFabricRejectedClauses() {
		name := string(clause)
		// Only names that unambiguously denote a NUMERIC bound. A `_shape`
		// clause wraps a helper covering trim/duplicate/separator rules as
		// well as item length, so requiring a boundary cell for every one of
		// them would demand cells for rules that have no boundary; those
		// helpers' own length boundaries are covered by the explicit item
		// cells above instead.
		bounded := strings.HasSuffix(name, "_length") || strings.HasSuffix(name, "above_maximum") ||
			strings.HasSuffix(name, "below_minimum")
		if !bounded {
			continue
		}
		if _, ok := covered[clause]; !ok {
			t.Errorf("clause %q is count- or length-shaped but has no at-the-boundary cell", clause)
		}
	}
}

// agreementSpecimens returns every value the two domain tables construct,
// plus adversarial shapes aimed at the ONE way a clause-order mirror can be
// wrong without any single cell noticing: disagreeing with the validator
// about where a threshold sits.
func agreementSpecimens() (drivers []ContextFabricDriverJudgment, claims []ContextFabricClaimedFact, findings []ContextFabricFinding) {
	for _, testCase := range clauseDomainCases() {
		switch {
		case testCase.driver != nil:
			drivers = append(drivers, testCase.driver(validDiagnosisDriverJudgment()))
		case testCase.claim != nil:
			claims = append(claims, testCase.claim(validDiagnosisClaimedFact()))
		case testCase.finding != nil:
			findings = append(findings, testCase.finding(validDiagnosisFinding()))
		}
	}
	for _, testCase := range boundaryCases() {
		switch {
		case testCase.driver != nil:
			drivers = append(drivers, testCase.driver(validDiagnosisDriverJudgment()))
		case testCase.claim != nil:
			claims = append(claims, testCase.claim(validDiagnosisClaimedFact()))
		case testCase.finding != nil:
			findings = append(findings, testCase.finding(validDiagnosisFinding()))
		}
	}

	// Raw-versus-trimmed: boundedText measures the RAW value first, so a
	// field padded past its maximum with whitespace is rejected even though
	// its trimmed length fits.
	padded := validDiagnosisDriverJudgment()
	padded.Title = strings.Repeat("a", ContextFabricDriverTitleMaxLength) + "   "
	drivers = append(drivers, padded)
	paddedSummary := validDiagnosisDriverJudgment()
	paddedSummary.Summary = strings.Repeat("a", ContextFabricDriverSummaryMaxLength) + " "
	drivers = append(drivers, paddedSummary)
	paddedFinding := validDiagnosisFinding()
	paddedFinding.Summary = strings.Repeat("s", ContextFabricFindingSummaryMaxLength) + "  "
	findings = append(findings, paddedFinding)

	// Counts either side of the NESTED evidence-ref cap, which is not the
	// top-level cap: the value between the two is the one a mirror reading
	// the wrong constant waves through.
	for _, n := range []int{
		contextFabricWriteBounds.nestedEvidenceRefs,
		contextFabricWriteBounds.nestedEvidenceRefs + 1,
	} {
		d := validDiagnosisDriverJudgment()
		d.EvidenceRefIDs = evidenceRefsOfSize(n)
		drivers = append(drivers, d)
		f := validDiagnosisFinding()
		f.EvidenceRefIDs = evidenceRefsOfSize(n)
		findings = append(findings, f)
	}
	return drivers, claims, findings
}

// TestClauseTraversalAgreesWithTheValidator is the mechanical pin behind
// every hand-written cell above.
//
// The property is exact: a clause traversal reports a clause if and only if
// the validator it mirrors rejects the same value. Nothing else has to be
// asserted for a threshold divergence to be caught — a mirror that checks a
// count against 500 where the validator enforces 200, or measures a trimmed
// length where the validator measures the raw one, breaks this property on
// the first specimen that lands between the two thresholds, and says so
// without anyone having to notice the constant by eye.
//
// Both such divergences were PRESENT and were found this way, not by
// reading: the driver and finding traversals used the top-level evidence-ref
// cap instead of the nested one, and four bounded-text clauses measured only
// the trimmed value. Each let the traversal walk past the statement that
// actually rejected and name a later one — the wrong-name failure the
// clause-by-clause construction exists to prevent.
func TestClauseTraversalAgreesWithTheValidator(t *testing.T) {
	drivers, claims, findings := agreementSpecimens()
	if len(drivers) == 0 || len(claims) == 0 || len(findings) == 0 {
		t.Fatalf("specimen sets must be non-empty: %d drivers, %d claims, %d findings", len(drivers), len(claims), len(findings))
	}

	for i, driver := range drivers {
		clause, _, _ := DiagnoseContextFabricDriverJudgmentClause(driver)
		err := driver.Validate()
		if (clause != ContextFabricClauseNone) != (err != nil) {
			t.Errorf("driver specimen %d: clause=%q but Validate()=%v -- traversal and validator disagree about acceptance", i, clause, err)
		}
	}
	for i, claim := range claims {
		clause, _, _ := DiagnoseContextFabricClaimedFactClause(claim)
		err := claim.Validate()
		if (clause != ContextFabricClauseNone) != (err != nil) {
			t.Errorf("claim specimen %d: clause=%q but Validate()=%v -- traversal and validator disagree about acceptance", i, clause, err)
		}
	}
	for i, finding := range findings {
		clause, _, _ := DiagnoseContextFabricFindingClause(finding)
		err := finding.Validate()
		if (clause != ContextFabricClauseNone) != (err != nil) {
			t.Errorf("finding specimen %d: clause=%q but Validate()=%v -- traversal and validator disagree about acceptance", i, clause, err)
		}
	}
}
