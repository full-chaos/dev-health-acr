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

// readSeed builds the planning-stage seed the way THE LIVE TURN builds it.
//
// THROUGH SeedRequirementOutcomes -- the DERIVATION-side seed -- and never
// through the published-plan gap builder beside it, because the two no longer
// produce the same row and only this one is on the path this evaluator runs
// on.
//
// SeedOutcomesFromPublishedPlanRequirements is the NEVER-REACHED gap-row
// builder: it mints `not_attempted` with impact `dimension` and the cause
// `answer_terminated_before_attempt`, for requirements a vetoed turn never got
// to. The derivation-side seed mints `satisfied` with impact `none`, for
// requirements the registry CAN serve. finalizeResult appends this evaluator
// onto the derivation-side seed; the gap builder only fills identities no row
// accounts for, inside ComputeAnswerCompleteness.
//
// The distinction is not cosmetic, and getting it wrong HIDES THE THING UNDER
// TEST. A `not_attempted`/`dimension` seed is already not-lossless, so the
// completeness derivation's FIRST pass returns `partial` on its own and the
// read pass this change adds is never consulted. An "evidence complete
// therefore state complete" case built on the gap row derives `partial` for a
// reason that has nothing to do with the evidence -- and, worse, a case built
// on it that PASSED would be passing for the wrong reason. That is what
// happened to the first draft of this file. The package's own
// TestTheSeedAndTheGapRowAgreeOnIdentityAndDisagreeOnOutcome already states
// the disagreement as a shipped invariant.
//
// The premise is ASSERTED rather than assumed, in all three parts -- one row,
// the identity the PUBLISHED requirement names, and the outcome the live seed
// carries -- so that if either builder moves again these tests fail here,
// naming the seed, instead of failing downstream where it looks like a defect
// in the evaluator.
func readSeed(t *testing.T, published contractsv1.ContextFabricPlanRequirement) []RequirementOutcomeRow {
	t.Helper()
	seed := SeedRequirementOutcomes([]DerivedRequirement{{
		RequirementCoordinate: RequirementCoordinate{
			Obligation: ObligationState,
			Role:       SubjectRoleSubject,
			Subject:    SubjectTeam,
		},
		Kind:      ObligationKindRead,
		FactKinds: published.FactKinds,
	}})
	if len(seed) != 1 {
		t.Fatalf("the derivation-side seed produced %d rows, want exactly 1: %+v", len(seed), seed)
	}
	if seed[0].Requirement != published.Requirement || seed[0].Obligation != published.Obligation {
		t.Fatalf("the seed names %q/%q and the published requirement names %q/%q; the two halves "+
			"of one account must agree on the identity, or every join below is comparing two requirements",
			seed[0].Requirement, seed[0].Obligation, published.Requirement, published.Obligation)
	}
	if seed[0].Stage != contractsv1.ContextFabricOutcomeStagePlanning ||
		seed[0].Outcome != contractsv1.ContextFabricRequirementSatisfied ||
		seed[0].Impact != contractsv1.ContextFabricAnswerImpactNone {
		t.Fatalf("the live seed's premise moved: %+v, want one planning row reading satisfied/none -- "+
			"these tests are written against the seed finalizeResult actually carries", seed[0])
	}
	return seed
}

// narrowedCoverage is factCoverage plus the PLANNER'S OWN narrowing record.
//
// The distinction this exists to express: the observation's STATE stays
// `available` -- the provider did return data -- while Coverage.Details carries
// `Narrowed` for the kinds whose subject set the planner cut. A fixture that
// varied the state instead would be testing a different thing entirely, and
// would not reproduce the shape that reads `satisfied` today.
// codedCoverage is factCoverage plus the coverage layer's AUTHORITATIVE cause
// code for a kind -- the field the row must carry rather than re-derive.
func codedCoverage(code contractsv1.ContextFabricCoverageDetailCode, kind FactKind, pairs ...any) Coverage {
	coverage := factCoverage(pairs...)
	coverage.Details = append(coverage.Details, contractsv1.ContextFabricCoverageDetail{
		DetailID: "detail_" + string(kind),
		Source:   canonicalFactSourcePrefix + string(kind),
		Code:     code,
		FactKind: kind,
		Label:    "coded",
	})
	return coverage
}

func narrowedCoverage(narrowed []FactKind, pairs ...any) Coverage {
	coverage := factCoverage(pairs...)
	for _, kind := range narrowed {
		coverage.Details = append(coverage.Details, contractsv1.ContextFabricCoverageDetail{
			DetailID:     "detail_" + string(kind),
			Source:       canonicalFactSourcePrefix + string(kind),
			Code:         contractsv1.ContextFabricCoverageDetailFactNarrowed,
			FactKind:     kind,
			SourceState:  SourceAvailable,
			SkippedKinds: []SubjectKind{SubjectRepository},
			Narrowed:     true,
			Label:        "narrowed",
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
		// wantRefinements is asserted on EVERY case, including the zero.
		//
		// A refinement is a before and an after with a named step between
		// them, so it is a claim that a reduction HAPPENED -- and the arm
		// that defaults its own cause has no such step to record. Asserting
		// only where one is expected would leave every other arm free to
		// invent one, which is the defect this column was added for.
		wantRefinements int
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
			// A provider REPORTED the truncation, so there is a real before
			// and after and the step is recorded. This is the complement of
			// the shortfall case below: same outcome, opposite provenance.
			wantRefinements: 1,
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
			wantCause: contractsv1.ContextFabricCoverageDetailFactNarrowed,
			// CauseObserved FALSE, and the comment above is why. The cause
			// on this arm is DEFAULTED by the evaluator, not reported by a
			// provider, and `cause_observed` is the only field a reader has
			// for telling those apart. This assertion read `true` while the
			// comment two lines above it said nothing was observed -- the
			// test stated the defect and asserted its opposite.
			wantObserved: false, wantServed: 1, wantDeclared: 2,
			// NO REFINEMENT: `Declared` here is the standard's demand, not a
			// population that ever existed, so a 2 -> 1 step would describe
			// a reduction of a source set that never held 2.
			wantRefinements: 0,
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
			// THE CAUSE IS CARRIED, NOT RE-DERIVED. A read that FAILED carries
			// `fact_read_failed` in its detail; deriving from the state would
			// publish `fact_provider_reported` and name the wrong mechanism.
			name: "a read failure carries its own code", quantifier: CompletionQuantifierAtLeastOne,
			coverage: codedCoverage(contractsv1.ContextFabricCoverageDetailFactReadFailed,
				health, health, SourceUnavailable),
			wantRow: true, wantOutcome: contractsv1.ContextFabricRequirementUnavailable,
			wantImpact:   contractsv1.ContextFabricAnswerImpactDimension,
			wantCause:    contractsv1.ContextFabricCoverageDetailFactReadFailed,
			wantObserved: true, wantServed: 0, wantDeclared: 1, wantRefinements: 0,
		},
		{
			// THE SHARPEST CASE: a scope expansion failed and NO PROVIDER RAN
			// AT ALL. Re-deriving from the state claimed a provider reported
			// something it never reported -- a false statement about which
			// mechanism fired, on the field a reader uses to decide what to do.
			name:       "a scope-expansion failure never claims a provider reported it",
			quantifier: CompletionQuantifierAtLeastOne,
			coverage: codedCoverage(contractsv1.ContextFabricCoverageDetailFactScopeUnexpanded,
				health, health, SourceUnavailable),
			wantRow: true, wantOutcome: contractsv1.ContextFabricRequirementUnavailable,
			wantImpact:   contractsv1.ContextFabricAnswerImpactDimension,
			wantCause:    contractsv1.ContextFabricCoverageDetailFactScopeUnexpanded,
			wantObserved: true, wantServed: 0, wantDeclared: 1, wantRefinements: 0,
		},
		{
			// TRUNCATION IS FACT-BEARING. The fact registry drops over-budget
			// facts and marks the source truncated RATHER THAN failing the
			// read, because "a partial, explicitly-truncated answer is the
			// honest outcome". Publishing `unavailable` told the reader they
			// got none of a cell they got part of, and degraded an answer the
			// layer below had deliberately kept partial.
			name:       "a truncated-only read is narrowed, never unavailable",
			quantifier: CompletionQuantifierAtLeastOne,
			coverage: codedCoverage(contractsv1.ContextFabricCoverageDetailFactProviderReported,
				health, health, SourceTruncated),
			wantRow: true, wantOutcome: contractsv1.ContextFabricRequirementNarrowed,
			wantImpact:   contractsv1.ContextFabricAnswerImpactDepth,
			wantCause:    contractsv1.ContextFabricCoverageDetailFactProviderReported,
			wantObserved: true, wantServed: 0, wantDeclared: 1, wantRefinements: 1,
		},
		{
			// THE COMPLEMENT of the case above: a state that is genuinely NOT
			// fact-bearing still reaches `unavailable`. Without it the
			// truncation case would pass on an evaluator that had simply
			// stopped emitting `unavailable` at all.
			name: "a no-data read is still unavailable", quantifier: CompletionQuantifierAtLeastOne,
			coverage: codedCoverage(contractsv1.ContextFabricCoverageDetailFactProviderReported,
				health, health, SourceNoData),
			wantRow: true, wantOutcome: contractsv1.ContextFabricRequirementUnavailable,
			wantImpact:   contractsv1.ContextFabricAnswerImpactDimension,
			wantCause:    contractsv1.ContextFabricCoverageDetailFactProviderReported,
			wantObserved: true, wantServed: 0, wantDeclared: 1, wantRefinements: 0,
		},
		{
			// A PLANNER NARROWING, recorded in Coverage.Details while the
			// source state stays `available`. Reading only the state publishes
			// `satisfied` for a read the document itself records as narrowed.
			name:       "a planner narrowing is not a satisfied read",
			quantifier: CompletionQuantifierCorroborated,
			coverage: narrowedCoverage([]FactKind{contractsv1.ContextFabricFactHealth},
				health, SourceAvailable, workload, SourceAvailable),
			wantRow: true, wantOutcome: contractsv1.ContextFabricRequirementNarrowed,
			wantImpact: contractsv1.ContextFabricAnswerImpactDepth,
			wantCause:  contractsv1.ContextFabricCoverageDetailFactNarrowed,
			// OBSERVED: the planner recorded this and the document carries the
			// detail. Something reported it, which is the whole of what this
			// flag means -- and the contrast with the shortfall arm, where
			// nothing did, is the point.
			wantObserved: true, wantServed: 1, wantDeclared: 2, wantRefinements: 1,
		},
		{
			// THE COMPLEMENT, byte-identical but for the narrowing record.
			// Without it the case above would pass on an evaluator that had
			// simply stopped emitting `satisfied`.
			name:       "the same evidence WITHOUT the narrowing record is satisfied",
			quantifier: CompletionQuantifierCorroborated,
			coverage:   factCoverage(health, SourceAvailable, workload, SourceAvailable),
			wantRow:    true, wantOutcome: contractsv1.ContextFabricRequirementSatisfied,
			wantImpact: contractsv1.ContextFabricAnswerImpactNone,
			wantServed: 2, wantDeclared: 2, wantRefinements: 0,
		},
		{
			// EVERY serving kind narrowed. This must NOT reach `unavailable`:
			// the kind returned data, so telling the reader they got none of
			// the cell would be false. The narrowing arm is therefore decided
			// before the served-nothing arm.
			name:       "a narrowing on the only served kind is still narrowed, never unavailable",
			quantifier: CompletionQuantifierAtLeastOne,
			coverage: narrowedCoverage([]FactKind{contractsv1.ContextFabricFactHealth},
				health, SourceAvailable),
			wantRow: true, wantOutcome: contractsv1.ContextFabricRequirementNarrowed,
			wantImpact:   contractsv1.ContextFabricAnswerImpactDepth,
			wantCause:    contractsv1.ContextFabricCoverageDetailFactNarrowed,
			wantObserved: true, wantServed: 0, wantDeclared: 1, wantRefinements: 1,
		},
		{
			// A PRUNE IS NOT A LOSS. `factStateDegrades` refuses to degrade on
			// `SourcePruned` deliberately: "A prune means the planner proved
			// the source had nothing to contribute to THIS question, so
			// nothing is missing and the answer is not degraded." Counting it
			// here would re-degrade what that decision protects, one layer up.
			name: "a pruned source does not lower a met standard", quantifier: CompletionQuantifierAtLeastOne,
			coverage: factCoverage(health, SourceAvailable, workload, SourcePruned),
			wantRow:  true, wantOutcome: contractsv1.ContextFabricRequirementSatisfied,
			wantImpact: contractsv1.ContextFabricAnswerImpactNone,
			wantServed: 1, wantDeclared: 1, wantRefinements: 0,
		},
		{
			// THE COMPLEMENT of the case above, and the reason it is not
			// vacuous: a genuinely absent source DOES lower the row, so the
			// prune case is not passing because nothing lowers anything.
			// Same shape, one state changed.
			name: "a failed source beside a served one narrows the row", quantifier: CompletionQuantifierAtLeastOne,
			coverage: factCoverage(health, SourceAvailable, workload, SourceUnavailable),
			wantRow:  true, wantOutcome: contractsv1.ContextFabricRequirementNarrowed,
			wantImpact:   contractsv1.ContextFabricAnswerImpactDepth,
			wantCause:    contractsv1.ContextFabricCoverageDetailFactProviderReported,
			wantObserved: true, wantServed: 1, wantDeclared: 2, wantRefinements: 1,
		},
		{
			// EVERY declared kind pruned. No kind is observed at all, so the
			// evaluator emits nothing and the requirement keeps its planning
			// seed -- which the completeness derivation reads as `partial`.
			// That is the honest floor: nothing was read, and nothing was lost.
			name: "every declared kind pruned emits no row", quantifier: CompletionQuantifierAtLeastOne,
			coverage: factCoverage(health, SourcePruned, workload, SourcePruned),
			wantRow:  false,
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
			if len(row.Refinements) != testCase.wantRefinements {
				t.Fatalf("row carries %d refinements, want %d: a refinement is a before and an after "+
					"with a step between them, so one on an arm that reduced nothing publishes a "+
					"reduction that never happened (%+v)",
					len(row.Refinements), testCase.wantRefinements, row.Refinements)
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
	seed := readSeed(t, requirement)

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
		{"every source pruned",
			factCoverage(health, SourcePruned, workload, SourcePruned),
			// PARTIAL, never `degraded`. The row above is the complement:
			// same shape, sources that are genuinely unusable, and THAT one
			// degrades. A prune must not reach the same state as a failure,
			// which is the whole of the fact layer's own rule.
			contractsv1.ContextFabricAnswerCompletenessPartial},
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
	seed := readSeed(t, requirement)

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

// TestAnUndeclaredCauseCodeEmitsNoRow is the REACH PROBE for the stop path, and
// it is written to fail loudly if that path ever becomes reachable in
// production rather than only in a fixture.
//
// A cause code outside the closed vocabulary must not reach the wire, and it
// must not be REMAPPED onto a declared one either -- a remap is how a code
// nobody declared becomes a code somebody did. So the requirement emits no row
// and keeps its planning seed, which derives `partial`: the state stays honest
// and only the cause is lost.
//
// The COMPLEMENT is asserted in the same run. Without it this test would pass
// on an evaluator that had stopped emitting rows altogether, which is the
// failure mode a "no row" assertion invites.
func TestAnUndeclaredCauseCodeEmitsNoRow(t *testing.T) {
	t.Parallel()
	requirement := readRequirement(CompletionQuantifierAtLeastOne)
	health := contractsv1.ContextFabricFactHealth

	// The premise, asserted rather than assumed: this code really is outside
	// the declared vocabulary. If it were ever added, this fixture would stop
	// exercising the branch and would silently prove nothing.
	const undeclared = contractsv1.ContextFabricCoverageDetailCode("fact_invented_by_a_future_producer")
	for _, declared := range contractsv1.ContextFabricCoverageDetailCodeVocabulary() {
		if declared == undeclared {
			t.Fatalf("%q is now a declared member; this fixture no longer reaches the stop path", undeclared)
		}
	}

	rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		codedCoverage(undeclared, health, health, SourceUnavailable))
	if len(rows) != 0 {
		t.Fatalf("an undeclared cause code produced %d rows: %+v -- it must reach the wire "+
			"neither as itself nor remapped onto a declared code", len(rows), rows)
	}

	// COMPLEMENT: the same fixture with a DECLARED code does emit its row.
	declaredRows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		codedCoverage(contractsv1.ContextFabricCoverageDetailFactReadFailed, health, health, SourceUnavailable))
	if len(declaredRows) != 1 {
		t.Fatalf("the same fixture with a DECLARED code produced %d rows, want 1 -- the assertion "+
			"above would pass on an evaluator that had stopped emitting rows at all", len(declaredRows))
	}
	if declaredRows[0].CauseCoverage != contractsv1.ContextFabricCoverageDetailFactReadFailed {
		t.Fatalf("the declared code was not carried: cause = %q", declaredRows[0].CauseCoverage)
	}
}

// TestFinalizingAServedTurnEvaluatesItsReadRequirements is the CALL-SITE test.
//
// Every other test in this file calls appendReadRequirementEvaluations
// directly, which proves the evaluator is right and proves nothing about
// whether anything CALLS it. A guard that cannot fire is not an assertion, and
// the one line that wires this change into the served document -- the append in
// finalizeResult -- was covered by nothing: deleting it left the whole package
// suite green while every answer went back to deriving `complete` from seeds
// alone, which is the entire defect.
//
// The ticket states that mutation as its acceptance criterion ("delete the
// evaluator and the state cannot reach complete"), so it is tested here rather
// than asserted in a comment. It drives finalizeResult through the real
// derivation, over a registry that actually declares producers, so the rows it
// reads are the rows the route serves.
func TestFinalizingAServedTurnEvaluatesItsReadRequirements(t *testing.T) {
	t.Parallel()
	frame := teamStateFrame(t)
	// A REGISTRY THAT CAN SERVE EVERY READ THE FRAME DERIVES, built from the
	// obligation vocabulary rather than from a guess about which obligations
	// this frame carries.
	//
	// The first version of this fixture declared `state` only. The frame also
	// derives `health`, nothing declared a producer for it, and its seed came
	// back `unavailable` -- which makes the whole set `degraded` for a reason
	// that has nothing to do with the evaluator. The premise guard below
	// caught that rather than the `complete` assertion failing mysteriously,
	// which is what premise guards are for.
	//
	// Deriving the capability set from the vocabulary keeps it that way: an
	// obligation ADDED to the read vocabulary later is served here
	// automatically, instead of silently reintroducing the same unservable
	// seed and turning this test red for an unrelated reason.
	var reads []AnswerObligation
	for obligation, kind := range contractsv1.ContextFabricAnswerObligationKindByObligation() {
		if kind == string(ObligationKindRead) {
			reads = append(reads, AnswerObligation(obligation))
		}
	}
	if len(reads) == 0 {
		t.Fatal("the obligation mirror classifies nothing as a read; this fixture would declare an empty registry")
	}
	servesEveryRead := func(kind FactKind) FactCapability {
		return FactCapability{
			Kind:                  kind,
			SupportedSubjectKinds: []SubjectKind{SubjectTeam},
			Obligations:           map[SubjectKind][]AnswerObligation{SubjectTeam: reads},
		}
	}
	deriver := registryDeriver{capabilities: []FactCapability{
		servesEveryRead(contractsv1.ContextFabricFactHealth),
		servesEveryRead(contractsv1.ContextFabricFactWorkload),
	}}
	engine := &Engine{requirements: deriver}

	published := PlanRequirementsFromDerived(deriver.DeriveRequirements(frame))
	// The identity the row assertions below are scoped to is read OFF THE
	// PLAN rather than typed in, so a frame that stops deriving
	// `state/subject/team` fails here by name instead of quietly matching no
	// rows and reporting "0 assembled-result rows" as if the evaluator were
	// broken.
	readIdentity := ""
	servable := 0
	for _, requirement := range published {
		if requirement.Kind != string(ObligationKindRead) || !requirement.Served() {
			continue
		}
		servable++
		if requirement.Obligation == string(ObligationState) {
			readIdentity = requirement.Requirement
		}
	}
	if servable == 0 {
		t.Fatal("the fixture publishes no servable READ requirement, so the evaluator would be " +
			"skipped and every assertion below would pass on an empty set")
	}
	if readIdentity == "" {
		t.Fatalf("the frame publishes %d servable read requirements but none for `state`; "+
			"the row assertions below have nothing to scope to", servable)
	}

	served := engine.finalizeResult(InvestigationResult{
		Status:   InvestigationComplete,
		ResultID: "result_51050001",
		Coverage: factCoverage(
			contractsv1.ContextFabricFactHealth, SourceAvailable,
			contractsv1.ContextFabricFactWorkload, SourceAvailable),
	}, AnswerPlan{Requirements: published}, &frame)

	evaluated, seeded := 0, 0
	for _, row := range served.Completeness.Outcomes {
		if row.Requirement != readIdentity {
			continue
		}
		switch row.Stage {
		case contractsv1.ContextFabricOutcomeStageAssembledResult:
			evaluated++
			if row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
				t.Fatalf("both declared kinds came back available and the evaluated row reads %q", row.Outcome)
			}
		case contractsv1.ContextFabricOutcomeStagePlanning:
			seeded++
		}
	}
	if evaluated != 1 {
		t.Fatalf("the served document carries %d assembled-result rows for the read requirement, want 1 -- "+
			"finalizeResult did not evaluate what the answer planned to read, so the seed is the last "+
			"word on it and the state below is derived from serveability rather than from evidence",
			evaluated)
	}
	// APPEND, not rewrite: the seed the planning stage wrote is still there.
	if seeded != 1 {
		t.Fatalf("%d planning rows survive for the read requirement, want 1", seeded)
	}

	// THE PREMISE, before the state assertion: every planning row is
	// lossless. If the fixture's registry ever stops serving one of the
	// frame's obligations, its seed reads `unavailable`, the derivation is
	// `degraded` for a reason that has nothing to do with this evaluator, and
	// a `complete` assertion below would fail while looking like a defect in
	// the change under test.
	for _, row := range served.Completeness.Outcomes {
		if row.Stage == contractsv1.ContextFabricOutcomeStagePlanning &&
			row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
			t.Fatalf("the fixture seeds a non-lossless row (%q for %q); the state assertion below "+
				"would then be measuring the fixture, not the evaluator", row.Outcome, row.Requirement)
		}
	}
	if served.Completeness.State != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("a turn that served every declared kind derives %q, want complete",
			served.Completeness.State)
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

	once := appendReadRequirementEvaluations(readSeed(t, requirement), published, coverage)
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
