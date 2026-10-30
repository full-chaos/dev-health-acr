package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"slices"
	"sort"
)

const WebAssertionPermissionCredentialIssue = "credential:issue"

// webAssertionOrganizationWidePaths are the only routes a web assertion may
// name every repository ("*") on: the two credential approvals, the typed-code
// device approval and the OAuth consent decision (api.OAuthConsentPath, pinned
// by a test in internal/api).
var webAssertionOrganizationWidePaths = []string{"/api/v1/oauth/device_approval", "/authorize/consent"}

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
