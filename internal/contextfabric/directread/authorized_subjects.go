package directread

import (
	"context"
	"errors"
	"slices"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// AuthorizedSubjects is the gate's proof that a set of subjects was admitted,
// live, for one principal. Only SubjectGate.Authorize makes a non-zero value:
// its one field is a pointer to an unexported type, so no other package can
// build, fill or change one, and the zero value holds nothing. Every direct
// read function takes this value, never a list of ids.
//
// It is bound to the principal it was issued to (organization, subject,
// credential and repository grant). A read that presents it with any other
// principal is refused, so a value cannot be carried from one caller or one
// credential to another.
type AuthorizedSubjects struct {
	grant *subjectGrant
}

type subjectGrant struct {
	orgID            string
	subject          string
	credentialID     string
	repositoryScopes []string
	subjects         []contextfabric.SubjectRef
}

func issue(principal storage.Principal, subjects []contextfabric.SubjectRef) AuthorizedSubjects {
	return AuthorizedSubjects{grant: &subjectGrant{
		orgID:            principal.OrgID,
		subject:          principal.Subject,
		credentialID:     principal.CredentialID,
		repositoryScopes: slices.Clone(principal.RepositoryScopes),
		subjects:         slices.Clone(subjects),
	}}
}

// Subjects returns a copy of the admitted subjects (kind and canonical id
// only). It is empty for the zero value.
func (a AuthorizedSubjects) Subjects() []contextfabric.SubjectRef {
	if a.grant == nil {
		return nil
	}
	return slices.Clone(a.grant.subjects)
}

// Len is the number of admitted subjects.
func (a AuthorizedSubjects) Len() int {
	if a.grant == nil {
		return 0
	}
	return len(a.grant.subjects)
}

// IssuedTo reports whether the value was issued by the gate to exactly this
// principal.
func (a AuthorizedSubjects) IssuedTo(principal storage.Principal) bool {
	if a.grant == nil {
		return false
	}
	return a.grant.orgID != "" &&
		a.grant.orgID == principal.OrgID &&
		a.grant.subject == principal.Subject &&
		a.grant.credentialID == principal.CredentialID &&
		slices.Equal(a.grant.repositoryScopes, principal.RepositoryScopes)
}

// ErrUngatedRead is returned by every direct read handed an AuthorizedSubjects
// value that the gate did not issue to the calling principal, or that holds
// no subject. It is a programming defect in the calling tool, never a caller
// answer; tools map it to an internal error, not to a subject refusal.
var ErrUngatedRead = errors.New("direct read without a subject gate decision for this principal")

// FactSource is the fact registry seam
// (*contextfabric.FactCapabilityRegistry satisfies it).
type FactSource interface {
	ReadFacts(ctx context.Context, principal storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error)
}

// FactReader is the ONLY path from a direct tool to the fact registry. The
// registry authorizes no subject (it checks only that the organization id is
// set), so a direct tool must not hold the registry: it holds a FactReader,
// and a FactReader reads only subjects the gate admitted for this caller.
// TestNoDirectRegistryReadOutsideContextFabric pins that no production code
// outside internal/contextfabric calls ReadFacts itself.
type FactReader struct {
	source FactSource
}

// NewFactReader wraps the registry. A nil source makes every read fail.
func NewFactReader(source FactSource) *FactReader {
	if storage.IsNil(source) {
		return &FactReader{}
	}
	return &FactReader{source: source}
}

// Read reads facts for the gate-admitted subjects only. The request's own
// Subjects, Cohort and Scope are replaced: Subjects by the admitted set,
// Cohort and Scope by nothing (the registry derives scope itself). A
// requirement that names a subject outside the admitted set is refused.
func (r *FactReader) Read(ctx context.Context, principal storage.Principal, subjects AuthorizedSubjects, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	if !subjects.IssuedTo(principal) || subjects.Len() == 0 {
		return contextfabric.CanonicalFactBundle{}, ErrUngatedRead
	}
	if r == nil || r.source == nil {
		return contextfabric.CanonicalFactBundle{}, errors.New("direct fact reader has no fact source")
	}
	admitted := subjects.Subjects()
	allowed := make(map[string]struct{}, len(admitted))
	for _, subject := range admitted {
		allowed[subjectKey(subject)] = struct{}{}
	}
	for _, requirement := range request.Requirements {
		for _, subject := range requirement.Subjects {
			if _, ok := allowed[subjectKey(subject)]; !ok {
				return contextfabric.CanonicalFactBundle{}, ErrUngatedRead
			}
		}
	}
	request.Subjects = admitted
	request.Cohort = nil
	request.Scope = nil
	return r.source.ReadFacts(ctx, principal, request)
}

func subjectKey(subject contextfabric.SubjectRef) string {
	return string(subject.Kind) + "\x00" + subject.CanonicalID
}
