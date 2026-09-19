package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

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
// THE COMPARISON IS OVER THE COMMITTED SET. The parent asserted one
// identity; this turn is about to serve every subject its resolution
// committed. Anything other than exactly that one identity -- another
// subject, a cohort of others, or a cohort that adds others beside the
// parent's -- answers about something the parent did not, and is a
// substitution. A single-subject comparison would let a turn that commits
// two subjects through untouched, which is the same silent change by
// another shape.
//
// ORIGIN-BLIND, DELIBERATELY. The comparison does not ask what produced
// this turn's commits -- a caller hint, a prior receipt, the engine's own
// carried anchor or the question's own words all reach the same commit and
// all substitute the same way. Origin is REPORTED (SubjectSubstitutionOrigin,
// on the ledger line) so the three populations stay countable apart, and it
// is never an input to the decision. Keying the guard on origin was the
// shape this defect already defeated once: the resolver-origin case is
// exactly the one a hint-shaped predicate does not see.
//
// THE ONE PERMITTED SUBSTITUTION is a choice the user actually made: this
// turn commits exactly one identity, and ONE prior-subject receipt redeemed
// this turn was both issued by the parent this turn continues and redeemed
// for that identity. A receipt from any OTHER result is not that: it says
// nothing about the exchange this turn continues.
//
// A CLARIFICATION THIS GUARD ISSUED SPEAKS FOR ITS PARENT. The guard's own
// clarification C is a result of its own, minted while continuing parent P,
// and its offers are C's receipts. C carries P's identity forward in its
// own payload -- the remembered subject it lists first -- so the identity a
// turn naming C is compared against is P's (parentIdentityOf), never
// "none", and C's receipts ARE P's receipts (parentReceiptIssuers). So
// answering C serves the choice the same way whichever of the two the
// caller names -- P, the answer it was reading, or C, the question it is
// answering -- and a choice redeemed from any other result still
// clarifies. Treating that answer as a substitution would make every
// clarification this guard raises unanswerable, which is the failure class
// chaos5637_answerable_clarification.go exists to end. The link from C to P
// is the remembered offer's receipt, minted from P's id
// (subjectSubstitutionIssuedFor); a C that listed no remembered subject
// carries no such link, so a turn naming it that commits a subject without
// redeeming C's own offer clarifies again, with the remembered subject
// unavailable.
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
	// stored result payload could not be read at all. A parent whose payload
	// read but whose semantic snapshot did not is NOT this: its served
	// subjects are compared exactly as they would be with the snapshot. A
	// DIFFERENT fact from a parent that read and held no subject -- an
	// operator who can only see "nothing to compare" cannot tell a storage
	// defect apart from an ordinary first turn.
	SubjectSubstitutionParentUnreadable SubjectSubstitutionOutcome = "parent_unreadable"
	// SubjectSubstitutionParentNoIdentity: the named parent's payload read
	// and served no single subject -- none, or several -- so it asserted no
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

// SubjectSubstitutionRememberedCheck is what the re-read of the remembered
// subject found (rememberedSubjectReadable). Every way the re-read can
// withhold the offer is its own member, so the line says WHY the
// remembered subject was not listed and never drops the verifier's answer.
type SubjectSubstitutionRememberedCheck string

const (
	// SubjectSubstitutionRememberedNotChecked: the re-read was not asked --
	// the guard was not about to fire, or the parent's identity was not held.
	SubjectSubstitutionRememberedNotChecked SubjectSubstitutionRememberedCheck = "not_checked"
	// SubjectSubstitutionRememberedReadable: the verifier proved the subject
	// valid at this turn's binding for this principal.
	SubjectSubstitutionRememberedReadable SubjectSubstitutionRememberedCheck = "readable"
	// SubjectSubstitutionRememberedVerifierUnwired: this deployment wires no
	// verifier, so nothing can prove the subject.
	SubjectSubstitutionRememberedVerifierUnwired SubjectSubstitutionRememberedCheck = "verifier_unwired"
	// SubjectSubstitutionRememberedCancelledBefore: the turn's context was
	// done before the verifier was asked.
	SubjectSubstitutionRememberedCancelledBefore SubjectSubstitutionRememberedCheck = "cancelled_before_check"
	// SubjectSubstitutionRememberedCancelledDuring: the turn's context was
	// done when the verifier returned, so its answer is not trusted.
	SubjectSubstitutionRememberedCancelledDuring SubjectSubstitutionRememberedCheck = "cancelled_during_check"
	// SubjectSubstitutionRememberedRefused: the verifier answered, and the
	// answer was not "valid"; substitution_remembered_reason carries it.
	SubjectSubstitutionRememberedRefused SubjectSubstitutionRememberedCheck = "refused"
)

// subjectSubstitutionRememberedChecks is the closed vocabulary in declared order.
var subjectSubstitutionRememberedChecks = [...]SubjectSubstitutionRememberedCheck{
	SubjectSubstitutionRememberedNotChecked,
	SubjectSubstitutionRememberedReadable,
	SubjectSubstitutionRememberedVerifierUnwired,
	SubjectSubstitutionRememberedCancelledBefore,
	SubjectSubstitutionRememberedCancelledDuring,
	SubjectSubstitutionRememberedRefused,
}

// SubjectSubstitutionRememberedCheckCount is the closed vocabulary's size.
const SubjectSubstitutionRememberedCheckCount = len(subjectSubstitutionRememberedChecks)

// SubjectSubstitutionRememberedCheckVocabulary returns the closed vocabulary
// in declared order, for the telemetry specification to read.
func SubjectSubstitutionRememberedCheckVocabulary() [SubjectSubstitutionRememberedCheckCount]SubjectSubstitutionRememberedCheck {
	return subjectSubstitutionRememberedChecks
}

// ValidSubjectSubstitutionRememberedCheck reports membership in the closed
// vocabulary.
func ValidSubjectSubstitutionRememberedCheck(value SubjectSubstitutionRememberedCheck) bool {
	for _, member := range subjectSubstitutionRememberedChecks {
		if member == value {
			return true
		}
	}
	return false
}

// rememberedSubjectCheck is the re-read's whole answer: the class, the
// verifier's own reason when it answered, and the context error when the
// turn's context was done. Nothing the verifier or the context said is
// dropped: the ledger line carries all three.
type rememberedSubjectCheck struct {
	Check        SubjectSubstitutionRememberedCheck
	Reason       CandidateVerificationReason
	ContextError string
}

// readable reports whether the remembered subject may be listed.
func (c rememberedSubjectCheck) readable() bool {
	return c.Check == SubjectSubstitutionRememberedReadable
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
	// Loaded is true when that parent's stored result PAYLOAD read, whatever
	// its semantic snapshot did. False with Referenced true means the payload
	// is unreadable, never "absent".
	Loaded bool
	// Subject is the one subject the parent's payload committed, or the zero
	// SubjectRef when it committed none or several (parentCommittedIdentityOf).
	// It carries the label the parent served, so an offer built from it reads
	// the way the caller already saw it.
	Subject SubjectRef
	// ResultID is the result whose subject Subject is: the named parent, or,
	// when the named parent is a clarification this guard issued, the result
	// it was proven to continue ("" when no receipt proves one).
	ResultID string
	// GuardIssued is true when the named parent is a clarification this
	// guard issued. Such a parent speaks for the result it continued, so it
	// is never read as asserting no identity; IssuedFor names that result
	// when the remembered offer's receipt proves it.
	GuardIssued bool
	IssuedFor   string
}

// held reports whether the parent asserted an identity for this turn to
// contradict.
func (p parentAnchorEvidence) held() bool {
	return p.Loaded && p.Subject.Kind != "" && p.Subject.CanonicalID != ""
}

// committedIsExactly reports whether committed is exactly the one identity
// subject -- one element, equal to it. The one statement of "this turn is
// about the parent's subject and nothing else".
func committedIsExactly(committed []SubjectRef, subject SubjectRef) bool {
	return len(committed) == 1 && sameSubjectIdentity(committed[0], subject)
}

// committedIDs renders committed as "<kind>:<canonical id>" in commit order --
// the identity reference the anchor-binding transition line uses, so the two
// lines join on it. Never nil, so the line always carries a list.
func committedIDs(committed []SubjectRef) []string {
	out := make([]string, 0, len(committed))
	for _, subject := range committed {
		out = append(out, string(subject.Kind)+":"+subject.CanonicalID)
	}
	return out
}

// parentCommittedIdentityOf is the one identity a STORED parent served:
// the one subject its stored result payload committed, or none when it
// committed none or several.
//
// THE PAYLOAD, NEVER THE SNAPSHOT. The parent's served document is what the
// person read, and a store returns it intact beside a semantic snapshot that
// is absent or malformed. Reading the identity off the snapshot would make
// the guard's comparison depend on whether a supplementary record decoded,
// and an unreadable snapshot would then silently remove the parent the guard
// compares against. From the payload alone, one parent yields one answer.
//
// Several committed subjects assert no single identity, and report none
// rather than one picked by slice order.
func parentCommittedIdentityOf(stored StoredInvestigationResult) SubjectRef {
	if committed := stored.Result.SubjectResolution.Committed; len(committed) == 1 {
		return committed[0]
	}
	return SubjectRef{}
}

// subjectSubstitutionIssued reports whether a stored result is a
// clarification THIS guard issued, from its payload alone: it commits
// nothing and its prompt is one the guard issues. Every field is
// server-written, so no caller input can make a result read as
// guard-issued. Each prompt the guard has ever issued stays in this switch,
// so a stored clarification keeps speaking for its parent.
func subjectSubstitutionIssued(stored StoredInvestigationResult) bool {
	resolution := stored.Result.SubjectResolution
	if len(resolution.Committed) != 0 {
		return false
	}
	switch resolution.ClarificationPrompt {
	case subjectSubstitutionClarificationPrompt, subjectSubstitutionRememberedUnavailablePrompt:
		return true
	default:
		return false
	}
}

// guardIssuedRememberedOf is the remembered subject a guard-issued
// clarification lists first, the zero SubjectRef when it lists none. The
// guard lists it first, under its own receipt prefix, beside the listing
// prompt. The prompt decides: a clarification whose prompt says the
// remembered subject was not listed holds none, whatever its offers are.
func guardIssuedRememberedOf(stored StoredInvestigationResult) SubjectRef {
	resolution := stored.Result.SubjectResolution
	if !subjectSubstitutionIssued(stored) || resolution.ClarificationPrompt != subjectSubstitutionClarificationPrompt || len(resolution.Candidates) == 0 {
		return SubjectRef{}
	}
	first := resolution.Candidates[0]
	if !strings.HasPrefix(first.ReceiptID, subjectSubstitutionReceiptPrefix) {
		return SubjectRef{}
	}
	return first.Subject
}

// subjectSubstitutionIssuedFor reports whether stored is a clarification
// this guard issued while continuing resultID. THE PROOF IS THE RECEIPT:
// the remembered offer's receipt id is minted from the continued result's
// id and the remembered subject (subjectSubstitutionReceiptID), so it
// verifies against exactly one result. Stored ancestry is never the proof:
// a result's recorded parent is withheld on question drift and can then
// name some other result a receipt came from. A clarification that listed
// no remembered subject carries no such proof and is issued for no result
// this check can name.
func subjectSubstitutionIssuedFor(stored StoredInvestigationResult, resultID string) bool {
	remembered := guardIssuedRememberedOf(stored)
	if remembered.CanonicalID == "" {
		return false
	}
	return stored.Result.SubjectResolution.Candidates[0].ReceiptID == subjectSubstitutionReceiptID(resultID, remembered)
}

// parentIdentityOf fills the identity half of the parent evidence from the
// named parent's stored payload, with no further store read. A
// clarification this guard issued speaks for the result it continued: the
// remembered subject it lists is that result's subject, and the result
// itself is named when its receipt proves which one it was. One that listed
// none is still guard-issued, so the decision reads it as "remembered
// subject unavailable", never as "no identity".
func parentIdentityOf(evidence parentAnchorEvidence, stored StoredInvestigationResult, named string) parentAnchorEvidence {
	evidence.Subject, evidence.ResultID = parentCommittedIdentityOf(stored), named
	if !subjectSubstitutionIssued(stored) {
		return evidence
	}
	evidence.GuardIssued, evidence.ResultID = true, ""
	evidence.Subject = guardIssuedRememberedOf(stored)
	if subjectSubstitutionIssuedFor(stored, stored.ParentResultID) {
		evidence.IssuedFor, evidence.ResultID = stored.ParentResultID, stored.ParentResultID
	}
	return evidence
}

// parentReceiptIssuers decides which results' receipts are the parent's own
// offer: the named parent's, the result a guard-issued named parent was
// proven to continue, and a clarification this guard issued while
// continuing the named parent. loaded is the set of issuing results this
// turn already read.
type parentReceiptIssuers struct {
	named     string
	issuedFor string
	loaded    map[string]StoredInvestigationResult
}

// issues reports whether resultID's receipts are the parent's own offer.
func (p parentReceiptIssuers) issues(resultID string) bool {
	id := strings.TrimSpace(resultID)
	if id == "" {
		return false
	}
	if id == p.named || id == p.issuedFor {
		return true
	}
	stored, ok := p.loaded[id]
	return ok && subjectSubstitutionIssuedFor(stored, p.named)
}

// issuedForOf names, for the line, the result resultID was proven to be a
// guard clarification of: the named parent when its receipt verifies
// against it, else the result its stored ancestry names when it verifies
// against that, else "".
func (p parentReceiptIssuers) issuedForOf(resultID string) string {
	stored, ok := p.loaded[strings.TrimSpace(resultID)]
	if !ok {
		return ""
	}
	if subjectSubstitutionIssuedFor(stored, p.named) {
		return p.named
	}
	if subjectSubstitutionIssuedFor(stored, stored.ParentResultID) {
		return stored.ParentResultID
	}
	return ""
}

// subjectSubstitutionInput is everything the decision reads. All of it is
// already computed by the turn that calls it; nothing here re-derives a
// signal another authority owns.
type subjectSubstitutionInput struct {
	Parent parentAnchorEvidence
	// Committed is EVERY subject this turn's resolution committed, in commit
	// order -- the set this turn is about to serve.
	Committed []SubjectRef
	// Origin is reported, never consulted. See the type's doc comment.
	Origin SubjectSubstitutionOrigin
	// OriginReceiptResultID/OriginReceiptID name the redeemed receipt that
	// carried the origin's subject; reported, never consulted.
	OriginReceiptResultID string
	OriginReceiptID       string
	// RedeemedChoice is true when this turn commits exactly one identity and
	// ONE receipt redeemed this turn was issued by the PARENT and
	// redeemed for that identity (subjectSubstitutionRedeemedChoice). One
	// predicate over one receipt: a parent receipt for one subject beside
	// another result's receipt for the committed one is not a choice of it.
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
	// Remembered is the re-read's whole answer, reported on the line; it is
	// never consulted beyond RememberedAvailable.
	Remembered rememberedSubjectCheck
	// OriginIssuedFor is the result the origin receipt's issuing result was
	// issued for, when this guard issued it; reported, never consulted.
	OriginIssuedFor string
}

// subjectSubstitutionDecision is the guard's whole output: the outcome, the
// origin it observed, the parent's identity and the set this turn committed.
type subjectSubstitutionDecision struct {
	Outcome SubjectSubstitutionOutcome
	Origin  SubjectSubstitutionOrigin
	// Parent is the identity the named parent asserted; Committed is every
	// subject this turn committed. The ledger line publishes both.
	Parent    SubjectRef
	Committed []SubjectRef
	// OriginReceiptResultID/OriginReceiptID name the redeemed receipt that
	// carried the origin's subject, empty unless Origin is prior_receipt.
	OriginReceiptResultID string
	OriginReceiptID       string
	// RememberedListed is whether the remembered subject may be listed back
	// to the caller on a firing branch. One authority for that, read by the
	// served shape and implied by the outcome, so the two cannot disagree.
	RememberedListed bool
	// ParentResultID is the result whose subject Parent is (the named parent,
	// or the result a guard-issued named parent speaks for); OriginIssuedFor
	// is the result the origin receipt's issuer was issued for.
	ParentResultID  string
	OriginIssuedFor string
	// Remembered is the re-read's whole answer.
	Remembered rememberedSubjectCheck
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
	decision := subjectSubstitutionDecision{Origin: SubjectSubstitutionOriginNotApplicable, Committed: in.Committed, Remembered: in.Remembered}
	if !ValidSubjectSubstitutionRememberedCheck(decision.Remembered.Check) {
		decision.Remembered = rememberedSubjectCheck{Check: SubjectSubstitutionRememberedNotChecked}
	}
	if in.Parent.Loaded {
		decision.ParentResultID = in.Parent.ResultID
	}
	if len(in.Committed) > 0 {
		decision.Origin = in.Origin
		if in.Origin == SubjectSubstitutionOriginPriorReceipt {
			decision.OriginReceiptResultID, decision.OriginReceiptID, decision.OriginIssuedFor = in.OriginReceiptResultID, in.OriginReceiptID, in.OriginIssuedFor
		}
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
	case !in.Parent.held() && !in.Parent.GuardIssued:
		decision.Outcome = SubjectSubstitutionParentNoIdentity
		return decision
	}
	decision.Parent = in.Parent.Subject
	if len(in.Committed) == 0 {
		decision.Outcome = SubjectSubstitutionNoCommittedSubject
		return decision
	}
	if committedIsExactly(in.Committed, in.Parent.Subject) {
		decision.Outcome = SubjectSubstitutionSameSubject
		return decision
	}
	if in.RedeemedChoice && len(in.Committed) == 1 {
		decision.Outcome = SubjectSubstitutionRedeemedChoice
		return decision
	}
	// Both firing branches list the two identities, remembered first, and
	// both withhold the remembered one when it fails its re-read. They
	// differ only in whether this caller can be asked to pick.
	// A guard-issued parent that listed no remembered subject holds no
	// identity to list, whatever the re-read input says.
	decision.RememberedListed = in.RememberedAvailable && in.Parent.held()
	switch {
	case !in.AllowClarification && decision.RememberedListed:
		decision.Outcome = SubjectSubstitutionRefused
	case !in.AllowClarification:
		decision.Outcome = SubjectSubstitutionRefusedRememberedUnavailable
	case decision.RememberedListed:
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
// to check. Whatever it found -- its class, the verifier's own reason and
// the context error -- is returned whole and published on the ledger line.
func (e *Engine) rememberedSubjectReadable(ctx context.Context, principal storage.Principal, request InvestigationRequest, binding ResolvedGraphBinding, subject SubjectRef) rememberedSubjectCheck {
	if e.candidateVerifier == nil || subject.Kind == "" || subject.CanonicalID == "" {
		return rememberedSubjectCheck{Check: SubjectSubstitutionRememberedVerifierUnwired}
	}
	if err := ctx.Err(); err != nil {
		return rememberedSubjectCheck{Check: SubjectSubstitutionRememberedCancelledBefore, ContextError: err.Error()}
	}
	ok, reason := e.candidateVerifier(ctx, principal, request.RequestedScope, binding, subject.Kind, subject.CanonicalID)
	if err := ctx.Err(); err != nil {
		return rememberedSubjectCheck{Check: SubjectSubstitutionRememberedCancelledDuring, Reason: reason, ContextError: err.Error()}
	}
	if !ok || reason != CandidateVerificationValid {
		return rememberedSubjectCheck{Check: SubjectSubstitutionRememberedRefused, Reason: reason}
	}
	return rememberedSubjectCheck{Check: SubjectSubstitutionRememberedReadable, Reason: reason}
}

// substitutionOriginFact is the channel that carried a subject into
// resolution and, when that channel is a redeemed receipt, WHICH receipt and
// which result issued it -- so a reader of the line tells a choice redeemed
// from the parent's own offer apart from one redeemed from some other result
// without reopening the request.
type substitutionOriginFact struct {
	Origin SubjectSubstitutionOrigin
	// ReceiptResultID and ReceiptID name the redeemed receipt that carried
	// the subject; both empty unless Origin is prior_receipt.
	ReceiptResultID string
	ReceiptID       string
	// IssuedFor is the result the issuing result was issued for when this
	// guard issued it; empty otherwise.
	IssuedFor string
}

// subjectSubstitutionOriginOf names what carried this turn's committed set
// into resolution, from the SAME channels resolution itself reads: the
// receipts this turn redeemed, the hints the caller's own request carried,
// and the engine's own carried anchor. Anything else is the resolver's own
// reach over the question.
//
// The subject it describes is the first committed identity that is not the
// parent's -- the one that substitutes -- or, when every commit is the
// parent's, the first commit.
//
// Redeemed receipts are read FIRST and from their own outcomes, because
// resolvePriorSubjectHints appends its redemptions into the same slice the
// caller's own hints travel in: asking the joined list alone would report
// every redemption as a caller hint. The engine's own carried anchor is read
// LAST of the three named channels, so a caller channel that also names the
// identity is reported as the caller's.
func subjectSubstitutionOriginOf(committed []SubjectRef, parent SubjectRef, issuers parentReceiptIssuers, outcomes []priorSubjectReceiptOutcome, requestHints []SubjectHint, carried map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember) substitutionOriginFact {
	if len(committed) == 0 {
		return substitutionOriginFact{Origin: SubjectSubstitutionOriginNotApplicable}
	}
	subject := committed[0]
	for _, candidate := range committed {
		if !sameSubjectIdentity(candidate, parent) {
			subject = candidate
			break
		}
	}
	return subjectOriginOf(subject, issuers, outcomes, requestHints, carried)
}

// subjectOriginOf names the channel that carried ONE subject into
// resolution. When several redeemed receipts carried it, one the parent
// issued (parentReceiptIssuers) is the one reported: that is the receipt a
// redeemed choice stands on, so the line names the same receipt the
// decision read, the result that issued it, and the result that result was
// issued for when this guard issued it.
func subjectOriginOf(subject SubjectRef, issuers parentReceiptIssuers, outcomes []priorSubjectReceiptOutcome, requestHints []SubjectHint, carried map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember) substitutionOriginFact {
	var redeemed *priorSubjectReceiptOutcome
	for i := range outcomes {
		outcome := &outcomes[i]
		if !outcome.hasHint || outcome.droppedByHintBudget || outcome.hint.Kind != subject.Kind || outcome.hint.ID != subject.CanonicalID {
			continue
		}
		if redeemed == nil || (!issuers.issues(redeemed.receipt.ResultID) && issuers.issues(outcome.receipt.ResultID)) {
			redeemed = outcome
		}
	}
	if redeemed != nil {
		return substitutionOriginFact{
			Origin:          SubjectSubstitutionOriginPriorReceipt,
			ReceiptResultID: strings.TrimSpace(redeemed.receipt.ResultID),
			ReceiptID:       strings.TrimSpace(redeemed.receipt.ReceiptID),
			IssuedFor:       issuers.issuedForOf(redeemed.receipt.ResultID),
		}
	}
	for _, hint := range requestHints {
		if hint.Kind == subject.Kind && hint.ID == subject.CanonicalID {
			return substitutionOriginFact{Origin: SubjectSubstitutionOriginCallerHint}
		}
	}
	if entry, ok := carried[contractsv1.ContextFabricStructureNeedSubjectAnchor]; ok &&
		entry.AppliedKind == subject.Kind && entry.AppliedValue == subject.CanonicalID {
		return substitutionOriginFact{Origin: SubjectSubstitutionOriginEngineCarry}
	}
	return substitutionOriginFact{Origin: SubjectSubstitutionOriginResolver}
}

// subjectSubstitutionRedeemedChoice reports whether this turn's commit is a
// choice the caller made from the parent's own offer: exactly one identity
// is committed, and ONE receipt redeemed this turn was both issued by the
// parent (parentReceiptIssuers) and redeemed for that identity.
//
// ONE PREDICATE OVER ONE RECEIPT. The issuer and the identity are read off
// the same outcome (the receipt and the hint its redemption produced), never
// checked apart. Checked apart, a parent receipt for one subject beside
// another result's receipt for a second would satisfy both halves and
// license the second -- a subject the parent never offered.
//
// The issuer constraint: only the parent this turn continues -- or a
// clarification this guard issued on its behalf -- can have offered the
// choice this turn is answering, and a redemption resolves only against the
// result that issued the receipt, so a hint for the committed identity from
// a receipt the parent issued proves the parent offered it.
// The identity constraint: a receipt redeemed for one subject is not a
// choice of another. A redemption the hint budget dropped never reached
// resolution and chose nothing.
func subjectSubstitutionRedeemedChoice(committed []SubjectRef, issuers parentReceiptIssuers, outcomes []priorSubjectReceiptOutcome) bool {
	if len(committed) != 1 {
		return false
	}
	chosen := committed[0]
	for _, outcome := range outcomes {
		if !outcome.hasHint || outcome.droppedByHintBudget {
			continue
		}
		if !issuers.issues(outcome.receipt.ResultID) {
			continue
		}
		if outcome.hint.Kind == chosen.Kind && outcome.hint.ID == chosen.CanonicalID {
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

// CHAOS-5926: the non-clarifying branch's basis and sentence, named rather
// than left to fall into the ordinary ambiguous-candidate ending. Same
// convention chaos5660_declared_kind_terminal.go and role_answerability.go
// each use for their own terminal member: a private alias here, beside the
// guard that decides it, of the one exported pair contracts/v1 publishes.
const (
	subjectIdentityUnconfirmedTerminalLimitation = contractsv1.ContextFabricSubjectIdentityUnconfirmedLimitation
	subjectIdentityUnconfirmedTerminalBasis      = contractsv1.ContextFabricRefusalBasisSubjectIdentityUnconfirmed
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

// subjectSubstitutionReceiptPrefix marks the remembered offer's receipt.
const subjectSubstitutionReceiptPrefix = "subr_"

// subjectSubstitutionReceiptID is the offer id the remembered subject's own
// candidate carries, so the caller can redeem it exactly like any other
// subject candidate (resolvePriorSubjectHints matches on this id against the
// result that published it). Deterministic and content-addressed from the
// parent result identity and the subject itself, the SAME discipline
// mintStructureReceiptID states: a retry of the same turn mints the same id,
// and two different subjects can never collide.
func subjectSubstitutionReceiptID(parentResultID string, subject SubjectRef) string {
	sum := sha256.Sum256([]byte("context-fabric-remembered-subject\x00" + parentResultID + "\x00" + SubjectMapKey(subject)))
	return subjectSubstitutionReceiptPrefix + hex.EncodeToString(sum[:])[:24]
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
