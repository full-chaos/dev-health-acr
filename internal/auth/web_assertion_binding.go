package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"sort"
)

const WebAssertionPermissionCredentialIssue = "credential:issue"

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
			method == http.MethodPost && path == "/api/v1/oauth/device_approval"
	}
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		normalized, err := NormalizeRepositorySlug(scope)
		if err != nil || normalized != scope {
			return false
		}
		if _, duplicate := seen[normalized]; duplicate {
			return false
		}
		seen[normalized] = struct{}{}
	}
	return true
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
		// ScopeContextAdmin (Codex round-1 finding F5, decided): a web
		// assertion may carry it for a READ of an organization's own BYO
		// LLM configuration -- GET /api/v1/context-fabric/model-config is
		// the product's own settings UI reading its own organization's
		// (masked) configuration, the same shape context:read already
		// covers for investigation results. The write/delete routes on
		// that same resource stay bearer-only regardless (they carry the
		// plaintext credential on the wire; see
		// ContextFabricOrgModelConfigPutHandler's doc comment) -- this
		// widens what a web assertion can READ, never what it can DO to a
		// stored credential.
		if permission != ScopeContextRead && permission != ScopeEvidenceRead && permission != WebAssertionPermissionCredentialIssue && permission != ScopeContextAdmin {
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
