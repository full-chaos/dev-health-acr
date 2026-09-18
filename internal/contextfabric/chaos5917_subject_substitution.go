package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5917: a turn that names a parent result answers ABOUT the subject
// that parent committed, unless the user picked a different one. The
// invariant this file enforces is one sentence: a turn carrying a parent
// reference never SERVES a subject the parent did not commit without the
// user having chosen it.
//
// What went wrong without it. The parent turn commits one repository. The
// follow-up names that parent, carries no subject hint of its own, and its
// OWN text resolves to a different repository. Every existing check passes:
// the per-need ledger correctly reports the question changed and drops the
// remembered members (chaos5639_confirmed_need.go), resolution commits the
// other repository on a proven identity, DecideCountPopulationScope reads
// anchor_committed over it, and the engine serves that other repository's
// count with status=complete. The reader believes they are still being told
// about the first repository. Nothing in the served document, and nothing in
// the ledger's own drop reason, says the subject moved.
//
// ORIGIN-BLIND, DELIBERATELY. The comparison is between the parent's
// committed subject identity and THIS turn's committed subject identity. It
// does not ask what produced this turn's identity -- a caller hint, a prior
// receipt, or the question's own words all reach the same commit and all
// substitute the same way. Origin is REPORTED (SubjectSubstitutionOrigin,
// on the ledger line) so the three populations stay countable apart, and it
// is never an input to the decision. Keying the guard on origin was the
// shape this defect already defeated once: the resolver-origin case is
// exactly the one a hint-shaped predicate does not see.
//
// THE ONE PERMITTED SUBSTITUTION is a choice the user actually made: a
// prior-subject receipt, redeemed this turn, that was minted by the very
// parent this turn names. That is the redemption of an offer the engine
// itself raised -- the clarification below being answered -- so treating it
// as a substitution would make every clarification this guard raises
// unanswerable, which is the failure class chaos5637_answerable_clarification.go
// exists to end. A receipt from any OTHER result is not that: it says
// nothing about the exchange this turn continues.
//
// CONSERVATIVE ON ABSENCE. No parent reference, a parent that does not read
// cleanly, a parent that committed no anchor, or a turn that commits nothing
// -- each reports its own outcome and serves exactly as it did before. The
// guard never invents comparison evidence it does not hold, and never
// refuses a turn because evidence was missing.

// SubjectSubstitutionOutcome is the closed vocabulary of what the guard
// decided for one Investigate call. Every member names a distinct state an
// operator must be able to tell apart from the trace alone: the five
// not-applicable states differ in WHY there was nothing to compare, and the
// three firing states differ in what the caller was handed instead.
type SubjectSubstitutionOutcome string

const (
	// SubjectSubstitutionNotEvaluated: the turn ended before the guard's own
	// decision point (an early gate, a reuse serve, a hard error). Set as
	// the explicit default at the top of Investigate rather than left as the
	// Go zero value, for the same reason CaptureSkipReasonNotApplicable is:
	// a line that reads "" cannot be told apart from a field nobody wrote.
	SubjectSubstitutionNotEvaluated SubjectSubstitutionOutcome = "not_evaluated"
	// SubjectSubstitutionNoParentReference: this turn names no parent, so
	// there is no parent-committed subject it could contradict.
	SubjectSubstitutionNoParentReference SubjectSubstitutionOutcome = "no_parent_reference"
	// SubjectSubstitutionParentUnreadable: this turn names a parent whose
	// stored semantic state did not read cleanly. A DIFFERENT fact from a
	// parent that read cleanly and held no subject -- an operator who can
	// only see "nothing to compare" cannot tell a storage or decode defect
	// apart from an ordinary first turn.
	SubjectSubstitutionParentUnreadable SubjectSubstitutionOutcome = "parent_unreadable"
	// SubjectSubstitutionParentNoIdentity: the named parent read cleanly and
	// committed no subject as its own scope anchor, so it asserted no
	// identity for this turn to contradict.
	SubjectSubstitutionParentNoIdentity SubjectSubstitutionOutcome = "parent_no_identity"
	// SubjectSubstitutionNoCommittedSubject: the parent held an identity and
	// THIS turn's resolution bound none to the frame's anchor. Nothing is
	// being served about a different subject, because nothing is being
	// served about a subject at all.
	SubjectSubstitutionNoCommittedSubject SubjectSubstitutionOutcome = "no_committed_subject"
	// SubjectSubstitutionSameSubject: both sides bound the same (kind,
	// canonical id). The ordinary continuation, served unchanged.
	SubjectSubstitutionSameSubject SubjectSubstitutionOutcome = "same_subject"
	// SubjectSubstitutionRedeemedChoice: the identities differ AND THIS
	// SUBJECT is the one the caller redeemed this turn, from a prior-subject
	// receipt the named parent itself minted. The user picked it, so it is
	// served -- and the trace says so, distinctly from every not-applicable
	// state, because a served subject change is exactly the thing an
	// operator must be able to count.
	SubjectSubstitutionRedeemedChoice SubjectSubstitutionOutcome = "redeemed_choice"
	// SubjectSubstitutionClarified: the identities differ, the caller
	// accepts a clarification, and the remembered subject re-read and
	// re-authorized, so both are offered with the remembered one first.
	// Nothing is served.
	SubjectSubstitutionClarified SubjectSubstitutionOutcome = "clarified_subject_changed"
	// SubjectSubstitutionClarifiedRememberedUnavailable: the identities
	// differ and the caller accepts a clarification, but the remembered
	// subject could not be re-read and re-authorized for this principal at
	// this turn's binding, so it is NOT offered. The clarification carries
	// only what this turn proposed, and still serves nothing -- an
	// unreadable memory is a reason to stop, never a reason to serve the
	// substitute.
	SubjectSubstitutionClarifiedRememberedUnavailable SubjectSubstitutionOutcome = "clarified_remembered_unavailable"
	// SubjectSubstitutionRefused: the identities differ and the caller
	// declined clarification, so the turn ends with the reason stated
	// instead of an answer about a subject the caller never asked for. Both
	// identities are still listed, the remembered one first, so a caller
	// that cannot be asked a question can still see what it would have been
	// asked.
	SubjectSubstitutionRefused SubjectSubstitutionOutcome = "refused_subject_changed"
	// SubjectSubstitutionRefusedRememberedUnavailable is that same refusal
	// for a turn whose remembered subject could not be re-read and
	// re-authorized, so only this turn's own proposal is listed. Split from
	// the member above for the reason the clarifying pair is split: which
	// identities a turn actually listed is not recoverable from
	// "refused_subject_changed" alone.
	SubjectSubstitutionRefusedRememberedUnavailable SubjectSubstitutionOutcome = "refused_remembered_unavailable"
)

// subjectSubstitutionOutcomes is the closed vocabulary in declared order.
var subjectSubstitutionOutcomes = [...]SubjectSubstitutionOutcome{
	SubjectSubstitutionNotEvaluated,
	SubjectSubstitutionNoParentReference,
	SubjectSubstitutionParentUnreadable,
	SubjectSubstitutionParentNoIdentity,
	SubjectSubstitutionNoCommittedSubject,
	SubjectSubstitutionSameSubject,
	SubjectSubstitutionRedeemedChoice,
	SubjectSubstitutionClarified,
	SubjectSubstitutionClarifiedRememberedUnavailable,
	SubjectSubstitutionRefused,
	SubjectSubstitutionRefusedRememberedUnavailable,
}

// SubjectSubstitutionOutcomeCount is the closed vocabulary's size.
const SubjectSubstitutionOutcomeCount = len(subjectSubstitutionOutcomes)

// SubjectSubstitutionOutcomeVocabulary returns the closed vocabulary in
// declared order, for the telemetry specification to read.
func SubjectSubstitutionOutcomeVocabulary() [SubjectSubstitutionOutcomeCount]SubjectSubstitutionOutcome {
	return subjectSubstitutionOutcomes
}

// ValidSubjectSubstitutionOutcome reports membership in the closed vocabulary.
func ValidSubjectSubstitutionOutcome(value SubjectSubstitutionOutcome) bool {
	for _, member := range subjectSubstitutionOutcomes {
		if member == value {
			return true
		}
	}
	return false
}

// Fired reports whether this outcome ended the turn without serving. The ONE
// definition, so the engine's control flow and any later counter cannot
// disagree about which members stop a turn.
func (o SubjectSubstitutionOutcome) Fired() bool {
	switch o {
	case SubjectSubstitutionClarified, SubjectSubstitutionClarifiedRememberedUnavailable,
		SubjectSubstitutionRefused, SubjectSubstitutionRefusedRememberedUnavailable:
		return true
	default:
		return false
	}
}

// SubjectSubstitutionOrigin names WHICH CHANNEL AT THE PRODUCER carried
// this turn's committed subject into resolution. The domain is that channel
// set and nothing else: the hints resolvePriorSubjectHints redeemed from the
// caller's prior-subject receipts, the hints the caller's own
// RequestedScope.SubjectHints carried, and neither -- which leaves
// resolution's own reach over the question as the only thing that could have
// produced it.
//
// Reported, never consulted by the decision -- see this file's own doc
// comment for why. Enumerated separately so the three populations stay
// countable apart on the ledger line.
type SubjectSubstitutionOrigin string

const (
	// SubjectSubstitutionOriginNotApplicable: this turn bound no subject to
	// the frame's anchor, so nothing has an origin.
	SubjectSubstitutionOriginNotApplicable SubjectSubstitutionOrigin = "not_applicable"
	// SubjectSubstitutionOriginPriorReceipt: the subject's canonical id
	// reached resolution as one of the hints resolvePriorSubjectHints
	// redeemed from this turn's own prior-subject receipts.
	SubjectSubstitutionOriginPriorReceipt SubjectSubstitutionOrigin = "prior_receipt"
	// SubjectSubstitutionOriginCallerHint: the caller's own request carried
	// this canonical id in RequestedScope.SubjectHints, and no redeemed
	// receipt did.
	SubjectSubstitutionOriginCallerHint SubjectSubstitutionOrigin = "caller_hint"
	// SubjectSubstitutionOriginEngineCarry: no caller-sourced channel
	// carried it; the engine's own carried subject_anchor ledger entry
	// (appliedNeeds) names this identity. Read AFTER the two caller channels
	// because a caller's own hint supersedes a carry the engine merely
	// remembered, which is the precedence the injection site already applies.
	SubjectSubstitutionOriginEngineCarry SubjectSubstitutionOrigin = "engine_carry"
	// SubjectSubstitutionOriginResolver: NONE of the three channels carried
	// it, so resolution reached this subject on its own, over the question.
	// The origin a hint-shaped predicate cannot see, and the one the executed
	// defect this guard closes travels on.
	SubjectSubstitutionOriginResolver SubjectSubstitutionOrigin = "resolver"
)

// subjectSubstitutionOrigins is the closed vocabulary in declared order.
var subjectSubstitutionOrigins = [...]SubjectSubstitutionOrigin{
	SubjectSubstitutionOriginNotApplicable,
	SubjectSubstitutionOriginPriorReceipt,
	SubjectSubstitutionOriginCallerHint,
	SubjectSubstitutionOriginEngineCarry,
	SubjectSubstitutionOriginResolver,
}

// SubjectSubstitutionOriginCount is the closed vocabulary's size.
const SubjectSubstitutionOriginCount = len(subjectSubstitutionOrigins)

// SubjectSubstitutionOriginVocabulary returns the closed vocabulary in
// declared order, for the telemetry specification to read.
func SubjectSubstitutionOriginVocabulary() [SubjectSubstitutionOriginCount]SubjectSubstitutionOrigin {
	return subjectSubstitutionOrigins
}

// ValidSubjectSubstitutionOrigin reports membership in the closed vocabulary.
func ValidSubjectSubstitutionOrigin(value SubjectSubstitutionOrigin) bool {
	for _, member := range subjectSubstitutionOrigins {
		if member == value {
			return true
		}
	}
	return false
}

// parentAnchorEvidence is what the named parent asserted, retained for
// comparison EVEN WHEN the per-need ledger refused to admit it. Those are
// two different questions: admission asks whether the parent's remembered
// members may be APPLIED to this turn, and this asks whether this turn is
// about to answer about a different subject than the parent did. A parent
// whose ledger was dropped for a changed question is precisely the case
// this guard exists for, so it must not lose the parent's identity along
// with the drop.
type parentAnchorEvidence struct {
	// Referenced is true when the request named a parent at all.
	Referenced bool
	// Loaded is true when that parent's stored semantic state read cleanly.
	// False with Referenced true means unreadable, never "absent".
	Loaded bool
	// Subject is the anchor the parent committed, or the zero SubjectRef
	// when it committed none. Label is filled from the parent's own served
	// resolution where it has one, so an offer built from this evidence
	// carries the label the caller already saw.
	Subject SubjectRef
}

// held reports whether the parent asserted an identity for this turn to
// contradict.
func (p parentAnchorEvidence) held() bool {
	return p.Loaded && p.Subject.Kind != "" && p.Subject.CanonicalID != ""
}

// committedSubjectIdentityOf is "the subject THIS turn is about to answer
// about", from ONE derivation both sides of the comparison use.
//
// anchor/haveAnchor are the capture gate's own already-computed decision
// (engineCommittedAnchorForCapture), never re-derived here: the guard and the
// capture gate must never be able to disagree about which committed subject
// the frame's anchor names. Its label is read back off resolution.Committed,
// where the served document already carries it.
//
// THE SOLE-COMMITTED FALLBACK sweeps the sibling shape. A frame with no
// scope anchor at all -- a question about one named subject -- commits that
// subject and binds no anchor, so an anchor-only comparison would leave
// exactly the same silent substitution open for it. One committed subject IS
// unambiguously what that turn is about. Two or more is not, and reports no
// identity rather than picking one by slice order.
func committedSubjectIdentityOf(anchor confirmedStructureMember, haveAnchor bool, resolution SubjectResolution) (SubjectRef, bool) {
	if haveAnchor {
		for _, subject := range resolution.Committed {
			if subject.Kind == anchor.AppliedKind && subject.CanonicalID == anchor.AppliedValue {
				return subject, true
			}
		}
		// The anchor bound but its subject is not on the committed list the
		// document carries: report no identity rather than a subject nothing
		// serves.
		return SubjectRef{}, false
	}
	if len(resolution.Committed) == 1 {
		return resolution.Committed[0], true
	}
	return SubjectRef{}, false
}

// parentCommittedAnchorOf is committedSubjectIdentityOf's own reading of a
// STORED parent, taken from what that turn itself recorded rather than from
// a re-decision over its stored document.
//
// Its anchor is the subject_anchor entry the parent's own capture gate wrote
// into its ledger -- the parent's record of the anchor IT bound -- and its
// label comes from the parent's own served resolution, so an offer built
// from this evidence carries the label the caller already read. The same
// sole-committed fallback applies, and for the same reason.
func parentCommittedAnchorOf(stored StoredInvestigationResult) SubjectRef {
	committed := stored.Result.SubjectResolution.Committed
	if stored.SemanticState != nil {
		for _, entry := range stored.SemanticState.ConfirmedNeeds {
			if entry.Member != contractsv1.ContextFabricStructureNeedSubjectAnchor {
				continue
			}
			if entry.AppliedKind == "" || entry.AppliedValue == "" {
				break
			}
			for _, subject := range committed {
				if subject.Kind == entry.AppliedKind && subject.CanonicalID == entry.AppliedValue {
					return subject
				}
			}
			// The parent recorded an anchor its own served document does not
			// list. Its label is the one thing missing, and the canonical id
			// stands in for it -- the identity is what the comparison needs,
			// and Label carries a v1 non-empty bound.
			return SubjectRef{Kind: entry.AppliedKind, CanonicalID: entry.AppliedValue, Label: entry.AppliedValue}
		}
	}
	if len(committed) == 1 {
		return committed[0]
	}
	return SubjectRef{}
}

// subjectSubstitutionInput is everything the decision reads. All of it is
// already computed by the turn that calls it; nothing here re-derives a
// signal another authority owns.
type subjectSubstitutionInput struct {
	Parent parentAnchorEvidence
	// Committed is the subject THIS turn's own resolution bound to the
	// frame's anchor (engineCommittedAnchorForCapture's own member), and
	// HaveCommitted whether it bound one at all.
	Committed     SubjectRef
	HaveCommitted bool
	// Origin is reported, never consulted. See the type's doc comment.
	Origin SubjectSubstitutionOrigin
	// RedeemedChoice is true when THIS SUBJECT'S OWN identity is the one the
	// caller redeemed this turn, from a prior-subject receipt minted by the
	// NAMED PARENT. It keys on identity equality with the redeemed choice,
	// never on "a receipt is present": a turn that redeems a receipt for one
	// subject and commits another has not been told to change to the one it
	// committed.
	RedeemedChoice bool
	// AllowClarification is the caller's own option: false means this caller
	// cannot answer a question, so a substitution ends the turn with the
	// reason stated rather than with an ask it will never answer.
	AllowClarification bool
	// RememberedAvailable reports whether the parent's subject re-read and
	// re-authorized for this principal at this turn's binding. It decides
	// what may be LISTED back to the caller, never what may be served, and
	// it is read on both firing branches -- a caller that cannot be asked a
	// question is still shown the two identities, and is never shown one it
	// cannot see.
	RememberedAvailable bool
}

// subjectSubstitutionDecision is the guard's whole output: the outcome, the
// origin it observed, and both identities in the hashed form the ledger line
// publishes.
type subjectSubstitutionDecision struct {
	Outcome SubjectSubstitutionOutcome
	Origin  SubjectSubstitutionOrigin
	// Parent/Substituted carry the raw subjects for the offer this turn may
	// compose; the ledger line publishes only their kind and a value hash.
	Parent      SubjectRef
	Substituted SubjectRef
	// RememberedListed is whether the remembered subject may be listed back
	// to the caller on a firing branch. One authority for that, read by the
	// served shape and implied by the outcome, so the two cannot disagree.
	RememberedListed bool
}

// sameSubjectIdentity is the ONE identity comparison this guard makes: kind
// and canonical id, both exactly. Identifiers are case-SENSITIVE (the ruled
// matching rule), so no normalization is applied to either side.
func sameSubjectIdentity(a, b SubjectRef) bool {
	return a.Kind == b.Kind && a.CanonicalID == b.CanonicalID
}

// decideSubjectSubstitution is the whole decision, in declared precedence
// order. PURE: reads its argument and mutates nothing, so the engine's
// control flow and the test table read the identical function.
func decideSubjectSubstitution(in subjectSubstitutionInput) subjectSubstitutionDecision {
	decision := subjectSubstitutionDecision{Origin: SubjectSubstitutionOriginNotApplicable}
	if in.HaveCommitted {
		decision.Origin = in.Origin
		decision.Substituted = in.Committed
	}
	if !ValidSubjectSubstitutionOrigin(decision.Origin) {
		// An origin nothing recorded reads as the resolver's, the strict
		// direction: the population this guard was added for is the one a
		// caller-shaped signal does not explain.
		decision.Origin = SubjectSubstitutionOriginResolver
	}
	switch {
	case !in.Parent.Referenced:
		decision.Outcome = SubjectSubstitutionNoParentReference
		return decision
	case !in.Parent.Loaded:
		decision.Outcome = SubjectSubstitutionParentUnreadable
		return decision
	case !in.Parent.held():
		decision.Outcome = SubjectSubstitutionParentNoIdentity
		return decision
	}
	decision.Parent = in.Parent.Subject
	if !in.HaveCommitted {
		decision.Outcome = SubjectSubstitutionNoCommittedSubject
		return decision
	}
	if sameSubjectIdentity(in.Committed, in.Parent.Subject) {
		decision.Outcome = SubjectSubstitutionSameSubject
		return decision
	}
	if in.RedeemedChoice {
		decision.Outcome = SubjectSubstitutionRedeemedChoice
		return decision
	}
	// Both firing branches list the two identities, remembered first, and
	// both withhold the remembered one when it fails its re-read. They
	// differ only in whether this caller can be asked to pick.
	decision.RememberedListed = in.RememberedAvailable
	switch {
	case !in.AllowClarification && in.RememberedAvailable:
		decision.Outcome = SubjectSubstitutionRefused
	case !in.AllowClarification:
		decision.Outcome = SubjectSubstitutionRefusedRememberedUnavailable
	case in.RememberedAvailable:
		decision.Outcome = SubjectSubstitutionClarified
	default:
		decision.Outcome = SubjectSubstitutionClarifiedRememberedUnavailable
	}
	return decision
}

// rememberedSubjectReadable proves the remembered subject still exists as a
// real, authorized node at THIS turn's binding, before it is ever offered
// back to the caller. It is the SAME verifier a candr_ redemption re-proves
// an offered candidate with, never a second notion of "still there".
//
// FAIL-CLOSED ON EVERY UNCERTAINTY, and the caller's own guard outcome says
// which: an unwired verifier, a refusal, a non-valid reason, and a cancelled
// context all withhold the offer. None of them ever lets the substitute be
// served -- the guard has already decided not to serve it before this is
// asked. The context error is checked rather than swallowed: a cancelled
// turn withholds the offer instead of reporting a subject it never managed
// to check.
func (e *Engine) rememberedSubjectReadable(ctx context.Context, principal storage.Principal, request InvestigationRequest, binding ResolvedGraphBinding, subject SubjectRef) bool {
	if e.candidateVerifier == nil || subject.Kind == "" || subject.CanonicalID == "" {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	ok, reason := e.candidateVerifier(ctx, principal, request.RequestedScope, binding, subject.Kind, subject.CanonicalID)
	if ctx.Err() != nil {
		return false
	}
	return ok && reason == CandidateVerificationValid
}

// subjectSubstitutionOriginOf names what produced subject, from the SAME two
// caller-sourced channels resolution itself reads: the hints this turn
// redeemed from prior-subject receipts, and the hints the caller's own
// request carried. Anything else is the resolver's own reach from the
// question's terms.
//
// Receipt-sourced hints are tested FIRST and independently of the caller's
// list, because resolvePriorSubjectHints appends its redemptions into the
// same slice the caller's own hints travel in: asking the joined list alone
// would report every redemption as a caller hint. The engine's own carried
// anchor is tested LAST of the three named channels, so a caller channel
// that also names the identity is reported as the caller's.
func subjectSubstitutionOriginOf(subject SubjectRef, receiptHints, requestHints []SubjectHint, carried map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember) SubjectSubstitutionOrigin {
	for _, hint := range receiptHints {
		if hint.Kind == subject.Kind && hint.ID == subject.CanonicalID {
			return SubjectSubstitutionOriginPriorReceipt
		}
	}
	for _, hint := range requestHints {
		if hint.Kind == subject.Kind && hint.ID == subject.CanonicalID {
			return SubjectSubstitutionOriginCallerHint
		}
	}
	if entry, ok := carried[contractsv1.ContextFabricStructureNeedSubjectAnchor]; ok &&
		entry.AppliedKind == subject.Kind && entry.AppliedValue == subject.CanonicalID {
		return SubjectSubstitutionOriginEngineCarry
	}
	return SubjectSubstitutionOriginResolver
}

// subjectSubstitutionRedeemedChoice reports whether SUBJECT ITSELF is what
// the caller redeemed this turn, from a prior-subject receipt the NAMED
// PARENT minted.
//
// TWO CONSTRAINTS, BOTH LOAD BEARING.
//
// The parent constraint: a receipt names the result that issued it, and only
// the result this turn continues can have offered the choice this turn is
// answering. A receipt carried over from some older result is a hint like
// any other -- it does not say the user was asked anything about THIS
// exchange, so it cannot license replacing the subject the exchange is about.
//
// The identity constraint: the redemption must have produced a hint for THIS
// subject. A turn that redeems a receipt for one subject and commits a
// different one was told to change to the first, not to the second, so the
// presence of a receipt alone never licenses the commit -- only equality
// with what was actually chosen does.
func subjectSubstitutionRedeemedChoice(subject SubjectRef, parentResultID string, validated []BoundSubjectReceipt, receiptHints []SubjectHint) bool {
	if parentResultID == "" || len(validated) == 0 {
		return false
	}
	named := false
	for _, receipt := range validated {
		if receipt.ResultID == parentResultID {
			named = true
			break
		}
	}
	if !named {
		return false
	}
	for _, hint := range receiptHints {
		if hint.Kind == subject.Kind && hint.ID == subject.CanonicalID {
			return true
		}
	}
	return false
}

// subjectSubstitutionClarificationPrompt is the sentence a guarded turn
// carries. It states what happened -- the parent's subject, this turn's
// reading, and that nothing was read for either -- and asks the one question
// that resolves it. Two wordings, because a clarification that cannot offer
// the remembered subject must not name it as something to pick.
const (
	subjectSubstitutionClarificationPrompt = "This follow-up reads as being about a different subject than the answer it follows up on, so no canonical facts were read for either. Pick the subject you mean: the one that answer was about is listed first."
	// subjectSubstitutionRememberedUnavailablePrompt is the same statement
	// for a turn whose remembered subject fails its re-read and re-authorization
	// at this turn's binding. It says so rather than offering something the
	// caller cannot see.
	subjectSubstitutionRememberedUnavailablePrompt = "This follow-up reads as being about a different subject than the answer it follows up on, and the subject of that answer cannot be read for you, so no canonical facts were read for either. Name the subject you mean."
)

// subjectSubstitutionPromptFor picks the prompt from the ONE fact that
// decides what the caller is looking at: whether the remembered subject is
// listed. A prompt that names it as something to pick beside a list that
// does not contain it would describe a different document.
func subjectSubstitutionPromptFor(rememberedListed bool) string {
	if !rememberedListed {
		return subjectSubstitutionRememberedUnavailablePrompt
	}
	return subjectSubstitutionClarificationPrompt
}

// subjectSubstitutionReceiptID is the offer id the remembered subject's own
// candidate carries, so the caller can redeem it exactly like any other
// subject candidate (resolvePriorSubjectHints matches on this id against the
// result that published it). Deterministic and content-addressed from the
// parent result identity and the subject itself, the SAME discipline
// mintStructureReceiptID states: a retry of the same turn mints the same id,
// and two different subjects can never collide.
func subjectSubstitutionReceiptID(parentResultID string, subject SubjectRef) string {
	sum := sha256.Sum256([]byte("context-fabric-remembered-subject\x00" + parentResultID + "\x00" + SubjectMapKey(subject)))
	return "subr_" + hex.EncodeToString(sum[:])[:24]
}

// subjectSubstitutionResolution is the resolution a guarded turn serves its
// terminal from: NOTHING committed, and the two identities offered as
// candidates with the REMEMBERED one first.
//
// Order is the offer. Ask Dev renders a result's options in the result's own
// order and never re-ranks, so "the remembered subject is the pre-selected
// option" is expressed by putting it first, with no new wire field and no
// new option-source token.
//
// This turn's OWN candidates are kept, unchanged, after it. They are real
// retrieval this turn performed, and dropping them would leave a caller who
// genuinely meant the new subject nothing to pick. Their State is left
// exactly as resolution recorded it EXCEPT that a candidate this turn had
// committed is re-stated as ambiguous: with Committed emptied, a candidate
// still claiming "committed" would contradict the document it travels in.
//
// CommitDecisionDigests is cleared with Committed: its own invariant is one
// entry per committed subject, so an emptied commit list must not keep them.
func subjectSubstitutionResolution(resolution SubjectResolution, decision subjectSubstitutionDecision, parentResultID string) SubjectResolution {
	candidates := make([]SubjectCandidate, 0, len(resolution.Candidates)+1)
	if decision.RememberedListed {
		candidates = append(candidates, SubjectCandidate{
			ReceiptID:      subjectSubstitutionReceiptID(parentResultID, decision.Parent),
			Subject:        decision.Parent,
			State:          contractsv1.ContextFabricResolutionAmbiguous,
			MatchedTerms:   []string{},
			MatchReasons:   []string{"the subject of the answer this follow-up continues"},
			Confidence:     0,
			EvidenceRefIDs: []string{},
		})
	}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		seen[candidate.ReceiptID] = struct{}{}
	}
	for _, candidate := range resolution.Candidates {
		if _, exists := seen[candidate.ReceiptID]; exists {
			continue
		}
		seen[candidate.ReceiptID] = struct{}{}
		if candidate.State == contractsv1.ContextFabricResolutionCommitted {
			candidate.State = contractsv1.ContextFabricResolutionAmbiguous
		}
		candidates = append(candidates, candidate)
	}
	guarded := resolution
	guarded.Candidates = candidates
	guarded.Committed = []SubjectRef{}
	guarded.CommitDecisionDigests = nil
	guarded.ClarificationPrompt = subjectSubstitutionPromptFor(decision.RememberedListed)
	return guarded
}
