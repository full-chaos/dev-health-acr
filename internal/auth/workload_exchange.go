package auth

import (
	"context"
	"errors"
	"time"
)

// WorkloadAccessTokenLifetime is the RFC 8693 access token lifetime for a
// workload-exchanged token: "~10-min TTL capped at subject expiry, no
// refresh tokens -- re-exchange the projected token" (CHAOS-4013 design
// brief).
const WorkloadAccessTokenLifetime = 10 * time.Minute

var (
	// ErrSubjectTokenInvalid is returned by a SubjectTokenValidator (and
	// wraps any failure downstream of it) for a subject_token that fails
	// validation: expired, wrong audience, not authenticated, or otherwise
	// rejected by Kubernetes TokenReview.
	ErrSubjectTokenInvalid = errors.New("subject token failed validation")
	// ErrWorkloadBindingNotFound is returned when a validated subject
	// identity resolves to no binding, or to a disabled one. It is
	// deliberately indistinguishable from "not found" to a caller: a
	// disabled binding must not leak its own existence.
	ErrWorkloadBindingNotFound = errors.New("workload binding not found or disabled")
	// ErrScopeNotGranted is returned when a request's scope parameter asks
	// for more than the resolved binding grants -- RFC 8693 scope may only
	// narrow, never widen, a grant.
	ErrScopeNotGranted = errors.New("requested scope exceeds the workload binding's grant")
)

// SubjectIdentity is the validated k8s ServiceAccount identity a subject
// token asserts, established ONLY via Kubernetes TokenReview -- never by
// decoding the JWT's own claims directly, which would trust a value the
// API server has not itself vouched for.
type SubjectIdentity struct {
	TrustDomain        string
	Namespace          string
	ServiceAccountName string
	ServiceAccountUID  string
	// ExpiresAt is the subject token's own expiry, used to cap the issued
	// access token's lifetime at WorkloadAccessTokenLifetime (see
	// AccessTokenIssuer).
	ExpiresAt time.Time
}

// SubjectTokenValidator validates an RFC 8693 subject_token and returns
// the SubjectIdentity it asserts. KubernetesTokenReviewValidator is the
// only production implementation; this seam exists so CHAOS-3270's future
// control plane can supply a different validator without the token-
// exchange handler changing.
type SubjectTokenValidator interface {
	Validate(ctx context.Context, subjectToken string) (SubjectIdentity, error)
}

// WorkloadBinding is the resolved grant for a validated subject identity:
// which organization it belongs to, what role it holds, and which
// repositories it may read. Mirrors storage.WorkloadBinding but stays a
// distinct type so this package's public seam does not force every caller
// to import internal/storage.
type WorkloadBinding struct {
	BindingID        string
	OrgID            string
	Role             string
	RepositoryScopes []string
}

// GrantResolver resolves a validated SubjectIdentity to its declarative
// WorkloadBinding. Implementations must resolve ONLY from the {trust
// domain, namespace, service account name, service account uid} tuple --
// never from anything request-supplied (design brief, CHAOS-4013).
type GrantResolver interface {
	Resolve(ctx context.Context, identity SubjectIdentity) (WorkloadBinding, error)
}

// AccessTokenIssuer issues an opaque ACR access token for a resolved
// workload binding. The production implementation
// (NewWorkloadAccessTokenIssuer) reuses the existing credential lifecycle
// machinery (hash-only storage, the same fcacr_ token shape every other
// credential uses) so a workload-exchanged token is indistinguishable in
// storage or at authentication time from any other credential, except for
// its WorkloadBindingID marker and short TTL.
type AccessTokenIssuer interface {
	// Issue mints a token scoped to scope (already resolved/narrowed
	// against binding by the caller -- see ResolveRequestedScope),
	// expiring at min(now+WorkloadAccessTokenLifetime, subjectExpiresAt).
	Issue(ctx context.Context, binding WorkloadBinding, scope []string, subjectExpiresAt time.Time) (IssuedCredential, error)
}

// ResolveRequestedScope narrows binding's role-derived scope set by an
// RFC 8693 requested scope list. RFC 8693 scope may only narrow a grant,
// never widen it: an empty requested list returns the binding's full
// scope unchanged; a non-empty list must be a non-empty subset of it, or
// ErrScopeNotGranted.
func ResolveRequestedScope(binding WorkloadBinding, requested []string) ([]string, error) {
	granted := RoleScopes(binding.Role)
	if len(granted) == 0 {
		return nil, ErrWorkloadBindingNotFound
	}
	if len(requested) == 0 {
		return granted, nil
	}
	grantedSet := make(map[string]struct{}, len(granted))
	for _, scope := range granted {
		grantedSet[scope] = struct{}{}
	}
	seen := make(map[string]struct{}, len(requested))
	narrowed := make([]string, 0, len(requested))
	for _, scope := range requested {
		if _, ok := grantedSet[scope]; !ok {
			return nil, ErrScopeNotGranted
		}
		if _, dup := seen[scope]; dup {
			continue
		}
		seen[scope] = struct{}{}
		narrowed = append(narrowed, scope)
	}
	return narrowed, nil
}

// RoleScopes maps a WorkloadBinding's role to its fixed scope set (design
// brief: "read = context:read + evidence:read; ops = read +
// episode:write; context:admin stays an explicit overlay, never implicit
// in ops"). An unrecognized role returns nil -- ResolveRequestedScope
// treats that as ErrWorkloadBindingNotFound rather than granting nothing
// silently.
func RoleScopes(role string) []string {
	switch role {
	case "read":
		return []string{ScopeContextRead, ScopeEvidenceRead}
	case "ops":
		return []string{ScopeContextRead, ScopeEvidenceRead, ScopeEpisodeWrite}
	default:
		return nil
	}
}
