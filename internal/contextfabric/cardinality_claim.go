package contextfabric

import (
	"fmt"

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
const cardinalityClaimIDPrefix = "claim_cardinality_"

// cardinalityClaimField names the field the claim asserts: the member kind
// being counted, suffixed so the field reads as a count rather than as the
// kind itself.
func cardinalityClaimField(kind SubjectKind) string {
	return string(kind) + "_count"
}

// cardinalityClaim builds the claim for a computed cardinality, or reports
// false when there is nothing to claim.
//
// The SUBJECT is the ORGANIZATION the investigation is scoped to. A population
// count is not a fact about any one member -- claiming it against a member
// would assert that member has 36 of something -- and the organization is the
// only subject the count is true of. Its canonical id is the principal's own
// org id, which is the identity the whole investigation already runs under.
func cardinalityClaim(principal storage.Principal, cardinality MembershipCardinality) (ClaimedFact, bool) {
	if !cardinality.Resolved || cardinality.Kind == "" {
		return ClaimedFact{}, false
	}
	if principal.OrgID == "" {
		// No organization identity, no subject to claim against. Reported as
		// an absence rather than claimed against a fabricated subject.
		return ClaimedFact{}, false
	}
	served := int64(cardinality.Served)
	return ClaimedFact{
		ClaimID: fmt.Sprintf("%s%s", cardinalityClaimIDPrefix, cardinality.Kind),
		Kind:    contractsv1.ContextFabricFactCardinality,
		Subject: SubjectRef{
			Kind:        SubjectOrganization,
			CanonicalID: principal.OrgID,
			Label:       principal.OrgID,
		},
		Field: cardinalityClaimField(cardinality.Kind),
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
	if cardinality.Declared > cardinality.Served {
		return fmt.Sprintf("Counted %d %s of %d found.", cardinality.Served, noun, cardinality.Declared)
	}
	return fmt.Sprintf("Counted %d %s.", cardinality.Served, noun)
}

// cardinalityNoun pluralises the counted kind for the answer sentence.
//
// Naive and deliberately so: every member of the subject-kind vocabulary is a
// regular noun ("team", "project", "repository"), so an "s" is correct for all
// of them and a table would be ceremony around one rule. A future kind with an
// irregular plural is the thing that makes this a table, and it does not exist
// yet.
func cardinalityNoun(kind SubjectKind, count int) string {
	if count == 1 {
		return string(kind)
	}
	return string(kind) + "s"
}
