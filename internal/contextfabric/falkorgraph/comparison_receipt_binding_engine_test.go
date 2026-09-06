package falkorgraph

// TURN-2 RECEIPT BINDING FOR A TWO-NAMED-OPERAND COMPARISON -- the
// red-at-parent battery's second half.
//
// A follow-up selection is matched SERVER-SIDE against the CURRENT question:
// no new request field exists, and none is added. Which operand a receipt
// answers is derived from that operand's own current terms, and a receipt
// that matches no operand, or both, stays UNBOUND rather than being guessed
// into a slot.
//
// WHY THE PARENT FAILS THESE. The commit gate in the merged-candidate
// resolver runs its ordinary gates only when NOTHING is pre-committed. A
// receipt-derived hint pre-commits one subject for the whole resolution, so
// the second operand's gate never runs at all: the parent publishes the
// receipt's subject alone and reports the comparison as having proceeded,
// whatever the second operand's own evidence says. Every arm here is that
// suppression, seen from a different side.
//
// These arms drive the SAME real Engine over the SAME real Adapter as the
// turn-1 file beside them; only the request carries receipts. The ticket is
// referred to by role; no tracker id appears in this file.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ---------------------------------------------------------------------------
// PRIOR-TURN CARRIER
// ---------------------------------------------------------------------------

const (
	comparisonPriorResultID = "result_comparison_turn_one"
	comparisonReceiptA      = "receipt_operand_platform"
	comparisonReceiptB      = "receipt_operand_payments"
	comparisonReceiptRivalB = "receipt_operand_payments_rival"
)

// priorComparisonStore hands back ONE fixed prior result, stamped with the
// graph epoch the caller supplies.
//
// THE EPOCH IS NOT GUESSED. The engine strips any carrier whose stamped epoch
// differs from this investigation's own binding, and a stripped receipt would
// make every arm below pass for the wrong reason -- "the receipt never bound"
// is exactly what some of them assert. The epoch is therefore read from the
// live adapter's own ResolveInvestigationBinding and threaded in, so a change
// to the adapter's epoch cannot leave this file silently measuring the
// taint-strip path instead of the binding path.
type priorComparisonStore struct {
	mu     sync.Mutex
	prior  contextfabric.InvestigationResult
	epoch  int64
	getFor []string
}

func (s *priorComparisonStore) Save(context.Context, storage.Principal, contextfabric.InvestigationResult, contextfabric.SourceWatermarkSnapshot, contextfabric.RebuildEpoch, string, contextfabric.ReuseRetrievalIdentity, contextfabric.ReusePromptVersions, contextfabric.ReuseVersionAuthorities, int64, string) error {
	return nil
}

func (s *priorComparisonStore) Get(_ context.Context, _ storage.Principal, resultID string) (contextfabric.StoredInvestigationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getFor = append(s.getFor, resultID)
	epoch := s.epoch
	return contextfabric.StoredInvestigationResult{Result: s.prior, GraphEpoch: &epoch}, nil
}

// priorCandidate builds one clarification candidate from the prior turn, with
// the receipt id a follow-up would quote back.
func priorCandidate(receiptID string, subject contextfabric.SubjectRef) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: receiptID, Subject: subject, State: contextfabric.ResolutionProposed,
		MatchReasons: []string{"Exact canonical subject label match."}, Confidence: 1,
		MatchedTerms: []string{subject.Label}, EvidenceRefIDs: []string{},
	}
}

// heldComparisonPriorResult is the document turn 1 published when it held:
// candidates for both operands, nothing committed, one clarification. It is
// the ONLY shape a real follow-up can quote a receipt from, which is why every
// arm here starts from it rather than from a hand-built candidate list with no
// provenance.
func heldComparisonPriorResult(candidates ...contextfabric.SubjectCandidate) contextfabric.InvestigationResult {
	return contextfabric.InvestigationResult{
		ResultID: comparisonPriorResultID,
		Status:   contextfabric.InvestigationClarificationRequired,
		SubjectResolution: contextfabric.SubjectResolution{
			Candidates: candidates,
			Committed:  []contextfabric.SubjectRef{},
		},
	}
}

// bindingEpoch reads the epoch this investigation's graph binding actually
// carries, from the live adapter.
func bindingEpoch(t *testing.T, adapter *Adapter) int64 {
	t.Helper()
	binding, err := adapter.ResolveInvestigationBinding(context.Background(), storage.Principal{OrgID: "org-1"})
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	return binding.Epoch
}

// nodeLookupRows answers the by-kind-and-id lookup a receipt's re-authorization
// performs, for the subjects declared authorized.
func nodeLookupRows(authorized ...contextfabric.SubjectRef) func(kind, id string) []row {
	byKey := map[string]contextfabric.SubjectRef{}
	for _, subject := range authorized {
		byKey[string(subject.Kind)+"/"+subject.CanonicalID] = subject
	}
	return func(kind, id string) []row {
		subject, ok := byKey[kind+"/"+id]
		if !ok {
			return nil
		}
		r := fakeSubjectNodeRow(string(subject.Kind), subject.CanonicalID, subject.Label)
		r["n"].(*node).Properties["authorization_repositories"] = "*"
		return []row{r}
	}
}

// newReceiptComparisonAdapter answers BOTH query classes: the full-text
// retrieval passes (keyed on the term, exactly as the turn-1 file does) and
// the by-kind-and-id node lookup a receipt's re-authorization issues.
//
// A double that answered only the full-text class would leave every receipt
// failing re-authorization, and every arm below would pass because nothing
// bound -- the silent-green shape a partially-implemented double always
// eventually produces.
func newReceiptComparisonAdapter(t *testing.T, conn *comparisonConn, lookup func(kind, id string) []row) *Adapter {
	t.Helper()
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		if strings.Contains(cypher, "fulltext") {
			query, _ := params["query"].(string)
			conn.record(query)
			if conn.rowsForTerm == nil {
				return nil, nil
			}
			return conn.rowsForTerm(query), nil
		}
		kind, kindOK := params["kind"].(string)
		id, idOK := params["id"].(string)
		if kindOK && idOK && lookup != nil {
			return lookup(kind, id), nil
		}
		return nil, nil
	}}
	return newFakeAdapter(t, fake)
}

// receiptDrive is comparisonDrive with the receipt-aware adapter and the
// prior-result carrier. It is a separate type rather than a flag on the other
// one because these arms need the node-lookup query answered, and a shared
// builder that sometimes answers it would be one more thing an arm could be
// wrong about without noticing.
type receiptDrive struct {
	frame       *contextfabric.QuestionFrame
	terms       []string
	conn        *comparisonConn
	lookup      func(kind, id string) []row
	prior       contextfabric.InvestigationResult
	receipts    []contextfabric.BoundSubjectReceipt
	facts       contextfabric.CanonicalFactReader
	synthesizer contextfabric.AnswerSynthesizer
}

func (d receiptDrive) run(t *testing.T) (contextfabric.InvestigationResult, *priorComparisonStore) {
	t.Helper()

	adapter := newReceiptComparisonAdapter(t, d.conn, d.lookup)
	store := &priorComparisonStore{prior: d.prior, epoch: bindingEpoch(t, adapter)}

	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: comparisonInterpreter{
			interpreted: contextfabric.InterpretedQuestion{
				Shape:             contextfabric.ShapeExplicitCohort,
				RequestedJudgment: "comparison",
				SubjectTerms:      d.terms,
				TimeContext:       contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				FactRequirements:  []contextfabric.FactRequirement{},
			},
			frame:  d.frame,
			family: contextfabric.QuestionFamilyExplicitComparison,
		},
		Graph:        adapter,
		Facts:        d.facts,
		Synthesizer:  d.synthesizer,
		Results:      store,
		Requirements: productionRequirementDeriver{},
	}, contextfabric.EngineOptions{
		ServiceVersion: "acr-test",
		NewResultID:    func() string { return "result_comparison_turn_two" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     "request_comparison_turn_two",
		Question:      comparisonQuestion,
		TimeContext: contextfabric.TimeContext{
			Axis:           contextfabric.TemporalCurrent,
			EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D},
		},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		PriorSubjectReceipts: d.receipts,
		Consumer:             contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(store.getFor) == 0 {
		t.Fatal("the prior result was never loaded -- the receipt never reached the binding path, so nothing below measures binding")
	}
	return result, store
}

// ---------------------------------------------------------------------------
// ARM 5 -- A RECEIPT FOR ONE OPERAND, TEXT FOR THE OTHER
// ---------------------------------------------------------------------------

// TestAReceiptForOneOperandStillResolvesTheOtherOperandIndependently is the
// ordinary follow-up: the user picked operand A from turn 1's clarification,
// and operand B is now unambiguously named in the current question.
//
// Both operands must end up bound: the receipt binds A, and B runs its OWN
// commit gate on its OWN terms.
//
// AT THE PARENT the receipt pre-commits A for the whole resolution, the
// ordinary gates are skipped wholesale because something is already
// committed, and B never gets a decision at all -- the turn publishes one
// subject and calls the comparison done.
func TestAReceiptForOneOperandStillResolvesTheOtherOperandIndependently(t *testing.T) {
	t.Parallel()

	conn := &comparisonConn{rowsForTerm: perOperandRows(
		[]row{comparisonAuthorizedRow(comparisonSubjectA, comparisonTermA, 1)},
		[]row{comparisonAuthorizedRow(comparisonSubjectB, comparisonTermB, 1)},
		nil,
	)}

	result, _ := receiptDrive{
		frame:  twoNamedOperandComparisonFrame(),
		terms:  []string{comparisonTermA, comparisonTermB},
		conn:   conn,
		lookup: nodeLookupRows(comparisonSubjectA, comparisonSubjectB),
		prior: heldComparisonPriorResult(
			priorCandidate(comparisonReceiptA, comparisonSubjectA),
			priorCandidate(comparisonReceiptB, comparisonSubjectB),
		),
		receipts:    []contextfabric.BoundSubjectReceipt{{ResultID: comparisonPriorResultID, ReceiptID: comparisonReceiptA}},
		facts:       emptyFactReader{},
		synthesizer: countingSynthesizer{},
	}.run(t)

	got := committedKeys(result.SubjectResolution)
	if len(got) != 2 {
		t.Fatalf("committed = %v (%d subjects), want both operands -- a receipt binding one operand must not suppress the other operand's own commit decision",
			got, len(got))
	}
	if !subjectCommitted(result.SubjectResolution, comparisonSubjectA) {
		t.Errorf("committed = %v, missing the receipt's own operand %s", got, subjectKey(comparisonSubjectA))
	}
	if !subjectCommitted(result.SubjectResolution, comparisonSubjectB) {
		t.Errorf("committed = %v, missing the text-named operand %s -- this is the gate the pre-commit suppresses", got, subjectKey(comparisonSubjectB))
	}
	if got[0] != subjectKey(comparisonSubjectA) {
		t.Errorf("committed order = %v, want the frame's operand order (%s first)", got, subjectKey(comparisonSubjectA))
	}
}

// TestASymmetricReceiptSelectionBindsTheOtherOperand is the mirror: the user
// picked operand B instead. Nothing about binding may depend on which operand
// position the receipt happens to answer.
func TestASymmetricReceiptSelectionBindsTheOtherOperand(t *testing.T) {
	t.Parallel()

	conn := &comparisonConn{rowsForTerm: perOperandRows(
		[]row{comparisonAuthorizedRow(comparisonSubjectA, comparisonTermA, 1)},
		[]row{comparisonAuthorizedRow(comparisonSubjectB, comparisonTermB, 1)},
		nil,
	)}

	result, _ := receiptDrive{
		frame:  twoNamedOperandComparisonFrame(),
		terms:  []string{comparisonTermA, comparisonTermB},
		conn:   conn,
		lookup: nodeLookupRows(comparisonSubjectA, comparisonSubjectB),
		prior: heldComparisonPriorResult(
			priorCandidate(comparisonReceiptA, comparisonSubjectA),
			priorCandidate(comparisonReceiptB, comparisonSubjectB),
		),
		receipts:    []contextfabric.BoundSubjectReceipt{{ResultID: comparisonPriorResultID, ReceiptID: comparisonReceiptB}},
		facts:       emptyFactReader{},
		synthesizer: countingSynthesizer{},
	}.run(t)

	if len(committedKeys(result.SubjectResolution)) != 2 {
		t.Fatalf("committed = %v, want both operands regardless of which one the receipt answered", committedKeys(result.SubjectResolution))
	}
}

// ---------------------------------------------------------------------------
// ARM 6 -- BINDING COUNT: ZERO OR TWO MATCHES LEAVE A RECEIPT UNBOUND
// ---------------------------------------------------------------------------

// TestTwoDistinctReceiptsTargetingOneOperandHoldTheComparison is the
// over-commit case: both carried receipts name subjects whose identity
// evidence sits in the SAME operand slot, and no receipt answers the other
// operand at all.
//
// One slot cannot hold two distinct identities. The comparison holds rather
// than dropping one of them, and rather than reassigning one to the empty
// slot to manufacture a completed pair.
//
// AT THE PARENT both receipts simply pre-commit, and the turn publishes a
// two-subject "comparison" in which both subjects answer the same operand and
// the other operand was never resolved at all.
func TestTwoDistinctReceiptsTargetingOneOperandHoldTheComparison(t *testing.T) {
	t.Parallel()

	// Both prior candidates carry operand B's term as their label, so the
	// only slot either of them has identity evidence in is operand B's.
	conn := &comparisonConn{rowsForTerm: perOperandRows(
		nil,
		[]row{
			comparisonAuthorizedRow(comparisonSubjectB, comparisonTermB, 1),
			comparisonAuthorizedRow(comparisonRivalB, comparisonTermB, 1),
		},
		nil,
	)}

	result, _ := receiptDrive{
		frame:  twoNamedOperandComparisonFrame(),
		terms:  []string{comparisonTermA, comparisonTermB},
		conn:   conn,
		lookup: nodeLookupRows(comparisonSubjectB, comparisonRivalB),
		prior: heldComparisonPriorResult(
			priorCandidate(comparisonReceiptB, comparisonSubjectB),
			priorCandidate(comparisonReceiptRivalB, comparisonRivalB),
		),
		receipts: []contextfabric.BoundSubjectReceipt{
			{ResultID: comparisonPriorResultID, ReceiptID: comparisonReceiptB},
			{ResultID: comparisonPriorResultID, ReceiptID: comparisonReceiptRivalB},
		},
		facts:       refusingFactReader{t: t},
		synthesizer: refusingSynthesizer{t: t},
	}.run(t)

	assertHeldComparison(t, result, comparisonTermA, comparisonTermB)

	// AND THE DISPOSITION SEMANTICS ARE NOT REWRITTEN TO SAY SO. The
	// disposition composer reports what happened to the RECEIPT, over
	// candidates as well as committed subjects; it is not an operand-binding
	// assertion, and a held comparison must not relabel a genuinely
	// reauthorized receipt as a no-match to make the outcome look tidy.
	for _, entry := range result.SubjectResolution.PriorSubjectReceiptDispositions {
		if entry.Disposition == contractsv1.ContextFabricPriorSubjectReceiptSkippedNoMatch {
			t.Errorf("receipt %q was relabelled %q by a comparison hold -- the hold reports an unbound OPERAND, which is a different decision from the receipt failing to match anything",
				entry.ReceiptID, entry.Disposition)
		}
	}
}

// TestAReceiptWhoseIdentityAnswersBothOperandsStaysUnbound is the other half
// of the count rule: one subject whose identity evidence sits in BOTH slots.
// Two matches is exactly as unbindable as zero.
func TestAReceiptWhoseIdentityAnswersBothOperandsStaysUnbound(t *testing.T) {
	t.Parallel()

	// A frame whose two operands name the SAME term is the smallest thing
	// that puts one subject's identity evidence in both slots without
	// inventing an aliasing mechanism the product does not have.
	ambiguous := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionExplicitSet,
			Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
				namedComparisonOperand(comparisonTermA, contextfabric.SubjectTeam),
				namedComparisonOperand(comparisonTermA, contextfabric.SubjectTeam),
			}},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)

	conn := &comparisonConn{rowsForTerm: func(query string) []row {
		if termQueryContains(query, comparisonTermA) {
			return []row{comparisonAuthorizedRow(comparisonSubjectA, comparisonTermA, 1)}
		}
		return nil
	}}

	result, _ := receiptDrive{
		frame:       &ambiguous,
		terms:       []string{comparisonTermA},
		conn:        conn,
		lookup:      nodeLookupRows(comparisonSubjectA),
		prior:       heldComparisonPriorResult(priorCandidate(comparisonReceiptA, comparisonSubjectA)),
		receipts:    []contextfabric.BoundSubjectReceipt{{ResultID: comparisonPriorResultID, ReceiptID: comparisonReceiptA}},
		facts:       refusingFactReader{t: t},
		synthesizer: refusingSynthesizer{t: t},
	}.run(t)

	if subjectCommitted(result.SubjectResolution, comparisonSubjectA) {
		t.Errorf("%s was bound to an operand although its identity evidence answers both operands equally -- a receipt matching two slots is guessed into one only by a rule that refuses to admit it does not know",
			subjectKey(comparisonSubjectA))
	}
	assertHeldComparison(t, result, comparisonTermA, comparisonTermA)
}

// TestAReceiptWinnerOfTheWrongStatedKindHoldsTheComparison pins the required
// kind check on the slot's winner. The question STATES each operand's kind;
// a carried receipt whose subject is a different kind answers no slot, and
// the comparison holds rather than dropping the operand to succeed.
func TestAReceiptWinnerOfTheWrongStatedKindHoldsTheComparison(t *testing.T) {
	t.Parallel()

	// Same label as operand A's term, different KIND. A rule that matched on
	// terms alone would bind it; the stated kind is what refuses it.
	wrongKind := contextfabric.SubjectRef{
		Kind: contextfabric.SubjectProject, CanonicalID: "project_platform", Label: comparisonTermA,
	}

	conn := &comparisonConn{rowsForTerm: perOperandRows(
		[]row{comparisonAuthorizedRow(wrongKind, comparisonTermA, 1)},
		[]row{comparisonAuthorizedRow(comparisonSubjectB, comparisonTermB, 1)},
		nil,
	)}

	result, _ := receiptDrive{
		frame:       twoNamedOperandComparisonFrame(),
		terms:       []string{comparisonTermA, comparisonTermB},
		conn:        conn,
		lookup:      nodeLookupRows(wrongKind, comparisonSubjectB),
		prior:       heldComparisonPriorResult(priorCandidate(comparisonReceiptA, wrongKind)),
		receipts:    []contextfabric.BoundSubjectReceipt{{ResultID: comparisonPriorResultID, ReceiptID: comparisonReceiptA}},
		facts:       refusingFactReader{t: t},
		synthesizer: refusingSynthesizer{t: t},
	}.run(t)

	if subjectCommitted(result.SubjectResolution, wrongKind) {
		t.Errorf("%s was bound to an operand whose STATED kind is %s -- the stated kind is a required check on the slot's winner, not retrieval guidance alone",
			subjectKey(wrongKind), contextfabric.SubjectTeam)
	}
	assertHeldComparison(t, result, comparisonTermA, comparisonTermB)
}
