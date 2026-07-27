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

// Locations returns the exact location of every cleanup that failed, in the
// order the purge attempted them. Callers report these verbatim: deriving a
// single location from the credential's own source instead names a place the
// purge may never have touched, and hides every other failure.
func (e *CredentialPurgeError) Locations() []string {
	locations := make([]string, 0, len(e.Failures))
	for _, failure := range e.Failures {
		locations = append(locations, failure.Location)
	}
	return locations
}

// CredentialCleanupLocations extracts every location a purge failed to clean
// from err. It returns nil for a nil or unrecognized error so an operator
// message never invents a location that was not actually reported.
func CredentialCleanupLocations(err error) []string {
	var purgeErr *CredentialPurgeError
	if errors.As(err, &purgeErr) {
		return purgeErr.Locations()
	}
	var cleanupErr *CredentialCleanupError
	if errors.As(err, &cleanupErr) {
		return []string{cleanupErr.Location}
	}
	return nil
}

// PurgeCredentialMaterial removes every configured or resolved removable
// credential location, continuing after individual cleanup failures.
//
// A keyring that is disabled, or whose disable flag cannot be parsed, never
// reaches the OS keyring seam: an unreadable setting must not authorize a
// keyring subprocess. Such a setting is reported as one more typed failure
// alongside the file locations rather than as an early return, because
// aborting here would leave a readable token file on disk while logout
// reported that cleanup had failed.
//
// An environment credential is the same shape of problem. ACR_API_TOKEN
// cannot be unset in the parent shell from here, so it is recorded as a
// typed failure at that exact location -- but it is never an early return:
// a process that exports ACR_API_TOKEN can still have a stale token file or
// keyring entry underneath it, and returning here would leave both behind
// while logout reported that cleanup had failed.
func PurgeCredentialMaterial(current CredentialResult) error {
	failures := make([]*CredentialCleanupError, 0)
	if current.Source == "environment" {
		failures = append(failures, &CredentialCleanupError{Location: TokenEnvironment, cause: ErrCredentialPersistenceSourceUnsupported})
	}
	keyringAllowed, keyringSettingErr := keyringEnabled()
	targets := credentialPurgeTargets(current, keyringAllowed)
	if keyringSettingErr != nil {
		failures = append(failures, &CredentialCleanupError{Location: TokenKeyringDisabledEnvironment, cause: keyringSettingErr})
	}
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

func credentialPurgeTargets(current CredentialResult, keyringAllowed bool) []credentialPurgeTarget {
	targets := make([]credentialPurgeTarget, 0, 4)
	seen := map[string]bool{}
	addFilePurgeTarget(&targets, seen, current.filePath)
	if keyringAllowed {
		addKeyringPurgeTarget(&targets, seen, current.keyringService, current.keyringAccount)
	}
	addFilePurgeTarget(&targets, seen, configuredTokenFilePath())
	if keyringAllowed {
		service, account, configured := credentialKeyringAddress()
		if configured {
			addKeyringPurgeTarget(&targets, seen, service, account)
		}
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
