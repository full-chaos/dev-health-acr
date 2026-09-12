package contextfabric

// THE TURN-ONE REQUEST IDENTITY.
//
// A window-only continuation must be the SAME QUESTION, asked the same way,
// with only the evidence window confirmed. The public plan records exactly one
// answer-shaping input -- the effective response byte budget -- so admission
// could compare only that one, and the other answer-shaping inputs rode the
// carrier unchecked: a turn that changed max_drivers alone was served under the
// carrier's plan while the changed cap truncated its drivers downstream.
//
// This digest is what closes that. It is taken at TURN ONE over every request
// input that shapes the answer and is not otherwise recorded, stored in the
// snapshot beside the reading, and recomputed on the continuing turn. Different
// digest, different question: the turn takes the fresh path.
//
// WHAT IT COVERS, and why the list is exactly this:
//   - all four requested_scope parts. A stated scope is independently a
//     disqualifier at admission, so the digest does not decide those turns --
//     it records what turn one was asked, so the identity is complete and a
//     later relaxation of that disqualifier cannot silently widen it.
//   - the seven answer-shaping options no other field records:
//     max_subject_candidates, max_cohort_members, max_relationship_paths,
//     max_drivers, max_evidence_refs, allow_clarification and
//     window_confirmation_mode. max_serialized_bytes is NOT here: the plan
//     records its effective value and admission compares it there, as the
//     effective value on both sides. include_debug is not here either: it
//     changes what is disclosed, never what is planned or retrieved.
//   - the conversation MINUS THE REFERENCED EXCHANGE. The continuing turn
//     legitimately carries the prior exchange it is continuing; comparing it
//     whole would make every continuation a fresh turn. The referenced
//     exchange is identified by content: a user turn whose bytes are the
//     referenced result's question, and the assistant turn immediately after
//     it. Everything else the caller has added since is compared.
//
// THE RECIPE IS VERSIONED. A digest carries the version that produced it, and
// a carrier stamped with a different one is not compared under today's recipe
// -- it is a reading this build cannot verify, like a family table it does not
// know.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// SemanticRequestIdentityVersion is the recipe in force. Bump it whenever the
// covered set or the canonical form changes.
const SemanticRequestIdentityVersion = "request-identity.v1"

// SemanticRequestIdentity is the turn-one request identity: a digest over the
// answer-shaping request inputs nothing else records, plus the version of the
// recipe that produced it.
type SemanticRequestIdentity struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// requestIdentityDocument is the canonical form the digest is taken over. It is
// an explicit struct, never a reflected walk of the request: a field joins the
// identity by being named here, so adding a request field cannot silently join
// or silently miss it. Every field is present on every turn -- no omitempty --
// so a zero value and an absent one cannot collide.
type requestIdentityDocument struct {
	RepositorySlugs []string                               `json:"repository_slugs"`
	ProjectIDs      []string                               `json:"project_ids"`
	TeamIDs         []string                               `json:"team_ids"`
	SubjectHints    []contractsv1.ContextFabricSubjectHint `json:"subject_hints"`

	MaxSubjectCandidates   int                                             `json:"max_subject_candidates"`
	MaxCohortMembers       int                                             `json:"max_cohort_members"`
	MaxRelationshipPaths   int                                             `json:"max_relationship_paths"`
	MaxDrivers             int                                             `json:"max_drivers"`
	MaxEvidenceRefs        int                                             `json:"max_evidence_refs"`
	AllowClarification     bool                                            `json:"allow_clarification"`
	WindowConfirmationMode contractsv1.ContextFabricWindowConfirmationMode `json:"window_confirmation_mode"`

	Conversation []contractsv1.ContextFabricConversationTurn `json:"conversation"`
}

// conversationWithoutReferencedExchange drops the exchange the continuing turn
// is continuing: a user turn whose content is the referenced question byte for
// byte, and the assistant turn immediately following it. referencedQuestion
// empty (a turn that references nothing) drops nothing.
//
// The rule is stated on CONTENT because a conversation turn carries no result
// id to join on. It is deliberately narrow: only an exact-byte user turn is
// dropped, so a caller who rephrases the earlier question keeps it in the
// comparison and gets the fresh path.
func conversationWithoutReferencedExchange(turns []contractsv1.ContextFabricConversationTurn, referencedQuestion string) []contractsv1.ContextFabricConversationTurn {
	kept := make([]contractsv1.ContextFabricConversationTurn, 0, len(turns))
	if referencedQuestion == "" {
		return append(kept, turns...)
	}
	skipAssistant := false
	for _, turn := range turns {
		if turn.Role == contractsv1.ContextFabricConversationUser && turn.Content == referencedQuestion {
			skipAssistant = true
			continue
		}
		if skipAssistant && turn.Role == contractsv1.ContextFabricConversationAssistant {
			skipAssistant = false
			continue
		}
		skipAssistant = false
		kept = append(kept, turn)
	}
	return kept
}

// SemanticRequestIdentityOf computes the identity for one request. On turn one
// referencedQuestion is empty and the whole conversation is covered; on a
// continuing turn it is the carrier's question, whose exchange is dropped.
func SemanticRequestIdentityOf(request InvestigationRequest, referencedQuestion string) SemanticRequestIdentity {
	scope := request.RequestedScope
	doc := requestIdentityDocument{
		RepositorySlugs:        append([]string{}, scope.RepositorySlugs...),
		ProjectIDs:             append([]string{}, scope.ProjectIDs...),
		TeamIDs:                append([]string{}, scope.TeamIDs...),
		SubjectHints:           append([]contractsv1.ContextFabricSubjectHint{}, scope.SubjectHints...),
		MaxSubjectCandidates:   request.Options.MaxSubjectCandidates,
		MaxCohortMembers:       request.Options.MaxCohortMembers,
		MaxRelationshipPaths:   request.Options.MaxRelationshipPaths,
		MaxDrivers:             request.Options.MaxDrivers,
		MaxEvidenceRefs:        request.Options.MaxEvidenceRefs,
		AllowClarification:     request.Options.AllowClarification,
		WindowConfirmationMode: request.Options.WindowConfirmationMode,
		Conversation:           conversationWithoutReferencedExchange(request.Conversation, referencedQuestion),
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		// Marshal of this document cannot fail for any value the request
		// contract admits; an error here is a defect, and an EMPTY digest is
		// how it is made loud rather than quietly equal to everything.
		return SemanticRequestIdentity{Version: SemanticRequestIdentityVersion}
	}
	sum := sha256.Sum256(encoded)
	return SemanticRequestIdentity{Version: SemanticRequestIdentityVersion, Digest: hex.EncodeToString(sum[:])}
}

// Comparable reports whether this identity can be compared under today's
// recipe: it must name the version in force and carry a digest.
func (i SemanticRequestIdentity) Comparable() bool {
	return i.Version == SemanticRequestIdentityVersion && i.Digest != ""
}

// Equal compares two identities. Two identities produced by different recipes
// are never equal, whatever their digests say.
func (i SemanticRequestIdentity) Equal(other SemanticRequestIdentity) bool {
	return i.Comparable() && other.Comparable() && i.Digest == other.Digest
}
