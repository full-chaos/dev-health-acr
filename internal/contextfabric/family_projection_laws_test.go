package contextfabric

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The eight algebra laws of design §13.2.2a, as property tests over the
// GENERATED frame corpus.
//
// WHY THE LAWS AND NOT MORE TABLES. Round 2 of the design review stopped
// with eight re-finds, and the root cause was one sentence: stage 2
// specified derivation TABLES without ever specifying the ALGEBRA those
// tables must satisfy. Each table was locally defensible; composed, they
// permitted removal, omission and conflict. A law is refusable by
// counterexample, which is what a table is not.
//
// EVERY EXPECTATION HERE IS DERIVED FROM THE INPUT'S OWN FIELDS OR FROM A
// STATED RULE -- never from the function under test. That constraint is not
// stylistic. It has been violated three times on this branch, once inside a
// guard written to close that very class, and each time the test kept
// passing while proving nothing: a non-vacuity check that asks the function
// under test whether the check should run is decided by the mutation it
// exists to catch.
//
// EVERY TEST THAT CAN SKIP AN INPUT COUNTS THE INPUTS THAT REACHED ITS
// ASSERTIONS and fails at zero. A mutation once routed all 200 trials of a
// property test down a refusal branch; the test stayed green and proved
// nothing. Red-on-parent shows a test CAN fail and a mutation kill shows it
// fails for the right reason; neither shows the assertions ever executed.

// reachCounter counts how many inputs reached a test's assertions, so a
// property that silently stops applying fails instead of passing.
type reachCounter struct {
	name    string
	reached int
	skipped int
}

func (r *reachCounter) reach() { r.reached++ }
func (r *reachCounter) skip()  { r.skipped++ }
func (r *reachCounter) require(t *testing.T, minimum int) {
	t.Helper()
	if r.reached < minimum {
		t.Fatalf("%s: only %d inputs reached the assertions (%d skipped), want at least %d -- the property was not exercised and this test proves nothing", r.name, r.reached, r.skipped, minimum)
	}
	t.Logf("%s: %d inputs reached the assertions, %d skipped", r.name, r.reached, r.skipped)
}

func obligationSet(obligations []AnswerObligation) map[AnswerObligation]bool {
	set := make(map[AnswerObligation]bool, len(obligations))
	for _, obligation := range obligations {
		set[obligation] = true
	}
	return set
}

// -----------------------------------------------------------------------
// L1 -- MONOTONICITY
// -----------------------------------------------------------------------

// frameExtension is one single-value extension of a frame: the design's
// "adding a value to a set-valued field, or setting an unset field".
type frameExtension struct {
	// axis names WHICH axis was extended, for the failure message and for
	// the coverage assertion below.
	axis string
	// label is the value added.
	label string
	// frame is the EXTENDED proposal, before validation.
	frame QuestionFrame
}

// extensionsOf enumerates every single-value extension of one frame.
//
// The extension is applied to the frame's own emitted axes and the result
// is re-validated, because a law over LEGAL frames says nothing about a
// proposal that never became legal. What it must never do is compare a
// validated frame with an unvalidated one: the obligations of an
// unvalidated frame were never derived, so the inclusion would be trivially
// satisfied and the law would be untested.
func extensionsOf(base generatedFrame) []frameExtension {
	var out []frameExtension

	// A goal the frame does not already carry. This is the extension the
	// law was written for: round-2's P1-1 showed a model emission REMOVING
	// an obligation, which is the thing L1 forbids.
	for _, goal := range InvestigationGoalVocabulary() {
		if base.frame.HasGoal(goal) {
			continue
		}
		extended := base.frame
		extended.Goals = append(append([]InvestigationGoal{}, base.frame.Goals...), goal)
		out = append(out, frameExtension{"goals", string(goal), extended})
	}

	// An emphasis the frame does not already carry.
	for _, emphasis := range AnswerEmphasisVocabulary() {
		if containsEmphasis(base.frame.Emphasis, emphasis) {
			continue
		}
		extended := base.frame
		extended.Emphasis = append(append([]AnswerEmphasis{}, base.frame.Emphasis...), emphasis)
		out = append(out, frameExtension{"emphasis", string(emphasis), extended})
	}

	// A dimension the frame does not already carry.
	for _, dimension := range HealthDimensionVocabulary() {
		if base.frame.HasDimension(dimension) {
			continue
		}
		extended := base.frame
		extended.Dimensions = append(append([]HealthDimension{}, base.frame.Dimensions...), dimension)
		out = append(out, frameExtension{"dimensions", string(dimension), extended})
	}

	// SETTING AN UNSET FIELD. Temporal is scalar and normalization derives
	// `current` for an unset one, so `current` IS the unset state and any
	// other member is the "setting" the law names. A frame that already
	// states a non-current temporal has no unset temporal to set.
	if base.frame.Temporal == TemporalIntentCurrent {
		for _, temporal := range TemporalIntentVocabulary() {
			if temporal == TemporalIntentCurrent {
				continue
			}
			extended := base.frame
			extended.Temporal = temporal
			out = append(out, frameExtension{"temporal", string(temporal), extended})
		}
	}

	// The organization scope's OPTIONAL member kind: absent is unset, and
	// naming one is the extension. This is the axis round-2's P1-4 found
	// collapsing "how many repositories" and "how many teams" into one
	// frame, so it is an extension the law must cover.
	if base.frame.SubjectExpression.Kind == SubjectExpressionOrganizationScope &&
		base.frame.SubjectExpression.Org != nil && base.frame.SubjectExpression.Org.MemberKind == nil {
		for _, kind := range []SubjectKind{SubjectTeam, SubjectProject, SubjectRepository} {
			member := kind
			extended := base.frame
			org := *base.frame.SubjectExpression.Org
			org.MemberKind = &member
			expression := base.frame.SubjectExpression
			expression.Org = &org
			extended.SubjectExpression = expression
			out = append(out, frameExtension{"org_member_kind", string(kind), extended})
		}
	}

	return out
}

func containsEmphasis(set []AnswerEmphasis, want AnswerEmphasis) bool {
	for _, member := range set {
		if member == want {
			return true
		}
	}
	return false
}

// TestLawL1ExtendingAFrameOnlyAddsObligations is law L1.
//
// "Extending a frame may only ADD obligations, never remove one." Formally:
// for legal frames f and f' where f' differs from f only by adding a value
// to a set-valued field or by setting an unset field,
// Obligations(f') is a superset of Obligations(f).
//
// WHAT IT KILLS: the health-on-EMPTY-Dimensions rule. Naming any dimension
// REMOVED the health obligation, so a model emission narrowed the plan --
// round 2's P1-1, which refuted the previous lane's "additive-only" claim
// outright. The rule is gone (health derives unconditionally from the three
// state-ish goals), and this is the property that keeps it gone.
//
// THE EXPECTATION IS SET INCLUSION OVER THE TWO FRAMES' OWN OBLIGATION
// FIELDS. Nothing here asks a derivation function what it should have
// produced; it reads what each validated frame carries and compares.
func TestLawL1ExtendingAFrameOnlyAddsObligations(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	reach := &reachCounter{name: "L1"}
	axes := map[string]int{}

	for _, base := range frames {
		baseSet := obligationSet(base.frame.Obligations)
		for _, extension := range extensionsOf(base) {
			result := ValidateFrame(extension.frame, nil, "")
			if result.Outcome != FrameValidationOutcomeValid {
				// An extension that does not validate is outside the law:
				// L1 quantifies over LEGAL frames on both sides.
				reach.skip()
				continue
			}
			reach.reach()
			axes[extension.axis]++
			extendedSet := obligationSet(result.Frame.Obligations)
			for obligation := range baseSet {
				if !extendedSet[obligation] {
					t.Fatalf("L1 VIOLATED: extending %s by %s=%q REMOVED obligation %q\n  base      goals=%v temporal=%s emphasis=%v dimensions=%v obligations=%v\n  extended  goals=%v temporal=%s emphasis=%v dimensions=%v obligations=%v",
						base, extension.axis, extension.label, obligation,
						base.frame.Goals, base.frame.Temporal, base.frame.Emphasis, base.frame.Dimensions, base.frame.Obligations,
						result.Frame.Goals, result.Frame.Temporal, result.Frame.Emphasis, result.Frame.Dimensions, result.Frame.Obligations)
				}
			}
		}
	}

	// Every extension AXIS must have been exercised. Counting only total
	// reach would let one axis carry the whole number while another was
	// never extended at all -- the margin-versus-cell hole again.
	for _, axis := range []string{"goals", "emphasis", "dimensions", "temporal", "org_member_kind"} {
		if axes[axis] == 0 {
			t.Errorf("L1: no legal extension along axis %q -- the law is untested on that axis", axis)
		}
	}
	reach.require(t, len(frames))
}

// -----------------------------------------------------------------------
// L2 -- SEMANTIC TOTALITY
// -----------------------------------------------------------------------

// TestLawL2EverySetAxisIsDischargedByAName is law L2.
//
// "Every axis a frame SETS must be discharged": for each set axis, either
// the obligation set contains an obligation characteristic of that axis, or
// a declared REQUIREMENT PROPERTY discharges it -- and the discharge mode is
// NAMED, never implicit.
//
// WHAT IT KILLS: {describe_trend, named_subject, bounded_window} deriving no
// temporal obligation at all (round 2's P1-2), where "no obligation" looked
// like a decision because nothing forced it to be written down.
//
// THE EXPECTATION IS ENUMERATED FROM THE FRAME'S OWN FIELDS. The axes a
// frame sets are its Goals, its Temporal and its Dimensions -- read
// directly off the validated frame. FrameAxisDischarges is then checked
// AGAINST that enumeration. The reverse (asking FrameAxisDischarges which
// axes exist and then checking those) would be the discharge table grading
// its own homework: an axis it forgot would be absent from both sides.
func TestLawL2EverySetAxisIsDischargedByAName(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	reach := &reachCounter{name: "L2"}
	modes := map[AxisDischargeMode]int{}
	currentFramesChecked := 0

	for _, generated := range frames {
		// The axes this frame SETS, read off the frame itself.
		type axis struct {
			kind  AxisKind
			value string
		}
		var expected []axis
		for _, goal := range generated.frame.Goals {
			expected = append(expected, axis{AxisGoal, string(goal)})
		}
		for _, dimension := range generated.frame.Dimensions {
			expected = append(expected, axis{AxisDimension, string(dimension)})
		}
		// `current` is NOT a set axis. Normalization derives it for an
		// unset (or out-of-vocabulary) temporal, so a frame carrying it
		// has stated nothing about time -- there is no axis to discharge,
		// which is why the shipped temporalDischarge table has no row for
		// it. This exclusion was found by the sweep: the property was
		// written over every temporal, it failed on `current`, and the
		// CODE was right. The exclusion is asserted rather than assumed
		// just below, so it cannot become a place for a real axis to hide.
		if generated.frame.Temporal != TemporalIntentCurrent {
			expected = append(expected, axis{AxisTemporal, string(generated.frame.Temporal)})
		}

		declared := map[axis]AxisDischarge{}
		for _, discharge := range FrameAxisDischarges(generated.frame) {
			declared[axis{discharge.Axis, discharge.Value}] = discharge
		}

		obligations := obligationSet(generated.frame.Obligations)
		for _, want := range expected {
			reach.reach()
			discharge, ok := declared[want]
			if !ok {
				t.Fatalf("L2 VIOLATED: frame %s sets axis %s=%q and NO discharge is declared for it", generated, want.kind, want.value)
			}
			switch discharge.Mode {
			case DischargeByObligation:
				if discharge.Obligation == "" {
					t.Fatalf("L2 VIOLATED: frame %s discharges axis %s=%q by obligation but names none", generated, want.kind, want.value)
				}
				if !obligations[discharge.Obligation] {
					t.Fatalf("L2 VIOLATED: frame %s discharges axis %s=%q by obligation %q, which is ABSENT from the derived set %v",
						generated, want.kind, want.value, discharge.Obligation, generated.frame.Obligations)
				}
			case DischargeByRequirementProperty:
				if discharge.Property == "" {
					t.Fatalf("L2 VIOLATED: frame %s discharges axis %s=%q by requirement property but names none", generated, want.kind, want.value)
				}
			default:
				t.Fatalf("L2 VIOLATED: frame %s discharges axis %s=%q with mode %q, which is not a named mode -- an implicit discharge is exactly what L2 forbids",
					generated, want.kind, want.value, discharge.Mode)
			}
			modes[discharge.Mode]++
		}

		// THE EXCLUSION, CHECKED. A frame at `current` must declare no
		// temporal discharge at all. If one ever appeared, the loop above
		// would skip a genuine axis and L2 would quietly cover less.
		if generated.frame.Temporal == TemporalIntentCurrent {
			for _, discharge := range FrameAxisDischarges(generated.frame) {
				if discharge.Axis == AxisTemporal {
					t.Fatalf("L2: frame %s is at the UNSET temporal `current` yet declares a temporal discharge %+v -- the exclusion above would then be skipping a real axis", generated, discharge)
				}
			}
			currentFramesChecked++
		}
	}

	if currentFramesChecked == 0 {
		t.Error("L2: no frame in the corpus carried the `current` temporal, so the exclusion assertion never ran")
	}

	// BOTH discharge modes must occur. A property test in which every
	// discharge happens to be by obligation would never execute the
	// requirement-property branch, and that branch is the one the design
	// argues about (`bounded_window` bounds the reads the other
	// obligations already make; `compare` demands matched evidence across
	// operands). A mode with zero occurrences is a dead tier.
	for _, mode := range []AxisDischargeMode{DischargeByObligation, DischargeByRequirementProperty} {
		if modes[mode] == 0 {
			t.Errorf("L2: discharge mode %q never occurred over %d frames -- that branch is untested", mode, len(frames))
		}
	}
	reach.require(t, len(frames))
}

// -----------------------------------------------------------------------
// L3 -- CONFLICT RESOLUTION
// -----------------------------------------------------------------------

// requirementFieldSources declares, for every field of a derived
// requirement row, the ONE thing it is derived from.
//
// This map is law L3 and law L5 made checkable. L3 says no scalar may be
// derivable from two sources without a documented precedence, and its
// preferred fix is to SPLIT the field so no conflict exists -- which is
// exactly what happened to the old CompletionRule, which received
// `all_operands` from the role table and `all_subjects` from the obligation
// row with no rule to choose. It is now two orthogonal fields that cannot
// conflict because they answer different questions: over WHAT, and HOW
// MANY.
//
// L5 says every field HAS a rule. The two laws are the two failure
// directions of this one map -- a field with two sources, and a field with
// none -- and both are checked below by reflection, so a field added later
// is covered without anyone remembering to cover it.
var requirementFieldSources = map[string]string{
	"Obligation":  "the frame's derived obligation set",
	"Role":        "the subject expression's variant (role slot)",
	"Subject":     "the role slot's subject kind",
	"Kind":        "the obligation KIND table (read / computed / answer-contract)",
	"FactKinds":   "the registry's producer declarations, via the obligation seed",
	"Step":        "the computed-step name declared for the obligation",
	"Scope":       "SubjectExpression.Kind",
	// ON A SERVED ROW. An UNSERVED row carries `none` by the row invariant
	// ("an unserved row has Unavailable non-empty and Quantifier `none`"),
	// which is the absence of a quantifier rather than a second source for
	// one -- and servability IS topology-dependent, by design, in the
	// derivation's population guard. Spelled out because the behavioural
	// half below partitions on exactly this distinction, and the two halves
	// of one law disagreeing is the state L3 exists to forbid.
	"Quantifier":  "the obligation's measured serving cardinality (served rows; an unserved row carries `none` by the row invariant)",
	"Unavailable": "the cause attribution over the registry declarations",
	"Dimensions":  "the serving FactKinds' FactCapability.Dimension declarations",
	// The §13.2.3 amendment. Both derive from the SAME single source -- the
	// step's input declaration -- and they are two fields rather than one
	// for the reason L3 prefers a split: the class answers "where do the
	// inputs come from" and the kinds answer "which ones", and a step that
	// reads no fact has an answer to the first and none to the second. One
	// field would have to encode "reads nothing" as an empty list, which is
	// indistinguishable from "not declared yet".
	"InputClass":     "the computed step's declared input class",
	"InputFactKinds": "the computed step's declared input fact kinds",
	"StepExecution":  "the computed step's declared execution (server-executed / declared-only)",
}

// TestLawL5EveryRequirementFieldNamesADerivationRule is law L5.
//
// "Every field of PlanRequirement has a derivation rule." Enumerated by
// REFLECTION over the shipped struct rather than from a hand-kept list,
// because the defect it kills is a field that exists with no rule at all --
// FactKinds had none while the flow diagram listed it as a requirement
// field -- and a hand-kept list of fields cannot notice a field nobody
// added to it.
func TestLawL5EveryRequirementFieldNamesADerivationRule(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	var walk func(typ reflect.Type)
	walk = func(typ reflect.Type) {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Anonymous && field.Type.Kind() == reflect.Struct {
				// The embedded coordinate's fields ARE requirement fields
				// and must not escape the law by being one level down.
				walk(field.Type)
				continue
			}
			seen[field.Name] = true
			if _, declared := requirementFieldSources[field.Name]; !declared {
				t.Errorf("L5 VIOLATED: DerivedRequirement.%s names no derivation rule. Add it to requirementFieldSources with the ONE thing it derives from, or the row carries a value nobody can explain.", field.Name)
			}
		}
	}
	walk(reflect.TypeOf(DerivedRequirement{}))

	for name := range requirementFieldSources {
		if !seen[name] {
			t.Errorf("L5: requirementFieldSources declares a rule for %q, which is not a field of DerivedRequirement -- a stale rule reads exactly like a covered field", name)
		}
	}
	if len(seen) == 0 {
		t.Fatal("L5: reflection walked zero fields -- the check proves nothing")
	}
	t.Logf("L5: %d requirement fields, each naming one derivation rule", len(seen))
}

// TestLawL3ScopeAndQuantifierCannotConflict is law L3's applied form.
//
// The structural half (one source per field) is the map above. This is the
// BEHAVIOURAL half, and it is the one that would catch a regression:
// CompletionScope derives from the subject expression's variant THROUGH the
// role slot, and from nothing else. So across the whole corpus, one ROLE
// must yield exactly one Scope -- whatever obligation the row is for, and
// whatever variant produced the role. A field that started reading the
// obligation as well shows up here as one role carrying two scopes, which
// is the two-sources-no-precedence state L3 forbids, observed rather than
// asserted.
//
// THE KEY IS THE ROLE, AND THE FIRST VERSION OF THIS TEST HAD IT WRONG.
// It grouped by (variant, obligation, role) and asked whether that triple
// ever carried two scopes -- which a deterministic function can never do,
// so the property was VACUOUS. Mutation M3 gave CompletionScope a second
// source keyed on the obligation and this test stayed green. The mutation
// found a hole in the test, not in the code; the key is now the population
// the law is about (every role) rather than the coordinate that happened to
// be convenient. This is the same defect shape the sweep rule names: a
// sweep's key is the population you must cover, never the helper you
// happened to call.
func TestLawL3ScopeAndQuantifierCannotConflict(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	// The CONSTRUCTED registry fixture, not the live one: L3 is a property
	// of the derivation's SHAPE (which field reads which source), and a
	// fixture keeps the law from moving when a producer's declarations
	// change. The live-registry cross is oracle O9's, already green.
	seed, capabilities := fixtureSeed(), fixtureCapabilities()
	reach := &reachCounter{name: "L3"}

	// Role -> the scopes observed for it, each with a witness naming the
	// input that produced it.
	scopeByRole := map[SubjectRole]map[CompletionScope]string{}
	// (obligation, subject kind) -> quantifiers observed, over SERVED ROWS
	// ONLY. The quantifier's declared source is the obligation and its
	// measured serving cardinality -- NOT the topology -- so a fixed pair
	// must yield one quantifier however the variant varies.
	//
	// WHY SERVED-ONLY, AND WHY THAT IS NOT A LOOSENING. `none` is not a
	// third quantifier: it is what the ROW INVARIANT gives an unserved row
	// ("an unserved row has Unavailable non-empty and Quantifier `none`",
	// DerivedRequirement's own doc comment). Whether a cell is servable IS
	// topology-dependent, by design and on purpose -- that is the whole of
	// `deriveRequirement`'s population guard, which returns `none` for a
	// computed obligation whose step has nothing to run over. Reading those
	// rows into this partition asks the law to forbid the mechanism the
	// layer was built with, so the partition was measuring the wrong
	// population.
	//
	// THE CONTRADICTION WAS ALREADY SHIPPED; THE CORPUS HID IT. Executed at
	// `0a172f93` (the commit before this lane's change), touching one line
	// of family_projection_corpus_test.go and nothing else:
	//
	//	control  -- L3 GREEN at the untouched parent, rc=0
	//	probe    -- organization_scope(member kind): SubjectRepository -> SubjectTeam
	//	result   -- L3 VIOLATED: cell "count@team" carries 2 distinct
	//	            CompletionQuantifier values
	//
	// `organization_scope` is not a cohort variant, so the shipped guard
	// already gave `count@<member kind>` a `none` there and `exact` on every
	// cohort variant. The two never met in one cell only because the corpus
	// gives the organization shape a subject kind no cohort shape uses. The
	// law and the derivation disagreed before this lane existed; extending
	// the guard to rank_cohort is what made them meet.
	quantifierByCell := map[string]map[CompletionQuantifier]string{}
	// Cells that carried an unserved row, so the served-only filter above
	// can be shown to be load-bearing rather than assumed to be.
	unservedCells := map[string]string{}

	for _, generated := range frames {
		for _, row := range DeriveRequirements(generated.frame, seed, capabilities) {
			reach.reach()
			if scopeByRole[row.Role] == nil {
				scopeByRole[row.Role] = map[CompletionScope]string{}
			}
			scopeByRole[row.Role][row.Scope] = fmt.Sprintf("%s obligation=%s", generated, row.Obligation)

			cell := string(row.Obligation) + "@" + string(row.Subject)
			if !row.Served() {
				unservedCells[cell] = fmt.Sprintf("%s role=%s unavailable=%s", generated, row.Role, row.Unavailable)
				continue
			}
			if quantifierByCell[cell] == nil {
				quantifierByCell[cell] = map[CompletionQuantifier]string{}
			}
			quantifierByCell[cell][row.Quantifier] = fmt.Sprintf("%s role=%s", generated, row.Role)
		}
	}

	for role, scopes := range scopeByRole {
		if len(scopes) > 1 {
			t.Errorf("L3 VIOLATED: role %q carries %d distinct CompletionScope values -- a scalar derivable from two sources with no precedence.\n  witnesses: %v", role, len(scopes), scopes)
		}
	}
	for cell, quantifiers := range quantifierByCell {
		if len(quantifiers) > 1 {
			t.Errorf("L3 VIOLATED: cell %q carries %d distinct CompletionQuantifier values -- the quantifier derives from the obligation and its measured cardinality, so the topology must not reach it.\n  witnesses: %v", cell, len(quantifiers), quantifiers)
		}
	}

	// NON-VACUITY, because this test's whole failure mode was proving
	// nothing. A property "one X per Y" is trivially satisfied when only
	// one Y was ever observed, so both partitions must be genuinely
	// populated -- and more than one SCOPE must appear across the corpus,
	// or a derivation that returned a constant would pass.
	if len(scopeByRole) < 2 {
		t.Fatalf("L3: only %d role(s) observed; a one-per-role property over one role proves nothing", len(scopeByRole))
	}
	distinctScopes := map[CompletionScope]bool{}
	for _, scopes := range scopeByRole {
		for scope := range scopes {
			distinctScopes[scope] = true
		}
	}
	if len(distinctScopes) < 2 {
		t.Fatalf("L3: every row carried the same scope (%v); a constant satisfies a one-per-role property without deriving anything", distinctScopes)
	}
	if len(quantifierByCell) < 2 {
		t.Fatalf("L3: only %d (obligation, subject) cell(s) observed", len(quantifierByCell))
	}
	// THE SERVED-ONLY FILTER MUST BE LOAD-BEARING ON THIS CORPUS. A filter
	// nothing exercises can be deleted with every assertion still green,
	// which is exactly how the contradiction above stayed hidden: the corpus
	// simply never put a servable and an unservable row in one cell. So the
	// partition must contain at least one cell that carried BOTH -- if a
	// future corpus edit stops producing one, this fails here rather than
	// quietly turning the filter back into dead code.
	both := map[string]string{}
	for cell, witness := range unservedCells {
		if _, servedToo := quantifierByCell[cell]; servedToo {
			both[cell] = witness
		}
	}
	if len(both) == 0 {
		t.Fatalf("L3: no (obligation, subject) cell carried BOTH a served and an unserved row, so the served-only filter is untested and could be deleted with this test still green (unserved cells seen: %d)", len(unservedCells))
	}
	reach.require(t, len(frames))
	t.Logf("L3: %d rows over %d roles (%d distinct scopes) and %d (obligation, subject) served cells; %d cells carried an unserved row, %d carried both: %v",
		reach.reached, len(scopeByRole), len(distinctScopes), len(quantifierByCell), len(unservedCells), len(both), both)
}

// -----------------------------------------------------------------------
// L4 -- DERIVATION BEFORE VALIDATION
// -----------------------------------------------------------------------

// TestLawL4PhaseA1CannotReadADerivedValue is law L4's BEHAVIOURAL half.
//
// The declarative half already ships: TestLawL4NoPhaseA1InvariantReadsA
// DerivedField asserts over the spec table that no A1 invariant NAMES a
// derived field. That is a check on the declarations. This is the check on
// the CODE, and the two are not the same claim -- a declaration can be
// right while the evaluator reads a derived value anyway.
//
// The method: take every legal frame, poison its DERIVED fields with values
// no derivation would produce, and assert phase A1's verdict is byte-identical.
// A1 runs before normalization, so by law L4 it cannot depend on any of
// them. If A1's answer moves, A1 read a derived value.
func TestLawL4PhaseA1CannotReadADerivedValue(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	reach := &reachCounter{name: "L4"}

	for _, generated := range frames {
		clean := generated.frame
		// Rebuild the PROPOSAL: A1 sees model-emitted fields, so strip the
		// derived ones back to what a proposal carries.
		proposal := clean
		proposal.Obligations = nil
		proposal.WidenedObligations = nil
		proposal.Version = ""
		wantFailure, wantBad := ValidateFramePhaseA1(proposal)

		poisoned := proposal
		poisoned.Obligations = []AnswerObligation{ObligationEvidence, ObligationCoverage}
		poisoned.WidenedObligations = []AnswerObligation{ObligationRanking}
		poisoned.Version = "not-the-server-constant"
		gotFailure, gotBad := ValidateFramePhaseA1(poisoned)

		reach.reach()
		if wantBad != gotBad || wantFailure != gotFailure {
			t.Fatalf("L4 VIOLATED: phase A1's verdict changed when only DERIVED fields moved, so A1 reads a derived value\n  frame %s\n  clean    bad=%v failure=%+v\n  poisoned bad=%v failure=%+v",
				generated, wantBad, wantFailure, gotBad, gotFailure)
		}
	}
	reach.require(t, len(frames))
}

// -----------------------------------------------------------------------
// L6 -- NO PARALLEL AUTHORITY
// -----------------------------------------------------------------------

// frameFieldConsumers declares, for every field of QuestionFrame, the named
// CONSUMER that reads it or the named DERIVATION it produces.
//
// L6: "Every frame field must have a named CONSUMER, or a named DERIVATION
// producing the legacy field its consumer actually reads." A field with
// neither is a declaration the substrate does not obey -- the frame saying
// one thing while a keyword matcher decides another.
var frameFieldConsumers = map[string]string{
	"Goals":              "family projection rows 5-6; obligation derivation table 1",
	"SubjectExpression":  "family projection rows 1-4; requirement role slots; DeriveShape",
	"Temporal":           "obligation derivation table 2; axis discharge",
	"Emphasis":           "invariant I14; obligation derivation",
	"Dimensions":         "obligation derivation table 3; axis discharge (§13.3: the question's own subject, NOT the requirement's evidence coverage)",
	"Obligations":        "requirement coordinate derivation; the plan",
	"WidenedObligations": "advisory-only widening (§13.2.4); telemetry",
	"Version":            "telemetry and replay; joins ReuseKey at phase 2",
}

// TestLawL6EveryFrameFieldHasANamedConsumer is law L6's structural half.
func TestLawL6EveryFrameFieldHasANamedConsumer(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(QuestionFrame{})
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		seen[name] = true
		if _, declared := frameFieldConsumers[name]; !declared {
			t.Errorf("L6 VIOLATED: QuestionFrame.%s has no named consumer and no named derivation. A frame field nothing reads is a declaration the substrate does not obey.", name)
		}
	}
	for name := range frameFieldConsumers {
		if !seen[name] {
			t.Errorf("L6: frameFieldConsumers names %q, which is not a field of QuestionFrame -- a stale entry reads like a covered field", name)
		}
	}
	if len(seen) == 0 {
		t.Fatal("L6: reflection walked zero fields -- the check proves nothing")
	}
	t.Logf("L6: %d frame fields, each with a named consumer or derivation", len(seen))
}

// -----------------------------------------------------------------------
// L7 -- CONSERVATIVE COMPLETENESS, and L8 -- GUARD UNIVERSE = INPUT UNIVERSE
//
// SCOPE, STATED BEFORE THE TESTS RATHER THAN CLAIMED BY THEM.
//
// These two laws are the only ones in §13.2.2a whose subject is NOT the
// frame. L7's counterexample is the grouped-cohort completeness path, which
// erases the discovery cap; L8's is the coverage-disclosure guard, which
// validates a model-emitted id against the canonical set rather than
// against the subset the model was shown. Both are real, both are
// CONFIRMED, and neither is this slice's to fix: they are owned by the
// truncation fix-now ticket and by the disclosure-language ticket
// respectively, and their files are outside this change's boundary.
//
// So what lands here is exactly two things, and no more:
//
//  1. The laws asserted over the surfaces THIS PR adds, where they hold BY
//     CONSTRUCTION -- and the tests below say what "by construction" means
//     rather than asserting it, because a law that holds because the
//     surface has no such field is a different claim from a law that is
//     enforced.
//  2. A REPORTED table of the known-violating legacy sites, each with the
//     ticket that owns it, pinned so that it cannot go stale.
//
// WHAT THIS DOES NOT CLAIM: that L7 or L8 holds anywhere else in this
// repository. They do not. A test header that claimed otherwise would be
// the inaccurate-coverage failure -- worse than an admitted gap, because a
// reader who sees a check stops verifying.
// -----------------------------------------------------------------------

// TestLawL7ThisSlicesSurfacesWriteNoCompletenessFlag is L7 over what this
// PR adds.
//
// L7: a completeness or truncation flag recomputed at a later stage may
// only become MORE conservative. The strongest form of "does not violate
// it" is "does not recompute one at all", and that is this slice's actual
// position: neither the family projection nor the requirement derivation
// has a completeness or truncation field to write. Asserted by REFLECTION
// over the returned types, so a field added later fails here rather than
// silently joining the flag population.
func TestLawL7ThisSlicesSurfacesWriteNoCompletenessFlag(t *testing.T) {
	t.Parallel()
	forbidden := []string{"complete", "truncated", "truncation"}
	checked := 0
	for _, typ := range []reflect.Type{
		reflect.TypeOf(FamilyProjection{}),
		reflect.TypeOf(DerivedRequirement{}),
		reflect.TypeOf(FamilyAgreement{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			checked++
			name := strings.ToLower(typ.Field(i).Name)
			for _, banned := range forbidden {
				if strings.Contains(name, banned) {
					t.Errorf("L7: %s.%s looks like a completeness or truncation flag. This slice's position is that it recomputes NONE, which is why L7 holds here by construction. A flag added here needs the conservative-recomputation property asserted against its upstream, not this test relaxed.", typ.Name(), typ.Field(i).Name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("L7: reflection walked zero fields -- the check proves nothing")
	}
	t.Logf("L7: %d fields across this slice's result types, none a completeness or truncation flag", checked)
}

// TestLawL8ThisSlicesSurfacesValidateNoModelReference is L8 over what this
// PR adds.
//
// L8: any validation of model output against a set of legal references must
// be evaluated over exactly the set the model was SHOWN, never a superset.
// This slice validates no model-emitted reference against any reference set
// -- the projection's only input is the validated frame, and its output is
// a closed-vocabulary family and row. The property is asserted the only way
// a negative can be: the projection is run over the whole corpus and every
// output is shown to be a CLOSED VOCABULARY MEMBER, so there is no
// free-text reference channel for a guard-universe mismatch to live in.
func TestLawL8ThisSlicesSurfacesValidateNoModelReference(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	reach := &reachCounter{name: "L8"}
	for _, generated := range frames {
		projection := DeriveQuestionFamily(generated.frame)
		reach.reach()
		if !contractsv1.ValidContextFabricQuestionFamily(projection.Family) {
			t.Fatalf("L8: frame %s projected to %q, which is not a closed-vocabulary family -- an open output channel is where an unvalidated model reference would travel", generated, projection.Family)
		}
		if !ValidFamilyProjectionRow(projection.Row) {
			t.Fatalf("L8: frame %s projected via row %q, which is not a closed-vocabulary row", generated, projection.Row)
		}
	}
	reach.require(t, len(frames))
}

// reportedLawViolation is one known site where L7 or L8 is violated TODAY,
// outside this change's boundary.
type reportedLawViolation struct {
	law   string
	file  string
	token string
	// owner names the work that removes it, by ROLE rather than by ticket
	// id -- the id lives in the ticket comment, not in the source.
	owner string
}

// reportedLawViolations is the table ruling 1 requires: every legacy site
// this slice REPORTS and does not fix.
//
// It is pinned by a presence check below so it cannot rot. If a site is
// fixed, this table is what tells the next reader that the report is stale
// -- which is the failure mode a prose paragraph in a PR body has and a
// test does not.
var reportedLawViolations = []reportedLawViolation{
	{
		law:   "L7",
		file:  "chaos4636_grouped_cohort.go",
		token: "func BuildCohortGroups",
		owner: "the cohort-truncation fix-now ticket",
		// Discovery sets Complete = len(members) < MaxCohortMembers. Group
		// building then constructs every group Complete:true, Truncated:
		// false with Total = len(members) over the ALREADY-CAPPED set, and
		// the cohort-level conjunction over those groups comes out
		// Complete=true. The discovery cap is ERASED, which makes a
		// downstream flag LESS conservative than its upstream -- the exact
		// implication L7 forbids.
	},
	{
		law:   "L8",
		file:  "model_runtime.go",
		token: "func applyCoverageDisclosures",
		owner: "the disclosure-language ticket",
		// The guard rejects a coverage detail id only when it is absent
		// from the CANONICAL set, while synthesis is deliberately shown a
		// narrower MATERIAL subset and ids are minted sequentially. A model
		// shown cov-01 and cov-03 can emit cov-02 -- a reference it could
		// only have produced by pattern rather than by reading -- and it
		// passes. Guard universe wider than input universe.
	},
}

// TestReportedLawViolationsStillExist keeps the REPORTED table honest.
//
// A report that names a site which no longer exists is worse than no
// report: it tells a reader a defect is outstanding when it is fixed, or
// hides that the shape moved somewhere this table does not name. This test
// does NOT assert the defect -- it asserts that the site this slice
// declined to touch is still the site it described.
func TestReportedLawViolationsStillExist(t *testing.T) {
	t.Parallel()
	if len(reportedLawViolations) == 0 {
		t.Fatal("the reported-violation table is empty; L7 and L8 both have a confirmed site and this slice fixes neither")
	}
	for _, violation := range reportedLawViolations {
		source, err := os.ReadFile(violation.file)
		if err != nil {
			t.Errorf("%s: cannot read %s, which this slice REPORTS as a violating site owned by %s: %v", violation.law, violation.file, violation.owner, err)
			continue
		}
		if !strings.Contains(string(source), violation.token) {
			t.Errorf("%s: %s no longer contains %q. If %s removed it, delete this row and say so; if the shape moved, point the row at where it went. A stale report is a false statement about the state of the code.",
				violation.law, violation.file, violation.token, violation.owner)
		}
	}
}
