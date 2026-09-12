package graphrank

// THE COMPARISON RUN'S OWN DECISIONS, at the unit these decisions are made.
//
// The engine-level arms in the falkorgraph package prove the behaviour end to
// end through a real adapter and a real engine. THIS file proves the ones that
// an end-to-end fixture can only reach by accident: the publication truth table
// with each condition falsified INDEPENDENTLY, the candidate budget's fairness
// across slots, and the prompt's bounds under inputs no realistic fixture
// produces. A truth table exercised only end to end tends to cover the row
// someone thought of and leave the rest to a comment.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func comparisonTestSubject(kind contextfabric.SubjectKind, id, label string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: label}
}

func comparisonTestCandidate(subject contextfabric.SubjectRef, receiptID string) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: receiptID, Subject: subject, State: contextfabric.ResolutionProposed,
		Confidence: 1, MatchedTerms: []string{subject.Label}, MatchReasons: []string{"Exact canonical subject label match."},
		EvidenceRefIDs: []string{},
	}
}

// resolvedSlot is a slot that met every condition: one winner, of the stated
// kind, retrieved for its own terms.
func resolvedSlot(position int, term string, subject contextfabric.SubjectRef) operandSlotRun {
	return operandSlotRun{
		slot: contextfabric.ComparisonOperandSlot{
			Position: position, Variant: contextfabric.ComparisonOperandNamed,
			Kind: subject.Kind, Terms: []string{term},
		},
		candidates: []contextfabric.SubjectCandidate{comparisonTestCandidate(subject, "candr_"+subject.CanonicalID)},
		committed:  []contextfabric.SubjectRef{subject},
		bases:      contextfabric.CommitBasisSet{},
		digests:    contextfabric.CommitDecisionDigestSet{},
	}
}

// emptySlot resolved nothing and retrieved nothing.
func emptySlot(position int, term string, kind contextfabric.SubjectKind) operandSlotRun {
	return operandSlotRun{
		slot: contextfabric.ComparisonOperandSlot{
			Position: position, Variant: contextfabric.ComparisonOperandNamed, Kind: kind, Terms: []string{term},
		},
		bases:   contextfabric.CommitBasisSet{},
		digests: contextfabric.CommitDecisionDigestSet{},
	}
}

func admittedRun(slots ...operandSlotRun) comparisonResolutionRun {
	return comparisonResolutionRun{admission: contextfabric.ComparisonAdmittedNamedPair, slots: slots}
}

var (
	comparisonAlpha = comparisonTestSubject(contextfabric.SubjectTeam, "team_alpha", "Alpha")
	comparisonBeta  = comparisonTestSubject(contextfabric.SubjectTeam, "team_beta", "Beta")
)

// TestPublicationReleasesOnlyWhenEveryConditionHolds is ruling 1A as a truth
// table, with EACH CONDITION FALSIFIED ON ITS OWN.
//
// The positive row is what makes the negative rows mean anything: without it,
// a publishable() that returned false unconditionally would pass every
// negative row in this table and the whole test would be measuring nothing.
func TestPublicationReleasesOnlyWhenEveryConditionHolds(t *testing.T) {
	t.Parallel()

	wrongKindWinner := resolvedSlot(1, "beta", comparisonBeta)
	wrongKindWinner.slot.Kind = contextfabric.SubjectProject // the question asked for a project

	overCommitted := resolvedSlot(1, "beta", comparisonBeta)
	overCommitted.committed = append(overCommitted.committed, comparisonAlpha)

	sameSubjectTwice := resolvedSlot(1, "beta", comparisonAlpha)

	withUnboundReceipt := admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), resolvedSlot(1, "beta", comparisonBeta))
	withUnboundReceipt.unboundReceipts = 1

	heldScoped := admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), resolvedSlot(1, "beta", comparisonBeta))
	heldScoped.admission = contextfabric.ComparisonHeldScopedOperand

	cases := []struct {
		name string
		run  comparisonResolutionRun
		want bool
	}{
		{
			name: "both operands resolved, distinct, no unbound receipt",
			run:  admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), resolvedSlot(1, "beta", comparisonBeta)),
			want: true,
		},
		{
			name: "one operand resolved and one empty",
			run:  admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), emptySlot(1, "beta", contextfabric.SubjectTeam)),
			want: false,
		},
		{
			name: "a winner of the wrong stated kind",
			run:  admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), wrongKindWinner),
			want: false,
		},
		{
			name: "a slot handed two subjects",
			run:  admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), overCommitted),
			want: false,
		},
		{
			name: "one subject winning BOTH slots",
			run:  admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), sameSubjectTwice),
			want: false,
		},
		{
			name: "an unbound carried selection",
			run:  withUnboundReceipt,
			want: false,
		},
		{
			name: "a pair the classifier held for its scoped operand",
			run:  heldScoped,
			want: false,
		},
		{
			name: "only one slot present",
			run:  admittedRun(resolvedSlot(0, "alpha", comparisonAlpha)),
			want: false,
		},
	}

	positives := 0
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := testCase.run.publishable(); got != testCase.want {
				t.Errorf("publishable() = %t, want %t", got, testCase.want)
			}
		})
		if testCase.want {
			positives++
		}
	}
	if positives == 0 {
		t.Fatal("the table has no publishable row -- every negative row would pass against a function that never publishes, and this test would measure nothing")
	}
}

// TestAHeldComparisonPublishesNoSubjectAndNoDigestButKeepsItsCandidates is the
// hold's published shape.
func TestAHeldComparisonPublishesNoSubjectAndNoDigestButKeepsItsCandidates(t *testing.T) {
	t.Parallel()

	run := admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), emptySlot(1, "beta", contextfabric.SubjectTeam))
	resolution, bases, digests := publishComparisonResolution(run, 10)

	if len(resolution.Committed) != 0 {
		t.Errorf("Committed = %v, want empty -- the resolved side is held with the unresolved one", resolution.Committed)
	}
	if len(digests) != 0 {
		t.Errorf("digests = %v, want none -- a digest describes a commit that was published, and nothing was", digests)
	}
	if len(bases) != 0 {
		t.Errorf("bases = %v, want none", bases)
	}
	// THE CANDIDATES AND THEIR REAL RECEIPT IDENTITIES SURVIVE. They are what
	// the user selects from, and a follow-up quotes the receipt id back.
	if len(resolution.Candidates) != 1 {
		t.Fatalf("Candidates = %d, want 1 -- holding must not discard the authorized candidate the user has to choose from", len(resolution.Candidates))
	}
	if got := resolution.Candidates[0].ReceiptID; got != "candr_team_alpha" {
		t.Errorf("candidate ReceiptID = %q, want the real one it was retrieved with -- a minted or dropped id breaks the follow-up that completes the answer", got)
	}
	if strings.TrimSpace(resolution.ClarificationPrompt) == "" {
		t.Error("a held comparison published an empty clarification prompt")
	}
}

// TestAPublishedComparisonKeepsFrameOperandOrderAndUnionsProofsFirstWriterWins
// pins the two determinism properties publication owns.
func TestAPublishedComparisonKeepsFrameOperandOrderAndUnionsProofsFirstWriterWins(t *testing.T) {
	t.Parallel()

	first := resolvedSlot(0, "alpha", comparisonAlpha)
	second := resolvedSlot(1, "beta", comparisonBeta)

	// BOTH slots carry a basis for the FIRST slot's subject. Only the first
	// slot's may survive: it describes the commit that actually released it.
	first.bases.Record(comparisonAlpha, contextfabric.CommitBasisAuthoritativeIdentity)
	second.bases.Record(comparisonAlpha, contextfabric.CommitBasisStatistical)
	second.bases.Record(comparisonBeta, contextfabric.CommitBasisAuthoritativeIdentity)

	resolution, bases, _ := publishComparisonResolution(admittedRun(first, second), 10)

	if len(resolution.Committed) != 2 {
		t.Fatalf("Committed = %d, want 2", len(resolution.Committed))
	}
	if resolution.Committed[0].CanonicalID != comparisonAlpha.CanonicalID {
		t.Errorf("Committed[0] = %q, want %q -- published order follows the frame's operand order, never a map walk",
			resolution.Committed[0].CanonicalID, comparisonAlpha.CanonicalID)
	}
	if resolution.Committed[1].CanonicalID != comparisonBeta.CanonicalID {
		t.Errorf("Committed[1] = %q, want %q", resolution.Committed[1].CanonicalID, comparisonBeta.CanonicalID)
	}
	if got := bases.For(comparisonAlpha); got != contextfabric.CommitBasisAuthoritativeIdentity {
		t.Errorf("basis for the first slot's subject = %q, want %q -- the second slot overwrote the first's proof, which is exactly what unioning by subject key exists to stop",
			got, contextfabric.CommitBasisAuthoritativeIdentity)
	}
	if strings.TrimSpace(resolution.ClarificationPrompt) != "" {
		t.Errorf("a fully resolved comparison published a clarification prompt %q", resolution.ClarificationPrompt)
	}
}

// TestTheCandidateBudgetIsSpentFairlyAcrossSlots is the fairness property, and
// the reason it is not first-slot-first.
//
// Without it a first operand with many rivals eats the whole budget and the
// user is asked to choose for one side of a comparison whose OTHER side has
// silently vanished from the offer.
func TestTheCandidateBudgetIsSpentFairlyAcrossSlots(t *testing.T) {
	t.Parallel()

	crowded := emptySlot(0, "alpha", contextfabric.SubjectTeam)
	for index := 0; index < 6; index++ {
		subject := comparisonTestSubject(contextfabric.SubjectTeam, "team_crowd_"+string(rune('a'+index)), "Crowd")
		crowded.candidates = append(crowded.candidates, comparisonTestCandidate(subject, "candr_crowd"))
	}
	lonely := resolvedSlot(1, "beta", comparisonBeta)

	resolution, _, _ := publishComparisonResolution(admittedRun(crowded, lonely), 4)

	if len(resolution.Candidates) != 4 {
		t.Fatalf("Candidates = %d, want the whole budget of 4", len(resolution.Candidates))
	}
	sawSecondSlot := false
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.CanonicalID == comparisonBeta.CanonicalID {
			sawSecondSlot = true
		}
	}
	if !sawSecondSlot {
		t.Errorf("the second operand's only candidate did not survive a budget of 4 against a first operand with 6 (published: %v) -- a budget spent first-slot-first offers the user a choice for one side of a comparison whose other side is not on the list",
			candidateSubjectKeysSorted(resolution.Candidates))
	}
	// And the first slot still leads, because slot order is the order.
	if resolution.Candidates[0].Subject.CanonicalID == comparisonBeta.CanonicalID {
		t.Error("the second operand's candidate leads the published list -- fairness interleaves, it does not reverse")
	}
}

// TestOneSubjectProposedByBothSlotsIsOfferedOnce keeps the offer honest: the
// same subject under two entries is the same choice twice.
func TestOneSubjectProposedByBothSlotsIsOfferedOnce(t *testing.T) {
	t.Parallel()

	first := emptySlot(0, "alpha", contextfabric.SubjectTeam)
	first.candidates = []contextfabric.SubjectCandidate{comparisonTestCandidate(comparisonAlpha, "candr_shared")}
	second := emptySlot(1, "beta", contextfabric.SubjectTeam)
	second.candidates = []contextfabric.SubjectCandidate{comparisonTestCandidate(comparisonAlpha, "candr_shared")}

	resolution, _, _ := publishComparisonResolution(admittedRun(first, second), 10)
	if len(resolution.Candidates) != 1 {
		t.Errorf("Candidates = %d, want 1 -- one subject proposed by both slots is one choice, not two", len(resolution.Candidates))
	}
}

// TestTheClarificationNamesBothOperandsAndTheRequiredAction is the prompt's
// contract, over the states that change its wording.
func TestTheClarificationNamesBothOperandsAndTheRequiredAction(t *testing.T) {
	t.Parallel()

	ambiguous := emptySlot(1, "beta", contextfabric.SubjectTeam)
	ambiguous.candidates = []contextfabric.SubjectCandidate{
		comparisonTestCandidate(comparisonBeta, "candr_b1"),
		comparisonTestCandidate(comparisonTestSubject(contextfabric.SubjectTeam, "team_beta_two", "Beta"), "candr_b2"),
	}

	scoped := operandSlotRun{slot: contextfabric.ComparisonOperandSlot{
		Position: 1, Variant: contextfabric.ComparisonOperandScoped,
		Kind: contextfabric.SubjectTeam, Terms: []string{"infrastructure"},
	}}

	cases := []struct {
		name         string
		run          comparisonResolutionRun
		wantNames    []string
		wantPhrase   string
		phraseWhy    string
		absentPhrase string
	}{
		{
			name:         "a missing side is asked to be RESTATED, not selected",
			run:          admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), emptySlot(1, "beta", contextfabric.SubjectTeam)),
			wantNames:    []string{"Alpha", "beta"},
			wantPhrase:   "Restate",
			phraseWhy:    "there is nothing to select from, and asking someone to choose from an empty set is worse than asking them to say it again",
			absentPhrase: "Say which one",
		},
		{
			name:         "an ambiguous side is asked to be SELECTED",
			run:          admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), ambiguous),
			wantNames:    []string{"Alpha", "beta"},
			wantPhrase:   "Say which one",
			phraseWhy:    "there are real candidates to choose between",
			absentPhrase: "Restate",
		},
		{
			name:         "a scoped side names the group and refuses the form",
			run:          comparisonResolutionRun{admission: contextfabric.ComparisonHeldScopedOperand, slots: []operandSlotRun{resolvedSlot(0, "alpha", comparisonAlpha), scoped}},
			wantNames:    []string{"Alpha", "infrastructure"},
			wantPhrase:   "Name a single subject",
			phraseWhy:    "the anchor is a retrieval pointer, and resolving it into an operand is the substitution this cut refuses",
			absentPhrase: "Restate",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			prompt := comparisonClarificationPrompt(testCase.run)

			for _, name := range testCase.wantNames {
				if !strings.Contains(prompt, name) {
					t.Errorf("prompt %q does not name %q -- a two-subject request answered with a one-subject question is the reported harm", prompt, name)
				}
			}
			if !strings.Contains(prompt, testCase.wantPhrase) {
				t.Errorf("prompt %q carries no %q instruction -- %s", prompt, testCase.wantPhrase, testCase.phraseWhy)
			}
			if strings.Contains(prompt, testCase.absentPhrase) {
				t.Errorf("prompt %q carries the %q instruction, which belongs to a different state", prompt, testCase.absentPhrase)
			}
			if strings.TrimSpace(prompt) != prompt {
				t.Errorf("prompt %q is not trimmed -- the contract validates that separately from the length and would reject it", prompt)
			}
		})
	}
}

// TestAnUnboundSelectionAddsOneSentenceAndNeverReplacesTheAction pins that the
// optional explanation is optional, appended, and never displaces the part
// that lets the user finish.
func TestAnUnboundSelectionAddsOneSentenceAndNeverReplacesTheAction(t *testing.T) {
	t.Parallel()

	base := admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), emptySlot(1, "beta", contextfabric.SubjectTeam))
	withoutReceipt := comparisonClarificationPrompt(base)

	held := base
	held.unboundReceipts = 1
	withReceipt := comparisonClarificationPrompt(held)

	if withReceipt == withoutReceipt {
		t.Fatal("an unbound carried selection changed nothing in the prompt -- the user is never told their selection completed neither operand")
	}
	if !strings.HasPrefix(withReceipt, withoutReceipt) {
		t.Errorf("the unbound-selection sentence did not APPEND to the required part:\n without = %q\n with    = %q", withoutReceipt, withReceipt)
	}
	if !strings.Contains(withReceipt, "Restate") {
		t.Error("the required action did not survive the optional explanation")
	}
}

// TestThePromptStaysInsideTheExistingBoundOnAdversarialInput is the bounds
// property, and it is the arm that would catch a byte budget.
//
// The contract counts RUNES, so a byte-budgeted truncation would split a
// multibyte label mid-rune and produce a string the validator rejects for a
// reason that looks unrelated to length. Both operand names here are long AND
// multibyte.
func TestThePromptStaysInsideTheExistingBoundOnAdversarialInput(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("Ünïcödé-Plätform-", 400)
	first := resolvedSlot(0, long, comparisonTestSubject(contextfabric.SubjectTeam, "team_long", long))
	second := emptySlot(1, long, contextfabric.SubjectTeam)

	run := admittedRun(first, second)
	run.unboundReceipts = 1
	prompt := comparisonClarificationPrompt(run)

	if got := utf8.RuneCountInString(prompt); got > comparisonPromptMaxRunes {
		t.Errorf("prompt is %d runes, over the existing published bound of %d -- this work does not widen it", got, comparisonPromptMaxRunes)
	}
	if !utf8.ValidString(prompt) {
		t.Error("the prompt is not valid UTF-8 -- a byte budget split a rune, and the contract counts runes")
	}
	if strings.TrimSpace(prompt) != prompt {
		t.Errorf("prompt is not trimmed after truncation")
	}
	// THE ACTION SURVIVES. Space for both descriptions and the action is
	// reserved before any optional explanation, so the long names must be what
	// gets cut, never the instruction that lets the user finish.
	if !strings.Contains(prompt, "Restate") {
		t.Errorf("the required action did not survive two maximal operand names -- the part that lets the user finish is the part that got truncated:\n%q", prompt)
	}
}

// TestAggregateDegradationIsTrueWhenEitherOperandDegraded pins that one
// operand's degraded retrieval is the comparison's.
func TestAggregateDegradationIsTrueWhenEitherOperandDegraded(t *testing.T) {
	t.Parallel()

	healthy := admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), resolvedSlot(1, "beta", comparisonBeta))
	if healthy.retrievalDegraded() {
		t.Error("a run with two healthy slots reports degraded -- the positive control for this property is gone")
	}

	degradedSecond := resolvedSlot(1, "beta", comparisonBeta)
	degradedSecond.retrievalDegraded = true
	if !admittedRun(resolvedSlot(0, "alpha", comparisonAlpha), degradedSecond).retrievalDegraded() {
		t.Error("only the SECOND operand's retrieval degraded and the aggregate reports healthy -- evidence missing from either side is missing from the answer")
	}

	degradedFirst := resolvedSlot(0, "alpha", comparisonAlpha)
	degradedFirst.retrievalDegraded = true
	if !admittedRun(degradedFirst, resolvedSlot(1, "beta", comparisonBeta)).retrievalDegraded() {
		t.Error("only the FIRST operand's retrieval degraded and the aggregate reports healthy")
	}
}

// TestSlotStateIsDerivedFromContentsRatherThanStored walks the state
// vocabulary, and fails at zero so a table that stopped reaching its
// assertions cannot read as green.
func TestSlotStateIsDerivedFromContentsRatherThanStored(t *testing.T) {
	t.Parallel()

	wrongKind := resolvedSlot(0, "alpha", comparisonAlpha)
	wrongKind.slot.Kind = contextfabric.SubjectProject

	over := resolvedSlot(0, "alpha", comparisonAlpha)
	over.committed = append(over.committed, comparisonBeta)

	ambiguous := emptySlot(0, "alpha", contextfabric.SubjectTeam)
	ambiguous.candidates = []contextfabric.SubjectCandidate{
		comparisonTestCandidate(comparisonAlpha, "candr_a"), comparisonTestCandidate(comparisonBeta, "candr_b"),
	}

	scoped := operandSlotRun{slot: contextfabric.ComparisonOperandSlot{
		Position: 0, Variant: contextfabric.ComparisonOperandScoped, Kind: contextfabric.SubjectTeam, Terms: []string{"anchor"},
	}}
	// A scoped slot reports scoped even when something was committed to it:
	// the variant decides, so a later change that started resolving anchors
	// cannot hide behind a resolved-looking state.
	scopedWithWinner := scoped
	scopedWithWinner.committed = []contextfabric.SubjectRef{comparisonAlpha}

	cases := map[operandSlotState]operandSlotRun{
		operandSlotResolved:      resolvedSlot(0, "alpha", comparisonAlpha),
		operandSlotNoCandidate:   emptySlot(0, "alpha", contextfabric.SubjectTeam),
		operandSlotAmbiguous:     ambiguous,
		operandSlotWrongKind:     wrongKind,
		operandSlotOverCommitted: over,
		operandSlotScoped:        scopedWithWinner,
	}

	checked := 0
	for want, run := range cases {
		if got := run.state(); got != want {
			t.Errorf("state() = %q, want %q", got, want)
		}
		checked++
	}
	if checked != len(cases) {
		t.Fatalf("only %d of %d states reached the assertion", checked, len(cases))
	}
}

// candidateSubjectKeysSorted is a TEST helper for assertions about set
// membership. It is named for its sorting so no reader mistakes it for the
// published order, which is the property other arms pin.
func candidateSubjectKeysSorted(candidates []contextfabric.SubjectCandidate) []string {
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, SubjectKey(candidate.Subject))
	}
	return keys
}
