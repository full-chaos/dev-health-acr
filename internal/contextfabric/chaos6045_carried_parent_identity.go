package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A clarification prompt answers nothing. It asks the caller to pick a window,
// a kind or a subject, and the caller's next turn names the PROMPT as its
// parent, because the prompt is the result the caller is answering. The
// subject-substitution guard (chaos5917_subject_substitution.go) compares a
// follow-up against the identity its parent served, so a prompt read as a
// parent served nothing and the guard never ran: a follow-up about another
// subject was served as though the conversation had no subject at all.
//
// INVARIANT. The guard evaluates every follow-up against the identity of the
// last ANSWERED turn in its chain, through a receipt verified against that
// turn's stored identity, however many clarification prompts lie between. A
// prompt never becomes an identity of its own, and a chain whose receipt
// cannot be verified fails closed to the remembered-unavailable outcome,
// never to parent_no_identity.
//
// MECHANISM. Every clarification prompt this engine saves carries, in its
// semantic snapshot, the identity its own turn's parent resolved to -- the
// answered result's id, its subject, a receipt minted from both, and how many
// prompts deep the chain is. Because each prompt records the RESOLVED
// identity rather than a pointer to the next link, every depth resolves in one
// hop, and the next turn verifies that hop by reading the answered result and
// comparing its served identity. Stored ancestry is never read for it.
//
// The member rides the snapshot's extensions envelope, which a binary that
// predates it decodes and ignores. A prompt row that carries no member -- an
// older row, or a snapshot that could not be written -- cannot say which
// subject its chain was about, and the guard is then told so rather than told
// there was none.

// carriedParentIdentityExtension is the snapshot extension member name.
const carriedParentIdentityExtension = "carried_parent_identity"

// CarriedParentIdentityState is what a prompt's own turn knew about the
// identity it continued. Closed: it is persisted, published on the
// persistence line, and decoded strictly.
type CarriedParentIdentityState string

const (
	// CarriedParentIdentityHeld: the chain's answered result served exactly
	// one identity, named with its result id and receipt.
	CarriedParentIdentityHeld CarriedParentIdentityState = "identity_held"
	// CarriedParentNoParentReference: the prompt's turn named no parent.
	CarriedParentNoParentReference CarriedParentIdentityState = "no_parent_reference"
	// CarriedParentNoIdentity: the chain's answered result served no single
	// identity (none, or a cohort of several).
	CarriedParentNoIdentity CarriedParentIdentityState = "parent_no_identity"
	// CarriedParentUnreadable: the prompt's turn could not read its parent.
	CarriedParentUnreadable CarriedParentIdentityState = "parent_unreadable"
	// CarriedParentIdentityUnavailable: the prompt's turn continued a chain
	// whose identity it could not verify. It carries that forward as
	// unavailable, never as "none".
	CarriedParentIdentityUnavailable CarriedParentIdentityState = "identity_unavailable"
)

// CarriedParentIdentityStateCount is the vocabulary size.
const CarriedParentIdentityStateCount = 5

// CarriedParentIdentityStateVocabulary returns every member.
func CarriedParentIdentityStateVocabulary() [CarriedParentIdentityStateCount]CarriedParentIdentityState {
	return [CarriedParentIdentityStateCount]CarriedParentIdentityState{
		CarriedParentIdentityHeld,
		CarriedParentNoParentReference,
		CarriedParentNoIdentity,
		CarriedParentUnreadable,
		CarriedParentIdentityUnavailable,
	}
}

// ValidCarriedParentIdentityState reports membership.
func ValidCarriedParentIdentityState(value CarriedParentIdentityState) bool {
	for _, member := range CarriedParentIdentityStateVocabulary() {
		if member == value {
			return true
		}
	}
	return false
}

// carriedParentIdentity is the persisted member.
type carriedParentIdentity struct {
	State CarriedParentIdentityState `json:"state"`
	// ResultID, Kind and CanonicalID name the answered result and the one
	// identity it served; ReceiptID is subjectSubstitutionReceiptID over
	// both. All four are set exactly when State is identity_held.
	ResultID    string      `json:"result_id,omitempty"`
	Kind        SubjectKind `json:"kind,omitempty"`
	CanonicalID string      `json:"canonical_id,omitempty"`
	ReceiptID   string      `json:"receipt_id,omitempty"`
	// Depth is how many prompts lie between the answered result and the
	// follow-up that names this prompt, this prompt included: at least 1.
	Depth int `json:"depth"`
}

// subject is the carried identity as a reference.
func (c carriedParentIdentity) subject() SubjectRef {
	return SubjectRef{Kind: c.Kind, CanonicalID: c.CanonicalID}
}

// valid reports whether the member is internally consistent.
func (c carriedParentIdentity) valid() bool {
	if !ValidCarriedParentIdentityState(c.State) || c.Depth < 1 {
		return false
	}
	identified := c.ResultID != "" && c.Kind != "" && c.CanonicalID != "" && c.ReceiptID != ""
	unidentified := c.ResultID == "" && c.Kind == "" && c.CanonicalID == "" && c.ReceiptID == ""
	if c.State == CarriedParentIdentityHeld {
		return identified
	}
	return unidentified
}

// carriedParentIdentityOf is the member a prompt saved this turn carries,
// from the parent evidence this turn already resolved. The chain it names is
// the one the guard would compare this turn against, so a prompt carries
// forward exactly what naming its parent directly would have shown.
func carriedParentIdentityOf(parent parentAnchorEvidence) carriedParentIdentity {
	member := carriedParentIdentity{Depth: parent.ChainDepth + 1}
	switch {
	case !parent.Referenced:
		member.State = CarriedParentNoParentReference
	case !parent.Loaded:
		member.State = CarriedParentUnreadable
	case parent.held() && parent.ResultID != "":
		member.State = CarriedParentIdentityHeld
		member.ResultID, member.Kind, member.CanonicalID = parent.ResultID, parent.Subject.Kind, parent.Subject.CanonicalID
		member.ReceiptID = subjectSubstitutionReceiptID(parent.ResultID, parent.Subject)
	case parent.held() || parent.GuardIssued || parent.Carried:
		// An identity held without the result that served it, or a chain
		// that could not be verified: carried as unavailable, so the next
		// turn fails closed exactly as this one did.
		member.State = CarriedParentIdentityUnavailable
	default:
		member.State = CarriedParentNoIdentity
	}
	return member
}

// resultIsPrompt reports whether a stored result is a clarification prompt:
// it asks the caller something and commits nothing. Both are server-written
// payload fields, so no caller input makes a result read as one.
func resultIsPrompt(result InvestigationResult) bool {
	return result.Status == InvestigationClarificationRequired && len(result.SubjectResolution.Committed) == 0
}

// SubjectSubstitutionParentResultKind is what the named parent was, read from
// its payload. Closed: it is published on the ledger line.
type SubjectSubstitutionParentResultKind string

const (
	SubjectSubstitutionParentResultNone               SubjectSubstitutionParentResultKind = "none"
	SubjectSubstitutionParentResultUnreadable         SubjectSubstitutionParentResultKind = "unreadable"
	SubjectSubstitutionParentResultAnswer             SubjectSubstitutionParentResultKind = "answer"
	SubjectSubstitutionParentResultPrompt             SubjectSubstitutionParentResultKind = "prompt"
	SubjectSubstitutionParentResultGuardClarification SubjectSubstitutionParentResultKind = "guard_clarification"
	SubjectSubstitutionParentResultRefusal            SubjectSubstitutionParentResultKind = "refusal"
)

// SubjectSubstitutionParentResultKindCount is the vocabulary size.
const SubjectSubstitutionParentResultKindCount = 6

// SubjectSubstitutionParentResultKindVocabulary returns every member.
func SubjectSubstitutionParentResultKindVocabulary() [SubjectSubstitutionParentResultKindCount]SubjectSubstitutionParentResultKind {
	return [SubjectSubstitutionParentResultKindCount]SubjectSubstitutionParentResultKind{
		SubjectSubstitutionParentResultNone,
		SubjectSubstitutionParentResultUnreadable,
		SubjectSubstitutionParentResultAnswer,
		SubjectSubstitutionParentResultPrompt,
		SubjectSubstitutionParentResultGuardClarification,
		SubjectSubstitutionParentResultRefusal,
	}
}

// parentResultKindOf classifies a READ parent from its payload.
func parentResultKindOf(stored StoredInvestigationResult) SubjectSubstitutionParentResultKind {
	switch {
	case subjectSubstitutionIssued(stored):
		return SubjectSubstitutionParentResultGuardClarification
	case resultIsPrompt(stored.Result):
		return SubjectSubstitutionParentResultPrompt
	case stored.Result.Status == InvestigationNoMatch:
		return SubjectSubstitutionParentResultRefusal
	default:
		return SubjectSubstitutionParentResultAnswer
	}
}

// SubjectSubstitutionParentChain is how the parent identity was reached when
// the named parent is a prompt, and why it was not when it could not be.
// Closed: it is published on the ledger line.
type SubjectSubstitutionParentChain string

const (
	// SubjectSubstitutionChainNotAPrompt: the named parent is not a prompt,
	// or was not read; its own payload decided.
	SubjectSubstitutionChainNotAPrompt SubjectSubstitutionParentChain = "not_a_prompt"
	// SubjectSubstitutionChainVerified: the carried receipt matched and the
	// answered result still serves the carried identity.
	SubjectSubstitutionChainVerified SubjectSubstitutionParentChain = "verified"
	// SubjectSubstitutionChainGuardReceipt: a guard clarification with no
	// carried member; its own remembered-offer receipt decided.
	SubjectSubstitutionChainGuardReceipt SubjectSubstitutionParentChain = "guard_receipt"
	// SubjectSubstitutionChainAbsent: the prompt carries no member (its
	// snapshot is unavailable, or it predates the member). Fails closed.
	SubjectSubstitutionChainAbsent SubjectSubstitutionParentChain = "absent"
	// SubjectSubstitutionChainMalformed: the member does not decode as a
	// consistent member. Fails closed.
	SubjectSubstitutionChainMalformed SubjectSubstitutionParentChain = "malformed"
	// SubjectSubstitutionChainReceiptMismatch: the receipt is not the one
	// minted from the carried result and identity. Fails closed.
	SubjectSubstitutionChainReceiptMismatch SubjectSubstitutionParentChain = "receipt_mismatch"
	// SubjectSubstitutionChainAnswerUnreadable: the answered result did not
	// read. Fails closed.
	SubjectSubstitutionChainAnswerUnreadable SubjectSubstitutionParentChain = "answer_unreadable"
	// SubjectSubstitutionChainAnswerMismatch: the answered result does not
	// serve exactly the carried identity. Fails closed.
	SubjectSubstitutionChainAnswerMismatch SubjectSubstitutionParentChain = "answer_mismatch"
	// SubjectSubstitutionChainNoIdentity: the chain carries no identity to
	// verify -- no parent, or an answer that served no single identity.
	SubjectSubstitutionChainNoIdentity SubjectSubstitutionParentChain = "no_identity"
	// SubjectSubstitutionChainUnavailable: the chain carried an identity it
	// could not verify, or a parent it could not read. Fails closed.
	SubjectSubstitutionChainUnavailable SubjectSubstitutionParentChain = "unavailable"
)

// SubjectSubstitutionParentChainCount is the vocabulary size.
const SubjectSubstitutionParentChainCount = 10

// SubjectSubstitutionParentChainVocabulary returns every member.
func SubjectSubstitutionParentChainVocabulary() [SubjectSubstitutionParentChainCount]SubjectSubstitutionParentChain {
	return [SubjectSubstitutionParentChainCount]SubjectSubstitutionParentChain{
		SubjectSubstitutionChainNotAPrompt,
		SubjectSubstitutionChainVerified,
		SubjectSubstitutionChainGuardReceipt,
		SubjectSubstitutionChainAbsent,
		SubjectSubstitutionChainMalformed,
		SubjectSubstitutionChainReceiptMismatch,
		SubjectSubstitutionChainAnswerUnreadable,
		SubjectSubstitutionChainAnswerMismatch,
		SubjectSubstitutionChainNoIdentity,
		SubjectSubstitutionChainUnavailable,
	}
}

// storedCarriedParentIdentity reads the member off a stored prompt: present
// is false when the snapshot is unavailable or has no member, and err is set
// when a member is there but is not a consistent one.
func storedCarriedParentIdentity(stored StoredInvestigationResult) (member carriedParentIdentity, present bool, err error) {
	if stored.SemanticStateRead != SemanticStateReadAvailable || stored.SemanticState == nil {
		return carriedParentIdentity{}, false, nil
	}
	raw, ok := stored.SemanticState.Extensions[carriedParentIdentityExtension]
	if !ok {
		return carriedParentIdentity{}, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&member); err != nil {
		return carriedParentIdentity{}, true, fmt.Errorf("%w: carried parent identity: %v", ErrSemanticStateRejected, err)
	}
	if decoder.More() {
		return carriedParentIdentity{}, true, fmt.Errorf("%w: carried parent identity is followed by more data", ErrSemanticStateRejected)
	}
	if !member.valid() {
		return carriedParentIdentity{}, true, fmt.Errorf("%w: carried parent identity is inconsistent", ErrSemanticStateRejected)
	}
	return member, true, nil
}

// chainIdentityOf completes the parent evidence when the named parent is a
// prompt: the identity it speaks for is the one its carried member names,
// verified against the answered result's own payload. Every way that proof
// fails leaves the evidence holding NO subject while still speaking for a
// chain, which the decision reads as the remembered subject being
// unavailable -- never as a parent that asserted none.
//
// A guard clarification keeps what its own remembered-offer receipt proved
// (parentIdentityOf); its member only reports the chain depth.
func (e *Engine) chainIdentityOf(ctx context.Context, principal storage.Principal, evidence parentAnchorEvidence, stored StoredInvestigationResult) parentAnchorEvidence {
	evidence.ResultKind = parentResultKindOf(stored)
	evidence.Chain = SubjectSubstitutionChainNotAPrompt
	if evidence.ResultKind != SubjectSubstitutionParentResultPrompt && evidence.ResultKind != SubjectSubstitutionParentResultGuardClarification {
		return evidence
	}
	member, present, err := storedCarriedParentIdentity(stored)
	if err == nil && present {
		evidence.ChainDepth = member.Depth
	} else {
		evidence.ChainDepth = 1
	}
	// A guard clarification speaks for its parent only through its own
	// remembered-offer receipt (parentIdentityOf), and one that listed no
	// remembered subject fails closed whatever its member says: the member
	// can name a candidate the receipt is tried against, never an identity
	// the clarification itself declined to list.
	if evidence.ResultKind == SubjectSubstitutionParentResultGuardClarification {
		evidence.Chain = SubjectSubstitutionChainGuardReceipt
		if err != nil {
			evidence.ChainError = SubjectSubstitutionChainErrorMalformedMember
		}
		return evidence
	}
	unavailable := func(chain SubjectSubstitutionParentChain) parentAnchorEvidence {
		evidence.Carried, evidence.Chain = true, chain
		evidence.Subject, evidence.ResultID, evidence.IssuedFor = SubjectRef{}, "", ""
		return evidence
	}
	switch {
	case err != nil:
		evidence.ChainError = SubjectSubstitutionChainErrorMalformedMember
		return unavailable(SubjectSubstitutionChainMalformed)
	case !present:
		return unavailable(SubjectSubstitutionChainAbsent)
	}
	switch member.State {
	case CarriedParentNoParentReference, CarriedParentNoIdentity:
		evidence.Carried, evidence.Chain = false, SubjectSubstitutionChainNoIdentity
		evidence.Subject, evidence.ResultID, evidence.IssuedFor = SubjectRef{}, "", ""
		return evidence
	case CarriedParentUnreadable, CarriedParentIdentityUnavailable:
		return unavailable(SubjectSubstitutionChainUnavailable)
	}
	if member.ReceiptID != subjectSubstitutionReceiptID(member.ResultID, member.subject()) {
		return unavailable(SubjectSubstitutionChainReceiptMismatch)
	}
	if e.results == nil {
		evidence.ChainError = SubjectSubstitutionChainErrorNoStore
		return unavailable(SubjectSubstitutionChainAnswerUnreadable)
	}
	answered, loadErr := carryLoadResult(ctx, e.results, principal, member.ResultID)
	if loadErr != nil {
		evidence.ChainError = parentReadErrorOf(loadErr)
		return unavailable(SubjectSubstitutionChainAnswerUnreadable)
	}
	// The answered result is read as the guard reads any parent: from its
	// payload, one committed identity or none. A prompt commits none, so a
	// member naming a prompt never verifies.
	served := parentCommittedIdentityOf(answered)
	if !sameSubjectIdentity(served, member.subject()) {
		return unavailable(SubjectSubstitutionChainAnswerMismatch)
	}
	evidence.Chain = SubjectSubstitutionChainVerified
	// The subject carries the label the answered result served, so an offer
	// built from it reads the way the caller already saw it.
	// Receipts the answered result issued are the parent's own offer, as
	// they are for a guard clarification proven to continue it.
	evidence.Subject, evidence.ResultID = served, strings.TrimSpace(member.ResultID)
	evidence.IssuedFor = evidence.ResultID
	return evidence
}

// SubjectSubstitutionChainError is why a parent or a chain did not read.
// Closed and derived from the error's class, never its text: a store error's
// message is not something this line may carry.
type SubjectSubstitutionChainError string

const (
	SubjectSubstitutionChainErrorNone            SubjectSubstitutionChainError = "none"
	SubjectSubstitutionChainErrorNotFound        SubjectSubstitutionChainError = "not_found"
	SubjectSubstitutionChainErrorCanceled        SubjectSubstitutionChainError = "context_canceled"
	SubjectSubstitutionChainErrorDeadline        SubjectSubstitutionChainError = "context_deadline_exceeded"
	SubjectSubstitutionChainErrorStore           SubjectSubstitutionChainError = "store_error"
	SubjectSubstitutionChainErrorNoStore         SubjectSubstitutionChainError = "no_store"
	SubjectSubstitutionChainErrorMalformedMember SubjectSubstitutionChainError = "malformed_member"
)

// SubjectSubstitutionChainErrorCount is the vocabulary size.
const SubjectSubstitutionChainErrorCount = 7

// SubjectSubstitutionChainErrorVocabulary returns every member.
func SubjectSubstitutionChainErrorVocabulary() [SubjectSubstitutionChainErrorCount]SubjectSubstitutionChainError {
	return [SubjectSubstitutionChainErrorCount]SubjectSubstitutionChainError{
		SubjectSubstitutionChainErrorNone,
		SubjectSubstitutionChainErrorNotFound,
		SubjectSubstitutionChainErrorCanceled,
		SubjectSubstitutionChainErrorDeadline,
		SubjectSubstitutionChainErrorStore,
		SubjectSubstitutionChainErrorNoStore,
		SubjectSubstitutionChainErrorMalformedMember,
	}
}

// parentReadErrorOf classifies a failed result read.
func parentReadErrorOf(err error) SubjectSubstitutionChainError {
	switch {
	case err == nil:
		return SubjectSubstitutionChainErrorNone
	case errors.Is(err, context.Canceled):
		return SubjectSubstitutionChainErrorCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return SubjectSubstitutionChainErrorDeadline
	case errors.Is(err, ErrInvestigationResultNotFound):
		return SubjectSubstitutionChainErrorNotFound
	default:
		return SubjectSubstitutionChainErrorStore
	}
}

// CarriedParentAttachment is what a Save did about the carried member.
// Closed: it is published on the persistence line.
type CarriedParentAttachment string

const (
	// CarriedParentAttachmentNotAPrompt: the saved result is not a prompt.
	CarriedParentAttachmentNotAPrompt CarriedParentAttachment = "not_a_prompt"
	// CarriedParentAttachmentAttached: the member was written.
	CarriedParentAttachmentAttached CarriedParentAttachment = "attached"
	// CarriedParentAttachmentNoSnapshot: the prompt saves no snapshot, so it
	// carries no member and its follow-up fails closed.
	CarriedParentAttachmentNoSnapshot CarriedParentAttachment = "no_snapshot"
	// CarriedParentAttachmentUnwired: the exit handed Save no parent
	// evidence. Its follow-up fails closed.
	CarriedParentAttachmentUnwired CarriedParentAttachment = "unwired"
	// CarriedParentAttachmentUnencodable: the member could not be encoded
	// into the snapshot. Its follow-up fails closed.
	CarriedParentAttachmentUnencodable CarriedParentAttachment = "unencodable"
)

// CarriedParentAttachmentCount is the vocabulary size.
const CarriedParentAttachmentCount = 5

// CarriedParentAttachmentVocabulary returns every member.
func CarriedParentAttachmentVocabulary() [CarriedParentAttachmentCount]CarriedParentAttachment {
	return [CarriedParentAttachmentCount]CarriedParentAttachment{
		CarriedParentAttachmentNotAPrompt,
		CarriedParentAttachmentAttached,
		CarriedParentAttachmentNoSnapshot,
		CarriedParentAttachmentUnwired,
		CarriedParentAttachmentUnencodable,
	}
}

// carriedParentOutcome is the persistence line's account of the member.
type carriedParentOutcome struct {
	Attachment CarriedParentAttachment
	// State and ResultID are the member's, empty unless attached.
	State    CarriedParentIdentityState
	ResultID string
	Depth    int
}

// withCarriedParent hands a capture this turn's resolved parent evidence, so
// a prompt it saves can carry the chain forward.
func (c semanticStateCapture) withCarriedParent(parent parentAnchorEvidence) semanticStateCapture {
	c.carriedParent = &parent
	return c
}

// turnParentEvidenceKey keys the turn's resolved parent evidence on the
// request context.
type turnParentEvidenceKey struct{}

// withTurnParentEvidence records the parent evidence this turn resolved, once,
// above every exit that saves: every Save the turn makes then reads the same
// evidence the guard read, and no exit can be left out of it.
func withTurnParentEvidence(ctx context.Context, parent parentAnchorEvidence) context.Context {
	return context.WithValue(ctx, turnParentEvidenceKey{}, parent)
}

// withTurnParentFrom hands the capture the evidence recorded on ctx, unless
// the capture already carries some. A context with none leaves the capture
// unwired, which the persistence line reports.
func (c semanticStateCapture) withTurnParentFrom(ctx context.Context) semanticStateCapture {
	if c.carriedParent != nil {
		return c
	}
	if parent, ok := ctx.Value(turnParentEvidenceKey{}).(parentAnchorEvidence); ok {
		return c.withCarriedParent(parent)
	}
	return c
}

// attachCarriedParent writes the member into a prompt's snapshot. Every
// branch that writes nothing says why on the persistence line, because each
// of them makes the prompt's follow-up fail closed.
func (c semanticStateCapture) attachCarriedParent(result InvestigationResult) (semanticStateCapture, carriedParentOutcome) {
	if !resultIsPrompt(result) {
		return c, carriedParentOutcome{Attachment: CarriedParentAttachmentNotAPrompt}
	}
	if c.carriedParent == nil {
		return c, carriedParentOutcome{Attachment: CarriedParentAttachmentUnwired}
	}
	if c.Write.State == nil {
		return c, carriedParentOutcome{Attachment: CarriedParentAttachmentNoSnapshot}
	}
	member := carriedParentIdentityOf(*c.carriedParent)
	state, err := withSemanticStateExtension(c.Write.State, carriedParentIdentityExtension, member)
	var encoded []byte
	if err == nil {
		encoded, err = EncodeSemanticState(state)
	}
	if err != nil {
		return c, carriedParentOutcome{Attachment: CarriedParentAttachmentUnencodable}
	}
	out := c
	out.Write = SemanticStateOf(state)
	out.EncodedBytes = len(encoded)
	return out, carriedParentOutcome{Attachment: CarriedParentAttachmentAttached, State: member.State, ResultID: member.ResultID, Depth: member.Depth}
}

// withSemanticStateExtension returns a copy of state carrying value under
// name; state itself is not modified.
func withSemanticStateExtension(state *PersistedSemanticState, name string, value any) (*PersistedSemanticState, error) {
	if path, ok := firstUnencodableString(name, value); !ok {
		return nil, fmt.Errorf("%w: %s is not valid UTF-8", ErrSemanticStateRejected, path)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSemanticStateRejected, err)
	}
	out := *state
	out.Extensions = make(SemanticStateExtensions)
	for member, existing := range state.Extensions {
		out.Extensions[member] = existing
	}
	out.Extensions[name] = raw
	return &out, nil
}

// noneChainError renders an empty chain error as its explicit token, so "no
// error" and "not written" never read the same on the line.
func noneChainError(value SubjectSubstitutionChainError) SubjectSubstitutionChainError {
	if value == "" {
		return SubjectSubstitutionChainErrorNone
	}
	return value
}

// carriedParentAttachmentToken is the line token: a Save that recorded no
// attachment at all reads as unwired, never as an empty value.
func carriedParentAttachmentToken(value CarriedParentAttachment) CarriedParentAttachment {
	for _, member := range CarriedParentAttachmentVocabulary() {
		if member == value {
			return value
		}
	}
	return CarriedParentAttachmentUnwired
}

// parentResultKindForLine is the line's result kind: evidence built before
// the parent was classified reads by what is known of it -- not named, or
// named and not read -- never as an empty value.
func parentResultKindForLine(parent parentAnchorEvidence) SubjectSubstitutionParentResultKind {
	switch {
	case parent.ResultKind != "":
		return parent.ResultKind
	case !parent.Referenced:
		return SubjectSubstitutionParentResultNone
	default:
		return SubjectSubstitutionParentResultUnreadable
	}
}

// parentResultKindToken renders an unrecorded result kind as "none", so the
// line never carries an empty value for it.
func parentResultKindToken(value SubjectSubstitutionParentResultKind) SubjectSubstitutionParentResultKind {
	if value == "" {
		return SubjectSubstitutionParentResultNone
	}
	return value
}

// parentChainToken renders an unrecorded chain as not_a_prompt.
func parentChainToken(value SubjectSubstitutionParentChain) SubjectSubstitutionParentChain {
	if value == "" {
		return SubjectSubstitutionChainNotAPrompt
	}
	return value
}
