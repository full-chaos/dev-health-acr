package auth

import (
	"slices"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// GrantClass is the ONE classification of a caller's grant that every gate
// derives its own vocabulary from (CHAOS-12297, the c1 step of CHAOS-12228;
// design: docs/design/context-fabric-project-only-scope.md).
//
// The gates used to classify a grant three times (directread's principal
// class, the stored-result principal scope and the operation policy's caller
// class), each with its own copy of the same rule. A fourth class added to one
// copy would leave the others reading it as whatever their own default said.
// Now each vocabulary is an exhaustive mapping from this type, and the
// internal/cigates/grantclass gate fails when a site compares, switches on or
// transports a class without a recorded decision.
type GrantClass string

const (
	// GrantUnrestricted: no repository grant list at all.
	GrantUnrestricted GrantClass = "unrestricted"
	// GrantUniversal: the grant list holds "*".
	GrantUniversal GrantClass = "universal"
	// GrantRestricted: specific repositories or owner wildcards.
	GrantRestricted GrantClass = "restricted"
	// GrantProjectOnly: an organization with no repository inventory whose
	// caller is granted an explicit set of projects (CHAOS-12107). It is a
	// RESTRICTED subtype: every gate that does not decide it explicitly
	// takes the restricted path. ClassifyGrant returns it for a non-empty
	// storage.Principal.ProjectScopes (CHAOS-12299), which only the
	// access-read decoder sets once it accepts the ops project scope
	// (CHAOS-12301); until then nothing produces it.
	GrantProjectOnly GrantClass = "project_only"
)

// GrantClassVocabulary is the closed set of grant classes.
func GrantClassVocabulary() [4]GrantClass {
	return [4]GrantClass{GrantUnrestricted, GrantUniversal, GrantRestricted, GrantProjectOnly}
}

// IsRestricted reports whether the class takes the restricted path:
// restricted and project_only. An unknown class is restricted (fail closed).
func (c GrantClass) IsRestricted() bool {
	switch c {
	case GrantUnrestricted, GrantUniversal:
		return false
	case GrantRestricted, GrantProjectOnly:
		return true
	}
	return true
}

// ClassifyGrant returns the caller's grant class. A caller with a project grant
// (principal.ProjectScopes non-empty) is project_only, whatever its repository
// list says (CHAOS-12299); only the access-read decoder sets that grant
// (CHAOS-12301). Otherwise it reads the repository grant list: empty is
// unrestricted, any entry that trims to "*" is universal, and anything else is
// restricted.
func ClassifyGrant(principal storage.Principal) GrantClass {
	switch {
	case len(principal.ProjectScopes) > 0:
		return GrantProjectOnly
	case len(principal.RepositoryScopes) == 0:
		return GrantUnrestricted
	case slices.ContainsFunc(principal.RepositoryScopes, func(scope string) bool { return strings.TrimSpace(scope) == "*" }):
		return GrantUniversal
	default:
		return GrantRestricted
	}
}
