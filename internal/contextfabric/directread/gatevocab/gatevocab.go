// Package gatevocab holds the closed vocabularies of the direct-read subject
// gate (package directread, CHAOS-7071) with no dependencies, so the event
// specification (eventspec) can derive its field vocabularies from them
// without an import cycle (graphrank, which directread uses, imports
// eventspec). directread re-exports every name here.
package gatevocab

// AuthorizationLogMessage is the one Info line every gate decision writes.
const AuthorizationLogMessage = "context fabric direct read authorization"

// PrincipalClass classifies a caller's repository grant the way the shared
// authorization predicate (graphrank.AuthorizedAttributes) reads it.
type PrincipalClass string

const (
	// ClassUnrestricted: no repository grant list at all. The shared
	// predicate applies no repository check to such a caller; the subject
	// gate still proves every subject is a node of the caller's own
	// organization graph.
	ClassUnrestricted PrincipalClass = "unrestricted"
	// ClassUniversal: the grant list holds "*".
	ClassUniversal PrincipalClass = "universal"
	// ClassRestricted: specific repositories or owner wildcards. Teams and
	// projects need ownership-reached repositories in the grant.
	ClassRestricted PrincipalClass = "restricted"
)

// PrincipalClassVocabulary is the closed set of principal classes.
func PrincipalClassVocabulary() [3]PrincipalClass {
	return [3]PrincipalClass{ClassUnrestricted, ClassUniversal, ClassRestricted}
}

// PublicDeniedOrNotFound is the ONE public answer for a subject the caller
// may not read: denied, absent from the caller's graph, and malformed all
// read the same on the wire.
const PublicDeniedOrNotFound = "denied_or_not_found"

// PublicAdmitted is the public answer for an admitted subject.
const PublicAdmitted = "admitted"

// SubjectOutcome is the internal, per-subject decision. It never leaves
// acr-api except as a telemetry count; the wire sees Public().
type SubjectOutcome string

const (
	SubjectAdmitted SubjectOutcome = "admitted"
	// SubjectDenied: the node exists in the caller's graph and the shared
	// predicate refuses the caller.
	SubjectDenied SubjectOutcome = "denied"
	// SubjectAbsent: no node of the caller's organization graph carries the
	// identity (a guessed id, another organization's id, or a graph never
	// projected).
	SubjectAbsent SubjectOutcome = "absent"
	// SubjectOwnershipUnproven: a team or project the predicate admits, but
	// no repository it reaches through ownership is in a restricted
	// caller's grant.
	SubjectOwnershipUnproven SubjectOutcome = "ownership_unproven"
	// SubjectOrganizationMismatch: an organization subject that is not the
	// caller's own organization.
	SubjectOrganizationMismatch SubjectOutcome = "organization_mismatch"
	// SubjectInvalid: no kind, an unknown kind, or a blank id.
	SubjectInvalid SubjectOutcome = "invalid"
)

// SubjectOutcomeVocabulary is the closed set of per-subject outcomes.
func SubjectOutcomeVocabulary() [6]SubjectOutcome {
	return [6]SubjectOutcome{SubjectAdmitted, SubjectDenied, SubjectAbsent, SubjectOwnershipUnproven, SubjectOrganizationMismatch, SubjectInvalid}
}

// Public maps an outcome to the wire answer.
func (o SubjectOutcome) Public() string {
	if o == SubjectAdmitted {
		return PublicAdmitted
	}
	return PublicDeniedOrNotFound
}

// Decision is the gate's overall answer.
type Decision string

const (
	// DecisionAdmitted: every requested subject is admitted.
	DecisionAdmitted Decision = "admitted"
	// DecisionPartial: at least one subject is admitted and at least one is
	// not. AuthorizedSubjects holds the admitted ones only.
	DecisionPartial Decision = "partial"
	// DecisionDenied: no subject is admitted. AuthorizedSubjects is empty
	// and a read with it is refused.
	DecisionDenied Decision = "denied"
	// DecisionUnavailable: the decision could not be taken. Nothing is
	// served; the tool fails with a retryable unavailability.
	DecisionUnavailable Decision = "unavailable"
)

// DecisionVocabulary is the closed set of decisions.
func DecisionVocabulary() [4]Decision {
	return [4]Decision{DecisionAdmitted, DecisionPartial, DecisionDenied, DecisionUnavailable}
}

// Reason is why the decision came out as it did.
type Reason string

const (
	ReasonSubjectsAdmitted Reason = "subjects_admitted"
	// Refusal reasons, in precedence order (the first that applies names a
	// denied or partial decision).
	ReasonPrincipalInvalid     Reason = "principal_invalid"
	ReasonNoSubjects           Reason = "no_subjects"
	ReasonSubjectLimit         Reason = "subject_limit"
	ReasonOrganizationMismatch Reason = "organization_mismatch"
	ReasonSubjectDenied        Reason = "subject_denied"
	ReasonOwnershipUnproven    Reason = "ownership_unproven"
	ReasonSubjectAbsent        Reason = "subject_absent"
	ReasonSubjectInvalid       Reason = "subject_invalid"
	ReasonGraphNotProjected    Reason = "graph_not_projected"
	// Unavailable reasons.
	ReasonAuthorizerMissing Reason = "authorizer_missing"
	ReasonGraphReadFailed   Reason = "graph_read_failed"
)

// ReasonVocabulary is the closed set of reasons.
func ReasonVocabulary() [12]Reason {
	return [12]Reason{
		ReasonSubjectsAdmitted,
		ReasonPrincipalInvalid, ReasonNoSubjects, ReasonSubjectLimit, ReasonOrganizationMismatch,
		ReasonSubjectDenied, ReasonOwnershipUnproven, ReasonSubjectAbsent, ReasonSubjectInvalid, ReasonGraphNotProjected,
		ReasonAuthorizerMissing, ReasonGraphReadFailed,
	}
}

// ErrorClassVocabulary is the closed set of error classes an unavailable
// decision records. It names the failure without its text.
func ErrorClassVocabulary() [4]string {
	return [4]string{"deadline_exceeded", "canceled", "dependency_unavailable", "graph_error"}
}
