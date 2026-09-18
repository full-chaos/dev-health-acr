package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Engine cells for three parts of the subject-substitution guard's domain:
// a clarification the guard issued, answered by a caller that names either
// the parent or the clarification; the prior-subject receipt domain at the
// real producer; and the shadow binder on a guarded turn, whatever the
// parent's snapshot holds.

// guardIssuedClarification runs the parent turn (repository one) and a
// follow-up that commits repository two, which the guard answers with its
// own clarification. It returns both results and the clarification's own
// receipt for repository two.
func guardIssuedClarification(t *testing.T, h *needTurnHarness, prefix string) (parent, clarification needTurnOutcome, chosen string) {
	t.Helper()
	parent, clarification = substitutionTurns(t, h, prefix,
		substitutionResponse(substitutionRepoOne, prefix+"_receipt_one"),
		substitutionResponse(substitutionRepoTwo, prefix+"_receipt_two"), nil)
	if clarification.result.Status != InvestigationClarificationRequired {
		t.Fatalf("premise: status = %q, want the guard's own clarification", clarification.result.Status)
	}
	for _, candidate := range clarification.result.SubjectResolution.Candidates {
		if sameSubjectIdentity(candidate.Subject, substitutionRepoTwo) {
			chosen = candidate.ReceiptID
		}
	}
	if chosen == "" {
		t.Fatalf("premise: the clarification offers no receipt for %q", substitutionRepoTwo.CanonicalID)
	}
	recordAncestry(h, clarification.result.ResultID, parent.result.ResultID)
	return parent, clarification, chosen
}

// recordAncestry makes the store report parent as child's recorded parent,
// as a real store does for a turn that named it.
func recordAncestry(h *needTurnHarness, child, parent string) {
	if h.store.parents == nil {
		h.store.parents = map[string]string{}
	}
	h.store.parents[child] = parent
}

// answerTurn is a follow-up naming named, redeeming receipts, whose own
// resolution commits response.
func answerTurn(h *needTurnHarness, requestID, named string, receipts []BoundSubjectReceipt, response needTurnResponse) needTurnOutcome {
	request := continuingNeedTurn(needTurnRequest(requestID, true), named)
	request.Question = "And how does the second one compare over the same period?"
	request.PriorSubjectReceipts = receipts
	return h.turn(request, response)
}

// assertServed is a turn that served exactly subject.
func assertServed(t *testing.T, result InvestigationResult, subject SubjectRef) {
	t.Helper()
	if result.Status == InvestigationClarificationRequired || result.Status == InvestigationNoMatch {
		t.Fatalf("status = %q, want %q served", result.Status, subject.CanonicalID)
	}
	if !committedIsExactly(result.SubjectResolution.Committed, subject) {
		t.Fatalf("committed = %+v, want exactly %q", result.SubjectResolution.Committed, subject.CanonicalID)
	}
}

// TestSubstitutionGuardServesAnAnswerToItsOwnClarification: the caller
// answers the guard's clarification by redeeming the clarification's own
// receipt. Whichever result the caller names -- the parent it was reading
// or the clarification it is answering -- the choice is served, and the
// line names the receipt, the result that issued it and the parent it
// speaks for.
func TestSubstitutionGuardServesAnAnswerToItsOwnClarification(t *testing.T) {
	t.Parallel()
	for _, names := range []string{"parent", "clarification"} {
		names := names
		t.Run("names_"+names, func(t *testing.T) {
			t.Parallel()
			h := newNeedTurnHarness(t, nil)
			parent, clarification, chosen := guardIssuedClarification(t, h, "request_5917_answer_"+names)
			named := parent.result.ResultID
			if names == "clarification" {
				named = clarification.result.ResultID
			}
			outcome := answerTurn(h, "request_5917_answer_"+names+"_three", named,
				[]BoundSubjectReceipt{{ResultID: clarification.result.ResultID, ReceiptID: chosen}},
				substitutionResponse(substitutionRepoTwo, "request_5917_answer_"+names+"_served"))
			assertServed(t, outcome.result, substitutionRepoTwo)
			event := lastSubstitution(t, outcome)
			assertGuard(t, event, SubjectSubstitutionRedeemedChoice, SubjectSubstitutionOriginPriorReceipt, substitutionRepoOne, substitutionRepoTwo)
			if event.SubstitutionOriginResultID != clarification.result.ResultID || event.SubstitutionOriginReceiptID != chosen {
				t.Errorf("origin receipt = (%q,%q), want the clarification's (%q,%q)", event.SubstitutionOriginResultID, event.SubstitutionOriginReceiptID, clarification.result.ResultID, chosen)
			}
			if event.SubstitutionOriginIssuedFor != parent.result.ResultID {
				t.Errorf("substitution_origin_issued_for = %q, want the parent %q", event.SubstitutionOriginIssuedFor, parent.result.ResultID)
			}
			if event.SubstitutionParentResultID != parent.result.ResultID {
				t.Errorf("substitution_parent_result_id = %q, want the parent %q", event.SubstitutionParentResultID, parent.result.ResultID)
			}
		})
	}
}

// TestSubstitutionGuardServesTheRememberedSubjectPickedFromItsClarification:
// picking the remembered subject the clarification lists first, while
// naming the clarification, is the parent's own subject.
func TestSubstitutionGuardServesTheRememberedSubjectPickedFromItsClarification(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	parent, clarification, _ := guardIssuedClarification(t, h, "request_5917_pick_remembered")
	remembered := clarification.result.SubjectResolution.Candidates[0]
	outcome := answerTurn(h, "request_5917_pick_remembered_three", clarification.result.ResultID,
		[]BoundSubjectReceipt{{ResultID: clarification.result.ResultID, ReceiptID: remembered.ReceiptID}},
		substitutionResponse(substitutionRepoOne, "request_5917_pick_remembered_served"))
	assertServed(t, outcome.result, substitutionRepoOne)
	event := lastSubstitution(t, outcome)
	if event.SubstitutionGuard != SubjectSubstitutionSameSubject || event.SubstitutionParentResultID != parent.result.ResultID {
		t.Errorf("guard = %q parent_result_id = %q, want same_subject against %q", event.SubstitutionGuard, event.SubstitutionParentResultID, parent.result.ResultID)
	}
}

// TestSubstitutionGuardComparesATurnNamingItsClarificationAgainstTheParent:
// a clarification the guard issued is never read as a parent that asserted
// no identity. A turn naming it that commits another subject with no choice
// behind it is compared against the parent's subject, and clarifies.
func TestSubstitutionGuardComparesATurnNamingItsClarificationAgainstTheParent(t *testing.T) {
	t.Parallel()
	third := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:gamma-service", Label: "gamma-service"}
	for _, committed := range []SubjectRef{substitutionRepoTwo, third} {
		committed := committed
		t.Run(committed.Label, func(t *testing.T) {
			t.Parallel()
			h := newNeedTurnHarness(t, nil)
			parent, clarification, _ := guardIssuedClarification(t, h, "request_5917_names_c_"+committed.Label[:4])
			outcome := answerTurn(h, "request_5917_names_c_"+committed.Label[:4]+"_three", clarification.result.ResultID, nil,
				substitutionResponse(committed, "request_5917_names_c_served"))
			if outcome.result.Status != InvestigationClarificationRequired {
				t.Fatalf("status = %q, want %q", outcome.result.Status, InvestigationClarificationRequired)
			}
			assertServedNothing(t, outcome.result)
			event := lastSubstitution(t, outcome)
			assertGuard(t, event, SubjectSubstitutionClarified, SubjectSubstitutionOriginResolver, substitutionRepoOne, committed)
			if event.SubstitutionParentResultID != parent.result.ResultID {
				t.Errorf("substitution_parent_result_id = %q, want the parent %q", event.SubstitutionParentResultID, parent.result.ResultID)
			}
		})
	}
}

// TestSubstitutionGuardClarifiesAChoiceRedeemedFromAnotherResultAfterItsClarification:
// a receipt some OTHER result issued is not the parent's offer, whichever of
// the parent and its clarification the caller names.
func TestSubstitutionGuardClarifiesAChoiceRedeemedFromAnotherResultAfterItsClarification(t *testing.T) {
	t.Parallel()
	for _, names := range []string{"parent", "clarification"} {
		names := names
		t.Run("names_"+names, func(t *testing.T) {
			t.Parallel()
			h := newNeedTurnHarness(t, nil)
			stranger := h.turn(needTurnRequest("request_5917_stranger_"+names, true), substitutionResponse(substitutionRepoTwo, "receipt_5917_stranger_"+names))
			parent, clarification, _ := guardIssuedClarification(t, h, "request_5917_other_"+names)
			named := parent.result.ResultID
			if names == "clarification" {
				named = clarification.result.ResultID
			}
			outcome := answerTurn(h, "request_5917_other_"+names+"_three", named,
				[]BoundSubjectReceipt{{ResultID: stranger.result.ResultID, ReceiptID: "receipt_5917_stranger_" + names}},
				substitutionResponse(substitutionRepoTwo, "request_5917_other_"+names+"_served"))
			if outcome.result.Status != InvestigationClarificationRequired {
				t.Fatalf("status = %q, want %q", outcome.result.Status, InvestigationClarificationRequired)
			}
			assertServedNothing(t, outcome.result)
			event := lastSubstitution(t, outcome)
			assertGuard(t, event, SubjectSubstitutionClarified, SubjectSubstitutionOriginPriorReceipt, substitutionRepoOne, substitutionRepoTwo)
			if event.SubstitutionOriginResultID != stranger.result.ResultID || event.SubstitutionOriginIssuedFor != "" {
				t.Errorf("origin = (%q, issued_for %q), want the stranger %q issued for nothing", event.SubstitutionOriginResultID, event.SubstitutionOriginIssuedFor, stranger.result.ResultID)
			}
		})
	}
}

// TestSubstitutionGuardProvesItsClarificationByTheRememberedReceipt: the
// link from a clarification to the parent it continued is the remembered
// offer's receipt, never stored ancestry. A recorded parent that names some
// other result -- the shape a question-drift refusal records -- neither
// breaks the answer nor names the wrong parent on the line.
func TestSubstitutionGuardProvesItsClarificationByTheRememberedReceipt(t *testing.T) {
	t.Parallel()
	for _, names := range []string{"parent", "clarification"} {
		names := names
		t.Run("names_"+names, func(t *testing.T) {
			t.Parallel()
			h := newNeedTurnHarness(t, nil)
			stranger := h.turn(needTurnRequest("request_5917_drift_str_"+names, true), substitutionResponse(substitutionRepoTwo, "receipt_5917_drift_str_"+names))
			parent, clarification, chosen := guardIssuedClarification(t, h, "request_5917_drift_"+names)
			recordAncestry(h, clarification.result.ResultID, stranger.result.ResultID)
			named := parent.result.ResultID
			wantParentResult := parent.result.ResultID
			if names == "clarification" {
				named = clarification.result.ResultID
				// The receipt verifies against the parent, not against the
				// recorded ancestry, so no parent result is named.
				wantParentResult = ""
			}
			outcome := answerTurn(h, "request_5917_drift_"+names+"_three", named,
				[]BoundSubjectReceipt{{ResultID: clarification.result.ResultID, ReceiptID: chosen}},
				substitutionResponse(substitutionRepoTwo, "request_5917_drift_"+names+"_served"))
			assertServed(t, outcome.result, substitutionRepoTwo)
			event := lastSubstitution(t, outcome)
			assertGuard(t, event, SubjectSubstitutionRedeemedChoice, SubjectSubstitutionOriginPriorReceipt, substitutionRepoOne, substitutionRepoTwo)
			if event.SubstitutionParentResultID != wantParentResult {
				t.Errorf("substitution_parent_result_id = %q, want %q", event.SubstitutionParentResultID, wantParentResult)
			}
			if names == "parent" && event.SubstitutionOriginIssuedFor != parent.result.ResultID {
				t.Errorf("substitution_origin_issued_for = %q, want the named parent %q its receipt verifies against", event.SubstitutionOriginIssuedFor, parent.result.ResultID)
			}
			if event.SubstitutionParentResultID == stranger.result.ResultID || event.SubstitutionOriginIssuedFor == stranger.result.ResultID {
				t.Errorf("the line names the recorded ancestry %q as the parent", stranger.result.ResultID)
			}
		})
	}
}

// TestSubstitutionGuardClarifiesPastAClarificationThatListedNoRememberedSubject:
// a clarification that could not list the remembered subject is still
// guard-issued, so a turn naming it is never read as a parent with no
// identity. With nothing to compare against it clarifies again, the
// remembered subject unavailable; redeeming the clarification's own offer
// still answers it.
func TestSubstitutionGuardClarifiesPastAClarificationThatListedNoRememberedSubject(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, prefix string) (*needTurnHarness, needTurnOutcome) {
		h := newNeedTurnHarness(t, nil)
		parent := h.turn(needTurnRequest(prefix+"_one", true), substitutionResponse(substitutionRepoOne, prefix+"_receipt_one"))
		h.refuseCandidates = true
		clarification := answerTurn(h, prefix+"_two", parent.result.ResultID, nil, substitutionResponse(substitutionRepoTwo, prefix+"_receipt_two"))
		h.refuseCandidates = false
		if got := lastSubstitution(t, clarification).SubstitutionGuard; got != SubjectSubstitutionClarifiedRememberedUnavailable {
			t.Fatalf("premise: guard = %q, want a clarification listing no remembered subject", got)
		}
		recordAncestry(h, clarification.result.ResultID, parent.result.ResultID)
		return h, clarification
	}
	t.Run("commits_another_subject", func(t *testing.T) {
		t.Parallel()
		h, clarification := setup(t, "request_5917_unlisted")
		outcome := answerTurn(h, "request_5917_unlisted_three", clarification.result.ResultID, nil, substitutionResponse(substitutionRepoTwo, "request_5917_unlisted_served"))
		if outcome.result.Status != InvestigationClarificationRequired {
			t.Fatalf("status = %q, want %q", outcome.result.Status, InvestigationClarificationRequired)
		}
		assertServedNothing(t, outcome.result)
		event := lastSubstitution(t, outcome)
		assertGuard(t, event, SubjectSubstitutionClarifiedRememberedUnavailable, SubjectSubstitutionOriginResolver, SubjectRef{}, substitutionRepoTwo)
		if event.SubstitutionParentResultID != "" {
			t.Errorf("substitution_parent_result_id = %q, want none: no receipt proves the continued result", event.SubstitutionParentResultID)
		}
	})
	t.Run("answered", func(t *testing.T) {
		t.Parallel()
		h, clarification := setup(t, "request_5917_unlisted_answer")
		var chosen string
		for _, candidate := range clarification.result.SubjectResolution.Candidates {
			if sameSubjectIdentity(candidate.Subject, substitutionRepoTwo) {
				chosen = candidate.ReceiptID
			}
		}
		outcome := answerTurn(h, "request_5917_unlisted_answer_three", clarification.result.ResultID,
			[]BoundSubjectReceipt{{ResultID: clarification.result.ResultID, ReceiptID: chosen}},
			substitutionResponse(substitutionRepoTwo, "request_5917_unlisted_answer_served"))
		assertServed(t, outcome.result, substitutionRepoTwo)
		if got := lastSubstitution(t, outcome).SubstitutionGuard; got != SubjectSubstitutionRedeemedChoice {
			t.Errorf("guard = %q, want %q", got, SubjectSubstitutionRedeemedChoice)
		}
	})
}

// parentOffering is a parent turn that commits repository one and offers
// the given further candidates.
func parentOffering(offers ...SubjectCandidate) needTurnResponse {
	bases := CommitBasisSet{}
	bases.Record(substitutionRepoOne, CommitBasisAuthoritativeIdentity)
	candidates := []SubjectCandidate{{ReceiptID: "receipt_5917_domain_parent", Subject: substitutionRepoOne, State: contractsv1.ContextFabricResolutionCommitted, MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{}}}
	for _, offer := range offers {
		offer.State, offer.MatchedTerms, offer.MatchReasons, offer.Confidence, offer.EvidenceRefIDs = contractsv1.ContextFabricResolutionAmbiguous, []string{"service"}, []string{"matched"}, 0.4, []string{}
		candidates = append(candidates, offer)
	}
	return needTurnResponse{resolution: SubjectResolution{Committed: []SubjectRef{substitutionRepoOne}, Candidates: candidates}, bases: bases}
}

// TestSubstitutionGuardReceiptDomainAtTheEngine executes the receipt cells
// through Engine.Investigate: the parent's own receipt for the committed
// identity beside a stranger's, a receipt for the same id under another
// kind, and the parent's receipt for one member of a committed set of two.
func TestSubstitutionGuardReceiptDomainAtTheEngine(t *testing.T) {
	t.Parallel()
	sameIDOtherKind := SubjectRef{Kind: SubjectTeam, CanonicalID: substitutionRepoTwo.CanonicalID, Label: substitutionRepoTwo.Label}
	t.Run("parent_match_beside_a_stranger", func(t *testing.T) {
		t.Parallel()
		h := newNeedTurnHarness(t, nil)
		stranger := h.turn(needTurnRequest("request_5917_domain_str", true), substitutionResponse(substitutionRepoTwo, "receipt_5917_domain_str"))
		parent := h.turn(needTurnRequest("request_5917_domain_one", true), parentOffering(SubjectCandidate{ReceiptID: "receipt_5917_domain_b", Subject: substitutionRepoTwo}))
		outcome := answerTurn(h, "request_5917_domain_two", parent.result.ResultID, []BoundSubjectReceipt{
			{ResultID: stranger.result.ResultID, ReceiptID: "receipt_5917_domain_str"},
			{ResultID: parent.result.ResultID, ReceiptID: "receipt_5917_domain_b"},
		}, substitutionResponse(substitutionRepoTwo, "receipt_5917_domain_srv"))
		assertServed(t, outcome.result, substitutionRepoTwo)
		event := lastSubstitution(t, outcome)
		assertGuard(t, event, SubjectSubstitutionRedeemedChoice, SubjectSubstitutionOriginPriorReceipt, substitutionRepoOne, substitutionRepoTwo)
		if event.SubstitutionOriginResultID != parent.result.ResultID || event.SubstitutionOriginReceiptID != "receipt_5917_domain_b" {
			t.Errorf("origin receipt = (%q,%q), want the parent's (%q,%q)", event.SubstitutionOriginResultID, event.SubstitutionOriginReceiptID, parent.result.ResultID, "receipt_5917_domain_b")
		}
	})
	t.Run("same_id_other_kind", func(t *testing.T) {
		t.Parallel()
		h := newNeedTurnHarness(t, nil)
		parent := h.turn(needTurnRequest("request_5917_domain_kind_one", true), parentOffering(SubjectCandidate{ReceiptID: "receipt_5917_domain_kind", Subject: sameIDOtherKind}))
		outcome := answerTurn(h, "request_5917_domain_kind_two", parent.result.ResultID,
			[]BoundSubjectReceipt{{ResultID: parent.result.ResultID, ReceiptID: "receipt_5917_domain_kind"}},
			substitutionResponse(substitutionRepoTwo, "receipt_5917_domain_kind_srv"))
		if outcome.result.Status != InvestigationClarificationRequired {
			t.Fatalf("status = %q, want %q: a receipt for another kind is not a choice of this one", outcome.result.Status, InvestigationClarificationRequired)
		}
		assertServedNothing(t, outcome.result)
		assertGuard(t, lastSubstitution(t, outcome), SubjectSubstitutionClarified, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoTwo)
	})
	t.Run("parent_and_chosen_committed_together", func(t *testing.T) {
		t.Parallel()
		h := newNeedTurnHarness(t, nil)
		parent := h.turn(needTurnRequest("request_5917_domain_set_one", true), parentOffering(SubjectCandidate{ReceiptID: "receipt_5917_domain_set", Subject: substitutionRepoTwo}))
		outcome := answerTurn(h, "request_5917_domain_set_two", parent.result.ResultID,
			[]BoundSubjectReceipt{{ResultID: parent.result.ResultID, ReceiptID: "receipt_5917_domain_set"}},
			cohortResponse(substitutionRepoOne, substitutionRepoTwo))
		if outcome.result.Status != InvestigationClarificationRequired {
			t.Fatalf("status = %q, want %q: a choice of one never licenses a set of two", outcome.result.Status, InvestigationClarificationRequired)
		}
		assertServedNothing(t, outcome.result)
		event := lastSubstitution(t, outcome)
		if event.SubstitutionGuard != SubjectSubstitutionClarified {
			t.Errorf("guard = %q, want %q", event.SubstitutionGuard, SubjectSubstitutionClarified)
		}
		if want := committedIDs([]SubjectRef{substitutionRepoOne, substitutionRepoTwo}); !equalStrings(event.SubstitutionCommittedIDs, want) {
			t.Errorf("committed ids = %v, want %v", event.SubstitutionCommittedIDs, want)
		}
	})
}

// TestSubstitutionGuardNeverBindsTheSubjectItWithheld: on a turn the guard
// fired on, the shadow binder never binds the withheld subject, whether the
// parent's snapshot carries a binding or not, and whichever channel carried
// the subject. With a parent binding the withheld subject contests it; with
// none the binding stays unbound on ambiguous proof.
func TestSubstitutionGuardNeverBindsTheSubjectItWithheld(t *testing.T) {
	t.Parallel()
	for _, snapshot := range []string{"present", "absent"} {
		for _, channel := range []string{"resolver", "caller_hint"} {
			snapshot, channel := snapshot, channel
			t.Run(snapshot+"/"+channel, func(t *testing.T) {
				t.Parallel()
				e, g, store, telemetry := buildCommittedAnchorEngineWithTelemetry(t)
				e.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, CandidateVerificationReason) {
					return true, CandidateVerificationValid
				}
				parent, _ := committedAnchorTurn(t, e, g, store, needTurnRequest("request_5917_withheld_"+snapshot+"_"+channel+"_one", true), reviewCarryIdentityResponse(committedAnchorRepo))
				if snapshot == "absent" {
					delete(store.states, parent.ResultID)
				}
				mark := len(telemetry.anchorBindingTransitions)
				guarded := continuingNeedTurn(needTurnRequest("request_5917_withheld_"+snapshot+"_"+channel+"_two", true), parent.ResultID)
				guarded.Question = "And how does the second one compare over the same period?"
				if channel == "caller_hint" {
					guarded.RequestedScope.SubjectHints = []SubjectHint{{Kind: committedAnchorRepoOther.Kind, ID: committedAnchorRepoOther.CanonicalID, Label: committedAnchorRepoOther.Label, Source: "caller"}}
				}
				child, _ := committedAnchorTurn(t, e, g, store, guarded, reviewCarryIdentityResponse(committedAnchorRepoOther))
				if child.Status != InvestigationClarificationRequired {
					t.Fatalf("status = %q, want the guard to fire", child.Status)
				}
				lines := telemetry.anchorBindingTransitions[mark:]
				if len(lines) == 0 {
					t.Fatal("the shadow binder emitted no transition for the guarded turn")
				}
				for _, line := range lines {
					if !line.SubstitutionGuard.Fired() {
						t.Errorf("line substitution_guard = %q, want the fired decision", line.SubstitutionGuard)
					}
					if line.To.CanonicalID == committedAnchorRepoOther.CanonicalID {
						t.Errorf("the binder bound the withheld subject: %+v", line.To)
					}
					switch snapshot {
					case "present":
						if line.To.State != AnchorBindingContested || line.To.CanonicalID != committedAnchorRepo.CanonicalID || line.To.ContenderID != committedAnchorRepoOther.CanonicalID {
							t.Errorf("to = %+v, want the parent's binding contested by the withheld subject", line.To)
						}
					case "absent":
						if line.To.State != AnchorBindingUnbound || line.To.Reason != AnchorBindingReasonAmbiguousProof {
							t.Errorf("to = %+v, want unbound on ambiguous proof", line.To)
						}
					}
				}
			})
		}
	}
}

// TestBindAnchorNeverBindsAWithheldSubjectOnAnyPath drives the binder
// itself over every path that binds a fresh identity -- proof alone, a
// caller hint, a redeemed receipt, a reuse serve -- with and without a
// parent binding. With the guard fired, none of them binds the withheld
// subject.
func TestBindAnchorNeverBindsAWithheldSubjectOnAnyPath(t *testing.T) {
	t.Parallel()
	proven, bases := proofOf(CommitBasisAuthoritativeIdentity, bindBeta)
	receipt := &confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: bindBeta.Kind, AppliedValue: bindBeta.ID}
	hint := []SubjectHint{{Kind: bindBeta.Kind, ID: bindBeta.ID, Label: "beta", Source: "caller"}}
	paths := map[string]func(*anchorBindingInput){
		"proof": func(*anchorBindingInput) {},
		// A commit list whose first entry names no identity: it can contest
		// nothing, so the withheld identity after it is the contender.
		"unidentified_first_commit": func(in *anchorBindingInput) {
			in.Resolution.Committed = append([]SubjectRef{{}}, in.Resolution.Committed...)
		},
		"caller_hint":  func(in *anchorBindingInput) { in.CallerHints = hint },
		"receipt":      func(in *anchorBindingInput) { in.Receipt = receipt },
		"reused_serve": func(in *anchorBindingInput) { in.Evaluation = AnchorBindingEvaluationReused; in.CallerHints = hint },
	}
	parents := map[string]AnchorBinding{
		"no_parent_binding":        {State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: AnchorBindingReasonNoProof},
		"parent_bound":             heldBinding(AnchorBindingBound, bindAlpha),
		"parent_bound_to_withheld": heldBinding(AnchorBindingBound, bindBeta),
	}
	for pathName, path := range paths {
		for parentName, from := range parents {
			pathName, path, parentName, from := pathName, path, parentName, from
			t.Run(pathName+"/"+parentName, func(t *testing.T) {
				t.Parallel()
				in := anchorBindingInput{
					From: from, Evaluation: AnchorBindingEvaluationResolved, Frame: countingFrame(SubjectTeam),
					Resolution: proven, Bases: bases, ResultID: "result_withheld", GraphEpoch: 7,
				}
				path(&in)
				open, _ := bindAnchor(in)
				if open.CanonicalID != bindBeta.ID && open.ContenderID != bindBeta.ID {
					t.Fatalf("premise: the unguarded binder decided %+v, which neither binds nor contests with beta", open)
				}
				withheldHeld := from.active() && from.CanonicalID == bindBeta.ID
				in.SubstitutionGuard = SubjectSubstitutionClarified
				to, _ := bindAnchor(in)
				if to.CanonicalID == bindBeta.ID {
					t.Fatalf("to = %+v: the binder bound the withheld subject", to)
				}
				if err := ValidateAnchorBinding(to); err != nil {
					t.Fatalf("to = %+v is not a valid binding: %v", to, err)
				}
				if from.active() && !withheldHeld {
					if to.State != AnchorBindingContested || to.CanonicalID != bindAlpha.ID || to.ContenderID != bindBeta.ID {
						t.Errorf("to = %+v, want alpha contested by the withheld beta", to)
					}
				} else if to.State != AnchorBindingUnbound || to.Reason != AnchorBindingReasonAmbiguousProof {
					t.Errorf("to = %+v, want unbound on ambiguous proof", to)
				}
			})
		}
	}
}

// TestSubstitutionGuardPublishesTheRememberedReReadOnTheLine: every answer
// the remembered subject's re-read gets reaches the production line at Info
// -- what it found, the verifier's own reason and the context error -- so a
// withheld offer always says why.
func TestSubstitutionGuardPublishesTheRememberedReReadOnTheLine(t *testing.T) {
	t.Parallel()
	cells := []struct {
		name       string
		options    []needHarnessOption
		refuse     bool
		child      SubjectRef
		wantCheck  SubjectSubstitutionRememberedCheck
		wantReason CandidateVerificationReason
	}{
		{name: "readable", child: substitutionRepoTwo, wantCheck: SubjectSubstitutionRememberedReadable, wantReason: CandidateVerificationValid},
		{name: "refused", refuse: true, child: substitutionRepoTwo, wantCheck: SubjectSubstitutionRememberedRefused, wantReason: CandidateVerificationClaimLost},
		{name: "verifier_unwired", options: []needHarnessOption{withoutNeedVerifiers()}, child: substitutionRepoTwo, wantCheck: SubjectSubstitutionRememberedVerifierUnwired},
		{name: "not_checked", child: substitutionRepoOne, wantCheck: SubjectSubstitutionRememberedNotChecked},
	}
	for _, cell := range cells {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			h := newNeedTurnHarness(t, nil, cell.options...)
			one := h.turn(needTurnRequest("request_5917_reread_"+cell.name+"_one", true), substitutionResponse(substitutionRepoOne, "receipt_5917_reread_one"))
			h.refuseCandidates = cell.refuse
			buf := swapToJSONLedgerTelemetry(h)
			two := continuingNeedTurn(needTurnRequest("request_5917_reread_"+cell.name+"_two", true), one.result.ResultID)
			two.Question = "And how does the second one compare over the same period?"
			h.graph.response = substitutionResponse(cell.child, "receipt_5917_reread_two")
			if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), two); err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			line := lastLedgerJSONLine(t, buf)
			want := map[string]string{
				"substitution_remembered_check":         string(cell.wantCheck),
				"substitution_remembered_reason":        string(cell.wantReason),
				"substitution_remembered_context_error": "",
			}
			for key, value := range want {
				if got, ok := line[key]; !ok || got != value {
					t.Errorf("%s = %v (present=%t), want %q", key, got, ok, value)
				}
			}
		})
	}
}

// guardIssuedStored is a stored clarification the guard issued while
// continuing continued, listing the remembered subject when listed is true.
func guardIssuedStored(continued, ancestry string, listed bool) StoredInvestigationResult {
	decision := subjectSubstitutionDecision{Outcome: SubjectSubstitutionClarified, Parent: substitutionRepoOne, RememberedListed: listed}
	resolution := subjectSubstitutionResolution(substitutionResponse(substitutionRepoTwo, "receipt_5917_issued_two").resolution, decision, continued)
	return StoredInvestigationResult{Result: InvestigationResult{SubjectResolution: resolution}, ParentResultID: ancestry}
}

// TestSubjectSubstitutionIssuedForIsProvenByTheRememberedReceipt pins the
// one link from a clarification to the result it continued: the remembered
// offer's receipt verifies against exactly that result, whatever the stored
// ancestry says; nothing else reads as guard-issued.
func TestSubjectSubstitutionIssuedForIsProvenByTheRememberedReceipt(t *testing.T) {
	t.Parallel()
	listed := guardIssuedStored("result_parent", "result_elsewhere", true)
	unlisted := guardIssuedStored("result_parent", "result_parent", false)
	served := StoredInvestigationResult{Result: InvestigationResult{SubjectResolution: substitutionResponse(substitutionRepoOne, "receipt_5917_issued_one").resolution}, ParentResultID: "result_parent"}
	otherPrompt := listed
	otherPrompt.Result.SubjectResolution.ClarificationPrompt = "Pick one."
	committedWithPrompt := listed
	committedWithPrompt.Result.SubjectResolution.Committed = []SubjectRef{substitutionRepoTwo}
	noOffers := listed
	noOffers.Result.SubjectResolution.Candidates = []SubjectCandidate{}
	foreignFirst := listed
	foreignFirst.Result.SubjectResolution.Candidates = append([]SubjectCandidate(nil), listed.Result.SubjectResolution.Candidates[1:]...)
	cases := []struct {
		name     string
		stored   StoredInvestigationResult
		resultID string
		issued   bool
		for_     bool
		subject  SubjectRef
	}{
		{"listed, the continued result", listed, "result_parent", true, true, substitutionRepoOne},
		{"listed, the recorded ancestry", listed, "result_elsewhere", true, false, substitutionRepoOne},
		{"listed, no result", listed, "", true, false, substitutionRepoOne},
		{"unlisted, the continued result", unlisted, "result_parent", true, false, SubjectRef{}},
		{"a served result", served, "result_parent", false, false, SubjectRef{}},
		{"another clarification prompt", otherPrompt, "result_parent", false, false, SubjectRef{}},
		{"a commit beside the guard's prompt", committedWithPrompt, "result_parent", false, false, SubjectRef{}},
		{"listed prompt, first offer not the remembered one", foreignFirst, "result_parent", true, false, SubjectRef{}},
		{"listed prompt, no offers", noOffers, "result_parent", true, false, SubjectRef{}},
	}
	for _, tc := range cases {
		if got := subjectSubstitutionIssued(tc.stored); got != tc.issued {
			t.Errorf("%s: subjectSubstitutionIssued = %t, want %t", tc.name, got, tc.issued)
		}
		if got := subjectSubstitutionIssuedFor(tc.stored, tc.resultID); got != tc.for_ {
			t.Errorf("%s: subjectSubstitutionIssuedFor(%q) = %t, want %t", tc.name, tc.resultID, got, tc.for_)
		}
		if got := guardIssuedRememberedOf(tc.stored); !sameSubjectIdentity(got, tc.subject) {
			t.Errorf("%s: remembered = %+v, want %+v", tc.name, got, tc.subject)
		}
	}
}

// TestParentIdentityOfSpeaksForTheContinuedResult: a named parent the guard
// issued is read as the result it continued -- its remembered subject, and
// that result's id only when the receipt proves it.
func TestParentIdentityOfSpeaksForTheContinuedResult(t *testing.T) {
	t.Parallel()
	base := parentAnchorEvidence{Referenced: true, Loaded: true}
	proven := parentIdentityOf(base, guardIssuedStored("result_parent", "result_parent", true), "result_clarification")
	if !proven.GuardIssued || !sameSubjectIdentity(proven.Subject, substitutionRepoOne) || proven.IssuedFor != "result_parent" || proven.ResultID != "result_parent" {
		t.Errorf("proven = %+v, want repository one for result_parent", proven)
	}
	drifted := parentIdentityOf(base, guardIssuedStored("result_parent", "result_elsewhere", true), "result_clarification")
	if !drifted.GuardIssued || !sameSubjectIdentity(drifted.Subject, substitutionRepoOne) || drifted.IssuedFor != "" || drifted.ResultID != "" {
		t.Errorf("drifted = %+v, want repository one with no continued result named", drifted)
	}
	unlisted := parentIdentityOf(base, guardIssuedStored("result_parent", "result_parent", false), "result_clarification")
	if !unlisted.GuardIssued || unlisted.held() || unlisted.IssuedFor != "" {
		t.Errorf("unlisted = %+v, want guard-issued with no identity held", unlisted)
	}
	served := parentIdentityOf(base, StoredInvestigationResult{Result: InvestigationResult{SubjectResolution: substitutionResponse(substitutionRepoTwo, "receipt_5917_pi").resolution}}, "result_served")
	if served.GuardIssued || !sameSubjectIdentity(served.Subject, substitutionRepoTwo) || served.ResultID != "result_served" {
		t.Errorf("served = %+v, want repository two for result_served", served)
	}
}

// TestParentReceiptIssuersNamesTheParentsOwnOffer pins which results'
// receipts are the parent's own offer.
func TestParentReceiptIssuersNamesTheParentsOwnOffer(t *testing.T) {
	t.Parallel()
	issuers := parentReceiptIssuers{
		named: "result_parent", issuedFor: "result_grandparent",
		loaded: map[string]StoredInvestigationResult{
			"result_clarification":       guardIssuedStored("result_parent", "result_elsewhere", true),
			"result_other_clarification": guardIssuedStored("result_other", "result_parent", true),
			"result_unlisted":            guardIssuedStored("result_parent", "result_parent", false),
			"result_served":              {Result: InvestigationResult{SubjectResolution: substitutionResponse(substitutionRepoTwo, "receipt_5917_ri").resolution}, ParentResultID: "result_parent"},
		},
	}
	want := map[string]bool{
		"result_parent": true, " result_parent ": true, "result_grandparent": true, "result_clarification": true,
		"result_other_clarification": false, "result_unlisted": false, "result_served": false, "result_stranger": false, "": false,
	}
	for id, expected := range want {
		if got := issuers.issues(id); got != expected {
			t.Errorf("issues(%q) = %t, want %t", id, got, expected)
		}
	}
	if got := issuers.issuedForOf("result_clarification"); got != "result_parent" {
		t.Errorf("issuedForOf(clarification) = %q, want result_parent", got)
	}
	if got := issuers.issuedForOf("result_other_clarification"); got != "" {
		t.Errorf("issuedForOf(other clarification) = %q, want none: its receipt proves neither the named parent nor its recorded ancestry", got)
	}
	if got := (parentReceiptIssuers{named: "", loaded: issuers.loaded}).issues("result_clarification"); got {
		t.Error("issues() with no named parent = true, want false")
	}
}

// TestSubstitutionGuardServesTheParentsOwnOfferRedeemedWhileNamingItsClarification:
// a turn naming the guard's clarification may redeem an offer the parent
// itself made; the parent's receipts are the parent's own offer whichever
// of the two the caller names.
func TestSubstitutionGuardServesTheParentsOwnOfferRedeemedWhileNamingItsClarification(t *testing.T) {
	t.Parallel()
	third := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:gamma-service", Label: "gamma-service"}
	h := newNeedTurnHarness(t, nil)
	parent := h.turn(needTurnRequest("request_5917_parent_offer_one", true), parentOffering(SubjectCandidate{ReceiptID: "receipt_5917_parent_offer_b", Subject: substitutionRepoTwo}))
	clarification := answerTurn(h, "request_5917_parent_offer_two", parent.result.ResultID, nil, substitutionResponse(third, "receipt_5917_parent_offer_c"))
	if clarification.result.Status != InvestigationClarificationRequired {
		t.Fatalf("premise: status = %q, want the guard's own clarification", clarification.result.Status)
	}
	recordAncestry(h, clarification.result.ResultID, parent.result.ResultID)
	outcome := answerTurn(h, "request_5917_parent_offer_three", clarification.result.ResultID,
		[]BoundSubjectReceipt{{ResultID: parent.result.ResultID, ReceiptID: "receipt_5917_parent_offer_b"}},
		substitutionResponse(substitutionRepoTwo, "receipt_5917_parent_offer_srv"))
	assertServed(t, outcome.result, substitutionRepoTwo)
	event := lastSubstitution(t, outcome)
	assertGuard(t, event, SubjectSubstitutionRedeemedChoice, SubjectSubstitutionOriginPriorReceipt, substitutionRepoOne, substitutionRepoTwo)
	if event.SubstitutionOriginResultID != parent.result.ResultID || event.SubstitutionParentResultID != parent.result.ResultID {
		t.Errorf("origin result = %q parent result = %q, want both %q", event.SubstitutionOriginResultID, event.SubstitutionParentResultID, parent.result.ResultID)
	}
}

// TestSubstitutionGuardPublishesTheClarificationLinkOnTheLine reads, off the
// production JSON line, the receipt that answered the guard's
// clarification, the clarification that issued it, and the parent it
// continued.
func TestSubstitutionGuardPublishesTheClarificationLinkOnTheLine(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	parent, clarification, chosen := guardIssuedClarification(t, h, "request_5917_link_line")
	buf := swapToJSONLedgerTelemetry(h)
	request := continuingNeedTurn(needTurnRequest("request_5917_link_line_three", true), parent.result.ResultID)
	request.Question = "And how does the second one compare over the same period?"
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: clarification.result.ResultID, ReceiptID: chosen}}
	h.graph.response = substitutionResponse(substitutionRepoTwo, "request_5917_link_line_served")
	if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	line := lastLedgerJSONLine(t, buf)
	want := map[string]string{
		"substitution_guard":             string(SubjectSubstitutionRedeemedChoice),
		"substitution_origin_result_id":  clarification.result.ResultID,
		"substitution_origin_receipt_id": chosen,
		"substitution_origin_issued_for": parent.result.ResultID,
		"substitution_parent_result_id":  parent.result.ResultID,
	}
	for key, value := range want {
		if got, ok := line[key]; !ok || got != value {
			t.Errorf("%s = %v (present=%t), want %q", key, got, ok, value)
		}
	}
}

// TestSubstitutionGuardPublishesACancelledReReadOnTheLine: a context that
// dies while the remembered subject is re-read withholds the offer, and the
// production line carries the class, the verifier's own reason and the
// context error.
func TestSubstitutionGuardPublishesACancelledReReadOnTheLine(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_5917_cancel_line_one", true), substitutionResponse(substitutionRepoOne, "receipt_5917_cancel_line_one"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.engine.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, CandidateVerificationReason) {
		cancel()
		return true, CandidateVerificationValid
	}
	buf := swapToJSONLedgerTelemetry(h)
	request := continuingNeedTurn(needTurnRequest("request_5917_cancel_line_two", true), one.result.ResultID)
	request.Question = "And how does the second one compare over the same period?"
	h.graph.response = substitutionResponse(substitutionRepoTwo, "receipt_5917_cancel_line_two")
	served, _ := h.engine.Investigate(ctx, acceptancePrincipal(), request)
	if len(served.SubjectResolution.Committed) != 0 {
		t.Errorf("committed = %+v, want the substitute never served", served.SubjectResolution.Committed)
	}
	line := lastLedgerJSONLine(t, buf)
	want := map[string]string{
		"substitution_remembered_check":         string(SubjectSubstitutionRememberedCancelledDuring),
		"substitution_remembered_reason":        string(CandidateVerificationValid),
		"substitution_remembered_context_error": context.Canceled.Error(),
	}
	for key, value := range want {
		if got, ok := line[key]; !ok || got != value {
			t.Errorf("%s = %v (present=%t), want %q", key, got, ok, value)
		}
	}
}

// TestSubstitutionGuardReportsNoParentWhenOnlyAClarificationsReceiptIsRedeemed:
// a turn that names no parent redeems a receipt the guard's own
// clarification issued. With no named parent there is no parent whose
// offer a receipt could be, so the decision stops at no_parent_reference
// and never reports a redeemed choice, exactly as any turn with no parent
// reference serves.
func TestSubstitutionGuardReportsNoParentWhenOnlyAClarificationsReceiptIsRedeemed(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	parent, clarification, chosen := guardIssuedClarification(t, h, "request_5917_unnamed")
	request := needTurnRequest("request_5917_unnamed_three", true)
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: clarification.result.ResultID, ReceiptID: chosen}}
	outcome := h.turn(request, substitutionResponse(substitutionRepoTwo, "request_5917_unnamed_served"))
	event := lastSubstitution(t, outcome)
	if event.SubstitutionGuard != SubjectSubstitutionNoParentReference {
		t.Fatalf("guard = %q, want %q", event.SubstitutionGuard, SubjectSubstitutionNoParentReference)
	}
	if event.SubstitutionParentResultID != "" {
		t.Errorf("parent result = %q, want none with no named parent", event.SubstitutionParentResultID)
	}
	// Reported, never consulted: the receipt's issuer is proven by its
	// recorded ancestry to continue the parent, and the line says so.
	if event.SubstitutionOriginIssuedFor != parent.result.ResultID {
		t.Errorf("issued for = %q, want %q", event.SubstitutionOriginIssuedFor, parent.result.ResultID)
	}
}

// TestSubstitutionGuardFailsClosedOnANonListingClarificationCarryingARememberedOffer
// plants the one row the guard never writes: a clarification whose prompt
// says the remembered subject was not listed, yet whose first offer is a
// remembered offer minted for the parent. The prompt decides: the row
// holds no remembered subject, so a turn naming it that commits a subject
// clarifies with the remembered subject unavailable, and the substitute is
// never served.
func TestSubstitutionGuardFailsClosedOnANonListingClarificationCarryingARememberedOffer(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	parent, clarification, _ := guardIssuedClarification(t, h, "request_5917_planted")
	planted := clarification.result
	planted.SubjectResolution.ClarificationPrompt = subjectSubstitutionRememberedUnavailablePrompt
	if first := planted.SubjectResolution.Candidates[0]; first.ReceiptID != subjectSubstitutionReceiptID(parent.result.ResultID, substitutionRepoOne) {
		t.Fatalf("premise: first offer %q is not the parent's remembered offer", first.ReceiptID)
	}
	h.store.results[planted.ResultID] = planted
	for _, committed := range []SubjectRef{substitutionRepoTwo, {Kind: SubjectRepository, CanonicalID: "repository:gamma-service", Label: "gamma-service"}} {
		outcome := answerTurn(h, "request_5917_planted_"+committed.Label[:4], planted.ResultID, nil, substitutionResponse(committed, "request_5917_planted_served"))
		if outcome.result.Status != InvestigationClarificationRequired {
			t.Fatalf("%s: status = %q, want %q", committed.CanonicalID, outcome.result.Status, InvestigationClarificationRequired)
		}
		assertServedNothing(t, outcome.result)
		event := lastSubstitution(t, outcome)
		assertGuard(t, event, SubjectSubstitutionClarifiedRememberedUnavailable, SubjectSubstitutionOriginResolver, SubjectRef{}, committed)
	}
}

// TestTheReuseServeTakesTheGuardDecisionAtItsOwnProducer pins the guard
// decision at the SECOND binder path in production. bindAnchor refuses a
// withheld subject for every caller, but only a caller that passes the
// decision gets that refusal, and the reuse serve builds its own input:
// reuseEvent, not decide. The engine reaches the reuse exit above the guard
// today, so the decision is not_evaluated there by construction -- which is
// exactly why an input the path never fills reads as "the guard never
// fired" rather than as "the guard was never asked". The tracker is driven
// with the decision set, so the path is measured on the state the ordering
// currently prevents rather than on the ordering.
func TestTheReuseServeTakesTheGuardDecisionAtItsOwnProducer(t *testing.T) {
	t.Parallel()
	proven, _ := proofOf(CommitBasisAuthoritativeIdentity, bindBeta)
	// A reuse serve reads its bases off the replayed row's digests, the way
	// the production path does, not off a basis set handed to it.
	proven.CommitDecisionDigests = []contractsv1.ContextFabricCommitDecisionDigest{
		{Subject: SubjectRef{Kind: bindBeta.Kind, CanonicalID: bindBeta.ID, Label: "beta"}, CommitGate: "identity_fast_path", IdentityProven: true},
	}
	reading := storedCountReading{Frame: countingFrame(SubjectTeam), AnchorKind: SubjectRepository}
	result := InvestigationResult{ResultID: "result_reuse_withheld", SubjectResolution: proven}
	newTracker := func(from AnchorBinding) *anchorBindingTracker {
		return &anchorBindingTracker{
			parent:      anchorBindingParent{ResultID: "result_bind_parent", Status: AnchorBindingParentPresent, Binding: from},
			epoch:       7,
			evaluation:  AnchorBindingEvaluationNotResolved,
			callerHints: []SubjectHint{{Kind: bindBeta.Kind, ID: bindBeta.ID, Label: "beta", Source: "caller"}},
		}
	}
	// With no binding to contest, the reuse serve BINDS the replayed proof,
	// so the two decisions differ in the one field the guard governs.
	open := newTracker(unboundFrom).reuseEvent(result, reading)
	if open.To.State != AnchorBindingBound || open.To.CanonicalID != bindBeta.ID {
		t.Fatalf("premise: the unguarded reuse serve decided %+v, want it bound to the substitute", open.To)
	}
	if open.SubstitutionGuard != SubjectSubstitutionNotEvaluated {
		t.Fatalf("premise: an unguarded reuse line reports %q, want %q", open.SubstitutionGuard, SubjectSubstitutionNotEvaluated)
	}
	withheld := newTracker(unboundFrom)
	withheld.observeSubstitutionGuard(SubjectSubstitutionClarified)
	event := withheld.reuseEvent(result, reading)
	if event.To.CanonicalID == bindBeta.ID {
		t.Fatalf("to = %+v: the reuse serve bound the subject the guard withheld", event.To)
	}
	if event.To.State != AnchorBindingUnbound || event.To.Reason != AnchorBindingReasonAmbiguousProof {
		t.Fatalf("to = %+v: want unbound on ambiguous proof", event.To)
	}
	if event.SubstitutionGuard != SubjectSubstitutionClarified {
		t.Fatalf("substitution_guard = %q, want %q", event.SubstitutionGuard, SubjectSubstitutionClarified)
	}
	// A parent binding turns the same withheld identity into a contender.
	held := newTracker(heldBinding(AnchorBindingBound, bindAlpha))
	held.observeSubstitutionGuard(SubjectSubstitutionClarified)
	contested := held.reuseEvent(result, reading).To
	if contested.CanonicalID == bindBeta.ID {
		t.Fatalf("to = %+v: the reuse serve bound the subject the guard withheld", contested)
	}
	if contested.State != AnchorBindingContested || contested.CanonicalID != bindAlpha.ID || contested.ContenderID != bindBeta.ID {
		t.Fatalf("to = %+v: want the parent's anchor contested by the withheld subject", contested)
	}
}
