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

// TestSameQuestionContainment_AReceiptRootedHitWinsOverADriftedParent exists
// because the mutation battery found the property UNPINNED.
//
// Deleting the early return that makes a receipt-rooted hit win outright left
// every test green. The reason is that no fixture in the suite linked a
// request by BOTH a redeemed receipt and a parent_result_id -- the
// `CarrySeedBoth` seed-source member had a name, a doc comment and a
// telemetry label, and no behavioural coverage at all. Every existing arm used
// one linkage or the other, and each of those passes under the mutation:
// with the early return gone, a receipts-only request still returns its hit
// (the parent branch is skipped), and a parent-only request has no receipt hit
// to protect.
//
// THE HARM the deletion admits, which is what this arm now catches: with both
// linkages present, the receipt's hit falls through into the parent walk, the
// parent walk hits, and the CHOKE POINT is applied to it -- so a caller who
// legitimately redeemed an offer AND named an unrelated parent gets their
// receipt-rooted carry refused for the parent's drift. That inverts the
// two-tier rule: the gate exists to constrain the weaker linkage, and here it
// would punish the stronger one.
//
// This is the "mixed fixture" class this codebase keeps rediscovering: a
// fixture whose identifiers are all deliberately distinct hides every
// interaction defect. A carry fixture needs a both-linkages case.
func TestSameQuestionContainment_AReceiptRootedHitWinsOverADriftedParent(t *testing.T) {
	t.Parallel()

	for _, axis := range containmentAxes() {
		t.Run(axis.name, func(t *testing.T) {
			t.Parallel()

			request := validInvestigationRequest()
			// The receipt names a SAME-QUESTION prior that carries the value.
			viaReceipt := axis.carriable("result_via_receipt", request.Question)
			// The parent names a DIFFERENT prior that also carries a value and
			// answers a different question. Both hold a carriable value, so
			// the walks cannot be told apart by "one of them found nothing".
			viaParent := axis.carriable("result_via_parent", driftQuestion)

			store := &staticResultStore{results: map[string]InvestigationResult{
				viaReceipt.ResultID: viaReceipt, viaParent.ResultID: viaParent,
			}}
			request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: viaReceipt.ResultID, ReceiptID: "candr_containment02"}}
			request.ParentResultID = viaParent.ResultID

			// SEED-SOURCE GUARD: this arm is only about the BOTH case, and a
			// fixture that silently stopped carrying one of the two linkages
			// would degenerate into an arm that already exists.
			if got := carrySeedSource(request, nil); got != CarrySeedBoth {
				t.Fatalf("carrySeedSource = %q, want %q -- this arm exists to cover the both-linkages case and cannot do so on a request linked only one way", got, CarrySeedBoth)
			}

			got := axis.resolve(t, buildCarryTestEngine(t, store), request)
			if got.drift {
				t.Fatalf("%s: a request carrying a valid receipt was refused for the PARENT's drift -- the gate exists to constrain the weaker linkage, and refusing the stronger one inverts the two-tier rule", axis.name)
			}
			if !got.hit {
				t.Errorf("%s carry outcome = %q, want a hit: the receipt-rooted walk found a same-question carrier and must win outright, without the parent chain being consulted at all", axis.name, got.outcome)
			}
		})
	}
}

// TestSameQuestionContainment_ComparesTheOriginNotTheHopItArrivedThrough is
// the arm that pins the DESIGN CLAIM, and it is the only one that separates
// this shape from a comparison against the parent the caller named.
//
// THE CHAIN. Turn A answers question Q and confirms the value. Turn B answers
// a DIFFERENT question Q' and legitimately inherits A's value by redeeming a
// receipt -- legitimate because receipt-rooted carries are ungated by design,
// which is what the arm below protects. Turn B therefore holds the value AND
// answers Q'. A caller now asks Q' again and names B through parent_result_id.
//
// Comparing the NAMED PARENT's question would pass: B answered Q'. Comparing
// the ORIGIN's question refuses: the value was confirmed against Q, and it is
// the value's provenance -- not the last turn to touch it -- that decides
// whether inheriting it continues this conversation or borrows another one.
//
// This is the difference between a gate that can be walked around by adding
// one legitimate hop and one that cannot. Deleting the carriedWindowOrigin /
// carriedKindOrigin resolution from the walk turns SourceResultID back into
// the hop, and this arm is what goes red.
func TestSameQuestionContainment_ComparesTheOriginNotTheHopItArrivedThrough(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// build returns turn B: a result answering the REQUEST's question that
		// holds a value whose declared origin is turn A (which answered a
		// different one).
		build   func(originID, hopID, hopQuestion string) InvestigationResult
		resolve func(engine *Engine, request InvestigationRequest) containmentOutcome
	}{
		{
			name: "window",
			build: func(originID, hopID, hopQuestion string) InvestigationResult {
				hop := carriableWindowResult(hopID, hopQuestion)
				hop.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
					Member:        contractsv1.ContextFabricStructureNeedWindow,
					AppliedValue:  string(RelativeWindowTrailing90D),
					Source:        contractsv1.ContextFabricStructureSourceCarried,
					PriorResultID: originID,
					Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
					Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
				}}
				return hop
			},
			resolve: func(engine *Engine, request InvestigationRequest) containmentOutcome {
				got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
				return containmentOutcome{hit: got.Outcome == WindowCarryHit, drift: got.Outcome == WindowCarryMissQuestionDrift, outcome: string(got.Outcome)}
			},
		},
		{
			name: "kind",
			build: func(originID, hopID, hopQuestion string) InvestigationResult {
				hop := validInvestigationResult()
				hop.ResultID = hopID
				hop.Question = hopQuestion
				hop.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
					Member:        contractsv1.ContextFabricStructureNeedExpectedKind,
					AppliedValue:  string(contractsv1.ContextFabricSubjectTeam),
					Source:        contractsv1.ContextFabricStructureSourceCarried,
					PriorResultID: originID,
					Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
					Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
				}}
				return hop
			},
			resolve: func(engine *Engine, request InvestigationRequest) containmentOutcome {
				got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
				return containmentOutcome{hit: got.Outcome == KindCarryHit, drift: got.Outcome == KindCarryMissQuestionDrift, outcome: string(got.Outcome)}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := validInvestigationRequest()
			// Turn A: the ORIGIN. Answered a different question.
			origin := emptyResult("result_origin_drifted", driftQuestion)
			// Turn B: the HOP. Answers THIS question, holds the value, and
			// declares turn A as where the value came from.
			hop := tc.build(origin.ResultID, "result_hop_same_question", request.Question)
			if hop.Question != request.Question {
				t.Fatalf("the hop must answer the REQUEST's question, or this arm degenerates into the ordinary drift case and proves nothing about the origin")
			}

			store := &staticResultStore{results: map[string]InvestigationResult{
				origin.ResultID: origin, hop.ResultID: hop,
			}}
			request.ParentResultID = hop.ResultID

			got := tc.resolve(buildCarryTestEngine(t, store), request)
			if got.hit {
				t.Fatalf("%s carry HIT: the named parent answers this question, but the value it holds was confirmed against a different one -- comparing the hop rather than the origin lets one legitimate intermediate turn launder any value in the org", tc.name)
			}
			if !got.drift {
				t.Errorf("%s carry outcome = %q, want the question-drift refusal: the walk must reach the value and refuse it on its ORIGIN's question, not fail to find it", tc.name, got.outcome)
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

// TestSameQuestionContainment_RefusesAQuestionWithNoIdentity is codex round 1's
// HIGH finding, and it is the one that would have shipped.
//
// CanonicalizeQuestion strips trailing terminal punctuation, so "?", "!!" and
// "..." all reduce to the empty string and therefore share ONE QuestionHash
// (sha256 of ""). Equal hashes are not the same question here -- they are
// questions the hash cannot tell apart. Before the guard, a parent answering
// "?" carried a confirmed axis into a request asking "!!" on ALL THREE axes;
// that was executed, not argued, before the fix went in.
//
// WHY IT MATTERS MORE THAN ITS SIZE. The answer-reuse path already fails
// closed on exactly this collision and has since its own round-2 review. The
// class was known and fixed one seam over, and this seam did not mirror it --
// so the sweep that enumerated every carry PRODUCER was the wrong axis of
// enumeration for this defect: every producer did reach the choke point, and
// the hole was in what the choke point ACCEPTS.
//
// The refusal is reported as its own basis. Folding it into drift would claim
// the two questions differ, which is precisely what cannot be established.
func TestSameQuestionContainment_RefusesAQuestionWithNoIdentity(t *testing.T) {
	t.Parallel()

	for _, axis := range containmentAxes() {
		t.Run(axis.name, func(t *testing.T) {
			t.Parallel()

			request := validInvestigationRequest()
			request.Question = "!!"
			prior := axis.carriable("result_punctuation_only", "?")

			// PREMISE GUARD. This test is only meaningful while the two
			// questions genuinely collide; if canonicalization is ever
			// narrowed so they do not, the arm must fail loudly rather than
			// pass by testing nothing.
			if QuestionHash(prior.Question) != QuestionHash(request.Question) {
				t.Fatalf("premise gone: %q and %q no longer share a hash, so this arm cannot exercise the collision", prior.Question, request.Question)
			}
			if CanonicalizeQuestion(request.Question) != "" {
				t.Fatalf("premise gone: %q no longer canonicalizes to the empty string", request.Question)
			}

			store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
			request.ParentResultID = prior.ResultID

			got := axis.resolve(t, buildCarryTestEngine(t, store), request)
			if got.hit {
				t.Fatalf("%s carry HIT: a result answering %q was inherited by a request asking %q -- two unrelated questions that share a hash only because both canonicalize to the empty string", axis.name, prior.Question, request.Question)
			}
			if got.drift {
				t.Errorf("%s reported question DRIFT: nothing was shown to differ, so claiming drift puts a false basis in the telemetry; the honest basis is that the question has no identity to compare", axis.name)
			}
			if got.outcome != "miss_question_indeterminate" {
				t.Errorf("%s carry outcome = %q, want %q -- the refusal must name its real basis", axis.name, got.outcome, "miss_question_indeterminate")
			}
		})
	}
}

// TestSameQuestionContainment_RefusesAHitWhoseOriginCannotBeRead is codex round
// 1's MEDIUM #2: the `unverifiable` state was reachable but pinned by nothing.
//
// Deleting its branch left the ENTIRE package green (measured: 36 s full run),
// because every existing arm stores both the hop and the origin. A missing
// origin then falls through to compare an empty stored question and reports
// question DRIFT -- a claim about what the origin said, made about an origin
// that was never read. That is exactly the mislabelling the three-state
// verdict exists to prevent, so the state needed a test, not just a constant.
func TestSameQuestionContainment_RefusesAHitWhoseOriginCannotBeRead(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		build func(originID, hopID, hopQuestion string) InvestigationResult
		want  string
	}{
		{name: "window", build: containmentWindowHopDeclaringOrigin, want: "miss_unloadable"},
		{name: "kind", build: containmentKindHopDeclaringOrigin, want: "miss_unloadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := validInvestigationRequest()
			// The hop is loadable, answers THIS question, and holds a
			// carriable value whose declared origin is ABSENT from the store.
			hop := tc.build("result_origin_absent", "result_hop_present", request.Question)
			store := &staticResultStore{results: map[string]InvestigationResult{hop.ResultID: hop}}
			request.ParentResultID = hop.ResultID

			var got containmentOutcome
			for _, axis := range containmentAxes() {
				if axis.name == tc.name {
					got = axis.resolve(t, buildCarryTestEngine(t, store), request)
				}
			}
			if got.hit {
				t.Fatalf("%s carry HIT on a value whose origin could not be read: an unreadable origin is NOT PROVEN, never proceed", tc.name)
			}
			if got.drift {
				t.Errorf("%s reported question DRIFT for an origin that was never read -- a predicate that could not be evaluated must not make a claim about what the origin said", tc.name)
			}
			if got.outcome != tc.want {
				t.Errorf("%s carry outcome = %q, want %q", tc.name, got.outcome, tc.want)
			}
		})
	}
}

// containmentWindowHopDeclaringOrigin / containmentKindHopDeclaringOrigin build
// a loadable hop whose carried value names an origin id the store does not
// hold, which is the only way to reach the unverifiable branch.
func containmentWindowHopDeclaringOrigin(originID, hopID, hopQuestion string) InvestigationResult {
	hop := carriableWindowResult(hopID, hopQuestion)
	hop.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
		Member:        contractsv1.ContextFabricStructureNeedWindow,
		AppliedValue:  string(RelativeWindowTrailing90D),
		Source:        contractsv1.ContextFabricStructureSourceCarried,
		PriorResultID: originID,
		Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
	}}
	return hop
}

func containmentKindHopDeclaringOrigin(originID, hopID, hopQuestion string) InvestigationResult {
	hop := validInvestigationResult()
	hop.ResultID = hopID
	hop.Question = hopQuestion
	hop.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
		Member:        contractsv1.ContextFabricStructureNeedExpectedKind,
		AppliedValue:  string(contractsv1.ContextFabricSubjectTeam),
		Source:        contractsv1.ContextFabricStructureSourceCarried,
		PriorResultID: originID,
		Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
	}}
	return hop
}
