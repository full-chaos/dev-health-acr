package sidecar

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type CredentialPurgeError struct {
	Failures []*CredentialCleanupError
}

func (e *CredentialPurgeError) Error() string {
	locations := make([]string, 0, len(e.Failures))
	for _, failure := range e.Failures {
		locations = append(locations, failure.Location)
	}
	return "remove ACR credential material from " + strings.Join(locations, ", ")
}

func (e *CredentialPurgeError) Unwrap() []error {
	errs := make([]error, 0, len(e.Failures))
	for _, failure := range e.Failures {
		errs = append(errs, failure)
	}
	return errs
}

// PurgeCredentialMaterial removes every configured or resolved removable
// credential location, continuing after individual cleanup failures.
func PurgeCredentialMaterial(current CredentialResult) error {
	targets := credentialPurgeTargets(current)
	failures := make([]*CredentialCleanupError, 0)
	for _, target := range targets {
		if err := target.remove(); err != nil {
			failures = append(failures, &CredentialCleanupError{Location: target.location, cause: err})
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return &CredentialPurgeError{Failures: failures}
}

type credentialPurgeTarget struct {
	location string
	remove   func() error
}

func credentialPurgeTargets(current CredentialResult) []credentialPurgeTarget {
	targets := make([]credentialPurgeTarget, 0, 4)
	seen := map[string]bool{}
	addFilePurgeTarget(&targets, seen, current.filePath)
	addKeyringPurgeTarget(&targets, seen, current.keyringService, current.keyringAccount)
	addFilePurgeTarget(&targets, seen, configuredTokenFilePath())
	service, account, configured := credentialKeyringAddress()
	if configured {
		addKeyringPurgeTarget(&targets, seen, service, account)
	}
	return targets
}

func addFilePurgeTarget(targets *[]credentialPurgeTarget, seen map[string]bool, path string) {
	if path == "" || seen["file:"+path] {
		return
	}
	seen["file:"+path] = true
	*targets = append(*targets, credentialPurgeTarget{location: path, remove: func() error {
		if err := removeCredentialFile(path); err != nil {
			return fmt.Errorf("remove ACR credential fallback file: %w", err)
		}
		return nil
	}})
}

func addKeyringPurgeTarget(targets *[]credentialPurgeTarget, seen map[string]bool, service, account string) {
	if service == "" || account == "" || seen["keyring:"+service+":"+account] {
		return
	}
	seen["keyring:"+service+":"+account] = true
	location := credentialKeyringLocation(service, account)
	*targets = append(*targets, credentialPurgeTarget{location: location, remove: func() error {
		if currentKeyringDeleter == nil {
			return errors.New("delete ACR keyring credential: keyring unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), keyringLookupTimeout)
		defer cancel()
		if err := currentKeyringDeleter(ctx, service, account); err != nil {
			return fmt.Errorf("delete ACR keyring credential: %w", err)
		}
		return nil
	}})
}
