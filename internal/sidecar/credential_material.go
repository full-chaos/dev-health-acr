package sidecar

import (
	"errors"
	"fmt"
)

// CollectCredentialMaterial enumerates every configured local location that
// currently holds a shape-valid ACR bearer credential, in precedence order:
// environment, then the explicit or default keyring entry, then the explicit
// or default token file.
//
// LoadCredential answers a different question -- "which single credential wins"
// -- and logout used it, so logout revoked the winner and then deleted every
// location. A host with an exported ACR_API_TOKEN over a token file, or a
// keyring entry over a file left by an earlier login, therefore had its
// lower-precedence credentials deleted locally while they stayed live on the
// server: an attacker holding a copy of one of them kept a working credential
// that the operator believed logout had ended.
//
// Enumeration fails closed. Anything that leaves it unknown whether a location
// holds a live credential -- a malformed environment value, an unreadable
// keyring flag, an operational keyring failure, an unreadable token file -- is
// returned as an error rather than skipped, because the caller's next step is
// to delete local material, and doing that around a location it could not read
// is how a live credential is stranded with nothing left pointing at it.
func CollectCredentialMaterial() ([]CredentialResult, error) {
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return session.CollectCredentialMaterial()
}

func (s *CredentialLifecycleSession) CollectCredentialMaterial() ([]CredentialResult, error) {
	finish, err := s.beginOperation()
	if err != nil {
		return nil, err
	}
	defer finish()
	material := make([]CredentialResult, 0, 3)
	if result, configured, err := loadFromEnvironment(); configured {
		if err != nil {
			return nil, err
		}
		material = append(material, result)
	}
	keyringAllowed, err := keyringEnabled()
	if err != nil {
		return nil, err
	}
	if keyringAllowed {
		result, ok, err := loadFromKeyring()
		if err != nil {
			return nil, fmt.Errorf("load ACR keyring credential: %w", err)
		}
		if ok {
			material = append(material, result)
		}
	}
	fileResult, err := loadFromFile()
	switch {
	case err == nil:
		material = append(material, fileResult)
	case errors.Is(err, ErrCredentialMissing):
		// Nothing configured, or nothing there: not a credential, and not an
		// unknown either.
	default:
		return nil, err
	}
	return material, nil
}

// DistinctCredentialTokens returns each distinct credential value in material
// exactly once, in the order first seen.
//
// The same credential routinely appears in more than one location -- a login
// writes the token file the environment also points at -- and revoking it twice
// turns the second attempt into a failure against a credential the server has
// already retired, which would abort a logout that had in fact succeeded.
func DistinctCredentialTokens(material []CredentialResult) []string {
	seen := make(map[string]bool, len(material))
	tokens := make([]string, 0, len(material))
	for _, credential := range material {
		if credential.Token == "" || seen[credential.Token] {
			continue
		}
		seen[credential.Token] = true
		tokens = append(tokens, credential.Token)
	}
	return tokens
}
