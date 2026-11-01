// Package directread holds the shared authorization base of the direct data
// tools of CHAOS-7036 (read_facts, run_operation, find_subjects,
// read_relationships): the subject gate every direct read must pass, the
// principal classes the gate and later edge rules share, and the one entry
// point through which a direct tool may reach the fact registry.
//
// Why it exists (CHAOS-7071, S0). contextfabric.FactCapabilityRegistry's
// ReadFacts authorizes no subject; it checks only that the caller's
// organization id is not empty. The engine authorizes subjects BEFORE it, in
// ResolveSubjects. A direct tool skips the engine, so without its own gate a
// repository-restricted caller could read any subject of its organization by
// id. This package makes that impossible by construction: FactReader.Read
// takes an AuthorizedSubjects value that only SubjectGate.Authorize can make,
// never a list of ids.
//
// Design of record: the CHAOS-7036 design, sections E.2 (subject gate) and
// J.6 (slice S0). Section E.2's differences from the stored-result gate
// (contextfabric.StoredResultGate) are deliberate:
//
//   - the graph lookup runs for EVERY principal, also an unrestricted one, so
//     an id that is not a node of the caller's own organization graph is
//     never served (a guessed id or another organization's id);
//   - a team or project is admitted to a repository-restricted caller only
//     when a repository it reaches through OWNERSHIP is in the grant. A
//     project node's repository list is the "*" wildcard (projection writes
//     an empty list that way), which admits every caller under the shared
//     predicate; that is not enough here;
//   - denied, absent and malformed subjects give ONE public answer,
//     denied_or_not_found, so a caller cannot probe which ids exist. The
//     telemetry records which one it was.
package directread

import (
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread/gatevocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The gate's closed vocabularies live in gatevocab (a leaf package the event
// specification can import); they are re-exported here unchanged.
type (
	PrincipalClass = gatevocab.PrincipalClass
	SubjectOutcome = gatevocab.SubjectOutcome
	Decision       = gatevocab.Decision
	Reason         = gatevocab.Reason
)

const (
	ClassUnrestricted = gatevocab.ClassUnrestricted
	ClassUniversal    = gatevocab.ClassUniversal
	ClassRestricted   = gatevocab.ClassRestricted

	PublicDeniedOrNotFound = gatevocab.PublicDeniedOrNotFound
	PublicAdmitted         = gatevocab.PublicAdmitted

	SubjectAdmitted             = gatevocab.SubjectAdmitted
	SubjectDenied               = gatevocab.SubjectDenied
	SubjectAbsent               = gatevocab.SubjectAbsent
	SubjectOwnershipUnproven    = gatevocab.SubjectOwnershipUnproven
	SubjectOrganizationMismatch = gatevocab.SubjectOrganizationMismatch
	SubjectInvalid              = gatevocab.SubjectInvalid

	DecisionAdmitted    = gatevocab.DecisionAdmitted
	DecisionPartial     = gatevocab.DecisionPartial
	DecisionDenied      = gatevocab.DecisionDenied
	DecisionUnavailable = gatevocab.DecisionUnavailable

	ReasonSubjectsAdmitted     = gatevocab.ReasonSubjectsAdmitted
	ReasonPrincipalInvalid     = gatevocab.ReasonPrincipalInvalid
	ReasonNoSubjects           = gatevocab.ReasonNoSubjects
	ReasonSubjectLimit         = gatevocab.ReasonSubjectLimit
	ReasonOrganizationMismatch = gatevocab.ReasonOrganizationMismatch
	ReasonSubjectDenied        = gatevocab.ReasonSubjectDenied
	ReasonOwnershipUnproven    = gatevocab.ReasonOwnershipUnproven
	ReasonSubjectAbsent        = gatevocab.ReasonSubjectAbsent
	ReasonSubjectInvalid       = gatevocab.ReasonSubjectInvalid
	ReasonGraphNotProjected    = gatevocab.ReasonGraphNotProjected
	ReasonAuthorizerMissing    = gatevocab.ReasonAuthorizerMissing
	ReasonGraphReadFailed      = gatevocab.ReasonGraphReadFailed

	// AuthorizationLogMessage is the one Info line every gate decision
	// writes. Its shape is declared in eventspec (DirectReadAuthorization)
	// and certified from the bytes SlogRecorder writes.
	AuthorizationLogMessage = gatevocab.AuthorizationLogMessage
)

// PrincipalClassVocabulary is the closed set of principal classes.
func PrincipalClassVocabulary() [3]PrincipalClass { return gatevocab.PrincipalClassVocabulary() }

// SubjectOutcomeVocabulary is the closed set of per-subject outcomes.
func SubjectOutcomeVocabulary() [6]SubjectOutcome { return gatevocab.SubjectOutcomeVocabulary() }

// DecisionVocabulary is the closed set of decisions.
func DecisionVocabulary() [4]Decision { return gatevocab.DecisionVocabulary() }

// ReasonVocabulary is the closed set of reasons.
func ReasonVocabulary() [12]Reason { return gatevocab.ReasonVocabulary() }

// ErrorClassVocabulary is the closed set of error classes.
func ErrorClassVocabulary() [4]string { return gatevocab.ErrorClassVocabulary() }

// ClassifyPrincipal returns the caller's class: auth.ClassifyGrant through
// PrincipalClassForGrant, the same grant class the stored-result gate's
// principal scope derives from, so both gates read a grant alike.
func ClassifyPrincipal(principal storage.Principal) PrincipalClass {
	return PrincipalClassForGrant(auth.ClassifyGrant(principal))
}

// PrincipalClassForGrant is the subject gate's view of a grant class
// (CHAOS-12297). project_only is a restricted subtype and reads as
// ClassRestricted, and no wire value changes. The subject gate reads no
// ownership reach for it: the project-only node rule decides its projects and
// denies its teams (CHAOS-12299). An unknown class is restricted (fail closed).
func PrincipalClassForGrant(class auth.GrantClass) PrincipalClass {
	switch class {
	case auth.GrantUnrestricted:
		return ClassUnrestricted
	case auth.GrantUniversal:
		return ClassUniversal
	case auth.GrantRestricted, auth.GrantProjectOnly:
		return ClassRestricted
	}
	return ClassRestricted
}
