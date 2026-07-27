package sidecar

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// VerifyCredential proves that expectedToken is stored at current's captured
// source and remains the globally selected credential.
func VerifyCredential(current CredentialResult, expectedToken string) error {
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		return err
	}
	defer session.Close()
	return session.VerifyCredential(current, expectedToken)
}

func (s *CredentialLifecycleSession) VerifyCredential(current CredentialResult, expectedToken string) error {
	expectedToken = strings.TrimSpace(expectedToken)
	if !auth.IsTokenShapeValid(expectedToken) {
		return ErrCredentialShapeInvalid
	}
	exact, err := readCapturedCredential(current)
	if err != nil {
		return fmt.Errorf("read captured ACR credential source: %w", err)
	}
	if exact.Token != expectedToken || exact.Source != current.Source {
		return errors.New("captured ACR credential source does not contain the expected credential")
	}
	resolved, err := LoadCredential()
	if err != nil {
		return fmt.Errorf("resolve ACR credential after persistence: %w", err)
	}
	if resolved.Token != expectedToken || resolved.Source != current.Source {
		return errors.New("persisted ACR credential is not the globally selected source")
	}
	return nil
}

func readCapturedCredential(current CredentialResult) (CredentialResult, error) {
	switch current.Source {
	case "file":
		if current.filePath == "" {
			return CredentialResult{}, ErrCredentialPersistenceSourceUnsupported
		}
		return loadCredentialFile(current.filePath)
	case "keyring":
		if current.keyringService == "" || current.keyringAccount == "" {
			return CredentialResult{}, ErrCredentialPersistenceSourceUnsupported
		}
		ctx, cancel := context.WithTimeout(context.Background(), keyringLookupTimeout)
		defer cancel()
		token, ok, err := currentKeyringLookup(ctx, current.keyringService, current.keyringAccount)
		if errors.Is(err, ErrExecutableUnavailable) || !ok && err == nil {
			return CredentialResult{}, ErrCredentialMissing
		}
		if err != nil {
			return CredentialResult{}, err
		}
		token = strings.TrimSpace(token)
		if !auth.IsTokenShapeValid(token) {
			return CredentialResult{}, ErrCredentialShapeInvalid
		}
		return CredentialResult{Token: token, Source: "keyring", keyringService: current.keyringService, keyringAccount: current.keyringAccount}, nil
	default:
		return CredentialResult{}, ErrCredentialPersistenceSourceUnsupported
	}
}
