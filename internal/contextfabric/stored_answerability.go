package contextfabric

import (
	"context"

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
// A clarification whose offers carry no subject kind at all (window options
// only) does not depend on the reading: the role decision cannot refuse a turn
// that offered no subject. It is answerable without one.

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
	// StoredAnswerabilityUnanswerable: the persisted reading has a role, an
	// offer carries a kind, and no offer advances any role.
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
		return StoredAnswerability{Determination: StoredAnswerabilityNotApplicable, Reading: storedReadingNotRead}
	}
	offers := answerabilityOffersOfResult(result)
	withoutReading := decideAnswerability(answerabilityReading{}, offers)
	if withoutReading.OffersEvaluated == 0 {
		return StoredAnswerability{Determination: StoredAnswerabilityAnswerable, Reading: storedReadingNotRead, decision: withoutReading}
	}
	if reading != string(SemanticStateReadAvailable) || state == nil {
		if reading == string(SemanticStateReadAvailable) {
			reading = storedReadingUnreported
		}
		return StoredAnswerability{Determination: StoredAnswerabilityUnavailable, Reading: reading, decision: withoutReading}
	}
	decision := decideAnswerability(answerabilityReadingOf(state.Frame, state.ScopeAnchor.Kind), offers)
	determination := StoredAnswerabilityAnswerable
	if decision.Unsatisfiable {
		determination = StoredAnswerabilityUnanswerable
	}
	return StoredAnswerability{Determination: determination, Reading: reading, decision: decision}
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
// reading and offers: no_match, the declared_kind_unmatched basis, that
// basis's own sentence in place of the clarification sentence, and the status
// sentence recomposed. Offers, candidates and the prompt are left as stored.
// The stored row itself is never touched: result is the caller's copy.
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
	limitations := make([]string, 0, len(result.Limitations)+1)
	replaced := false
	for _, limitation := range result.Limitations {
		if limitation == clarificationRequiredLimitationOne || limitation == clarificationRequiredLimitation {
			if !replaced {
				limitations = append(limitations, declaredKindTerminalLimitation)
				replaced = true
			}
			continue
		}
		limitations = append(limitations, limitation)
	}
	if !replaced {
		limitations = append([]string{declaredKindTerminalLimitation}, limitations...)
	}
	result.Limitations = limitations
	result.Status = InvestigationNoMatch
	result.RefusalBasis = declaredKindTerminalBasis
	result.DeterministicAnswer = statusSentence(InvestigationNoMatch, result.SubjectResolution)
	answerability.Repaired = true
	return answerability
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
