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

func validWebRepositories(scopes []string) bool {
	if len(scopes) == 0 {
		return false
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
