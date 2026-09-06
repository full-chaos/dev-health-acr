package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The legacy completeness arm, tested AT THE CONSUMER THAT RECEIVES IT.
//
// Amending the completeness derivation would make every already-stored
// document whose state the new rule no longer produces unreadable: results are
// immutable, the payload IS the document, and the store validates on every
// read. The stored-path validator admits exactly one shape so those rows keep
// serving; this route says WHEN that happened, so the exemption's sunset is a
// measurement rather than a guess.
//
// The disclosure is asserted through the ROUTE rather than by calling the
// function that writes it: a disclosure is tested by the consumer that receives
// it. Two halves, and both are needed -- the request must still SUCCEED (a
// legacy row still serves, which is the entire point of the exemption) AND the
// line must be emitted (an exemption nobody can observe is one nobody can
// retire).

// completenessRows is the join both arrays must satisfy: one published plan
// requirement and the outcome rows accounting for it.
//
// Built as a PAIR rather than as an outcome row alone, because the document
// validator's requirement join is total in both directions -- an outcome naming
// a requirement the plan does not describe is as invalid as the reverse, and a
// fixture that carried only one side would exercise a shape no producer emits.
func completenessRows(evaluated bool) (contractsv1.ContextFabricAnswerPlan, []contractsv1.ContextFabricPlanRequirementOutcomeRow) {
	const identity, obligation = "state/subject/team", "state"
	plan := contractsv1.ContextFabricAnswerPlan{
		Requirements: []contractsv1.ContextFabricPlanRequirement{{
			Requirement: identity,
			Obligation:  obligation,
			Role:        "subject",
			Subject:     contractsv1.ContextFabricSubjectTeam,
			Kind:        "read",
			FactKinds:   []contractsv1.ContextFabricFactKind{contractsv1.ContextFabricFactHealth},
			Scope:       "single_subject",
			Quantifier:  "at_least_one",
		}},
	}
	rows := []contractsv1.ContextFabricPlanRequirementOutcomeRow{{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: identity,
		Obligation:  obligation,
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}}
	if evaluated {
		answered := rows[0]
		answered.Stage = contractsv1.ContextFabricOutcomeStageAssembledResult
		rows = append(rows, answered)
	}
	return plan, rows
}

// TestTheReadRouteDisclosesALegacyCompletenessState is the PAIR, plus a silent
// control.
func TestTheReadRouteDisclosesALegacyCompletenessState(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		evaluated      bool
		wantDisclosure bool
	}{
		{
			// PRE-AMENDMENT: the read requirement carries only its planning
			// seed, so the old rule derived `complete` and the amended rule
			// derives `partial`. This is the one shape the exemption admits.
			name: "a document written before the read evaluator", evaluated: false, wantDisclosure: true,
		},
		{
			// THE SILENT CONTROL, byte-identical but for the evaluated row.
			// Without it the assertion above would pass on a route that
			// logged unconditionally.
			name: "a document the amended rule still derives", evaluated: true, wantDisclosure: false,
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			result := validContextFabricInvestigationResult()
			result.ResultID = "result_legacy_state01"
			plan, rows := completenessRows(testCase.evaluated)
			result.AnswerPlan = &plan
			result.Completeness.Outcomes = rows
			// The state a PRE-AMENDMENT binary would have stamped, computed
			// with the frozen predicate rather than typed in, so the fixture
			// cannot drift from the rule it is standing in for.
			result.Completeness.State = contractsv1.DeriveContextFabricAnswerCompletenessStateBeforeReadEvaluation(rows)

			// The premise, asserted rather than assumed: this fixture only
			// exercises the arm if the two rules actually disagree on it.
			amended := contractsv1.DeriveContextFabricAnswerCompletenessState(rows)
			if disagrees := amended != result.Completeness.State; disagrees != testCase.wantDisclosure {
				t.Fatalf("fixture premise moved: frozen=%q amended=%q, so disagreement=%v, want %v",
					result.Completeness.State, amended, disagrees, testCase.wantDisclosure)
			}

			// THE PACKAGE'S OWN legacyResultStore, not a second one beside
			// it. It already exists in answer_surface_parity_test.go for the
			// same reason this test needs one -- it serves a stored document
			// WITHOUT re-validating it on the way out -- and a duplicate type
			// would have been a second fixture drifting from the first.
			//
			// A stub rather than the real store, and that is a finding rather
			// than a shortcut: the legacy shape CANNOT be produced by this
			// binary at all, because memoryinvestigation.Save runs the
			// fresh-path validator and that validator refuses a state the
			// amended rule does not derive. Which is correct -- a result
			// produced today has no older-deployment excuse -- and it means
			// the only faithful way to present a pre-amendment row to the
			// route is to hand it one, exactly as a database row written
			// months ago arrives.
			app, token, logs := newContextFabricTestAppWithResultsAndLogs(t, nil, legacyResultStore{result: result})
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, result.ResultID))

			// HALF ONE: the row still SERVES. An exemption that kept the
			// document readable in the validator but broke the route would
			// have missed the entire point.
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 -- a legacy document must still serve (body %s)",
					recorder.Code, recorder.Body.String())
			}

			// HALF TWO: the disclosure, read back from the real log sink.
			const line = "context fabric legacy completeness state admitted"
			got := strings.Contains(logs.String(), line)
			if got != testCase.wantDisclosure {
				t.Fatalf("legacy disclosure emitted = %v, want %v (logs: %s)",
					got, testCase.wantDisclosure, logs.String())
			}
			if !testCase.wantDisclosure {
				return
			}
			// The line must carry the state it is disclosing. A disclosure
			// that says something happened without saying what cannot drive
			// the sunset decision it exists for.
			if !strings.Contains(logs.String(), string(result.Completeness.State)) {
				t.Fatalf("the disclosure does not name the stored state %q: %s",
					result.Completeness.State, logs.String())
			}
		})
	}
}
