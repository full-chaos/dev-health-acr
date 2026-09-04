package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// SAME-QUESTION CONTAINMENT, PER AXIS.
//
// The AST closure test (carry_gate_closure_test.go) proves every carry hit is
// CONSTRUCTED inside a gated producer. That is a structural property and it is
// the one that survives a fourth axis being added. It says nothing about
// whether the gate, once reached, decides correctly.
//
// This file is the other half: the same four arms run against each of the
// three producers, driving the real method on a real store.
//
//	drift, one hop   -- the parent answered a different question.      MISS.
//	drift, two hops  -- the r3 laundering shape: the parent answers    MISS.
//	                    THIS question, carries nothing, and its stored
//	                    ancestry reaches a result that answered a
//	                    different one.
//	control, two hops -- same question all the way down.               HIT at depth 1.
//	control, receipt  -- drifted, but linked by a redeemed receipt.    HIT (ungated).
//
// WHY THE SECOND ARM IS THE IMPORTANT ONE. It is the exact escape codex found
// in round 3, and under the old per-edge shape it was refused by a rule that
// had to be re-stated on the ancestry edge. Here nothing is stated on any
// edge: the producer compares the hit's ORIGIN, which carriedWindowOrigin /
// carriedKindOrigin have already resolved through the carried hops, so depth
// is irrelevant by construction. If this arm ever needs a per-edge rule again,
// the design has regressed.
//
// WHY THE FOURTH ARM IS NOT OPTIONAL. A containment that also refuses
// legitimate receipt-rooted carries would pass every arm above while breaking
// the clarification loop the whole carry mechanism exists to serve. The
// two-tier rule is a rule about BOTH answers.

// driftQuestion is a question no fixture in this file uses as the request's
// own, so a chain rooted at a result carrying it is genuinely drifted.
const driftQuestion = "how many pull requests did the platform team merge last quarter?"

// carriableKindResult builds a prior turn that confirmed expected_kind=team.
func carriableKindResult(resultID, question string) InvestigationResult {
	prior := validInvestigationResult()
	prior.ResultID = resultID
	prior.Question = question
	prior.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, resultID+"_origin", "kindr_containment01"),
	}
	return prior
}

// carriableWindowResult builds a prior turn that confirmed a 90-day window.
func carriableWindowResult(resultID, question string) InvestigationResult {
	prior := validInvestigationResult()
	prior.ResultID = resultID
	prior.Question = question
	prior.ConfirmedStructure = nil
	prior.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed,
	}
	return prior
}

// carriablePlanResult builds a prior turn that classified a family.
func carriablePlanResult(resultID, question string) InvestigationResult {
	prior := validInvestigationResult()
	prior.ResultID = resultID
	prior.Question = question
	prior.ConfirmedStructure = nil
	prior.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:    QuestionFamilyGroupedCohortStatus,
		GroupKind: contractsv1.ContextFabricSubjectTeam,
	}
	return prior
}

// emptyResult is a turn that reached a terminal but confirmed and classified
// NOTHING -- the hole in a chain that durable ancestry exists to bridge, and
// the middle hop of every two-hop arm below.
func emptyResult(resultID, question string) InvestigationResult {
	turn := validInvestigationResult()
	turn.ResultID = resultID
	turn.Question = question
	turn.ConfirmedStructure = nil
	turn.EffectiveEvidenceWindow = nil
	turn.AnswerPlan = nil
	return turn
}

// containmentAxis is one carry producer reduced to the three things these
// arms need: how to build a carriable prior, how to invoke the producer, and
// how to read the two outcomes off whatever result type it returns.
type containmentAxis struct {
	name string
	// carriable builds a prior turn that this axis can carry FROM.
	carriable func(resultID, question string) InvestigationResult
	// resolve invokes the producer and normalises its answer.
	resolve func(t *testing.T, engine *Engine, request InvestigationRequest) containmentOutcome
}

// containmentOutcome is the axis-independent view of a carry result: was it a
// hit, was the refusal specifically question drift, and how deep did it walk.
// Normalising here rather than in each arm is what lets one table cover three
// separate outcome vocabularies without a type switch in every assertion.
type containmentOutcome struct {
	hit        bool
	drift      bool
	outcome    string
	chainDepth int
}

func containmentAxes() []containmentAxis {
	return []containmentAxis{
		{
			name:      "window",
			carriable: carriableWindowResult,
			resolve: func(_ *testing.T, engine *Engine, request InvestigationRequest) containmentOutcome {
				got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
				return containmentOutcome{
					hit:        got.Outcome == WindowCarryHit,
					drift:      got.Outcome == WindowCarryMissQuestionDrift,
					outcome:    string(got.Outcome),
					chainDepth: got.ChainDepth,
				}
			},
		},
		{
			name:      "kind",
			carriable: carriableKindResult,
			resolve: func(_ *testing.T, engine *Engine, request InvestigationRequest) containmentOutcome {
				got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
				return containmentOutcome{
					hit:        got.Outcome == KindCarryHit,
					drift:      got.Outcome == KindCarryMissQuestionDrift,
					outcome:    string(got.Outcome),
					chainDepth: got.ChainDepth,
				}
			},
		},
		{
			name:      "plan",
			carriable: carriablePlanResult,
			resolve: func(_ *testing.T, engine *Engine, request InvestigationRequest) containmentOutcome {
				got := engine.resolveCarriedPlan(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0}, nil)
				return containmentOutcome{
					hit:     got.Outcome == PlanCarryHit,
					drift:   got.Outcome == PlanCarryMissQuestionDrift,
					outcome: string(got.Outcome),
					// The plan axis is one hop by design, so it publishes no
					// chain depth. Reported as 0 and never asserted for this
					// axis -- see the two-hop control's own guard.
				}
			},
		},
	}
}

// TestSameQuestionContainment_RefusesADriftedParentOnEveryAxis is arm 1: a
// caller who names a result that answered a different question inherits
// nothing from it, and the refusal says WHY.
//
// The reason matters as much as the refusal. A carry that missed for an
// unrelated reason (unloadable, stale epoch, nothing to carry) would satisfy a
// bare "no hit" assertion while proving nothing about containment -- so the
// arm asserts the specific drift outcome, not merely the absence of a hit.
func TestSameQuestionContainment_RefusesADriftedParentOnEveryAxis(t *testing.T) {
	t.Parallel()

	for _, axis := range containmentAxes() {
		t.Run(axis.name, func(t *testing.T) {
			t.Parallel()

			request := validInvestigationRequest()
			prior := axis.carriable("result_drifted_parent", driftQuestion)
			if prior.Question == request.Question {
				t.Fatalf("the fixture's prior question equals the request's, so this arm cannot exercise drift at all")
			}

			store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
			request.ParentResultID = prior.ResultID

			got := axis.resolve(t, buildCarryTestEngine(t, store), request)
			if got.hit {
				t.Fatalf("%s carry HIT: an unrelated investigation's confirmed value was inherited by a turn that merely named its id", axis.name)
			}
			if !got.drift {
				t.Errorf("%s carry outcome = %q, want the question-drift refusal: missing for a vaguer reason hides WHY the carry was refused and would pass on an unrelated failure", axis.name, got.outcome)
			}
		})
	}
}

// TestSameQuestionContainment_RefusesDriftReachedThroughAnAncestryEdge is arm
// 2, and it is the round-3 escape reproduced exactly.
//
// Turn B answers THIS question and confirms nothing, so it is a legitimate
// parent and the walk must continue through it. Its stored ancestry reaches
// turn A, which answered a different question and does hold a carriable value.
// Under a per-edge containment this is refused only if someone remembered to
// re-state the rule on the ancestry edge; three rounds proved that someone
// does not always remember.
//
// Here the producer compares the ORIGIN of whatever the walk returns, so the
// number of hops between the named parent and the drifted value is irrelevant.
// The arm asserts the drift outcome specifically, so a walk that simply failed
// to traverse cannot pass it -- the two-hop control below is what proves the
// traversal is alive.
func TestSameQuestionContainment_RefusesDriftReachedThroughAnAncestryEdge(t *testing.T) {
	t.Parallel()

	for _, axis := range containmentAxes() {
		if axis.name == "plan" {
			// The plan axis walks ONE hop by deliberate design (a family is a
			// reading of the question just asked, not a commitment inherited
			// through many turns -- see chaos4636_plan_carry.go's header), so
			// there is no second hop for a value to arrive through. Skipping
			// with the reason stated, rather than silently omitting the axis
			// from the table, so the asymmetry is visible to the next reader.
			t.Run(axis.name+"_not_applicable_one_hop_by_design", func(t *testing.T) {
				t.Skip("the plan axis walks one hop by design; there is no ancestry edge for a value to arrive through")
			})
			continue
		}
		t.Run(axis.name, func(t *testing.T) {
			t.Parallel()

			request := validInvestigationRequest()
			drifted := axis.carriable("result_turn_a_drifted", driftQuestion)
			bridge := emptyResult("result_turn_b_bridge", request.Question)

			store := &ancestryLinkedStore{
				staticResultStore: &staticResultStore{results: map[string]InvestigationResult{
					drifted.ResultID: drifted, bridge.ResultID: bridge,
				}},
				parents: map[string]string{bridge.ResultID: drifted.ResultID},
			}
			// The named parent's own question MATCHES, so the refusal cannot
			// come from a check on the named id -- it has to come from the
			// origin comparison one hop deeper.
			request.ParentResultID = bridge.ResultID

			got := axis.resolve(t, buildCarryTestEngine(t, store), request)
			if got.hit {
				t.Fatalf("%s carry HIT at depth %d: the value came from a result answering a different question, reached through an ancestry edge -- gating only the named parent is a speed bump, not a barrier", axis.name, got.chainDepth)
			}
			if !got.drift {
				t.Errorf("%s carry outcome = %q, want the question-drift refusal: the walk must reach the drifted origin and REFUSE it, not fail to reach it", axis.name, got.outcome)
			}
		})
	}
}

// TestSameQuestionContainment_ASameQuestionChainStillWalksTwoHops is the
// CONTROL for the two arms above, and the reason it exists is that every one
// of them can be satisfied by a carry mechanism that simply stopped working.
//
// Same shape as the laundering arm, one field changed: turn A answers THIS
// question. The carry must hit, and it must hit at depth 1 -- asserting the
// depth rather than merely the hit is what distinguishes "the walk crossed the
// ancestry edge" from "the walk found something at depth 0 for an unrelated
// reason".
func TestSameQuestionContainment_ASameQuestionChainStillWalksTwoHops(t *testing.T) {
	t.Parallel()

	for _, axis := range containmentAxes() {
		if axis.name == "plan" {
			t.Run(axis.name+"_not_applicable_one_hop_by_design", func(t *testing.T) {
				t.Skip("the plan axis walks one hop by design; its depth-0 control is the same-question arm below")
			})
			continue
		}
		t.Run(axis.name, func(t *testing.T) {
			t.Parallel()

			request := validInvestigationRequest()
			origin := axis.carriable("result_turn_a_same", request.Question)
			bridge := emptyResult("result_turn_b_bridge", request.Question)

			store := &ancestryLinkedStore{
				staticResultStore: &staticResultStore{results: map[string]InvestigationResult{
					origin.ResultID: origin, bridge.ResultID: bridge,
				}},
				parents: map[string]string{bridge.ResultID: origin.ResultID},
			}
			request.ParentResultID = bridge.ResultID

			got := axis.resolve(t, buildCarryTestEngine(t, store), request)
			if !got.hit {
				t.Fatalf("%s carry outcome = %q, want a hit: the remedy must not break the ordinary clarification loop, which is the case the whole mechanism exists for", axis.name, got.outcome)
			}
			if got.chainDepth != 1 {
				t.Errorf("%s carry ChainDepth = %d, want 1: a hit at depth 0 would mean the value was found on the named parent itself, so this arm would not be proving the ancestry edge was crossed", axis.name, got.chainDepth)
			}
		})
	}
}

// TestSameQuestionContainment_ARedeemedReceiptIsNeverGated is the
// BEHAVIOUR-PRESERVING control, and it is the half of the two-tier rule that a
// containment change is most likely to break silently.
//
// Same drift as arm 1, linked by a redeemed receipt instead of the field. A
// receipt is an ACCEPTANCE of an offer the server chose to show this caller;
// accepting one and then asking a genuinely different follow-up ("what about
// last quarter?") is legitimate and has worked since the carry shipped.
// Gating it to harden the bearer path would break a working conversation to
// fix a different mechanism.
func TestSameQuestionContainment_ARedeemedReceiptIsNeverGated(t *testing.T) {
	t.Parallel()

	for _, axis := range containmentAxes() {
		t.Run(axis.name, func(t *testing.T) {
			t.Parallel()

			request := validInvestigationRequest()
			prior := axis.carriable("result_drifted_receipt", driftQuestion)
			store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}

			// Linked ONLY by the receipt. No ParentResultID, so a hit here
			// cannot be attributed to the ungated field path.
			request.ParentResultID = ""
			request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "candr_containment01"}}

			got := axis.resolve(t, buildCarryTestEngine(t, store), request)
			if got.drift {
				t.Fatalf("%s: the drift gate fired on a RECEIPT-linked chain -- redeeming an offer and then asking a different follow-up is legitimate today and must keep working", axis.name)
			}
			if !got.hit {
				t.Errorf("%s carry outcome = %q, want a hit: a receipt-linked chain is ungated by design, so drift on the prior result must not affect it", axis.name, got.outcome)
			}
		})
	}
}
