package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The read-requirement evaluator, held to the acceptance the design states:
// hold the frame and the registry CONSTANT and vary only the runtime evidence,
// and the outcome rows and the completeness state must follow the evidence.
//
// Every case below shares one requirement and one plan. What differs between
// them is `Coverage.Sources` and nothing else, which is what makes the table an
// evidence test rather than a fixture test -- a difference in the output has
// exactly one possible cause.

// readRequirement is the fixture requirement: a served READ cell with two
// declared fact kinds, so both the `at_least_one` and the `corroborated`
// standards are expressible over the same row.
func readRequirement(quantifier CompletionQuantifier) contractsv1.ContextFabricPlanRequirement {
	return contractsv1.ContextFabricPlanRequirement{
		Requirement: "state/subject/team",
		Obligation:  string(ObligationState),
		Role:        string(SubjectRoleSubject),
		Subject:     SubjectTeam,
		Kind:        string(ObligationKindRead),
		FactKinds:   []FactKind{contractsv1.ContextFabricFactHealth, contractsv1.ContextFabricFactWorkload},
		Scope:       string(CompletionScopeSingleSubject),
		Quantifier:  string(quantifier),
	}
}

// factCoverage builds a Coverage carrying one canonical-fact observation per
// (kind, state) pair given, in the shape appendFactCoverage produces.
func factCoverage(pairs ...any) Coverage {
	coverage := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}
	for index := 0; index+1 < len(pairs); index += 2 {
		kind := pairs[index].(FactKind)
		state := pairs[index+1].(SourceState)
		coverage.Sources = append(coverage.Sources, SourceObservation{
			Source: canonicalFactSourcePrefix + string(kind),
			State:  state,
		})
	}
	return coverage
}

// TestTheOutcomeRowFollowsTheEvidence is the acceptance table.
//
// The harm each case carries is stated positively -- the expected outcome, the
// expected cause and the expected counts -- rather than as "not satisfied".
// A negative assertion on a last-evaluated arm proves nothing, because any
// earlier arm satisfies it.
func TestTheOutcomeRowFollowsTheEvidence(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	workload := contractsv1.ContextFabricFactWorkload

	for _, testCase := range []struct {
		name         string
		quantifier   CompletionQuantifier
		coverage     Coverage
		wantRow      bool
		wantOutcome  contractsv1.ContextFabricPlanRequirementOutcome
		wantImpact   contractsv1.ContextFabricAnswerImpactKind
		wantCause    contractsv1.ContextFabricCoverageDetailCode
		wantObserved bool
		wantServed   int
		wantDeclared int
	}{
		{
			name: "complete evidence, both kinds available", quantifier: CompletionQuantifierCorroborated,
			coverage: factCoverage(health, SourceAvailable, workload, SourceAvailable),
			wantRow:  true, wantOutcome: contractsv1.ContextFabricRequirementSatisfied,
			wantImpact: contractsv1.ContextFabricAnswerImpactNone,
			wantServed: 2, wantDeclared: 2,
		},
		{
			name: "stale is served", quantifier: CompletionQuantifierAtLeastOne,
			coverage: factCoverage(health, SourceStale),
			wantRow:  true, wantOutcome: contractsv1.ContextFabricRequirementSatisfied,
			wantImpact: contractsv1.ContextFabricAnswerImpactNone,
			wantServed: 1, wantDeclared: 1,
		},
		{
			name: "a truncated source narrows a met standard", quantifier: CompletionQuantifierAtLeastOne,
			coverage: factCoverage(health, SourceAvailable, workload, SourceTruncated),
			wantRow:  true, wantOutcome: contractsv1.ContextFabricRequirementNarrowed,
			wantImpact:   contractsv1.ContextFabricAnswerImpactDepth,
			wantCause:    contractsv1.ContextFabricCoverageDetailFactProviderReported,
			wantObserved: true, wantServed: 1, wantDeclared: 2,
		},
		{
			name:       "a missing operand is a source shortfall against corroboration",
			quantifier: CompletionQuantifierCorroborated,
			coverage:   factCoverage(health, SourceAvailable),
			wantRow:    true, wantOutcome: contractsv1.ContextFabricRequirementNarrowed,
			wantImpact: contractsv1.ContextFabricAnswerImpactDepth,
			// Nothing was observed failing: every kind that was read came
			// back usable, and there were fewer of them than the standard
			// demands. The cause names the narrowing, not a provider.
			wantCause:    contractsv1.ContextFabricCoverageDetailFactNarrowed,
			wantObserved: true, wantServed: 1, wantDeclared: 2,
		},
		{
			name: "a provider failure with nothing served is unavailable", quantifier: CompletionQuantifierAtLeastOne,
			coverage: factCoverage(health, SourceUnavailable, workload, SourceUnavailable),
			wantRow:  true, wantOutcome: contractsv1.ContextFabricRequirementUnavailable,
			wantImpact:   contractsv1.ContextFabricAnswerImpactDimension,
			wantCause:    contractsv1.ContextFabricCoverageDetailFactProviderReported,
			wantObserved: true, wantServed: 0, wantDeclared: 2,
		},
		{
			name: "PROVEN EMPTY is unavailable with an OBSERVED cause", quantifier: CompletionQuantifierAtLeastOne,
			coverage: factCoverage(health, SourceNoData, workload, SourceNoData),
			wantRow:  true, wantOutcome: contractsv1.ContextFabricRequirementUnavailable,
			wantImpact: contractsv1.ContextFabricAnswerImpactDimension,
			wantCause:  contractsv1.ContextFabricCoverageDetailFactProviderReported,
			// THE DISCRIMINATOR. A provider ran and reported empty, so the
			// cause was REPORTED. The not-read case below reaches no row at
			// all today and will carry CauseObserved false when it does.
			wantObserved: true, wantServed: 0, wantDeclared: 2,
		},
		{
			name: "unconfigured names the configuration, not the provider", quantifier: CompletionQuantifierAtLeastOne,
			coverage: factCoverage(health, SourceUnconfigured),
			wantRow:  true, wantOutcome: contractsv1.ContextFabricRequirementUnavailable,
			wantImpact:   contractsv1.ContextFabricAnswerImpactDimension,
			wantCause:    contractsv1.ContextFabricCoverageDetailFactUnconfigured,
			wantObserved: true, wantServed: 0, wantDeclared: 1,
		},
		{
			name:       "NOT READ AT ALL emits no row while the cause vocabulary is the other lane's",
			quantifier: CompletionQuantifierAtLeastOne,
			coverage:   factCoverage(),
			wantRow:    false,
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			requirement := readRequirement(testCase.quantifier)
			rows := appendReadRequirementEvaluations(nil, []contractsv1.ContextFabricPlanRequirement{requirement}, testCase.coverage)

			if !testCase.wantRow {
				if len(rows) != 0 {
					t.Fatalf("appended %d rows, want none: %+v", len(rows), rows)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("appended %d rows, want exactly 1: %+v", len(rows), rows)
			}
			row := rows[0]
			if row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult {
				t.Fatalf("stage = %q, want assembled_result", row.Stage)
			}
			if row.Requirement != requirement.Requirement || row.Obligation != requirement.Obligation {
				t.Fatalf("row names %q/%q, want %q/%q", row.Requirement, row.Obligation,
					requirement.Requirement, requirement.Obligation)
			}
			if row.Outcome != testCase.wantOutcome {
				t.Fatalf("outcome = %q, want %q", row.Outcome, testCase.wantOutcome)
			}
			if row.Impact != testCase.wantImpact {
				t.Fatalf("impact = %q, want %q", row.Impact, testCase.wantImpact)
			}
			if row.CauseCoverage != testCase.wantCause {
				t.Fatalf("cause_coverage = %q, want %q", row.CauseCoverage, testCase.wantCause)
			}
			if row.CauseObserved != testCase.wantObserved {
				t.Fatalf("cause_observed = %v, want %v", row.CauseObserved, testCase.wantObserved)
			}
			if row.Served != testCase.wantServed || row.Declared != testCase.wantDeclared {
				t.Fatalf("served/declared = %d/%d, want %d/%d",
					row.Served, row.Declared, testCase.wantServed, testCase.wantDeclared)
			}
			// EVERY ROW THIS EVALUATOR EMITS MUST BE CONTRACT-VALID. A row
			// that describes the evidence correctly and cannot be published
			// is not a fix; and the validator is where the outcome/impact
			// pairing, the cause rule and the narrowing arithmetic are
			// actually enforced.
			if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
				t.Fatalf("the emitted row is not contract-valid: %v", err)
			}
		})
	}
}

// TestTheStateFollowsTheEvidence pairs the table above with the state each
// evidence shape derives, because the row is only half the disclosure.
func TestTheStateFollowsTheEvidence(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	workload := contractsv1.ContextFabricFactWorkload
	requirement := readRequirement(CompletionQuantifierCorroborated)
	seed := SeedOutcomesFromPublishedPlanRequirements([]contractsv1.ContextFabricPlanRequirement{requirement})

	for _, testCase := range []struct {
		name     string
		coverage Coverage
		want     contractsv1.ContextFabricAnswerCompletenessState
	}{
		{"both served", factCoverage(health, SourceAvailable, workload, SourceAvailable),
			contractsv1.ContextFabricAnswerCompletenessComplete},
		{"one truncated", factCoverage(health, SourceAvailable, workload, SourceTruncated),
			contractsv1.ContextFabricAnswerCompletenessPartial},
		{"nothing usable", factCoverage(health, SourceNoData, workload, SourceNoData),
			contractsv1.ContextFabricAnswerCompletenessDegraded},
		{"nothing read at all", factCoverage(),
			// No evaluated row exists, so the seed is the only row for this
			// identity -- and a planning-only READ identity is exactly what
			// the amended derivation refuses to call complete.
			contractsv1.ContextFabricAnswerCompletenessPartial},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rows := appendReadRequirementEvaluations(seed, []contractsv1.ContextFabricPlanRequirement{requirement}, testCase.coverage)
			if got := contractsv1.DeriveContextFabricAnswerCompletenessState(rows); got != testCase.want {
				t.Fatalf("state = %q, want %q (rows: %+v)", got, testCase.want, rows)
			}
		})
	}
}

// TestTheSeedIsNeverTheLastRowForAServedReadRequirement is the acceptance
// criterion stated as its own test, because it is the defect in one sentence.
//
// It asserts over ALL rows of the identity rather than the last one: "the last
// row is not a planning seed" would pass on a set whose only rows are a seed
// followed by a seed.
func TestTheSeedIsNeverTheLastRowForAServedReadRequirement(t *testing.T) {
	t.Parallel()
	requirement := readRequirement(CompletionQuantifierAtLeastOne)
	seed := SeedOutcomesFromPublishedPlanRequirements([]contractsv1.ContextFabricPlanRequirement{requirement})
	if len(seed) != 1 || seed[0].Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("the fixture's premise moved: seed = %+v, want one satisfied planning row", seed)
	}

	rows := appendReadRequirementEvaluations(seed,
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		factCoverage(contractsv1.ContextFabricFactHealth, SourceAvailable))

	evaluated := 0
	for _, row := range rows {
		if row.Requirement == requirement.Requirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			evaluated++
		}
	}
	if evaluated != 1 {
		t.Fatalf("%d assembled-result rows for %q, want exactly 1 -- the seed's `satisfied` is the "+
			"last word on a read requirement, which is the defect this evaluator exists to close",
			evaluated, requirement.Requirement)
	}
}

// TestEvaluatingTwiceAppendsOneRow is the re-entry guard.
//
// finalizeResult runs again on the synthesis retry and again after stage 3
// narrows and re-finalizes. It counts the TOTAL number of assembled-result rows
// for the identity rather than asserting the expected one is present: a test
// that counts only what it expects cannot detect a surplus.
func TestEvaluatingTwiceAppendsOneRow(t *testing.T) {
	t.Parallel()
	requirement := readRequirement(CompletionQuantifierAtLeastOne)
	published := []contractsv1.ContextFabricPlanRequirement{requirement}
	coverage := factCoverage(contractsv1.ContextFabricFactHealth, SourceAvailable)

	once := appendReadRequirementEvaluations(
		SeedOutcomesFromPublishedPlanRequirements(published), published, coverage)
	twice := appendReadRequirementEvaluations(once, published, coverage)

	total := 0
	for _, row := range twice {
		if row.Requirement == requirement.Requirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			total++
		}
	}
	if total != 1 {
		t.Fatalf("after two evaluations: %d assembled-result rows for one requirement, want 1", total)
	}
}

// TestUnservableAndComputedRequirementsAreNotEvaluated pins the two
// populations this evaluator deliberately does not touch, each for its own
// reason and each asserted separately -- a single "no rows" assertion over both
// would pass if one population were dropped for the wrong reason.
func TestUnservableAndComputedRequirementsAreNotEvaluated(t *testing.T) {
	t.Parallel()
	coverage := factCoverage(contractsv1.ContextFabricFactHealth, SourceAvailable)

	unservable := readRequirement(CompletionQuantifierAtLeastOne)
	unservable.Unavailable = string(RequirementReasonNoDeclaringProducer)
	unservable.Quantifier = string(CompletionQuantifierNone)
	unservable.FactKinds = nil
	if rows := appendReadRequirementEvaluations(nil, []contractsv1.ContextFabricPlanRequirement{unservable}, coverage); len(rows) != 0 {
		t.Fatalf("an UNSERVABLE requirement was evaluated (%d rows) -- the derivation already "+
			"attributed that cell and re-deciding it here is a second authority", len(rows))
	}

	computed := contractsv1.ContextFabricPlanRequirement{
		Requirement: "ranking/member/team", Obligation: string(ObligationRanking),
		Role: string(SubjectRoleMember), Subject: SubjectTeam,
		Kind: string(ObligationKindComputed), Step: string(ComputedStepRankCohort),
		Scope: string(CompletionScopeEachMember), Quantifier: string(CompletionQuantifierAll),
	}
	if rows := appendReadRequirementEvaluations(nil, []contractsv1.ContextFabricPlanRequirement{computed}, coverage); len(rows) != 0 {
		t.Fatalf("a COMPUTED requirement was evaluated (%d rows) -- this evaluator observes fact "+
			"reads and cannot observe a server step", len(rows))
	}
}

// TestGraphSourcesAreNotReadAsFactEvidence keeps the evaluator's input scoped
// to canonical-fact observations.
//
// Attributing a graph read to a declared fact requirement would be the wrong
// attribution the projection layer already refuses to make -- and it would do
// so INVISIBLY, by making a requirement look served when its own producers said
// nothing.
func TestGraphSourcesAreNotReadAsFactEvidence(t *testing.T) {
	t.Parallel()
	requirement := readRequirement(CompletionQuantifierAtLeastOne)
	coverage := Coverage{Sources: []SourceObservation{
		{Source: "graph:cohort", State: SourceAvailable},
		{Source: "canonical_fact:", State: SourceAvailable},
	}}
	if rows := appendReadRequirementEvaluations(nil, []contractsv1.ContextFabricPlanRequirement{requirement}, coverage); len(rows) != 0 {
		t.Fatalf("a graph source (or an empty fact suffix) was read as fact evidence: %+v", rows)
	}
}

// TestTheWorstObservationDecidesTheRow pins the multi-observation rule.
//
// One kind can be observed more than once. Taking the first or the last would
// make the published row depend on the order the coverage merge happened to
// produce, which is not a property of the answer.
func TestTheWorstObservationDecidesTheRow(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	requirement := readRequirement(CompletionQuantifierAtLeastOne)
	requirement.FactKinds = []FactKind{health}

	for _, order := range []struct {
		name  string
		first SourceState
		last  SourceState
	}{
		{"served then failed", SourceAvailable, SourceUnavailable},
		{"failed then served", SourceUnavailable, SourceAvailable},
	} {
		order := order
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()
			rows := appendReadRequirementEvaluations(nil,
				[]contractsv1.ContextFabricPlanRequirement{requirement},
				factCoverage(health, order.first, health, order.last))
			if len(rows) != 1 {
				t.Fatalf("appended %d rows, want 1", len(rows))
			}
			if rows[0].Outcome != contractsv1.ContextFabricRequirementUnavailable {
				t.Fatalf("outcome = %q, want unavailable -- a failure was observed for this kind and "+
					"the row must disclose it whichever order the merge produced", rows[0].Outcome)
			}
		})
	}
}

// TestTheNotReadArmIsReachedAndIsTemporary is the REACH PROBE for the interim
// skip, and it is written to fail in BOTH directions.
//
// The arm exists only until the coverage vocabulary can carry a truthful cause
// for "planned, and no read of any serving kind was attempted" -- a member this
// branch mints in its own commits, after the count-population change lands on
// main, because both edit the same closed array.
//
// INVERT THIS TEST IN THAT COMMIT: the same fixture must then reach the append
// and this must assert the row, its cause and CauseObserved false. A disclosure
// that says "not covered" is a claim, and it ships with a test that fails if
// the path executes -- here, the reverse: it fails if the path stops executing,
// so the interim cannot quietly become permanent by nobody noticing.
func TestTheNotReadArmIsReachedAndIsTemporary(t *testing.T) {
	t.Parallel()
	requirement := readRequirement(CompletionQuantifierAtLeastOne)
	evidence := evaluateReadRequirement(requirement, factCoverage())
	if evidence.Observed != 0 {
		t.Fatalf("the fixture no longer reaches the not-read arm (observed=%d); if the evaluator "+
			"now covers it, this test is the one that must be inverted", evidence.Observed)
	}
	rows := appendReadRequirementEvaluations(nil, []contractsv1.ContextFabricPlanRequirement{requirement}, factCoverage())
	if len(rows) != 0 {
		t.Fatalf("the not-read arm emitted %d rows -- if the cause vocabulary now carries a member "+
			"for it, invert this test rather than deleting it", len(rows))
	}
}

// TestTheCanonicalFactPrefixMatchesTheProducer keeps the evaluator's lookup key
// equal to the key the fact registry actually writes.
//
// A prefix that drifts turns every lookup into a miss, which reads as "nothing
// was observed" and would mark every read requirement unavailable -- a silent,
// total failure that no arm-level test would catch, because every arm would
// still behave correctly for the input it was given.
func TestTheCanonicalFactPrefixMatchesTheProducer(t *testing.T) {
	t.Parallel()
	bundle := CanonicalFactBundle{}
	appendFactCoverage(&bundle, contractsv1.ContextFabricFactHealth, SourceAvailable, nil, "", "", coverageDetailSpec{})
	if len(bundle.Coverage.Sources) == 0 {
		t.Fatal("the producer wrote no source observation; this test cannot say anything")
	}
	kind, ok := canonicalFactKindOf(bundle.Coverage.Sources[0].Source)
	if !ok || kind != contractsv1.ContextFabricFactHealth {
		t.Fatalf("the evaluator cannot read the producer's own source key %q (parsed %q, ok=%v)",
			bundle.Coverage.Sources[0].Source, kind, ok)
	}
}

// TestEverySourceStateIsRankedAndMapped keeps the two tables total over the
// shipped vocabulary.
//
// An unranked state sorts below `available` and would read as fully served,
// which is the one direction this ordering must never fail in; an unmapped one
// would leave a non-satisfied row with no cause, which the row validator
// refuses outright.
func TestEverySourceStateIsRankedAndMapped(t *testing.T) {
	t.Parallel()
	states := []SourceState{
		SourceAvailable, SourceStale, SourceUnavailable, SourceUnconfigured,
		SourceUnauthorized, SourceNoData, SourceTruncated, SourceConflicted,
		SourceNotApplicable, SourcePruned,
	}
	if len(states) == 0 {
		t.Fatal("the vocabulary list is empty; this test proves nothing")
	}
	unranked := sourceStateSeverity(SourceState("not_a_member"))
	for _, state := range states {
		if severity := sourceStateSeverity(state); severity >= unranked {
			t.Fatalf("source state %q ranks %d, at or above the unranked fallback %d -- it is "+
				"missing from the severity table", state, severity, unranked)
		}
		if state == SourceAvailable || state == SourceStale {
			continue
		}
		if readCoverageCauseFor(state) == "" {
			t.Fatalf("source state %q maps to no coverage code, so a row caused by it would name "+
				"no cause and the row validator would refuse it", state)
		}
	}
}
