package contextfabric

// THE HELD-COMPARISON TERMINAL SEAM, over its whole input domain.
//
// Two guards decide what a caller receives when a comparison cannot publish:
// comparisonHeldWithoutCandidates, which reads the frame to tell a held
// comparison's empty pool apart from an ordinary one, and the branch
// resolveTerminalStatus reaches on its answer. Both are tabled here in one
// pass rather than sampled, because the interesting cells are the ones nobody
// thinks to write down: a frame one operand short of the cut, a prompt that is
// only whitespace, an operand carrying neither variant pointer.
//
// THE ANSWERABILITY AXIS IS PART OF THE DOMAIN, NOT A SEPARATE CONCERN. A
// comparison publishes no structure material at all, so when its own candidate
// list is empty the turn has nothing the caller can send back. A clarification
// with no redeemable offer is refused at composition
// (assertAnswerableClarification), so the terminal has to depend on
// answerability -- and the table below asserts every combination of it with
// AllowClarification rather than the one pairing that motivated the change.
//
// EVERY LOOP COUNTS WHAT REACHED ITS ASSERTIONS AND FAILS AT ZERO, the same
// discipline comparison_operand_classification_test.go states for this layer.

import (
	"strings"
	"testing"
)

const (
	heldTermA = "platform"
	heldTermB = "payments"
)

// heldPrompt is a comparison hold's own published prompt. Its CONTENT is
// irrelevant to both guards -- only its non-emptiness is read -- so it is a
// plain sentence rather than a copy of the real builder's prose, which would
// make this file a second authority for wording it does not own.
const heldPrompt = "The first is \"platform\". The second, \"payments\", matched nothing. Restate that side and ask again."

func heldResolution(prompt string, candidateCount int) SubjectResolution {
	resolution := SubjectResolution{
		Candidates: []SubjectCandidate{},
		Committed:  []SubjectRef{},

		ClarificationPrompt: prompt,
	}
	for i := 0; i < candidateCount; i++ {
		resolution.Candidates = append(resolution.Candidates, SubjectCandidate{
			Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_" + strings.Repeat("x", i+1), Label: heldTermA},
		})
	}
	return resolution
}

// ---------------------------------------------------------------------------
// GUARD 1 -- comparisonHeldWithoutCandidates, whole domain
// ---------------------------------------------------------------------------

// TestComparisonHeldWithoutCandidatesOverItsWholeInputDomain walks both
// arguments across every shape either can take, including the ones Go's own
// types make reachable only by construction.
//
// THE FRAME AXIS IS THE CLASSIFIER'S CUT, RESTATED AS THIS GUARD'S CONCERN.
// The guard delegates the cut to ClassifyComparisonOperands, so what it owns
// is which admissions it treats as "a comparison held" -- and that ownership
// is only visible if the out-of-cut admissions are executed here too, not
// assumed correct because another file tests the classifier.
func TestComparisonHeldWithoutCandidatesOverItsWholeInputDomain(t *testing.T) {
	t.Parallel()

	admittedPair := comparisonFrame(
		comparisonNamedOperand(heldTermA, SubjectTeam),
		comparisonNamedOperand(heldTermB, SubjectTeam),
	)
	scopedHold := comparisonFrame(
		comparisonNamedOperand(heldTermA, SubjectTeam),
		comparisonScopedOperand("infrastructure", SubjectTeam),
	)
	oneOperand := comparisonFrame(comparisonNamedOperand(heldTermA, SubjectTeam))
	threeOperands := comparisonFrame(
		comparisonNamedOperand(heldTermA, SubjectTeam),
		comparisonNamedOperand(heldTermB, SubjectTeam),
		comparisonNamedOperand("infrastructure", SubjectTeam),
	)
	zeroOperands := comparisonFrame()
	unstatedKind := comparisonFrame(
		comparisonNamedOperand(heldTermA, SubjectTeam),
		comparisonNamedOperandWithoutKind(heldTermB),
	)
	duplicateOperands := comparisonFrame(
		comparisonNamedOperand(heldTermA, SubjectTeam),
		comparisonNamedOperand(heldTermA, SubjectTeam),
	)
	// NEITHER VARIANT POINTER SET: a frame that never passed invariant I1.
	// Unreachable through the builders above, so it is assembled directly --
	// the point of the cell is that the guard must not panic on it.
	variantlessOperands := comparisonFrame(SubjectOperand{}, SubjectOperand{})
	// A NON-EXPLICIT-SET SUBJECT EXPRESSION. The classifier refuses on Kind
	// before it reads operands at all, and this is the cell that proves the
	// guard inherits that refusal rather than keying on something else.
	notAnExplicitSet := frameWith(
		[]InvestigationGoal{GoalCompare},
		SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{heldTermA}, ExpectedKind: kindPointer(SubjectTeam)},
		},
		TemporalIntentCurrent,
		nil,
	)
	// EXPLICIT SET WITH A NIL Explicit POINTER: the shape's own null cell.
	explicitSetWithNilBody := frameWith(
		[]InvestigationGoal{GoalCompare},
		SubjectExpression{Kind: SubjectExpressionExplicitSet},
		TemporalIntentCurrent,
		nil,
	)

	withPrompt := heldResolution(heldPrompt, 0)
	emptyPrompt := heldResolution("", 0)
	whitespacePrompt := heldResolution("   \t\n ", 0)
	promptWithCandidates := heldResolution(heldPrompt, 2)

	executed := 0
	for _, cell := range []struct {
		name       string
		frame      *QuestionFrame
		resolution *SubjectResolution
		want       bool
		why        string
	}{
		// --- resolution axis, held frame held constant ---
		{"canonical: admitted pair, prompt present", &admittedPair, &withPrompt, true,
			"the pair is in cut and publication wrote a prompt, which is the evidence this empty pool came from the comparison path"},
		{"absent: nil resolution", &admittedPair, nil, false,
			"nothing to read; a nil resolution cannot be evidence of anything and must not panic"},
		{"empty: prompt is the empty string", &admittedPair, &emptyPrompt, false,
			"frame alone is a claim about which code ran, never a fact about what it produced"},
		{"whitespace: prompt trims to nothing", &admittedPair, &whitespacePrompt, false,
			"a prompt of only whitespace asks nothing, and TrimSpace is what keeps it from counting"},
		{"candidates present alongside the prompt", &admittedPair, &promptWithCandidates, true,
			"this guard answers only whether the hold is a comparison's; the caller owns the emptiness of the pool"},

		// --- frame axis, prompt held constant ---
		{"absent: nil frame", nil, &withPrompt, false,
			"no frame, no comparison; the classifier's own nil arm"},
		{"canonical: scoped hold", &scopedHold, &withPrompt, true,
			"a scoped operand is held before retrieval and is exactly the empty-pool case this guard exists for"},
		{"boundary-1: one operand", &oneOperand, &withPrompt, false,
			"below the cut; the resolver never dispatched a comparison for it"},
		{"boundary+1: three operands", &threeOperands, &withPrompt, false,
			"above the cut, for the same reason"},
		{"empty container: zero operands", &zeroOperands, &withPrompt, false,
			"an explicit set with no operands is out of cut on count"},
		{"out of vocabulary: an operand whose kind the question never stated", &unstatedKind, &withPrompt, false,
			"unstated kind is its own out-of-cut admission and must not be read as a hold"},
		{"duplicate: both operands name the same term", &duplicateOperands, &withPrompt, true,
			"duplicate TERMS are still two in-cut named operands; distinctness is a publication rule, not a classification one"},
		{"wrong shape: operands with neither variant pointer", &variantlessOperands, &withPrompt, false,
			"a frame that never passed I1 contributes positioned slots with no kind, which puts it out of cut"},
		{"wrong container: subject expression is not an explicit set", &notAnExplicitSet, &withPrompt, false,
			"the classifier refuses on Kind before operands are read"},
		{"null: explicit set with a nil body", &explicitSetWithNilBody, &withPrompt, false,
			"an explicit set carrying no operand list is not a comparison"},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			if got := comparisonHeldWithoutCandidates(cell.frame, cell.resolution); got != cell.want {
				t.Errorf("comparisonHeldWithoutCandidates() = %t, want %t -- %s", got, cell.want, cell.why)
			}
		})
		executed++
	}
	if executed == 0 {
		t.Fatal("the domain table executed no cells")
	}
}

// ---------------------------------------------------------------------------
// GUARD 2 -- the terminal this seam returns, over the whole answerability grid
// ---------------------------------------------------------------------------

// TestHeldComparisonTerminalOverTheWholeAnswerabilityGrid is the 2x2 of
// AllowClarification and redeemability, on a held comparison AND on the
// ordinary empty pool beside it.
//
// THE NON-COMPARISON ROWS ARE THE CONTROL, and they are why this table is
// worth more than four assertions about the new branch. The change adds an arm
// ahead of an existing one; a table covering only the new arm cannot show that
// the old one still decides everything else, which is the property a reader
// actually needs.
//
// EACH ROW STATES ITS EXPECTED LIMITATION AS WELL AS ITS STATUS. Two rows here
// return the same status for different reasons, and a table asserting status
// alone would pass while the caller was told the wrong thing about their own
// question -- the exact failure the limitation constants exist to prevent.
func TestHeldComparisonTerminalOverTheWholeAnswerabilityGrid(t *testing.T) {
	t.Parallel()

	heldFrame := comparisonFrame(
		comparisonNamedOperand(heldTermA, SubjectTeam),
		comparisonNamedOperand(heldTermB, SubjectTeam),
	)

	executed := 0
	for _, cell := range []struct {
		name           string
		frame          *QuestionFrame
		resolution     SubjectResolution
		allow          bool
		redeemable     bool
		wantStatus     InvestigationStatus
		wantLimitation string
		why            string
	}{
		{
			name:  "held, clarification allowed, another channel can carry the answer",
			frame: heldFrame_(heldFrame), resolution: heldResolution(heldPrompt, 0),
			allow: true, redeemable: true,
			wantStatus: InvestigationClarificationRequired, wantLimitation: comparisonHeldLimitation,
			why: "the caller can answer, so the pair is asked about and the projection carries the completion action",
		},
		{
			name:  "held, clarification allowed, every offer channel empty",
			frame: heldFrame_(heldFrame), resolution: heldResolution(heldPrompt, 0),
			allow: true, redeemable: false,
			wantStatus: InvestigationNoMatch, wantLimitation: comparisonHeldUnanswerableLimitation,
			why: "asking a question nothing can redeem is the unanswerable clarification composition refuses outright",
		},
		{
			name:  "held, clarification refused by the caller, answer channel available",
			frame: heldFrame_(heldFrame), resolution: heldResolution(heldPrompt, 0),
			allow: false, redeemable: true,
			wantStatus: InvestigationNoMatch, wantLimitation: comparisonHeldNoClarificationLimitation,
			why: "the caller declined the question; redeemability cannot override that and the prose must say which reason applied",
		},
		{
			name:  "held, clarification refused by the caller, every offer channel empty",
			frame: heldFrame_(heldFrame), resolution: heldResolution(heldPrompt, 0),
			allow: false, redeemable: false,
			wantStatus: InvestigationNoMatch, wantLimitation: comparisonHeldNoClarificationLimitation,
			why: "both reasons apply and the caller's refusal is the one they can act on, so it is the one reported",
		},
		{
			name:  "held frame but publication wrote no prompt",
			frame: heldFrame_(heldFrame), resolution: heldResolution("", 0),
			allow: true, redeemable: true,
			wantStatus: InvestigationNoMatch, wantLimitation: noMatchLimitationForEmptyPool(&SubjectResolution{}),
			why: "without the prompt there is no evidence the comparison path produced this pool, so the ordinary empty-pool arm decides",
		},
		{
			name:  "not a comparison: the withheld-pool arm still clarifies",
			frame: nil, resolution: heldResolution(OfferPoolEmptiedClarificationPrompt, 0),
			allow: true, redeemable: true,
			wantStatus: InvestigationClarificationRequired, wantLimitation: clarificationRequiredLimitationOne,
			why: "the arm this change is placed ahead of must be untouched for every shape that is not a held comparison",
		},
		{
			name:  "not a comparison: the withheld-pool arm still downgrades when nothing is redeemable",
			frame: nil, resolution: heldResolution(OfferPoolEmptiedClarificationPrompt, 0),
			allow: true, redeemable: false,
			wantStatus: InvestigationNoMatch, wantLimitation: noMatchLimitationOfferPoolEmptied,
			why: "the existing answerability downgrade keeps its own limitation rather than borrowing the comparison's",
		},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			resolution := cell.resolution
			request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: cell.allow}}
			status, limitation := resolveTerminalStatus(request, &resolution, cell.frame, cell.redeemable)
			if status != cell.wantStatus {
				t.Errorf("status = %q, want %q -- %s", status, cell.wantStatus, cell.why)
			}
			if limitation != cell.wantLimitation {
				t.Errorf("limitation = %q, want %q -- %s", limitation, cell.wantLimitation, cell.why)
			}
			// THE PROMPT IS NEVER CLEARED, on any arm. It is what
			// subjectlessTerminalReason reads to tell a withheld pool from an
			// empty one, so a downgrade that dropped it would collapse that
			// distinction in the telemetry.
			if resolution.ClarificationPrompt != cell.resolution.ClarificationPrompt {
				t.Errorf("the prompt was rewritten to %q -- the terminal decision reads the resolution, it does not edit it", resolution.ClarificationPrompt)
			}
		})
		executed++
	}
	if executed == 0 {
		t.Fatal("the answerability grid executed no cells")
	}
}

// TestEveryHeldComparisonLimitationIsDistinct is the pin the three-way split
// needs and the grid above cannot give it: two arms returning the same status
// are told apart only by their prose, so prose that coincided would make one
// of them unobservable while every status assertion still passed.
func TestEveryHeldComparisonLimitationIsDistinct(t *testing.T) {
	t.Parallel()

	seen := map[string]string{}
	for name, limitation := range map[string]string{
		"held, asked":                     comparisonHeldLimitation,
		"held, clarification refused":     comparisonHeldNoClarificationLimitation,
		"held, nothing to redeem":         comparisonHeldUnanswerableLimitation,
		"withheld pool, asked":            clarificationRequiredLimitationOne,
		"withheld pool, nothing to offer": noMatchLimitationOfferPoolEmptied,
	} {
		if strings.TrimSpace(limitation) == "" {
			t.Errorf("%s carries an empty limitation -- a terminal that explains nothing is worse than no terminal", name)
		}
		if other, duplicate := seen[limitation]; duplicate {
			t.Errorf("%s and %s carry byte-identical prose -- an arm indistinguishable from another arm cannot be observed to have fired", name, other)
		}
		seen[limitation] = name
	}
}

// heldFrame_ returns a pointer to a copy, so parallel subtests sharing one
// fixture cannot alias a frame another subtest could mutate.
func heldFrame_(frame QuestionFrame) *QuestionFrame {
	copied := frame
	return &copied
}
