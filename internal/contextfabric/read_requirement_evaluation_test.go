package contextfabric

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
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
			// Was "emits no row while the cause vocabulary is the other
			// lane's". The member landed, so the row lands with it.
			name:       "NOT READ AT ALL names its own cause",
			quantifier: CompletionQuantifierAtLeastOne,
			coverage:   factCoverage(),
			wantRow:    true, wantOutcome: contractsv1.ContextFabricRequirementUnavailable,
			wantImpact: contractsv1.ContextFabricAnswerImpactDimension,
			wantCause:  contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned,
			// INFERRED from an absence, never reported. The contrast with the
			// PROVEN EMPTY case above is the whole point of the field: there a
			// provider ran and reported empty, here nothing ran at all.
			wantObserved: false, wantServed: 0, wantDeclared: 1, wantRefinements: 0,
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
			// DEGRADED now, and the change is the point. While the vocabulary
			// had no member for an unread requirement this emitted no row, so
			// the seed stood alone and the state was `partial` -- honest, but
			// weaker than the evidence warranted. The row now names the cause,
			// `unavailable` is absorbing, and the answer says the reader got
			// none of a cell they asked for. The interim only ever lost the
			// CAUSE; this is what recovering it is worth.
			contractsv1.ContextFabricAnswerCompletenessDegraded},
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

// TestAnUnrecognisedQuantifierEmitsNoRow closes the gap a surviving mutant
// named: deleting the `if !known` guard changed no test result, because every
// fixture used a recognised quantifier.
//
// The guard matters in one direction only, and it is the dangerous one. An
// unrecognised quantifier defaulting to a threshold would SILENTLY LOWER a
// standard -- `corroborated` demanding two sources becoming whatever a zero
// value implies -- and the row would read `satisfied` on evidence that never
// met the bar. Skipping the requirement instead loses the row and keeps the
// planning seed, which reads `partial`: the state stays honest.
//
// The COMPLEMENT is asserted in the same run, because "no row" is exactly the
// assertion that passes on an evaluator emitting nothing at all.
func TestAnUnrecognisedQuantifierEmitsNoRow(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	coverage := factCoverage(health, SourceAvailable)

	unrecognised := readRequirement(CompletionQuantifierAtLeastOne)
	unrecognised.Quantifier = "quantifier_from_a_later_vocabulary"
	// The premise, asserted rather than assumed: this really is unrecognised.
	// If it were ever added to the table, the fixture would stop reaching the
	// guard and would prove nothing while still passing.
	if _, known := readQuantifierThreshold(unrecognised.Quantifier); known {
		t.Fatalf("%q is now a recognised quantifier; this fixture no longer reaches the guard",
			unrecognised.Quantifier)
	}

	rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{unrecognised}, coverage)
	if len(rows) != 0 {
		t.Fatalf("an unrecognised quantifier produced %d rows: %+v -- evaluating against a defaulted "+
			"threshold silently lowers the standard the requirement declared", len(rows), rows)
	}

	// COMPLEMENT: the same fixture with a RECOGNISED quantifier does emit.
	recognised := readRequirement(CompletionQuantifierAtLeastOne)
	got := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{recognised}, coverage)
	if len(got) != 1 {
		t.Fatalf("the same fixture with a recognised quantifier produced %d rows, want 1 -- the "+
			"assertion above would pass on an evaluator that emitted nothing at all", len(got))
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

	// THE LINE IS READ BACK, not assumed. A dropped row with nothing in the
	// log is a swallowed signal: the answer goes out one disclosure short and
	// the only thing that would notice is this test, which does not run in
	// production. So the drop is required to SAY SO, and the assertion is on
	// the emitted text rather than on a counter.
	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		codedCoverage(undeclared, health, health, SourceUnavailable))
	if len(rows) != 0 {
		t.Fatalf("an undeclared cause code produced %d rows: %+v -- it must reach the wire "+
			"neither as itself nor remapped onto a declared code", len(rows), rows)
	}

	const line = "context fabric read requirement dropped for an undeclared coverage code"
	if !strings.Contains(logs.String(), line) {
		t.Fatalf("the drop emitted no disclosure; a silently dropped row is a swallowed signal. logs: %s", logs.String())
	}
	// The line must NAME what was dropped and why, or it cannot drive the fix.
	// A disclosure that says something happened without saying what is the
	// generic bit this whole layer exists to replace.
	for _, want := range []string{requirement.Requirement, string(undeclared)} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("the disclosure does not name %q: %s", want, logs.String())
		}
	}

	// EVERY FIELD OF THE DISCLOSURE IS ASSERTED, AS A KEY:VALUE PAIR, and the
	// pairing is the whole point.
	//
	// The substring checks above cannot see a FIELD disappear. `Obligation` is
	// "state", and the requirement id is "state/subject/team", so a bare
	// Contains("state") passes with the obligation attribute deleted -- the
	// value is still in the line, inside a different field. Both field
	// deletions survived the battery for exactly that reason.
	//
	// This is the drop path: the one place this evaluator swallows a row. That
	// is only acceptable because it says so in a line a human can grep, and a
	// line that names a requirement without saying which CELL it belonged to,
	// or how much was observed before the drop, is most of the way back to the
	// generic "something happened" logging this layer exists to replace. So
	// each attribute is pinned individually, in the handler's own encoding.
	for _, want := range []string{
		fmt.Sprintf("%q:%q", "requirement", requirement.Requirement),
		fmt.Sprintf("%q:%q", "obligation", requirement.Obligation),
		fmt.Sprintf("%q:%q", "undeclared_code", string(undeclared)),
		fmt.Sprintf("%q:%d", "observed_kinds", 1),
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("the disclosure is missing the attribute %s -- a dropped row must say WHICH "+
				"requirement, which CELL, which code and how much was observed, or it cannot "+
				"drive a fix: %s", want, logs.String())
		}
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
	// AND THE COMPLEMENT ON THE LINE ITSELF: a declared code must not emit the
	// drop disclosure. Without this the assertion above would pass on an
	// evaluator that logged it unconditionally.
	if strings.Count(logs.String(), line) != 1 {
		t.Fatalf("the drop disclosure was emitted %d times across both fixtures, want exactly 1 -- "+
			"a declared code must not log a drop: %s", strings.Count(logs.String(), line), logs.String())
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

	// THE CASE ABOVE PASSES FOR THE WRONG REASON ON ITS OWN, and that is why
	// this one exists.
	//
	// It carries quantifier `all`, which belongs to a computed obligation and
	// which readQuantifierThreshold refuses a few lines further down. So the
	// row is dropped by the QUANTIFIER check whether or not the kind check runs
	// at all -- the assertion holds on an evaluator with no computed guard
	// whatsoever, and measures the wrong gate.
	//
	// This one is a computed requirement that would otherwise sail through:
	// declared fact kinds that the coverage below actually observes, and a READ
	// quantifier the threshold table accepts. The ONLY thing standing between
	// it and an assembled-result read row is the kind-and-served guard. A
	// computed step is served by running it, not by reading a fact, so a read
	// row here would attribute a server's work to a provider that did nothing.
	computedButOtherwiseEligible := computed
	computedButOtherwiseEligible.Quantifier = string(CompletionQuantifierAtLeastOne)
	computedButOtherwiseEligible.FactKinds = []FactKind{contractsv1.ContextFabricFactHealth}
	if _, known := readQuantifierThreshold(computedButOtherwiseEligible.Quantifier); !known {
		t.Fatalf("the premise moved: %q is no longer a recognised read quantifier, so this fixture "+
			"is stopped by the threshold check again and measures nothing",
			computedButOtherwiseEligible.Quantifier)
	}
	if rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{computedButOtherwiseEligible}, coverage); len(rows) != 0 {
		t.Fatalf("a COMPUTED requirement carrying a READ quantifier and observed fact kinds was "+
			"evaluated (%d rows) -- nothing downstream would have stopped it, so the kind guard is "+
			"the only thing keeping a server step from being reported as a fact read", len(rows))
	}

	// AND THE UNSERVABLE CASE HAS THE SAME PROBLEM ITS COMPUTED SIBLING HAD.
	//
	// The `unservable` fixture above also carries quantifier `none` and no fact
	// kinds, so the quantifier check suppresses it whether or not the SERVED
	// half of the entry guard exists -- deleting `|| !requirement.Served()`
	// left the whole suite green. This one is unservable and NOTHING ELSE: a
	// read obligation, a quantifier the threshold table accepts, and declared
	// kinds the coverage below actually observes. The derivation has already
	// attributed that cell by writing `Unavailable`; re-deciding it here on the
	// strength of observations that belong to some other requirement is the
	// second-authority defect this evaluator is written not to become.
	unservableButOtherwiseEligible := readRequirement(CompletionQuantifierAtLeastOne)
	unservableButOtherwiseEligible.Unavailable = string(RequirementReasonNoDeclaringProducer)
	unservableButOtherwiseEligible.FactKinds = []FactKind{contractsv1.ContextFabricFactHealth}
	if unservableButOtherwiseEligible.Served() {
		t.Fatalf("the premise moved: the fixture still reports Served() with Unavailable=%q, so it "+
			"is not reaching the guard this case exists to measure", unservableButOtherwiseEligible.Unavailable)
	}
	if _, known := readQuantifierThreshold(unservableButOtherwiseEligible.Quantifier); !known {
		t.Fatalf("the premise moved: %q is no longer a recognised read quantifier, so this fixture "+
			"is stopped by the threshold check and measures nothing", unservableButOtherwiseEligible.Quantifier)
	}
	if rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{unservableButOtherwiseEligible}, coverage); len(rows) != 0 {
		t.Fatalf("an UNSERVABLE requirement carrying a READ quantifier and observed fact kinds was "+
			"evaluated (%d rows) -- nothing downstream would have stopped it, so the served half of "+
			"the entry guard is the only thing keeping this evaluator from overriding the "+
			"derivation's own attribution", len(rows))
	}

	// POSITIVE CONTROL: the same shape as a READ requirement DOES produce a
	// row, so the two refusals above cannot be passing on an evaluator that
	// stopped emitting rows at all.
	served := computedButOtherwiseEligible
	served.Kind = string(ObligationKindRead)
	if rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{served}, coverage); len(rows) != 1 {
		t.Fatalf("the same requirement as a READ produced %d rows, want 1 -- without this the "+
			"refusals above would pass on an evaluator that appends nothing", len(rows))
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
	// The assertion moved from "no row" to "the NOT-PLANNED row", and it is
	// stronger for it. While the vocabulary had no member for an unread
	// requirement, "no row" was the only way to say the graph source had not
	// been counted. Now there is a row, and it must say the requirement was
	// never looked at -- which is a POSITIVE statement that the graph source
	// contributed nothing, where absence was only the lack of a statement.
	rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{requirement}, coverage)
	if len(rows) != 1 {
		t.Fatalf("want exactly the not-planned row, got %d: %+v", len(rows), rows)
	}
	if rows[0].CauseCoverage != contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned {
		t.Fatalf("a graph source (or an empty fact suffix) was read as fact evidence: %+v", rows[0])
	}
	if rows[0].Served != 0 {
		t.Fatalf("a graph source was counted as a serving source: served=%d", rows[0].Served)
	}
	// THE COMPLEMENT: the same requirement WITH a real canonical-fact source
	// does serve. Without it this test would pass on an evaluator that read
	// nothing at all as fact evidence.
	served := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		factCoverage(contractsv1.ContextFabricFactHealth, SourceAvailable))
	if len(served) != 1 || served[0].Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("the complement did not serve: %+v -- this test would then be asserting that "+
			"nothing is ever read as fact evidence", served)
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

	// AND THE CAUSE FOLLOWS THE WORST OBSERVATION TOO, which the cases above
	// cannot show.
	//
	// They vary one kind's observations, so both orders reach the same OUTCOME
	// and the same single cause -- the assertion holds no matter which
	// observation the ranking picked. The interesting case is TWO kinds failing
	// at DIFFERENT severities with DIFFERENT carried codes: the row publishes
	// one cause, and it must be the worst kind's, not the last one seen.
	//
	// The fixture is arranged so those two disagree. `health` is
	// `unavailable` (severity 8) and is declared FIRST; `workload` is `no_data`
	// (severity 4) and is declared SECOND. An evaluator that stops tracking the
	// running maximum keeps overwriting and ends on `workload` -- the LESS
	// severe kind naming the failure -- which reads as a milder answer than the
	// evidence supports.
	twoKinds := readRequirement(CompletionQuantifierAtLeastOne)
	workload := contractsv1.ContextFabricFactWorkload
	if len(twoKinds.FactKinds) < 2 || twoKinds.FactKinds[0] != health || twoKinds.FactKinds[1] != workload {
		t.Fatalf("the fixture requirement declares %v; this case needs health then workload, or the "+
			"severities below are not being compared in the order it claims", twoKinds.FactKinds)
	}
	if sourceStateSeverity(SourceUnavailable) <= sourceStateSeverity(SourceNoData) {
		t.Fatalf("the premise moved: unavailable (%d) must outrank no_data (%d), or this case is "+
			"asserting a preference the ranking does not express",
			sourceStateSeverity(SourceUnavailable), sourceStateSeverity(SourceNoData))
	}
	coverage := factCoverage(health, SourceUnavailable, workload, SourceNoData)
	coverage.Details = append(coverage.Details,
		contractsv1.ContextFabricCoverageDetail{
			DetailID: "detail_health", Source: canonicalFactSourcePrefix + string(health),
			Code: contractsv1.ContextFabricCoverageDetailFactReadFailed, FactKind: health, Label: "worst",
		},
		contractsv1.ContextFabricCoverageDetail{
			DetailID: "detail_workload", Source: canonicalFactSourcePrefix + string(workload),
			Code: contractsv1.ContextFabricCoverageDetailFactProviderReported, FactKind: workload, Label: "milder",
		})

	rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{twoKinds}, coverage)
	if len(rows) != 1 {
		t.Fatalf("appended %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].CauseCoverage != contractsv1.ContextFabricCoverageDetailFactReadFailed {
		t.Fatalf("cause = %q, want %q -- the WORST observation names the cause; the milder kind is "+
			"declared later, so an evaluator that stopped tracking the running maximum publishes "+
			"the milder code and understates the failure",
			rows[0].CauseCoverage, contractsv1.ContextFabricCoverageDetailFactReadFailed)
	}
	if !rows[0].CauseObserved {
		t.Fatalf("cause_observed = false: the code was defaulted rather than carried, and both " +
			"states default to the SAME code, so this case would stop discriminating")
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
func TestTheNotReadArmNamesItsOwnCause(t *testing.T) {
	t.Parallel()
	requirement := readRequirement(CompletionQuantifierAtLeastOne)

	// INVERTED, not replaced, and the previous version asked for exactly this.
	// While the vocabulary had no truthful member the arm emitted NO row and
	// this test asserted that, with a comment saying "invert this rather than
	// deleting it" once the code existed. It exists now. Keeping the same test
	// identity is what makes the interim visible in the history instead of
	// looking like a feature that was always there.
	evidence := evaluateReadRequirement(requirement, factCoverage())
	if evidence.Observed != 0 {
		t.Fatalf("the fixture no longer reaches the not-read arm (observed=%d); this test would "+
			"then be asserting something else entirely", evidence.Observed)
	}

	rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{requirement}, factCoverage())
	if len(rows) != 1 {
		t.Fatalf("the not-read arm emitted %d rows, want 1 -- the requirement is now nameable and "+
			"must be named", len(rows))
	}
	row := rows[0]
	if row.Outcome != contractsv1.ContextFabricRequirementUnavailable ||
		row.Impact != contractsv1.ContextFabricAnswerImpactDimension {
		t.Fatalf("outcome/impact = %q/%q, want unavailable/dimension: the reader asked for this "+
			"cell and gets none of it", row.Outcome, row.Impact)
	}
	if row.CauseCoverage != contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned {
		t.Fatalf("cause = %q, want requirement_read_not_planned -- every neighbouring code names a "+
			"mechanism that did not run here", row.CauseCoverage)
	}
	// INFERRED, NOT REPORTED. Nothing observed this; the evaluator concluded it
	// from the absence of any observation, and the one field a reader has for
	// telling those apart must say so.
	if row.CauseObserved {
		t.Fatal("cause_observed is true on a cause nothing reported; it was inferred from an absence")
	}
	if row.Served != 0 || row.Declared != 1 {
		t.Fatalf("served/declared = %d/%d, want 0/1: zero sources served against the standard's "+
			"own demand, both measured", row.Served, row.Declared)
	}
	if len(row.Refinements) != 0 {
		t.Fatalf("the row carries %d refinements; nothing was reduced -- nothing was read",
			len(row.Refinements))
	}
	// It must be publishable. A row that describes the absence correctly and
	// cannot be served is not a fix.
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("the not-read row is not contract-valid: %v", err)
	}

	// AND THE STATE FOLLOWS IT. `unavailable` is absorbing, so an answer that
	// never looked at a planned read is `degraded` -- strictly stronger than
	// the `partial` the interim produced, and the reason the interim was only
	// ever a loss of the CAUSE.
	if got := contractsv1.DeriveContextFabricAnswerCompletenessState(rows); got != contractsv1.ContextFabricAnswerCompletenessDegraded {
		t.Fatalf("state = %q, want degraded", got)
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

// TestFactBearingAgreesWithTheRegistrysOwnRule is the AUDIT assertion: this
// file's idea of "did anything come back" must be the fact registry's, not a
// second opinion beside it.
//
// The registry owns the question. `stateRejectsFacts` names every state that
// contributes no facts; its complement is therefore exactly the fact-bearing
// set, and a read requirement whose evidence is fact-bearing must NOT publish
// `unavailable`, which says the reader got none of the cell.
//
// This is the test that would have caught the truncation defect before a
// reviewer did. `SourceTruncated` is absent from `stateRejectsFacts` -- the
// registry drops over-budget facts and marks the source truncated RATHER THAN
// failing the read -- and this evaluator was publishing `unavailable` for it
// anyway. Quoting that comment in a fix is worth less than asserting against
// the predicate, because the predicate moves and the quote does not.
//
// Driven through the whole evaluator rather than against an internal helper,
// so it constrains what the DOCUMENT says rather than what an intermediate
// counter holds.
func TestFactBearingAgreesWithTheRegistrysOwnRule(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	requirement := readRequirement(CompletionQuantifierAtLeastOne)

	states := []SourceState{
		SourceAvailable, SourceStale, SourceTruncated, SourceNoData,
		SourceUnavailable, SourceUnconfigured, SourceUnauthorized,
		SourceConflicted, SourceNotApplicable, SourcePruned,
	}
	// The set must be TOTAL over what a provider may return, or a state could
	// drift out of both this test and the evaluator together.
	checked := 0
	for _, state := range states {
		state := state
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			rows := appendReadRequirementEvaluations(nil,
				[]contractsv1.ContextFabricPlanRequirement{requirement},
				factCoverage(health, state))

			// A PRUNE IS ITS OWN CASE and the two layers still agree: the
			// registry says it contributes no facts, and this evaluator emits
			// no row at all rather than an `unavailable` one, because a prune
			// is not a loss. Asserted here so the prune cannot quietly rejoin
			// the unavailable set.
			if state == SourcePruned {
				if len(rows) != 0 {
					t.Fatalf("a pruned observation produced %d rows: %+v", len(rows), rows)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("state %q produced %d rows, want 1", state, len(rows))
			}

			bearing := !stateRejectsFacts(state)
			unavailable := rows[0].Outcome == contractsv1.ContextFabricRequirementUnavailable
			if bearing == unavailable {
				t.Fatalf("state %q: the registry says fact-bearing=%v, this evaluator published %q. "+
					"A fact-bearing state must never read `unavailable` -- that tells a reader they got "+
					"NONE of a cell they got part of -- and a state that rejects facts must not read as "+
					"anything else", state, bearing, rows[0].Outcome)
			}
		})
		checked++
	}
	if checked != len(states) {
		t.Fatalf("checked %d states of %d", checked, len(states))
	}
	// POSITIVE CONTROL on the predicate itself: if `stateRejectsFacts` ever
	// became constant, every assertion above would agree with it vacuously.
	if !stateRejectsFacts(SourceUnavailable) || stateRejectsFacts(SourceAvailable) {
		t.Fatal("stateRejectsFacts no longer discriminates; every assertion above is vacuous")
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

// TestAPartialCoverageDetailDoesNotSuppressTheRow bounds the BLAST RADIUS of
// the undeclared-code stop path.
//
// That stop path is deliberately severe: rather than publish a cause nobody
// declared, it drops the requirement's row entirely. Severity that large is
// only safe while it fires on a detail that genuinely names a bad cause FOR A
// KIND. `Coverage.Details` is a shared, optional-first array -- it carries
// every detail the coverage layer wrote for the WHOLE turn, not only the ones
// about this requirement, and any field of one may be absent. So the two
// guards below decide which details are allowed to speak at all, and without
// them one unrelated half-filled entry silences an unrelated requirement's
// disclosure. Each guard is pinned from the side it protects:
//
//   - NO KIND. A detail naming no fact kind cannot be about any requirement's
//     kinds, so nothing it carries -- including a code outside the vocabulary
//     -- may decide this row.
//   - NO CODE. `Code` is optional, so a record written before the field
//     existed carries none. An empty code is an ABSENT cause, not an
//     undeclared one; reading absence as a vocabulary violation would drop
//     rows for documents that are merely older than the field.
func TestAPartialCoverageDetailDoesNotSuppressTheRow(t *testing.T) {
	// NOT PARALLEL, AND IT MUST STAY THAT WAY.
	//
	// The complement below drives the undeclared-code stop path, and that path
	// DISCLOSES through the process-global `slog.Default()`. A sibling test,
	// TestAnUndeclaredCauseCodeEmitsNoRow, swaps that global for its own buffer
	// and asserts an EXACT count of one drop line on it. Run in parallel, this
	// test's warning lands in the sibling's buffer and fails it at two -- and
	// the two tests write the same buffer concurrently, which `-race` reports.
	// Measured, not theorised: `-race -count=100 -parallel=2` over the two names
	// fails with "emitted 2 times ... want exactly 1" plus "race detected".
	//
	// A test that reaches a global disclosure is a SEQUENTIAL test. The local
	// buffer below is the second half of the fix: it keeps this test's own
	// output out of whatever handler happens to be installed, so the isolation
	// does not rest on ordering alone.
	health := contractsv1.ContextFabricFactHealth
	requirement := readRequirement(CompletionQuantifierAtLeastOne)

	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// The premise, asserted rather than assumed, exactly as the stop-path test
	// asserts it: if this token were ever declared, every fixture below would
	// stop carrying a bad code and would silently prove nothing.
	const undeclared = contractsv1.ContextFabricCoverageDetailCode("fact_invented_by_a_future_producer")
	for _, declared := range contractsv1.ContextFabricCoverageDetailCodeVocabulary() {
		if declared == undeclared {
			t.Fatalf("%q is now a declared member; these fixtures no longer carry an undeclared code", undeclared)
		}
	}

	// One served observation, plus the one detail under test. The served
	// observation is what makes the assertion legible: the row that must
	// survive is a POSITIVE one, so a suppression shows up as a missing
	// `satisfied` rather than as one flavour of absence replacing another.
	withDetail := func(detail contractsv1.ContextFabricCoverageDetail) Coverage {
		coverage := factCoverage(health, SourceAvailable)
		coverage.Details = append(coverage.Details, detail)
		return coverage
	}

	for _, testCase := range []struct {
		name   string
		detail contractsv1.ContextFabricCoverageDetail
	}{
		{
			name: "a detail naming no fact kind",
			detail: contractsv1.ContextFabricCoverageDetail{
				DetailID: "detail_kindless",
				Source:   canonicalFactSourcePrefix,
				Code:     undeclared,
				Label:    "kindless",
			},
		},
		{
			name: "a detail naming a kind but no code",
			detail: contractsv1.ContextFabricCoverageDetail{
				DetailID: "detail_codeless",
				Source:   canonicalFactSourcePrefix + string(health),
				FactKind: health,
				Label:    "codeless",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rows := appendReadRequirementEvaluations(nil,
				[]contractsv1.ContextFabricPlanRequirement{requirement}, withDetail(testCase.detail))
			if len(rows) != 1 {
				t.Fatalf("%s suppressed the row (%d rows, want 1) -- an incomplete detail is not an "+
					"undeclared cause, and the stop path must not reach it", testCase.name, len(rows))
			}
			if rows[0].Outcome != contractsv1.ContextFabricRequirementSatisfied {
				t.Fatalf("outcome = %q, want satisfied -- the served observation decides this row, "+
					"not the half-filled detail beside it", rows[0].Outcome)
			}
		})
	}

	// COMPLEMENT, in the same run: the same shape with BOTH fields filled and
	// the same bad code DOES suppress the row. Without it the two cases above
	// would pass on an evaluator whose stop path had been deleted outright --
	// the opposite defect, and the more dangerous one.
	poisoned := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		withDetail(contractsv1.ContextFabricCoverageDetail{
			DetailID: "detail_poison",
			Source:   canonicalFactSourcePrefix + string(health),
			Code:     undeclared,
			FactKind: health,
			Label:    "poison",
		}))
	if len(poisoned) != 0 {
		t.Fatalf("a detail naming BOTH a kind and an undeclared code produced %d rows, want 0 -- "+
			"the cases above show the guards are NARROW only while the stop path still fires", len(poisoned))
	}
}

// TestTheNarrowedCauseIsTheFirstNarrowedKindInPublishedOrder pins WHICH
// narrowing names the row's cause when more than one declared kind was
// narrowed.
//
// The row publishes ONE cause. When two kinds were both narrowed and the
// coverage layer recorded a different code for each, something has to choose,
// and the choice has to be a RULE rather than whichever entry the loop
// happened to see last. The rule is: **the first narrowed kind in the
// PUBLISHED REQUIREMENT ORDER names the cause -- the requirement's own
// declared order, a deterministic tiebreak, NOT a priority claim.** Stopping at
// the first match is what makes that true; without the stop it is last-wins, a
// different published cause for identical evidence.
//
// TWO EARLIER DRAFTS OF THIS COMMENT WERE WRONG, IN THE SAME WAY, AND THAT IS
// WHY THIS ONE NAMES NO MECHANISM IT HAS NOT TRACED.
//
// The first called FactKinds "the plan's priority order for that cell" and
// asserted the cause flips when the list is reversed -- pinning behaviour
// production cannot construct, justified by a claim about a layer this file
// does not own. The second replaced that with "canonical vocabulary-ordered
// FactKinds", citing `sortedFactKinds`. ALSO FALSE, and worse for being a
// correction: `sortedFactKinds` is applied in `InputsForComputedStep`
// (frame_vocab.go), to COMPUTED STEP INPUTS -- it never touches a read
// requirement's FactKinds. The seed sorts LEXICALLY
// (`kinds[i] < kinds[j]`, obligation_seed.go), and plan_requirements.go copies
// the slice with `copyFactKinds` WITHOUT reordering. Lexical and vocabulary
// order genuinely differ -- {health, flow} is [health flow] by vocabulary and
// [flow health] lexically -- and this test's {health, workload} fixture sorts
// identically under both, which is exactly why it could not catch its own
// comment.
//
// So the order this evaluator reads is simply THE ORDER THE PUBLISHED
// REQUIREMENT CARRIES. That is a true statement about the input, it needs no
// claim about any producer's sorting, and it is what the loop actually does.
// A comment naming a specific mechanism is an assertion and needs the same
// evidence as code.
//
// REACHABILITY, stated rather than assumed: `factDetailSpecForRead` mints
// `fact_narrowed` for every narrowing today, so two different codes cannot
// arise in service and the choice is currently unobservable. It is pinned for
// the same reason the undeclared-code stop path is pinned -- one producer
// change away from mattering, and far cheaper to fix in place than to
// rediscover from a field report about a cause that "changes for no reason".
func TestTheNarrowedCauseIsTheFirstNarrowedKindInPublishedOrder(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	workload := contractsv1.ContextFabricFactWorkload

	// Two narrowings, DIFFERENT codes, so the choice is observable at all. A
	// fixture giving both kinds the same code could not tell first from last
	// and would prove nothing -- which is exactly why the shipped
	// narrowedCoverage helper (one code for every kind) does not reach this.
	narrowing := func(kind FactKind, code contractsv1.ContextFabricCoverageDetailCode) contractsv1.ContextFabricCoverageDetail {
		return contractsv1.ContextFabricCoverageDetail{
			DetailID:     "detail_" + string(kind),
			Source:       canonicalFactSourcePrefix + string(kind),
			Code:         code,
			FactKind:     kind,
			SourceState:  SourceAvailable,
			SkippedKinds: []SubjectKind{SubjectRepository},
			Narrowed:     true,
			Label:        "narrowed",
		}
	}

	// The requirement's kinds are left exactly as readRequirement declares
	// them, because the published order is the only order this evaluator sees.
	requirement := readRequirement(CompletionQuantifierAtLeastOne)
	if len(requirement.FactKinds) < 2 || requirement.FactKinds[0] != health || requirement.FactKinds[1] != workload {
		t.Fatalf("the fixture requirement declares %v; this test needs health then workload as "+
			"published, or it is not measuring the tiebreak it names", requirement.FactKinds)
	}

	// THE DETAIL ARRAY IS IN THE OPPOSITE ORDER TO THE DECLARED KINDS, and that
	// is what makes the assertion mean something. If the evaluator tracked the
	// array it would answer `fact_scope_unexpanded`; it answers the first
	// NARROWED KIND in published order instead. Deleting the stop after the
	// first match makes the later kind win and this fails.
	coverage := factCoverage(health, SourceAvailable, workload, SourceAvailable)
	coverage.Details = append(coverage.Details,
		narrowing(workload, contractsv1.ContextFabricCoverageDetailFactScopeUnexpanded),
		narrowing(health, contractsv1.ContextFabricCoverageDetailFactNarrowed))

	rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{requirement}, coverage)
	if len(rows) != 1 {
		t.Fatalf("produced %d rows, want 1: %+v", len(rows), rows)
	}
	want := contractsv1.ContextFabricCoverageDetailFactNarrowed
	if rows[0].CauseCoverage != want {
		t.Fatalf("cause = %q, want %q -- with both kinds narrowed the FIRST narrowed kind IN "+
			"PUBLISHED ORDER names the cause; the detail array is in the opposite order, so an "+
			"evaluator reading the array rather than the published kinds fails here",
			rows[0].CauseCoverage, want)
	}
	// The cause is CARRIED here, not defaulted -- a narrowed kind ranks at zero
	// severity, so if the carry ever stopped happening the arm would default
	// `fact_narrowed` and the assertion above would still pass while measuring
	// nothing.
	if !rows[0].CauseObserved {
		t.Fatalf("cause_observed = false: the code was defaulted, not carried from the planner's " +
			"own detail, so this test would no longer discriminate")
	}

	// AND ONLY A NARROWED KIND MAY NAME A NARROWING'S CAUSE.
	//
	// This carry runs when nothing outranked zero severity, so every observed
	// kind is `available` -- including kinds that were NOT narrowed and that
	// carry a coverage code of their own. Those codes describe something else
	// entirely. Letting one name the shortfall attributes the narrowing to a
	// mechanism that did not produce it, which is the false-provenance defect
	// this row's CauseObserved flag exists to prevent one field up.
	//
	// The fixture puts the non-narrowed kind FIRST in the published order, so
	// the filter is the only thing that can skip it: drop the filter and the
	// first published kind wins on the strength of a code about a different
	// thing.
	mixed := factCoverage(health, SourceAvailable, workload, SourceAvailable)
	mixed.Details = append(mixed.Details,
		contractsv1.ContextFabricCoverageDetail{
			DetailID: "detail_" + string(health), Source: canonicalFactSourcePrefix + string(health),
			Code: contractsv1.ContextFabricCoverageDetailFactProviderReported, FactKind: health,
			SourceState: SourceAvailable, Label: "not narrowed",
		},
		narrowing(workload, contractsv1.ContextFabricCoverageDetailFactNarrowed))

	mixedRows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{requirement}, mixed)
	if len(mixedRows) != 1 {
		t.Fatalf("produced %d rows, want 1: %+v", len(mixedRows), mixedRows)
	}
	if mixedRows[0].CauseCoverage != contractsv1.ContextFabricCoverageDetailFactNarrowed {
		t.Fatalf("cause = %q, want %q -- only the NARROWED kind may name a narrowing's cause; the "+
			"non-narrowed kind is declared first and carries a code about something else, so an "+
			"evaluator that skipped the narrowed-kind filter publishes it and attributes the "+
			"shortfall to a mechanism that did not produce it",
			mixedRows[0].CauseCoverage, contractsv1.ContextFabricCoverageDetailFactNarrowed)
	}
	if mixedRows[0].Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("outcome = %q, want narrowed -- one kind served in full and one was narrowed; if "+
			"this ever reads satisfied the cause assertion above is measuring a row nobody looks at",
			mixedRows[0].Outcome)
	}
}
