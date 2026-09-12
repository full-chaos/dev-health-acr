package contextfabric

import (
	"errors"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-5637: a clarification the caller cannot answer is not a
// clarification.
//
// WHAT WAS MEASURED, on the 2026-09-12 yardstick of record
// (~/.cache/acr-kiac-askdev/proofs/2026-09-12-main-a5f44c7f, 36 rows x 3
// replicates): 76 of 254 clarification_required turns -- 13 distinct rows,
// every replicate -- carried NO redeemable offer of any kind. Not a window
// option, not a kind/anchor/handle/candidate option, not a subject
// candidate. Six offer channels, all empty, on a status whose entire
// contract is "send one of these back and I will proceed".
//
// The caller's only move is to re-ask the bare question. That re-ask
// arrives with no receipts, so every need already satisfied on an earlier
// turn is raised again from scratch, and the exchange alternates between
// the window ask and this one until the turn budget runs out: 29 of the 34
// (row, replicate) pairs that exhausted their turns contain at least one
// such turn. Four turns of the five were spent re-deriving state the
// conversation had already established.
//
// THE ONE PRODUCER, and it is the same one on all 76: graphrank's
// offer-pool exclusion (resolution.go) withholds every candidate that
// matched by semantic similarity alone and signals the withholding by
// pairing an empty candidate list with OfferPoolEmptiedClarificationPrompt
// (unresolved.go). resolveTerminalStatus reads that pairing and returns
// clarification_required. The exclusion is right -- identifying a subject
// by vector proximity is not identification -- and so is refusing to
// collapse a withheld pool into the same no_match a genuinely empty graph
// produces. What was wrong is the status: the turn asks a question it has
// supplied no means of answering.
//
// WHAT THIS FILE DECIDES, and deliberately no more. It decides ANSWERABILITY
// -- whether the document a caller is about to receive carries at least one
// receipt they can redeem -- and it decides it in ONE place, so the status
// and the offers cannot disagree. It does not decide which candidates may be
// offered (graphrank's exclusion is untouched), which axes a family may
// raise (GateOffersByFamily is untouched), or what a terminal says (the
// limitation constants are untouched but for the one new arm this outcome
// needs).
//
// WHAT IT COSTS, stated plainly because it is not a gain: the rows this
// changes move from clarification_required to no_match. They do not become
// answered. Every one of the 76 turns was measured with an empty cohort, and
// on 8 of the 13 rows every plan requirement was already unavailable before
// any fact read. There was no answer behind the clarification to reach. What
// this buys is a truthful terminal on turn one instead of five turns of an
// unanswerable ask -- and a caller that can trust the status.

// ErrUnanswerableClarification is the sentinel form of the invariant this
// file exists to hold: a composed result carrying
// clarification_required and no redeemable offer.
//
// It is asserted, not returned, on today's control flow -- the one producer
// above is fixed at its source, so no path reaches it. It exists for the
// same reason ErrNoInvestigationSubjects does (unresolved.go): "unreachable"
// is a claim about today's control flow, and a future path that breaks it
// must fail as a NAMED condition rather than quietly serving a caller a
// question they cannot answer. A named error is also what keeps the claim
// TESTABLE -- a test can assert the sentinel, where it could only assert the
// absence of a log line.
var ErrUnanswerableClarification = errors.New("context fabric clarification carries no redeemable offer")

// offerMaterialRedeemable reports whether material carries at least one
// OPTION a caller can send a receipt back for.
//
// OPTIONS, NEVER Missing. StructureNeedsWouldDisclose (structure.go) answers
// a different question -- whether anything is worth disclosing at all -- and
// it is true for a Missing row with an empty option list, which the standing
// zero-candidates ruling (chaos3900_structure_offers.go) deliberately allows.
// A Missing row alone tells a caller WHAT is wanted and gives them nothing to
// answer it with, so counting it here would let exactly the class of turn
// this invariant forbids through the check that exists to catch it.
func offerMaterialRedeemable(material StructureOfferMaterial) bool {
	return len(material.KindOptions) > 0 ||
		len(material.AnchorOptions) > 0 ||
		len(material.HandleOptions) > 0 ||
		len(material.CandidateOptions) > 0
}

// windowOfferRedeemable reports whether a composed window clarification
// carries at least one option. nil and empty are the same answer, mirroring
// composeWindowClarification's own nil-means-nothing-in-play convention.
func windowOfferRedeemable(clarification *contractsv1.ContextFabricWindowClarification) bool {
	return clarification != nil && len(clarification.Options) > 0
}

// clarificationOffersRedeemable is the predicate resolveTerminalStatus
// consults for the ONE branch that can otherwise compose an unanswerable
// clarification: zero candidates, plus a prompt from the offer-pool
// exclusion.
//
// The subject candidates are checked here alongside the other two channels
// even though resolveTerminalStatus's own zero-candidate branch has already
// established they are absent. The predicate is about the DOCUMENT, not
// about one call site's control flow, and a predicate that answered
// correctly only under its caller's precondition would be wrong the first
// time a second caller asked it.
// CHAOS-5660 EXTENDS IT FROM "AN OPTION" TO "A SATISFYING OPTION", and the
// extension is a second conjunct rather than a rewrite: a turn is answerable
// only if it carries a redeemable offer AND -- when its frame declared a kind
// -- at least one offered option carries that kind. The first conjunct is
// CHAOS-5637's, unchanged, and it still decides every turn whose frame
// declared nothing; the second decides the turns that offered five candidate
// options and twenty handle options against a question about a project and
// carried no project among any of them. See
// chaos5660_declared_kind_terminal.go for what was measured, and for why a
// window option cannot satisfy a declared KIND need.
//
// THE DECISION IS PASSED IN, already taken, rather than derived here. The
// caller takes it once and hands the SAME value to this predicate and to the
// log line that reports it, so the status and the line can never describe two
// different turns -- the same discipline that already makes the caller, not
// this file, the authority on gated material.
func clarificationOffersRedeemable(
	resolution SubjectResolution,
	material StructureOfferMaterial,
	windowClarification *contractsv1.ContextFabricWindowClarification,
	declaredKind declaredKindDecision,
) bool {
	if declaredKind.Unsatisfiable {
		return false
	}
	return len(resolution.Candidates) > 0 ||
		offerMaterialRedeemable(material) ||
		windowOfferRedeemable(windowClarification)
}

// resultOffersRedeemable is the same question asked of a COMPOSED result,
// which is what the assertion below can actually see.
//
// It reads the six channels a caller reads, off the served document itself,
// rather than the material the document was built from: an invariant checked
// against the builder's inputs proves the builder consistent with itself,
// which is not the property owed here. The channels are the wire's, so this
// stays correct for any future producer that composes a result some other
// way.
func resultOffersRedeemable(result InvestigationResult) bool {
	if len(result.SubjectResolution.Candidates) > 0 {
		return true
	}
	if windowOfferRedeemable(result.WindowClarification) {
		return true
	}
	needs := result.StructureNeeds
	if needs == nil {
		return false
	}
	return len(needs.WindowOptions) > 0 ||
		len(needs.KindOptions) > 0 ||
		len(needs.AnchorOptions) > 0 ||
		len(needs.HandleOptions) > 0 ||
		len(needs.CandidateOptions) > 0
}

// RepairLegacyUnanswerableClarification brings a STORED row composed by an
// earlier build into current semantics, in place, and reports whether it
// changed anything.
//
// THE READ SIDE OF THE INVARIANT, and it is a separate surface from the
// write side rather than the same one reached twice. finalizeServed covers
// every path that COMPOSES a result. It does not cover the path that hands
// back a row composed months ago: the result-by-ID route reads a stored
// document, admits it under the deliberately lenient stored-read validator,
// and serves it -- and the MCP investigation_result tool forwards that same
// canonical response. Both were outside the guard, and both would have gone
// on serving exactly the 76-turn shape this ticket exists to end: a GET of
// such a row returns 200 with status clarification_required, zero
// candidates, no structure needs and no window clarification.
//
// A REPAIR, NOT A REFUSAL. The row is real and the caller asked for it by
// id; erroring the read would deny them a document they are entitled to
// over a defect in how it was labelled. So the status is corrected to the
// terminal it would be composed as today, and the clarification limitation
// is swapped for the one that describes what actually happened. Nothing is
// invented: every value written here is a pure function of fields the row
// already carries, which is the same standard the route's own legacy
// completeness backfill already meets a few lines above the call site.
//
// The offer channels are left exactly as they are, because there is nothing
// to correct in them -- their emptiness IS the condition. The prompt is left
// too, for the reason the write side leaves it: it is the only thing that
// distinguishes a withheld pool from an empty one, and it never reaches the
// answer sentence, which this function rewrites from the corrected status.
func RepairLegacyUnanswerableClarification(result *InvestigationResult) bool {
	if result == nil || result.Status != InvestigationClarificationRequired {
		return false
	}
	if resultOffersRedeemable(*result) {
		return false
	}
	result.Status = InvestigationNoMatch
	for index, limitation := range result.Limitations {
		if limitation == clarificationRequiredLimitationOne || limitation == clarificationRequiredLimitation {
			result.Limitations[index] = noMatchLimitationOfferPoolEmptied
		}
	}
	result.DeterministicAnswer = statusSentence(InvestigationNoMatch, result.SubjectResolution)
	return true
}

// assertAnswerableClarification holds the invariant on the document a route
// is about to serialize.
//
// Called immediately before Validate on every path that can compose a
// clarification_required terminal, which is the same placement rule the
// completeness stamp, the display-label stamp and the budget assertion on
// those paths already follow -- late enough to see the finished document,
// early enough that nothing has been persisted or served.
//
// A non-clarification result is never examined: no_match, degraded and the
// answer-bearing statuses make no offer and owe none.
func assertAnswerableClarification(result InvestigationResult) error {
	if result.Status != InvestigationClarificationRequired {
		return nil
	}
	if resultOffersRedeemable(result) {
		return nil
	}
	return ErrUnanswerableClarification
}
