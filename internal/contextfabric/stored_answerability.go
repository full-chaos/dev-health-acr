package contextfabric

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5672: the READ side of role answerability.
//
// Fresh composition refuses a clarification whose offers advance no role of
// its accepted reading (role_answerability.go). Two surfaces hand back a
// clarification that was composed EARLIER instead of composing one: the reuse
// lookup, and the result-by-id route that the MCP investigation_result tool
// forwards. Before this file both consulted only whether the stored document
// carried AN option (resultOffersRedeemable), so a clarification the fresh path
// now refuses stayed reusable and readable as a clarification, and the answer
// a caller received depended on which surface happened to serve it.
//
// THE READING IS THE PERSISTED ONE. A stored clarification offering kind X is
// byte-identical whether its question was about X or about Y, so no
// document-only predicate can take this decision. The accepted reading -- the
// validated frame and the winning sample's anchor kind -- is persisted beside
// every row that reached interpretation (semantic_state.go), and the decision
// here is decideAnswerability over that reading and the offers the stored
// document serves: the same predicate, a different adapter.
//
// WHEN THE READING IS NOT THERE. A row saved before the snapshot existed, a
// turn that ended before interpretation, or a snapshot that does not decode
// has no reading. The determination is then UNAVAILABLE, and each surface does
// the one honest thing its API permits: the reuse lookup declines the row and
// the engine investigates afresh; the result-by-id route serves the row as
// stored -- it is a history read and must never run an investigation -- and
// logs the unavailable determination. No reading is reconstructed from the
// payload, and no stored row is rewritten.
//
// THE PRECEDENCE IS FRESH COMPOSITION'S, IN ITS ORDER. Fresh composition
// reaches a clarification through one of these steps, and the read side takes
// them in the same order so that a stored row is served as fresh composition
// would serve it today:
//
//  1. The window gate. A clarification whose structure needs carry window
//     options is the gate's (only composeGatedStructureNeeds writes them). The
//     gate raises the window need before any terminal runs, so fresh
//     composition serves it whatever the reading and whatever other options
//     its offers-only material added. It is answerable as stored.
//  2. The organization scope. An available reading of an organization-scope
//     question that asks for anything besides a count is refused on
//     organization_scope_unsupported whatever the document offers (D48).
//  3. The role decision. Offers that carry a subject kind are judged against
//     the reading's roles; with no reading the determination is unavailable.
//  4. No subject offer. A clarification whose offers carry no subject kind is
//     answerable without the reading when it still offers something.
//  5. Offer-less. A clarification that offers nothing on any channel is
//     unanswerable on its offers alone, and is repaired the way every earlier
//     offer-less row is (chaos5637_answerable_clarification.go).

// StoredAnswerabilitySurface names the read surface that took a determination.
type StoredAnswerabilitySurface string

const (
	// StoredAnswerabilitySurfaceReuse: the answer-reuse lookup.
	StoredAnswerabilitySurfaceReuse StoredAnswerabilitySurface = "reuse"
	// StoredAnswerabilitySurfaceResultByID: the result-by-id route, which
	// the MCP investigation_result tool forwards.
	StoredAnswerabilitySurfaceResultByID StoredAnswerabilitySurface = "result_by_id"
)

// StoredAnswerabilitySurfaces returns the closed surface vocabulary.
func StoredAnswerabilitySurfaces() []StoredAnswerabilitySurface {
	return []StoredAnswerabilitySurface{StoredAnswerabilitySurfaceReuse, StoredAnswerabilitySurfaceResultByID}
}

// StoredAnswerabilityDetermination is what a read surface concluded about a
// stored result.
type StoredAnswerabilityDetermination string

const (
	// StoredAnswerabilityNotApplicable: the stored result is not a
	// clarification; it makes no offer and owes none.
	StoredAnswerabilityNotApplicable StoredAnswerabilityDetermination = "not_applicable"
	// StoredAnswerabilityAnswerable: an offer advances a role of the
	// persisted reading, or no offer carries a subject kind.
	StoredAnswerabilityAnswerable StoredAnswerabilityDetermination = "answerable"
	// StoredAnswerabilityUnanswerable: the persisted reading refuses the
	// organization-scope question, or it has a role, an offer carries a kind
	// and no offer advances any role, or the clarification offers nothing.
	StoredAnswerabilityUnanswerable StoredAnswerabilityDetermination = "unanswerable"
	// StoredAnswerabilityUnavailable: the decision depends on the reading and
	// the reading is not available.
	StoredAnswerabilityUnavailable StoredAnswerabilityDetermination = "unavailable"
)

// StoredAnswerabilityDeterminations returns the closed determination
// vocabulary.
func StoredAnswerabilityDeterminations() []StoredAnswerabilityDetermination {
	return []StoredAnswerabilityDetermination{
		StoredAnswerabilityNotApplicable, StoredAnswerabilityAnswerable,
		StoredAnswerabilityUnanswerable, StoredAnswerabilityUnavailable,
	}
}

// StoredAnswerabilityStep names the step of fresh composition's precedence
// that took a stored result's determination.
type StoredAnswerabilityStep string

const (
	// StoredAnswerabilityStepNone: the stored result is not a clarification.
	StoredAnswerabilityStepNone StoredAnswerabilityStep = "none"
	// StoredAnswerabilityStepWindowGate: the window gate's clarification.
	StoredAnswerabilityStepWindowGate StoredAnswerabilityStep = "window_gate"
	// StoredAnswerabilityStepOrganizationScope: the organization-scope check.
	StoredAnswerabilityStepOrganizationScope StoredAnswerabilityStep = "organization_scope"
	// StoredAnswerabilityStepRole: the role decision over subject-kind offers.
	StoredAnswerabilityStepRole StoredAnswerabilityStep = "role"
	// StoredAnswerabilityStepNoSubjectOffer: offers that carry no subject kind.
	StoredAnswerabilityStepNoSubjectOffer StoredAnswerabilityStep = "no_subject_offer"
	// StoredAnswerabilityStepOfferLess: no offer on any channel.
	StoredAnswerabilityStepOfferLess StoredAnswerabilityStep = "offer_less"
)

// StoredAnswerabilitySteps returns the closed step vocabulary, in precedence
// order after the not-a-clarification member.
func StoredAnswerabilitySteps() []StoredAnswerabilityStep {
	return []StoredAnswerabilityStep{
		StoredAnswerabilityStepNone, StoredAnswerabilityStepWindowGate, StoredAnswerabilityStepOrganizationScope,
		StoredAnswerabilityStepRole, StoredAnswerabilityStepNoSubjectOffer, StoredAnswerabilityStepOfferLess,
	}
}

// The reading tokens that are not SemanticStateReadStatus members.
const (
	// storedReadingNotRead: the determination did not consult the reading.
	storedReadingNotRead = "not_read"
	// storedReadingLoadFailed: the row's own Get failed, so no read status
	// exists.
	storedReadingLoadFailed = "load_failed"
	// storedReadingStoreUnconfigured: the engine has no result store to load
	// the reading from.
	storedReadingStoreUnconfigured = "store_unconfigured"
	// storedReadingUnreported: the store returned no read status at all.
	storedReadingUnreported = "unreported"
)

// StoredAnswerability is one read surface's determination on a stored result.
type StoredAnswerability struct {
	Determination StoredAnswerabilityDetermination
	// Reading is the persisted reading's read status as a closed token: a
	// SemanticStateReadStatus member, or not_read / load_failed /
	// store_unconfigured / unreported.
	Reading string
	// Step is the precedence step that took the determination.
	Step StoredAnswerabilityStep
	// Repaired reports that RepairStoredClarification rewrote the served
	// copy of an unanswerable clarification.
	Repaired bool
	decision declaredKindDecision
}

// ObservableAnswerability renders the role half of the decision, with the
// explicit "none" tokens when no reading was evaluated.
func (a StoredAnswerability) ObservableAnswerability() SubjectlessTerminalAnswerability {
	return a.decision.ObservableAnswerability()
}

// answerabilityOffersOfResult lists the offers a stored document serves, in
// the channel order the composing adapter uses: the four structure option
// channels, then the subject candidates. Window options are not a channel.
func answerabilityOffersOfResult(result InvestigationResult) []answerabilityOffer {
	var offers []answerabilityOffer
	if needs := result.StructureNeeds; needs != nil {
		for _, option := range needs.KindOptions {
			offers = append(offers, answerabilityOffer{Channel: answerabilityChannelKindOption, Kind: option.Kind})
		}
		for _, option := range needs.AnchorOptions {
			offers = append(offers, answerabilityOffer{Channel: answerabilityChannelAnchorOption, Kind: option.Kind})
		}
		for _, option := range needs.HandleOptions {
			offers = append(offers, answerabilityOffer{Channel: answerabilityChannelHandleOption, Kind: option.Kind})
		}
		for _, option := range needs.CandidateOptions {
			offers = append(offers, answerabilityOffer{Channel: answerabilityChannelCandidateOption, Kind: option.Kind})
		}
	}
	for _, candidate := range result.SubjectResolution.Candidates {
		offers = append(offers, answerabilityOffer{Channel: answerabilityChannelSubjectCandidate, Kind: candidate.Subject.Kind})
	}
	return offers
}

// DecideStoredAnswerability takes the read-side determination for a stored
// result, its persisted semantic state, and that state's read status.
func DecideStoredAnswerability(result InvestigationResult, state *PersistedSemanticState, read SemanticStateReadStatus) StoredAnswerability {
	reading := string(read)
	if reading == "" {
		reading = storedReadingUnreported
	}
	return decideStoredAnswerability(result, state, reading)
}

func decideStoredAnswerability(result InvestigationResult, state *PersistedSemanticState, reading string) StoredAnswerability {
	if result.Status != InvestigationClarificationRequired {
		return StoredAnswerability{Determination: StoredAnswerabilityNotApplicable, Reading: storedReadingNotRead, Step: StoredAnswerabilityStepNone}
	}
	offers := answerabilityOffersOfResult(result)
	withoutReading := decideAnswerability(answerabilityReading{}, offers)
	if windowGateClarification(result) {
		return StoredAnswerability{Determination: StoredAnswerabilityAnswerable, Reading: storedReadingNotRead, Step: StoredAnswerabilityStepWindowGate, decision: withoutReading}
	}
	available := reading == string(SemanticStateReadAvailable) && state != nil
	if available && organizationScopeUnsupported(state.Frame) {
		decision := decideAnswerability(answerabilityReadingOf(state.Frame, state.ScopeAnchor.Kind), offers)
		return StoredAnswerability{Determination: StoredAnswerabilityUnanswerable, Reading: reading, Step: StoredAnswerabilityStepOrganizationScope, decision: decision}
	}
	if withoutReading.OffersEvaluated > 0 {
		if !available {
			if reading == string(SemanticStateReadAvailable) {
				reading = storedReadingUnreported
			}
			return StoredAnswerability{Determination: StoredAnswerabilityUnavailable, Reading: reading, Step: StoredAnswerabilityStepRole, decision: withoutReading}
		}
		decision := decideAnswerability(answerabilityReadingOf(state.Frame, state.ScopeAnchor.Kind), offers)
		determination := StoredAnswerabilityAnswerable
		if decision.Unsatisfiable {
			determination = StoredAnswerabilityUnanswerable
		}
		return StoredAnswerability{Determination: determination, Reading: reading, Step: StoredAnswerabilityStepRole, decision: decision}
	}
	if !resultOffersRedeemable(result) {
		return StoredAnswerability{Determination: StoredAnswerabilityUnanswerable, Reading: storedReadingNotRead, Step: StoredAnswerabilityStepOfferLess, decision: withoutReading}
	}
	return StoredAnswerability{Determination: StoredAnswerabilityAnswerable, Reading: storedReadingNotRead, Step: StoredAnswerabilityStepNoSubjectOffer, decision: withoutReading}
}

// windowGateClarification reports whether a stored clarification is the
// window gate's. Its structure needs carry window options, which only
// composeGatedStructureNeeds writes and only windowConfirmationRequiredResult
// calls. A window clarification alone does not mark the gate: the subjectless
// terminal and synthesis attach one too.
func windowGateClarification(result InvestigationResult) bool {
	return result.StructureNeeds != nil && len(result.StructureNeeds.WindowOptions) > 0
}

// WireSemanticReading is the read's wire disclosure (D49): present exactly
// when the determination is unavailable, with the reason the stored reading
// could not be had. A row with no reading reports absent; a reading that did
// not decode, exceeded a bound, or names an unsupported format reports
// unreadable. A store that reported no status at all reports absent: it
// returned no reading, and nothing says one exists.
func (a StoredAnswerability) WireSemanticReading() *contractsv1.ContextFabricSemanticReading {
	if a.Determination != StoredAnswerabilityUnavailable {
		return nil
	}
	reason := contractsv1.ContextFabricSemanticReadingStateUnreadable
	switch a.Reading {
	case string(SemanticStateReadAbsent), storedReadingUnreported:
		reason = contractsv1.ContextFabricSemanticReadingStateAbsent
	}
	return &contractsv1.ContextFabricSemanticReading{Status: contractsv1.ContextFabricSemanticReadingUnavailable, Reason: reason}
}

// storedClarificationAnswerability loads the persisted reading of a reuse
// candidate by its own id and takes the determination on the candidate
// document. A failed load is an unavailable determination with its own token,
// never a guess.
func (e *Engine) storedClarificationAnswerability(ctx context.Context, principal storage.Principal, candidate InvestigationResult) StoredAnswerability {
	if e.results == nil {
		return decideStoredAnswerability(candidate, nil, storedReadingStoreUnconfigured)
	}
	stored, err := e.results.Get(ctx, principal, candidate.ResultID)
	if err != nil {
		return decideStoredAnswerability(candidate, nil, storedReadingLoadFailed)
	}
	return DecideStoredAnswerability(candidate, stored.SemanticState, stored.SemanticStateRead)
}

// RepairStoredClarification brings the SERVED copy of a stored clarification
// into current semantics when its persisted reading shows it unanswerable,
// and reports the determination either way.
//
// The copy is rewritten to the terminal fresh composition takes for the same
// reading and offers: no_match, the basis of the step that refused it
// (organization_scope_unsupported or declared_kind_unmatched), that basis's
// own sentence in place of the clarification sentence, and the status
// sentence recomposed. An offer-less clarification is repaired the way every
// earlier offer-less row is, with no basis. Offers, candidates and the prompt
// are left as stored. The stored row itself is never touched: result is the
// caller's copy.
//
// An answerable, unavailable or not-applicable determination changes nothing;
// the caller serves the row as stored and logs the determination.
func RepairStoredClarification(result *InvestigationResult, state *PersistedSemanticState, read SemanticStateReadStatus) StoredAnswerability {
	if result == nil {
		return StoredAnswerability{Determination: StoredAnswerabilityNotApplicable, Reading: storedReadingNotRead}
	}
	answerability := DecideStoredAnswerability(*result, state, read)
	if answerability.Determination != StoredAnswerabilityUnanswerable {
		return answerability
	}
	if answerability.Step == StoredAnswerabilityStepOfferLess {
		answerability.Repaired = RepairLegacyUnanswerableClarification(result)
		return answerability
	}
	basis, sentence := declaredKindTerminalBasis, declaredKindTerminalLimitation
	if answerability.Step == StoredAnswerabilityStepOrganizationScope {
		basis, sentence = organizationScopeTerminalBasis, organizationScopeTerminalLimitation
	}
	// In place, the way the offer-less repair replaces its sentence: the copy
	// served is decoded per read, and a replacement keeps the list's length,
	// so the limitation bound cannot move. A row carrying no clarification
	// sentence gains the basis sentence through the bounded appender.
	replaced := false
	for index, limitation := range result.Limitations {
		if limitation == clarificationRequiredLimitationOne || limitation == clarificationRequiredLimitation {
			result.Limitations[index] = sentence
			replaced = true
		}
	}
	if !replaced {
		composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
		result.Limitations = composed
		result.LimitationsDisplaced += displaced
	}
	result.Status = InvestigationNoMatch
	result.RefusalBasis = basis
	result.DeterministicAnswer = statusSentence(InvestigationNoMatch, result.SubjectResolution)
	answerability.Repaired = true
	return answerability
}

// stepOrNone renders an unset step as "none".
func stepOrNone(step StoredAnswerabilityStep) StoredAnswerabilityStep {
	if step == "" {
		return StoredAnswerabilityStepNone
	}
	return step
}

// StoredAnswerabilityLogMessage is the message of the stored-answerability
// line, shared by the engine sink and the result-by-id route.
const StoredAnswerabilityLogMessage = "context fabric stored clarification answerability"

// StoredAnswerabilityLogArgs composes the stored-answerability line's values.
// Every string is a closed token; the counts go through SanitizeLogInt. An
// empty served status renders as "none": the surface served nothing.
func StoredAnswerabilityLogArgs(surface StoredAnswerabilitySurface, answerability StoredAnswerability, storedStatus InvestigationStatus, servedStatus InvestigationStatus) []any {
	observed := answerability.ObservableAnswerability()
	served := string(servedStatus)
	if served == "" {
		served = "none"
	}
	return []any{
		"surface", SanitizeLogAttr(string(surface)),
		"determination", SanitizeLogAttr(string(answerability.Determination)),
		"decided_by", SanitizeLogAttr(string(stepOrNone(answerability.Step))),
		"semantic_state", SanitizeLogAttr(answerability.Reading),
		"repaired", answerability.Repaired,
		"stored_status", SanitizeLogAttr(string(storedStatus)),
		"served_status", SanitizeLogAttr(served),
		"evaluated_roles", SanitizeLogAttr(observed.EvaluatedRoles),
		"advanced_role", SanitizeLogAttr(observed.AdvancedRole),
		"advancing_channel", SanitizeLogAttr(observed.AdvancingChannel),
		"offers_evaluated", SanitizeLogInt(int64(observed.OffersEvaluated)),
		"offers_advancing", SanitizeLogInt(int64(observed.OffersAdvancing)),
	}
}
