package v1

import (
	"strings"
	"testing"
)

// clauseDomainCase is one cell of the rejected-clause input domain: a
// mutation of an HONESTLY valid baseline that makes exactly one clause of
// that struct's Validate() the first to fail, and the clause name that
// must be reported for it.
//
// Exactly one mutator is set per case. A case whose clause is
// ContextFabricClauseNone mutates nothing and asserts the baseline.
type clauseDomainCase struct {
	clause  ContextFabricRejectedClause
	name    string
	driver  func(ContextFabricDriverJudgment) ContextFabricDriverJudgment
	claim   func(ContextFabricClaimedFact) ContextFabricClaimedFact
	finding func(ContextFabricFinding) ContextFabricFinding
}

func subjectRefsOfSize(n int) []ContextFabricSubjectRef {
	subjects := make([]ContextFabricSubjectRef, n)
	for i := range subjects {
		subjects[i] = ContextFabricSubjectRef{
			Kind: ContextFabricSubjectProject, CanonicalID: strings.Repeat("p", i+1), Label: "P",
		}
	}
	return subjects
}

func trimmedStringsOfSize(n int, seed string) []string {
	values := make([]string, n)
	for i := range values {
		values[i] = strings.Repeat(seed, i+1)
	}
	return values
}

func rowsOfSize(rows, fields int) []ContextFabricClaimedFactRow {
	built := make([]ContextFabricClaimedFactRow, rows)
	for i := range built {
		cells := make(map[string]ContextFabricScalarValue, fields)
		for j := 0; j < fields; j++ {
			cells[strings.Repeat("f", j+1)] = ContextFabricScalarValue{Null: true}
		}
		built[i] = ContextFabricClaimedFactRow{Fields: cells}
	}
	return built
}

// clauseDomainCases enumerates EVERY member of the
// ContextFabricRejectedClause vocabulary exactly once.
//
// The enumeration is not trusted to be complete by inspection:
// TestDiagnoseContextFabricClauseDomainCoversTheWholeVocabulary compares
// the clauses this table covers against
// ContextFabricRejectedClauses() -- the vocabulary's own single producer --
// and fails on any member with no executed cell. Adding a constant without
// adding a cell here is therefore a test failure, not a silent hole.
func clauseDomainCases() []clauseDomainCase {
	return []clauseDomainCase{
		// --- Driver judgment, statement 1 -------------------------------
		{clause: ContextFabricClauseNone, name: "driver baseline is honestly valid",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment { return d }},
		{clause: ContextFabricClauseDriverIDLength, name: "driver_id over maximum",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.DriverID = strings.Repeat("d", ContextFabricModelMintedIDMaxLength+1)
				return d
			}},
		{clause: ContextFabricClauseDriverStanding, name: "standing out of vocabulary",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Standing = "not_a_standing"
				return d
			}},
		{clause: ContextFabricClauseDriverCategory, name: "category out of vocabulary",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Category = "not_a_category"
				return d
			}},
		{clause: ContextFabricClauseDriverTitle, name: "title over maximum",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Title = strings.Repeat("a", ContextFabricDriverTitleMaxLength+1)
				return d
			}},
		{clause: ContextFabricClauseDriverSummary, name: "summary over maximum",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Summary = strings.Repeat("a", ContextFabricDriverSummaryMaxLength+1)
				return d
			}},
		{clause: ContextFabricClauseDriverDerivation, name: "derivation out of vocabulary",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Derivation = "not_a_derivation"
				return d
			}},
		{clause: ContextFabricClauseDriverEpistemicStatus, name: "epistemic_status out of vocabulary",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.EpistemicStatus = "not_a_status"
				return d
			}},
		{clause: ContextFabricClauseDriverConfidenceRange, name: "confidence above 1",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Confidence = 1.5
				return d
			}},
		{clause: ContextFabricClauseDriverQualificationSize, name: "qualification over maximum",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Qualification = strings.Repeat("a", ContextFabricDriverQualificationMaxLength+1)
				return d
			}},
		// --- Driver judgment, statement 2 -------------------------------
		{clause: ContextFabricClauseDriverAffectedSubjectsBelowMinimum, name: "affected_subjects empty",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.AffectedSubjects = nil
				return d
			}},
		{clause: ContextFabricClauseDriverAffectedSubjectsAboveMaximum, name: "affected_subjects over maximum",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.AffectedSubjects = subjectRefsOfSize(ContextFabricDriverAffectedSubjectsMaxCount + 1)
				return d
			}},
		{clause: ContextFabricClauseDriverAffectedSubjectsShape, name: "affected_subjects duplicated",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				subject := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_1", Label: "Project"}
				d.AffectedSubjects = []ContextFabricSubjectRef{subject, subject}
				return d
			}},
		{clause: ContextFabricClauseDriverPathIDsAboveMaximum, name: "path_ids over maximum",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.PathIDs = trimmedStringsOfSize(ContextFabricDriverPathIDsMaxCount+1, "p")
				return d
			}},
		{clause: ContextFabricClauseDriverPathIDsShape, name: "path_id untrimmed",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.PathIDs = []string{" path_1"}
				return d
			}},
		{clause: ContextFabricClauseDriverEvidenceRefIDsShape, name: "evidence_ref_id under item minimum",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.EvidenceRefIDs = []string{"tooshrt"}
				return d
			}},
		// --- Driver judgment, statement 3 -------------------------------
		{clause: ContextFabricClauseDriverClaimedFactIDsAboveMaximum, name: "claimed_fact_ids over maximum",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.ClaimedFactIDs = trimmedStringsOfSize(ContextFabricDriverClaimedFactIDsMaxCount+1, "c")
				return d
			}},
		{clause: ContextFabricClauseDriverClaimedFactIDsShape, name: "claimed_fact_id untrimmed",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.ClaimedFactIDs = []string{" claim_1"}
				return d
			}},
		// --- Driver judgment, statements 4-6 ----------------------------
		{clause: ContextFabricClauseDriverEvidenceClosureAbsent, name: "non-withheld driver cites neither path nor evidence",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				// An EMPTY, non-nil slice: boundedEvidenceRefs rejects a NIL
				// evidence array outright even with allowEmpty, so a nil here
				// would stop at statement 2 and never reach this rule.
				d.PathIDs, d.EvidenceRefIDs = nil, []string{}
				return d
			}},
		{clause: ContextFabricClauseDriverCategoryRequiresClaimedFact, name: "fact-shaped category cites no claimed fact",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Category = string(ContextFabricDriverCategoryStatus)
				d.ClaimedFactIDs = nil
				return d
			}},
		{clause: ContextFabricClauseDriverWithheldRequiresQualification, name: "withheld driver carries no qualification",
			driver: func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
				d.Standing, d.Qualification = ContextFabricDriverWithheld, ""
				return d
			}},
		// --- Claimed fact -----------------------------------------------
		{clause: ContextFabricClauseNone, name: "claimed fact baseline is honestly valid",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact { return c }},
		{clause: ContextFabricClauseClaimIDLength, name: "claim_id over maximum",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.ClaimID = strings.Repeat("c", ContextFabricModelMintedIDMaxLength+1)
				return c
			}},
		{clause: ContextFabricClauseClaimKind, name: "kind out of vocabulary",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Kind = "not_a_fact_kind"
				return c
			}},
		{clause: ContextFabricClauseClaimFieldLength, name: "field over maximum",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Field = strings.Repeat("f", ContextFabricClaimedFieldMaxLength+1)
				return c
			}},
		{clause: ContextFabricClauseClaimFieldUntrimmed, name: "field untrimmed",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Field = " release_ready"
				return c
			}},
		{clause: ContextFabricClauseClaimSubjectShape, name: "subject kind out of vocabulary",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Subject.Kind = "not_a_subject_kind"
				return c
			}},
		{clause: ContextFabricClauseClaimValueShape, name: "value sets no variant",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Value = ContextFabricScalarValue{}
				return c
			}},
		{clause: ContextFabricClauseClaimRowsShape, name: "row carries no fields",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Rows = []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{}}}
				return c
			}},
		{clause: ContextFabricClauseClaimTableShape, name: "table declared for rows the fact does not carry",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.Table, c.Rows = &ContextFabricClaimedFactTable{}, nil
				return c
			}},
		{clause: ContextFabricClauseClaimTimeSeriesRowsShape, name: "time series row carries no fields",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.TimeSeriesRows = []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{}}}
				return c
			}},
		{clause: ContextFabricClauseClaimTimeSeriesTableShape, name: "time series table declared for rows the fact does not carry",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				c.TimeSeriesTable, c.TimeSeriesRows = &ContextFabricClaimedFactTable{}, nil
				return c
			}},
		{clause: ContextFabricClauseClaimRowsCombinedShape, name: "rows and time series rows exceed the combined cell cap",
			claim: func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
				// Each array is INDEPENDENTLY within its own caps (64 rows x
				// 32 fields); only their SUM breaks the combined rule, which
				// is the only way to reach this statement rather than one of
				// the two per-array statements before it.
				c.Rows = rowsOfSize(ContextFabricClaimedFactMaxRows, ContextFabricClaimedFactRowMaxFields)
				c.TimeSeriesRows = rowsOfSize(1, 1)
				return c
			}},
		// --- Finding -----------------------------------------------------
		{clause: ContextFabricClauseNone, name: "finding baseline is honestly valid",
			finding: func(f ContextFabricFinding) ContextFabricFinding { return f }},
		{clause: ContextFabricClauseFindingIDLength, name: "finding_id over maximum",
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.FindingID = strings.Repeat("f", ContextFabricModelMintedIDMaxLength+1)
				return f
			}},
		{clause: ContextFabricClauseFindingKindLength, name: "kind over maximum",
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.Kind = strings.Repeat("k", ContextFabricFindingKindMaxLength+1)
				return f
			}},
		{clause: ContextFabricClauseFindingSummaryLength, name: "summary over maximum",
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.Summary = strings.Repeat("s", ContextFabricFindingSummaryMaxLength+1)
				return f
			}},
		{clause: ContextFabricClauseFindingSubjectsAboveMaximum, name: "subjects over maximum",
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.Subjects = subjectRefsOfSize(ContextFabricFindingSubjectsMaxCount + 1)
				return f
			}},
		{clause: ContextFabricClauseFindingSubjectsShape, name: "subjects duplicated",
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				subject := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_1", Label: "Project"}
				f.Subjects = []ContextFabricSubjectRef{subject, subject}
				return f
			}},
		{clause: ContextFabricClauseFindingEvidenceRefIDsShape, name: "evidence_ref_ids empty where a finding must cite",
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.EvidenceRefIDs = []string{}
				return f
			}},
		{clause: ContextFabricClauseFindingClaimedFactIDsAboveMaximum, name: "claimed_fact_ids over maximum",
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.ClaimedFactIDs = trimmedStringsOfSize(ContextFabricDriverClaimedFactIDsMaxCount+1, "c")
				return f
			}},
		{clause: ContextFabricClauseFindingClaimedFactIDsShape, name: "claimed_fact_id untrimmed",
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.ClaimedFactIDs = []string{" claim_1"}
				return f
			}},
		{clause: ContextFabricClauseFindingKindOutOfVocabulary, name: "kind out of the closed category vocabulary",
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.Kind = "not_a_category"
				return f
			}},
		{clause: ContextFabricClauseFindingKindRequiresClaimedFact, name: "fact-shaped kind cites no claimed fact",
			finding: func(f ContextFabricFinding) ContextFabricFinding {
				f.Kind, f.ClaimedFactIDs = string(ContextFabricDriverCategoryStatus), nil
				return f
			}},
	}
}

// TestDiagnoseContextFabricClauseDomain executes every cell of the
// rejected-clause input domain.
//
// Each cell asserts BOTH halves, because either alone is worthless: that
// the mutated value is genuinely rejected by its own Validate() (a cell
// whose value actually validates pins a clause for a rejection that never
// happens), and that the clause traversal names the clause the cell was
// built to trigger. The ContextFabricClauseNone cells invert both: the
// baseline must validate AND diagnose to "none" -- the non-vacuous control
// that catches a fixture which silently stopped being valid.
func TestDiagnoseContextFabricClauseDomain(t *testing.T) {
	for _, testCase := range clauseDomainCases() {
		t.Run(string(testCase.clause)+"/"+testCase.name, func(t *testing.T) {
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

			if testCase.clause == ContextFabricClauseNone {
				if validateErr != nil {
					t.Fatalf("baseline Validate() = %v, want nil -- the baseline this domain mutates is not itself valid, so every other cell is mutating an already-rejected value", validateErr)
				}
			} else if validateErr == nil {
				t.Fatalf("Validate() = nil, want a rejection -- this cell pins clause %q for a rejection that does not happen", testCase.clause)
			}
			if clause != testCase.clause {
				t.Fatalf("clause = %q, want %q (Validate() said: %v)", clause, testCase.clause, validateErr)
			}
		})
	}
}

// TestDiagnoseContextFabricClauseDomainCoversTheWholeVocabulary enumerates
// the vocabulary from its OWN producer and fails on any member with no
// executed cell above -- so a clause added to the vocabulary without a cell
// that actually produces it is a failure, not a silent hole. An instrument
// that cannot report what it does not cover is not an instrument.
func TestDiagnoseContextFabricClauseDomainCoversTheWholeVocabulary(t *testing.T) {
	covered := make(map[ContextFabricRejectedClause]int)
	for _, testCase := range clauseDomainCases() {
		covered[testCase.clause]++
	}
	for _, clause := range ContextFabricRejectedClauses() {
		if covered[clause] == 0 {
			t.Errorf("clause %q has no executed domain cell", clause)
		}
		delete(covered, clause)
	}
	for clause := range covered {
		t.Errorf("domain cell names %q, which is not a vocabulary member", clause)
	}
}

// TestContextFabricRejectedClauseOfReturnsTheTableConstant pins the
// log-injection guarantee: the accessor returns the package's own constant,
// and a value that never was a member cannot pass through it.
func TestContextFabricRejectedClauseOfReturnsTheTableConstant(t *testing.T) {
	if got := ContextFabricRejectedClauseOf("driver.title_length"); got != ContextFabricClauseDriverTitle {
		t.Fatalf("ContextFabricRejectedClauseOf(member) = %q, want %q", got, ContextFabricClauseDriverTitle)
	}
	if got := ContextFabricRejectedClauseOf("'; DROP TABLE receipts--"); got != ContextFabricClauseNone {
		t.Fatalf("ContextFabricRejectedClauseOf(non-member) = %q, want %q", got, ContextFabricClauseNone)
	}
	if ValidContextFabricRejectedClause("driver.not_a_clause") {
		t.Fatalf("ValidContextFabricRejectedClause(non-member) = true, want false")
	}
}

// TestDiagnoseContextFabricBoundIsAProjectionOfTheClauseTraversal pins the
// de-duplication itself: the bound each Diagnose*Bound reports must be the
// bound its Clause counterpart reports for the SAME value, over the whole
// domain. Two separately-written clause-order bodies could drift; one body
// with two projections cannot.
func TestDiagnoseContextFabricBoundIsAProjectionOfTheClauseTraversal(t *testing.T) {
	for _, testCase := range clauseDomainCases() {
		t.Run(string(testCase.clause)+"/"+testCase.name, func(t *testing.T) {
			var wantBound, gotBound string
			var wantOK, gotOK bool
			switch {
			case testCase.driver != nil:
				driver := testCase.driver(validDiagnosisDriverJudgment())
				_, wantBound, wantOK = DiagnoseContextFabricDriverJudgmentClause(driver)
				gotBound, gotOK = DiagnoseContextFabricDriverJudgmentBound(driver)
			case testCase.claim != nil:
				claim := testCase.claim(validDiagnosisClaimedFact())
				_, wantBound, wantOK = DiagnoseContextFabricClaimedFactClause(claim)
				gotBound, gotOK = DiagnoseContextFabricClaimedFactBound(claim)
			case testCase.finding != nil:
				finding := testCase.finding(validDiagnosisFinding())
				_, wantBound, wantOK = DiagnoseContextFabricFindingClause(finding)
				gotBound, gotOK = DiagnoseContextFabricFindingBound(finding)
			}
			if gotBound != wantBound || gotOK != wantOK {
				t.Fatalf("Bound projection = (%q, %v), clause traversal = (%q, %v)", gotBound, gotOK, wantBound, wantOK)
			}
		})
	}
}
