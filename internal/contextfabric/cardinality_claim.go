package contextfabric

import (
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The `membership_cardinality` result, minted as a claim.
//
// WHY A CLAIM AND NOT JUST A ROW. The step already states its result on the
// completeness outcomes, which is where an OPERATOR reads it. A reader of the
// ANSWER had only the prose, and prose is the model's: an answer could say
// "fourteen projects" while the row said 36, and nothing tied the two
// together. A claim makes the number an addressable field with a subject, a
// field name and a value -- the same shape every other asserted number in the
// answer has.
//
// IT HAS NO CITATION, AND THAT IS THE POINT RATHER THAN A GAP. Every other
// minted claim re-derives against `facts.Facts` through
// validateMintedClaimsGrounded, because every other minted claim asserts
// something a PRODUCER READ. This one asserts something the SERVER COMPUTED,
// over the resolved member set, reading no fact at all. Routing it through
// that validator would fail by construction and passing it a fabricated
// citation would be worse, so it is appended separately -- and its grounding
// is pinned differently: the claim's value must equal the count the same pass
// already stated on its outcome row, which is asserted by a guard. One number,
// two surfaces, and they cannot drift.
// The id every server-minted cardinality claim carries, and the prefix no
// model-authored claim may use.
//
// RESERVED, NOT MERELY CONVENTIONAL. The id is fixed per kind, so a model that
// emits the same string produces two claims with one id and the assembled
// answer fails whole-result validation for duplicate ids -- the server's own
// claim taking the answer down with it. The namespace is refused at the
// boundary where model output is admitted (SynthesisDraft.ValidateAgainst), so
// the collision is impossible rather than caught late, and the colon shape is
// what makes the reservation legible: no model-authored id in the corpus has
// ever contained one, and the refusal names the prefix rather than the
// individual ids, so a kind added later inherits the protection.
const cardinalityClaimIDPrefix = "server:cardinality:"

// cardinalityClaimField names the field the claim asserts: the member kind
// being counted, suffixed so the field reads as a count rather than as the
// kind itself.
func cardinalityClaimField(kind SubjectKind) string {
	return string(kind) + "_count"
}

// cardinalityOwed is THE precondition, and it is the only one.
//
// The count reaches a reader on three surfaces -- the outcome row, the claim
// and the answer sentence -- and they must agree about whether the answer owes
// a count at all. They did not: the row was stated only when the frame carried
// a count obligation, while the claim and the sentence were minted for every
// resolved cohort. A cohort answer that owed no count therefore served the
// number twice with no row to reconcile it against, which is a document
// disagreeing with itself.
//
// So all three now ask ONE question, of the same authority: does this answer's
// own planning carry a count obligation, and did the count resolve. The row's
// gate is countRequirement over the planning rows; this reads the same rows
// through the same function rather than a second predicate that could drift
// from it.
func cardinalityOwed(rows []RequirementOutcomeRow, cardinality MembershipCardinality) bool {
	if !cardinality.Resolved || cardinality.Kind == "" {
		return false
	}
	requirement, _ := countRequirement(rows)
	return requirement != ""
}

// cardinalityOwedByFrame is the same question asked EARLIER IN THE TURN, where
// the rows do not exist yet.
//
// Assembly mints the claim and composes the sentence before finalization seeds
// the outcome rows, so it cannot read the rows the way the reuse path and the
// row builder do. It asks the SAME derivation instead: seedRequirementOutcomes
// is what finalization will run, and the derivation is pure, so the answer here
// and the rows there agree by construction rather than by two predicates
// happening to match.
//
// That purity is the whole reason this is a second entry point and not a second
// rule -- both funnel into countRequirement, which is the row's own gate.
func cardinalityOwedByFrame(frame *QuestionFrame, deriver RequirementDeriver, cardinality MembershipCardinality) bool {
	if !cardinality.Resolved || cardinality.Kind == "" {
		return false
	}
	return cardinalityOwed(seedRequirementOutcomes(frame, deriver), cardinality)
}

// cardinalityClaim builds the claim for a computed cardinality, or reports
// false when there is nothing to claim.
//
// The SUBJECT is the counted population itself -- see cardinalityClaimSubject.
// A population count is not a fact about any one MEMBER of that population --
// claiming it against a member would assert that member has 36 of something --
// so the subject is always the bounding population, never a member.
func cardinalityClaim(principal storage.Principal, cardinality MembershipCardinality) (ClaimedFact, bool) {
	if !cardinality.Resolved || cardinality.Kind == "" {
		return ClaimedFact{}, false
	}
	subject, ok := cardinalityClaimSubject(principal, cardinality.Scope)
	if !ok {
		return ClaimedFact{}, false
	}
	served := int64(cardinality.Served)
	return ClaimedFact{
		ClaimID: fmt.Sprintf("%s%s", cardinalityClaimIDPrefix, cardinality.Kind),
		Kind:    contractsv1.ContextFabricFactCardinality,
		Subject: subject,
		Field:   cardinalityClaimField(cardinality.Kind),
		// SERVED, not Declared. The claim states what the answer CARRIES,
		// which is what a reader can check against the members in front of
		// them; the population it was cut from is disclosed on the outcome
		// row, where the cause travels with it. Claiming the population here
		// would assert a number the document cannot show the workings for.
		Value: contractsv1.ContextFabricScalarValue{
			Integer: &served,
		},
	}, true
}

// cardinalityClaimSubject is the subject a cardinality claim is about: the
// population the count describes, never a member of it.
//
// AN ANCHOR-BOUND COUNT NAMES THE ANCHOR. When the frame counts members under
// an anchor and resolution committed one (count_population_scope.go's
// anchor_committed), the counted population IS that anchor -- of the anchor's
// OWN kind and canonical id, as resolution bound it, never the reading's
// merely-stated kind (which the anchor's own AnchorSubjectKind can disagree
// with when the question named no explicit kind).
//
// EVERY OTHER COUNTED POPULATION IS THE ORGANIZATION. organization_scope, and
// the zero-value Scope a caller that predates this decision still passes, both
// describe a population no anchor bounds -- a discovered kind, a grouped or
// compared set, or the organization itself -- and the organization is the only
// subject such a count is true of. Its canonical id is the principal's own org
// id, the identity the whole investigation already runs under.
func cardinalityClaimSubject(principal storage.Principal, scope CountPopulationScope) (SubjectRef, bool) {
	if scope.Decision == CountPopulationScopeAnchorCommitted {
		if scope.AnchorSubjectKind == "" || scope.AnchorID == "" {
			// An invariant this file relies on elsewhere did not hold --
			// report an absence rather than claim a fabricated subject.
			return SubjectRef{}, false
		}
		return SubjectRef{Kind: scope.AnchorSubjectKind, CanonicalID: scope.AnchorID, Label: scope.AnchorID}, true
	}
	if principal.OrgID == "" {
		// No organization identity, no subject to claim against. Reported as
		// an absence rather than claimed against a fabricated subject.
		return SubjectRef{}, false
	}
	return SubjectRef{Kind: SubjectOrganization, CanonicalID: principal.OrgID, Label: principal.OrgID}, true
}

// cardinalityAnswerSentence is the prose half: the number the claim asserts,
// stated in the served answer.
//
// COMPOSED WHERE THE CLAIM IS, not in composeDeterministicAnswerFrom, even
// though that renderer already accepts a claims slice it deliberately ignores.
// That renderer runs over the model's DRAFT, before this claim exists -- it
// could not read it if it wanted to. Composing both from one site is what
// makes the sentence and the claim the same number by construction rather than
// by two derivations agreeing.
//
// It states the SERVED count and, when the population was larger, says so.
// "Fourteen of thirty-six" is the honest reading of a clamped cohort, and it is
// the disclosure the outcome row carries in its own vocabulary.
func cardinalityAnswerSentence(cardinality MembershipCardinality) string {
	if !cardinality.Resolved || cardinality.Kind == "" {
		return ""
	}
	noun := cardinalityNoun(cardinality.Kind, cardinality.Served)
	if cardinality.Kind == SubjectWorkItem && cardinality.PopulationIncomplete {
		return fmt.Sprintf("Counted at least %d %s.", cardinality.Served, noun)
	}
	if cardinality.Declared > cardinality.Served {
		return fmt.Sprintf("Counted %d %s of %d found.", cardinality.Served, noun, cardinality.Declared)
	}
	return fmt.Sprintf("Counted %d %s.", cardinality.Served, noun)
}

// cardinalityNoun pluralises the counted kind for the answer sentence.
// Work-item counts use the human label rather than the wire discriminator.
func cardinalityNoun(kind SubjectKind, count int) string {
	noun := string(kind)
	if kind == SubjectWorkItem {
		noun = "work item"
	}
	if count == 1 {
		return noun
	}
	return noun + "s"
}

// resultCarriesCardinalityClaim reports whether the served document actually
// carries the count as a claim.
//
// Read off the document rather than remembered from the mint, so the telemetry
// cannot say "claimed" about an answer that does not carry one.
func resultCarriesCardinalityClaim(result InvestigationResult) bool {
	return cardinalityClaimIndex(result.ClaimedFacts) >= 0
}

// cardinalityClaimIndex returns the index of the first claim of kind
// cardinality in claims, or -1 when none is present.
//
// SHARED BY THE MINT SITES so "does this document already carry one" and
// "which one, to correct it" read the same claim -- never two predicates
// that could disagree about which entry is the cardinality claim.
func cardinalityClaimIndex(claims []ClaimedFact) int {
	for i, claim := range claims {
		if claim.Kind == contractsv1.ContextFabricFactCardinality {
			return i
		}
	}
	return -1
}

// cardinalityClaimAdmitted reports whether one more claim fits.
//
// EXTRACTED SO THE BOUNDARY IS PINNABLE. The decision is one comparison, and
// inline it could only be exercised by building a document with 250 claims
// through the whole engine. The off-by-one here is the difference between an
// answer that serves without its count field and an answer that fails
// validation entirely, which is the kind of edge that deserves a test at 249,
// 250 and 251 rather than at whatever number a fixture happens to produce.
func cardinalityClaimAdmitted(existingClaims int) bool {
	return existingClaims < contractsv1.ContextFabricClaimedFactsMaxCount
}

// appendCardinalitySentence puts the count sentence on the answer and keeps the
// result inside the contract bound.
//
// THE SENTENCE STANDS; THE EARLIER PROSE GIVES. Appending blindly is what made
// a valid 11,985-rune answer oversized and got the whole result rejected: the
// composer had already truncated to the bound, so anything added afterwards
// broke it. Refusing to append instead would drop the count from an answer that
// owes one, which is the same silent loss on a different path.
//
// The count is the one part of this prose the server computed and the claim
// must agree with, so it is the part that survives: the earlier prose is
// truncated at a sentence boundary to make room. An answer that cannot fit even
// the sentence alone keeps the sentence and nothing else, which is degenerate
// but honest -- and unreachable in practice, the sentence being a few dozen
// runes against a bound of twelve thousand.
func appendCardinalitySentence(answer, sentence string) string {
	if sentence == "" {
		return answer
	}
	joined := strings.TrimSpace(answer + " " + sentence)
	if len([]rune(joined)) <= deterministicAnswerMaxLength {
		return joined
	}
	room := deterministicAnswerMaxLength - len([]rune(sentence)) - 1
	if room <= 0 {
		return truncateAtSentenceBoundary(sentence, deterministicAnswerMaxLength)
	}
	return strings.TrimSpace(truncateAtSentenceBoundary(answer, room) + " " + sentence)
}

// cardinalityClaimSubjectRepair is the ONE construction that decides
// whether, and how, to correct a stored document's EXISTING cardinality
// claim subject -- shared by every serving surface that re-reads a stored
// document, so a claim minted under an earlier authority is corrected the
// same way wherever it is read again, not once per surface. Reading is
// already derived here (storedCountReading, Frame + AnchorKind); the reuse
// path already carries its own, RepairStoredCardinalityClaimSubject below
// derives it for an external caller.
//
// IT REPAIRS, IT NEVER MINTS. A document with no cardinality claim at all --
// predating the step entirely -- is untouched; that absence is the reuse
// path's own backfill concern (engine.go), a different decision with a
// different precondition. This function only corrects a claim that is
// already there.
//
// THE IDENTITY IS UNCHANGED. Only Subject moves; ClaimID, Kind, Field and
// Value are the claim's own and are never touched, so nothing that cites the
// claim by id is disturbed and no second cardinality claim is ever minted.
//
// SILENT ON A READING THAT CANNOT RE-DERIVE THE SCOPE. When the stored
// semantic reading is absent, unreadable, or the anchor it names cannot be
// resolved from the document's own subject resolution, this function leaves
// the claim exactly as stored -- a decision it has no evidence for is not a
// decision it is entitled to force onto an already-served claim, and leaving
// it is never worse than what was already being served.
//
// Reports whether it changed anything, so a caller that logs repairs (as the
// by-id read route already does for other fields) has something to log.
func cardinalityClaimSubjectRepair(principal storage.Principal, result *InvestigationResult, reading storedCountReading) bool {
	if result == nil {
		return false
	}
	idx := cardinalityClaimIndex(result.ClaimedFacts)
	if idx < 0 {
		return false
	}
	scope := DecideCountPopulationScope(reading.Frame, reading.AnchorKind, result.SubjectResolution, nil, CohortMemberSourceNotApplicable)
	if !scope.Counts() {
		return false
	}
	subject, ok := cardinalityClaimSubject(principal, scope)
	if !ok || result.ClaimedFacts[idx].Subject == subject {
		return false
	}
	result.ClaimedFacts[idx].Subject = subject
	return true
}

// RepairStoredCardinalityClaimSubject is cardinalityClaimSubjectRepair for a
// caller outside this package (the by-id read route), which holds the raw
// persisted semantic state rather than an already-derived reading -- the
// same two arguments RepairStoredClarification already takes, for the same
// reason: this route never reaches the pass that would derive it fresh.
func RepairStoredCardinalityClaimSubject(principal storage.Principal, result *InvestigationResult, state *PersistedSemanticState, read SemanticStateReadStatus) bool {
	if result == nil {
		return false
	}
	reading := storedCountReadingOf(StoredInvestigationResult{Result: *result, SemanticState: state, SemanticStateRead: read})
	return cardinalityClaimSubjectRepair(principal, result, reading)
}
