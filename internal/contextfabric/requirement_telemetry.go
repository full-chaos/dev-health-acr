package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Telemetry for the obligation -> requirement derivation. Design §13.15's
// telemetry bar: rows, cells, and unavailable reasons as CLOSED TOKENS.
//
// WHY THE COUNTS ARE FIXED-LENGTH ARRAYS OVER THE CLOSED VOCABULARIES
// RATHER THAN MAPS. Two reasons, both of them defects this program has
// already paid for:
//
//  1. A map produces a different key order on every run, so two runs of one
//     frame do not produce a diffable row -- the property every other event
//     in this package is careful to have.
//  2. A map omits the members that counted zero, and an omitted zero is
//     indistinguishable from a tier that never ran. That is the "a gate tier
//     with no positive fixture can be dead for its whole life and read as
//     green" failure, applied to telemetry: an operator seeing no
//     `table_shape_undeclared` key cannot tell whether no cell hit that
//     cause or whether the classifier never reached it. Every closed token
//     gets a key on every event, so a zero is an OBSERVED zero.
//
// The arrays are indexed by vocabulary position, so a member added to a
// vocabulary changes the array length and every read of it fails to
// compile -- which is the same "a silent sink is a compile error"
// discipline FrameValidationTelemetry adopted.

// RequirementDerivationVersion is the derivation-table version this build
// implements: the role table in subject_role.go crossed with the
// completion rules in requirement_derivation.go.
//
// It is bumped whenever a role rule, a completion scope, a quantifier rule
// or an unavailable-reason classification changes MEANING, on the same
// ground as QuestionFrameVersion and RankingFormulaVersion: a persisted
// receipt must be replayable against the table that produced it, and two
// rows derived under different tables are not comparable.
//
// v1 -> v2: a computed obligation whose SERVER STEP runs over the resolved
// member set is now UNAVAILABLE on a frame that resolves none, where before it
// derived SERVED and its planning row seeded `satisfied`. That is an
// unavailable-reason classification changing meaning, which is exactly the
// condition above -- receipts written before and after this build report
// different rows for the same frame, and under one version marker nothing
// downstream could tell them apart. The rows are shadow-only in this phase
// (see reuse_key_completeness_test.go's RequirementDerivationVersion entry), so
// the bump costs nothing today; it is made because the marker's whole purpose
// is to be wrong the moment it is not bumped.
const RequirementDerivationVersion = "requirement-derivation.v2"

// RequirementDerivationSummary is the requirement layer's telemetry row.
//
// COUNTS AND CLOSED TOKENS ONLY. No subject label, no canonical id, no
// question text, no fact-kind list -- the same rule frame_telemetry.go
// holds itself to. The cells are countable and their causes are named;
// WHICH team was unserved is not a telemetry question.
type RequirementDerivationSummary struct {
	// Derived is every requirement row the frame produced -- the
	// denominator. Served + Unserved always equals it, and the identity is
	// asserted rather than assumed.
	Derived int
	// Served is the rows that name a fact kind or a server step.
	Served int
	// Unserved is the rows that name an unavailable reason.
	Unserved int

	// UnavailableCells counts unserved cells per reason, indexed by
	// RequirementUnavailableReasonVocabulary position.
	UnavailableCells [RequirementUnavailableReasonCount]int

	// The two ARMS of `computed_population_absent`, split because they are
	// actionable by different parties -- the test every token in that
	// vocabulary has to pass, applied one level down.
	//
	// `not_a_population` means the COORDINATE asked for the impossible: an
	// ordering or a cardinality of the organization itself, or of a grouping
	// axis. The remedy is upstream, in what the interpreter emitted.
	// `unresolvable_member_set` means the coordinate was legitimate -- a
	// named subject is a population of one -- but the FRAME resolves no
	// member set, so the step's executor is never invoked. The remedy is a
	// different subject expression, or a step that does not need a cohort.
	// One token would report both as "nothing to run over" and send an
	// operator to the wrong half of the pipeline.
	//
	// `unresolvable_member_set` NOW HAS TWO CAUSES and this counter cannot
	// tell them apart: the expression enumerates nothing, or it declares a
	// member kind no discovery arm serves. Both are truthfully "the frame
	// resolves no member set", so this stays a correct partition -- the
	// distinction is carried by the frame-validation line's own
	// `cohort_discoverability` key, on the same event, which names the
	// shared predicate's closed reason. It is a per-FRAME fact and not a
	// per-row one, which is why it is a field there rather than a fourth arm
	// here.
	//
	// DERIVED FROM THE ROW'S OWN COORDINATE through the same pure predicate
	// the derivation used, never re-read from the step table: the two arms
	// partition exactly (`deriveRequirement` takes the first when
	// `coordinateNamesAPopulation` is false and the second only when it is
	// true), so this cannot disagree with the row it describes. Their sum is
	// UnavailableCells[computed_population_absent], which the emitter's own
	// keys let an operator check.
	ComputedPopulationAbsentNotAPopulation        int
	ComputedPopulationAbsentUnresolvableMemberSet int
	// ComputedPopulationAbsentNonComputedRow is the RESIDUAL that makes the
	// split above a TOTAL partition of its bucket rather than two counters
	// that usually add up.
	//
	// The two arms are COMPUTED-only by design -- a read row has no server
	// step and no declared step inputs, so neither arm describes it. But
	// `UnavailableCells` counts the token on ANY row, so without this a row
	// that is unavailable for this reason and NOT computed would leave the
	// aggregate at one and both arms at zero: an emitted line claiming a
	// refusal that no arm accounts for. Found by an adversarial round, on a
	// state this package's own test constructs deliberately.
	//
	// It reads zero on every production frame today, because
	// `classifyUnavailable` never returns this token for a read row. That is
	// the point of emitting it anyway: a zero here is an OBSERVED zero, and
	// the day it is not, the line says so instead of silently losing a row.
	//
	// INVARIANT, asserted rather than assumed:
	//   not_a_population + unresolvable_member_set + non_computed_row
	//     == UnavailableCells[computed_population_absent]
	ComputedPopulationAbsentNonComputedRow int
	// ComputedInputKindsUnplanned counts, per fact kind, how many computed
	// rows declared that kind as an input the turn did NOT plan a read for,
	// because the row's step has nothing to run over.
	//
	// It is the other half of the diagnosis. The arm counters above say a
	// decision fired; this says what the decision COST -- which reads the
	// answer did not get, in the same closed-vocabulary histogram shape as
	// ComputedInputKinds beside it, so the two are directly comparable on
	// one line ("declared N, planned N-M, dropped M").
	//
	// RE-DERIVED FROM THE STEP TABLE, and this is the ONE place in this file
	// where that is right rather than wrong. The rule above -- "counted off
	// the ROW, not re-derived from the step" -- protects a row that CARRIES
	// a declaration from being described by a second opinion. This row
	// deliberately carries none: the derivation clears Step, InputClass and
	// InputFactKinds on an unavailable row, because "a row that named both a
	// step and a reason it cannot run would be two answers to what became of
	// the cell". The table is therefore the only source, the lookup is pure
	// and total over the obligation, and an operator who cannot see the
	// dropped kinds cannot tell this arm from any other unavailable cell.
	ComputedInputKindsUnplanned [contractsv1.ContextFabricFactKindCount]int

	// Quantifiers counts rows per completion quantifier, indexed by
	// CompletionQuantifierVocabulary position.
	Quantifiers [CompletionQuantifierCount]int
	// Roles counts rows per subject role, indexed by
	// SubjectRoleVocabulary position.
	Roles [SubjectRoleCount]int

	// ComputedRowsWithDeclaredInputs is how many computed rows carried an
	// input declaration -- the denominator for the two arrays below, and
	// the number an operator reads to tell "no computed row declared
	// inputs" from "no computed row was derived at all".
	ComputedRowsWithDeclaredInputs int
	// ComputedInputClasses counts declared computed rows per input class,
	// indexed by ComputedStepInputClassVocabulary position.
	ComputedInputClasses [ComputedStepInputClassCount]int
	// ComputedInputKinds counts, per fact kind, how many computed rows
	// declared that kind as an INPUT. Indexed by the contracts' closed
	// fact-kind vocabulary position.
	//
	// A HISTOGRAM, NOT A LIST, and the distinction is this file's own rule
	// rather than a workaround for it. "No fact-kind list" above forbids a
	// variable-length per-row list, which would carry cardinality and let a
	// reader correlate a row with a subject. A fixed-length count over the
	// closed vocabulary carries neither, and it gives the property the
	// other arrays here exist for: a kind no step consumes is present and
	// ZERO, so "nothing declared this kind" is an observed zero rather than
	// an absent key.
	//
	// This is what makes the amendment's resolved inputs readable from the
	// run's own artifacts, which is the same-change telemetry bar. The
	// consumer is FrameValidationEvent.RequirementDerivation
	// (frame_telemetry.go), already recorded on every validated frame.
	ComputedInputKinds [contractsv1.ContextFabricFactKindCount]int
	// ComputedStepExecutions counts declared computed rows per step
	// execution, indexed by ComputedStepExecutionVocabulary position.
	//
	// Worth its own array rather than being folded into the class counts: a
	// declared-only step is an OPERATIONAL fact (an obligation the server
	// names but does not satisfy), and an operator reading a run should be
	// able to see it without knowing which steps happen to be wired today.
	ComputedStepExecutions [ComputedStepExecutionCount]int

	// Version is RequirementDerivationVersion, so an event can be read
	// against the table that produced it.
	Version string
}

// RequirementDerivationSummaryFrom folds a frame's rows into the telemetry
// row.
//
// Total over its input and safe on nil: a frame that produced no rows
// yields a zeroed summary CARRYING THE VERSION, so "the derivation ran and
// found nothing to require" is distinguishable from "the derivation never
// ran" (whose summary has an empty Version). That distinction is the whole
// reason Version is set unconditionally here rather than only when there
// are rows.
func RequirementDerivationSummaryFrom(rows []DerivedRequirement) RequirementDerivationSummary {
	summary := RequirementDerivationSummary{Version: RequirementDerivationVersion}
	summary.Derived = len(rows)
	for _, row := range rows {
		if row.Served() {
			summary.Served++
		} else {
			summary.Unserved++
			if index, ok := unavailableReasonIndex(row.Unavailable); ok {
				summary.UnavailableCells[index]++
			}
			// The arm split. Gated on the COMPUTED kind as well as the
			// token so a future read-side classifier returning this reason
			// cannot silently land in a computed-only counter.
			if row.Unavailable == RequirementReasonComputedPopulationAbsent && row.Kind != ObligationKindComputed {
				// The residual: the bucket counted it, so the split must too.
				summary.ComputedPopulationAbsentNonComputedRow++
			}
			if row.Kind == ObligationKindComputed && row.Unavailable == RequirementReasonComputedPopulationAbsent {
				if coordinateNamesAPopulation(row.RequirementCoordinate) {
					summary.ComputedPopulationAbsentUnresolvableMemberSet++
				} else {
					summary.ComputedPopulationAbsentNotAPopulation++
				}
				// What the decision cost: the inputs this cell would have
				// planned had its step been runnable. The row carries none
				// (the invariant clears them), so the step table is the
				// source -- see the field's own doc comment.
				if step, named := StepForComputedObligation(row.Obligation); named {
					if declared, ok := InputsForComputedStep(step); ok {
						for _, kind := range declared.FactKinds {
							if index, ok := factKindIndex(kind); ok {
								summary.ComputedInputKindsUnplanned[index]++
							}
						}
					}
				}
			}
		}
		if index, ok := quantifierIndex(row.Quantifier); ok {
			summary.Quantifiers[index]++
		}
		if index, ok := subjectRoleIndex(row.Role); ok {
			summary.Roles[index]++
		}
		// Counted off the ROW, not re-derived from the step: the row is
		// what a persisted artifact and the parity proof read, so telemetry
		// that consulted the table again could disagree with the thing it
		// claims to describe.
		if row.InputClass == "" {
			continue
		}
		summary.ComputedRowsWithDeclaredInputs++
		if index, ok := computedStepInputClassIndex(row.InputClass); ok {
			summary.ComputedInputClasses[index]++
		}
		for _, kind := range row.InputFactKinds {
			if index, ok := factKindIndex(kind); ok {
				summary.ComputedInputKinds[index]++
			}
		}
		if index, ok := computedStepExecutionIndex(row.StepExecution); ok {
			summary.ComputedStepExecutions[index]++
		}
	}
	return summary
}

// Balanced reports whether Served + Unserved accounts for every derived
// row.
//
// A row that is neither -- which an out-of-vocabulary reason token or a
// future third row state would produce -- is exactly the kind of silent
// loss the render-shape accounting field exists to catch elsewhere in this
// package, so the same positive statement is available here.
func (s RequirementDerivationSummary) Balanced() bool {
	return s.Served+s.Unserved == s.Derived
}

func unavailableReasonIndex(value RequirementUnavailableReason) (int, bool) {
	for index, member := range requirementUnavailableReasons {
		if member == value {
			return index, true
		}
	}
	return 0, false
}

func quantifierIndex(value CompletionQuantifier) (int, bool) {
	for index, member := range completionQuantifiers {
		if member == value {
			return index, true
		}
	}
	return 0, false
}

func computedStepExecutionIndex(value ComputedStepExecution) (int, bool) {
	for index, member := range computedStepExecutions {
		if member == value {
			return index, true
		}
	}
	return 0, false
}

func computedStepInputClassIndex(value ComputedStepInputClass) (int, bool) {
	for index, member := range computedStepInputClasses {
		if member == value {
			return index, true
		}
	}
	return 0, false
}

func subjectRoleIndex(value SubjectRole) (int, bool) {
	for index, member := range subjectRoles {
		if member == value {
			return index, true
		}
	}
	return 0, false
}

// RequirementDeriver is the port RuntimeQuestionInterpreter derives
// requirement rows through.
//
// AN EXPLICITLY-WIRED FIELD, never a type assertion on something else --
// the discipline FamilyTelemetry and FrameTelemetry beside it already
// follow, for the recorded reason that CommitAffirmationTelemetry was
// optional, nothing in production implemented it, every event failed its
// type assertion and the whole signal disappeared with tests passing
// throughout. *FactCapabilityRegistry implements this, and hosted/open.go
// wires the registry it already builds.
//
// It is an interface rather than the concrete registry so that the
// interpreter does not gain a dependency on fact reading -- it needs the
// DECLARATIONS, not the reader.
type RequirementDeriver interface {
	// DeriveRequirements returns the requirement rows a validated frame
	// demands, against the declarations this registry holds.
	DeriveRequirements(frame QuestionFrame) []DerivedRequirement
}

// DeriveRequirements implements RequirementDeriver against the registry's
// own declarations.
//
// The seed is GENERATED on each call rather than cached at construction.
// That is deliberate: the capability set is immutable after construction,
// so a cache would be safe, but it would be a SECOND COPY of derived state
// living beside the thing it is derived from -- the exact shape that let a
// hand table and its artifact drift apart through two review rounds of the
// declaration slice. Capabilities() already returns deep copies, the
// registry holds at most a few dozen of them, and this runs once per
// interpret call.
func (r *FactCapabilityRegistry) DeriveRequirements(frame QuestionFrame) []DerivedRequirement {
	if r == nil {
		return nil
	}
	capabilities := r.Capabilities()
	return DeriveRequirements(frame, GenerateObligationSeed(capabilities), capabilities)
}
