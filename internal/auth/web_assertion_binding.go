package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sort"
)

const WebAssertionPermissionCredentialIssue = "credential:issue"

// webAssertionOrganizationWidePaths are the only routes a web assertion may
// name every repository ("*") on: the two credential approvals, the typed-code
// device approval, the OAuth consent decision (api.OAuthConsentPath) and the
// first-party authorize that starts it (api.OAuthFirstPartyAuthorizePath,
// CHAOS-12568), both pinned by a test in internal/api. T20 (CHAOS-12109)
// narrows web assertions on data routes only; these routes stay outside it.
var webAssertionOrganizationWidePaths = []string{"/api/v1/oauth/device_approval", "/authorize/consent", "/authorize/first-party"}

// WebAssertionOrganizationWidePaths returns the routes an organization-wide
// web assertion may name.
func WebAssertionOrganizationWidePaths() []string {
	return append([]string(nil), webAssertionOrganizationWidePaths...)
}

func readAssertionBody(r *http.Request, maximum int64) ([]byte, error) {
	if r.Body == nil {
		return []byte{}, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maximum+1))
	if err != nil || int64(len(body)) > maximum {
		return nil, ErrInvalidWebAssertion
	}
	if err := r.Body.Close(); err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func assertionBodyDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// webAssertionBodyScopedPaths are the approvals whose request body names the
// repositories (device_approval_request.repository_scopes, the consent
// approve's repository_scopes). On them the repository_scopes claim may be
// left out (CHAOS-13562): the body, bound by body_sha256, carries the list,
// so the header stays the same size at any count.
var webAssertionBodyScopedPaths = []string{"/api/v1/oauth/device_approval", "/authorize/consent"}

// webAssertionScopeClaim is the repository_scopes claim with its presence: an
// assertion that leaves the member out differs from one that sends null or [].
type webAssertionScopeClaim struct {
	present bool
	values  []string
}

func (c *webAssertionScopeClaim) UnmarshalJSON(raw []byte) error {
	c.present = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		c.values = nil
		return nil
	}
	return json.Unmarshal(raw, &c.values)
}

// validWebRepositoryClaim admits an absent claim only for a credential issue
// on a body-scoped approval path; a present claim follows validWebRepositories.
func validWebRepositoryClaim(claim webAssertionScopeClaim, permissions []string, method, path string) bool {
	if !claim.present {
		return len(permissions) == 1 && permissions[0] == WebAssertionPermissionCredentialIssue &&
			method == http.MethodPost && slices.Contains(webAssertionBodyScopedPaths, path)
	}
	return validWebRepositories(claim.values, permissions, method, path)
}

func validWebRepositories(scopes, permissions []string, method, path string) bool {
	if len(scopes) == 0 {
		return false
	}
	if len(scopes) == 1 && scopes[0] == "*" {
		return len(permissions) == 1 && permissions[0] == WebAssertionPermissionCredentialIssue &&
			method == http.MethodPost && slices.Contains(webAssertionOrganizationWidePaths, path)
	}
	_, _, refused := webRepositoryScopeFault(scopes)
	return !refused
}

// webRepositoryScopeFault names the first repository scope that makes a web
// assertion invalid and why. refused is false when every scope is acceptable;
// it is a separate result because the refused scope can itself be the empty
// string. The caller has already handled the lone "*" organization-wide form.
func webRepositoryScopeFault(scopes []string) (scope, reason string, refused bool) {
	seen := make(map[string]struct{}, len(scopes))
	for _, candidate := range scopes {
		normalized, err := NormalizeRepositorySlug(candidate)
		if err != nil {
			return candidate, repositorySlugFault(candidate), true
		}
		if normalized != candidate {
			return candidate, "not in normalized (lower-case, trimmed) form", true
		}
		if _, duplicate := seen[normalized]; duplicate {
			return candidate, "duplicate scope", true
		}
		seen[normalized] = struct{}{}
	}
	return "", "", false
}

func normalizeWebRepositoryScopes(scopes []string) []string {
	result := append([]string(nil), scopes...)
	sort.Strings(result)
	return result
}

func validWebPermissions(permissions []string) bool {
	if len(permissions) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		if permission != ScopeContextRead && permission != ScopeEvidenceRead && permission != WebAssertionPermissionCredentialIssue {
			return false
		}
		if _, duplicate := seen[permission]; duplicate {
			return false
		}
		seen[permission] = struct{}{}
	}
	return true
}

func normalizeWebPermissions(permissions []string) []string {
	result := append([]string(nil), permissions...)
	sort.Strings(result)
	return result
}
