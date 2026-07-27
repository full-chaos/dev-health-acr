package sidecar

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/full-chaos/dev-health-acr/internal/auth"
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

const (
	// maxCleanupLocationBytes bounds one rendered location. A location is a
	// variable name, a keyring service/account pair, or an operator-supplied
	// path -- ACR_API_TOKEN_FILE can be arbitrarily long -- so an unbounded
	// render lets a configured value dictate how much text lands in an
	// operator's terminal or log.
	maxCleanupLocationBytes = 160
	// maxReportedCleanupLocations bounds how many locations are rendered at
	// all. The purge attempts a small fixed set today, but the cap is what
	// keeps that a property of the output rather than of the input.
	maxReportedCleanupLocations = 8
	// redactedTokenMarker replaces ACR bearer text found inside a location.
	redactedTokenMarker = auth.TokenPrefix + "[redacted]"
)

// SafeCredentialCleanupLocations renders every location a purge failed at for
// operator-facing output.
//
// The locations remain useful -- an operator has to be able to go and clean
// the exact place that failed -- but they are not trusted text. A location can
// be an operator-supplied path from ACR_API_TOKEN_FILE or a keyring
// service/account from ACR_API_TOKEN_KEYRING_*, so it can carry newlines and
// terminal control sequences that forge log lines, arbitrary length, and, if a
// credential was ever pasted into one of those variables by mistake, bearer
// text. Each location is therefore token-redacted, length-bounded, and quoted
// (which escapes every control character and makes the boundaries of the value
// unambiguous), and the list itself is bounded with an explicit count of what
// was omitted rather than silently truncated.
func SafeCredentialCleanupLocations(err error) []string {
	locations := CredentialCleanupLocations(err)
	if len(locations) == 0 {
		return nil
	}
	omitted := 0
	if len(locations) > maxReportedCleanupLocations {
		omitted = len(locations) - maxReportedCleanupLocations
		locations = locations[:maxReportedCleanupLocations]
	}
	safe := make([]string, 0, len(locations)+1)
	for _, location := range locations {
		safe = append(safe, safeCleanupLocation(location))
	}
	if omitted != 0 {
		safe = append(safe, fmt.Sprintf("and %d more location(s)", omitted))
	}
	return safe
}

func safeCleanupLocation(location string) string {
	location = redactTokenText(location)
	if len(location) > maxCleanupLocationBytes {
		// Truncate on a rune boundary so quoting never has to escape a byte
		// this function itself split in half.
		cut := maxCleanupLocationBytes
		for cut > 0 && !utf8RuneStart(location[cut]) {
			cut--
		}
		location = location[:cut] + "..."
	}
	return strconv.Quote(location)
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// redactTokenText removes ACR bearer text from an operator-facing location.
// The whole prefixed run is replaced rather than only well-formed tokens: a
// truncated secret is still a secret, and this output is destined for logs.
func redactTokenText(value string) string {
	lower := strings.ToLower(value)
	prefix := strings.ToLower(auth.TokenPrefix)
	var builder strings.Builder
	for index := 0; index < len(value); {
		if !strings.HasPrefix(lower[index:], prefix) {
			builder.WriteByte(value[index])
			index++
			continue
		}
		builder.WriteString(redactedTokenMarker)
		index += len(prefix)
		for index < len(value) && isTokenSecretByte(value[index]) {
			index++
		}
	}
	return builder.String()
}

func isTokenSecretByte(b byte) bool {
	r := rune(b)
	return unicode.IsLetter(r) && r < 0x80 || unicode.IsDigit(r) && r < 0x80 || b == '-' || b == '_'
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
	return PurgeAllCredentialMaterial([]CredentialResult{current})
}

// PurgeAllCredentialMaterial removes the removable locations of every supplied
// credential, plus the configured ones, as a single deduplicated pass.
//
// Purging one credential at a time reported the same configured location as a
// separate failure once per credential, and, worse, could remove a location
// belonging to a later credential before that credential's own remote
// revocation had been attempted. Callers therefore hand the whole set here,
// after every remote revocation has succeeded.
func PurgeAllCredentialMaterial(material []CredentialResult) error {
	failures := make([]*CredentialCleanupError, 0)
	for _, current := range material {
		if current.Source == "environment" {
			failures = append(failures, &CredentialCleanupError{Location: TokenEnvironment, cause: ErrCredentialPersistenceSourceUnsupported})
			break
		}
	}
	keyringAllowed, keyringSettingErr := keyringEnabled()
	if keyringSettingErr != nil {
		failures = append(failures, &CredentialCleanupError{Location: TokenKeyringDisabledEnvironment, cause: keyringSettingErr})
	}
	seen := map[credentialPurgeKey]bool{}
	targets := make([]credentialPurgeTarget, 0, 4)
	for _, current := range material {
		appendCredentialPurgeTargets(&targets, seen, current, keyringAllowed)
	}
	if len(material) == 0 {
		appendCredentialPurgeTargets(&targets, seen, CredentialResult{}, keyringAllowed)
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

// credentialPurgeKey identifies one purge target exactly.
//
// It is a typed tuple rather than a joined string. Both halves of a keyring
// address are operator-supplied (ACR_API_TOKEN_KEYRING_SERVICE and
// ACR_API_TOKEN_KEYRING_ACCOUNT), so a string key built by joining them on a
// separator collides whenever that separator appears inside either half:
// service "a" with account "b:c" and service "a:b" with account "c" produced
// the same key, and the second, genuinely different entry was silently dropped
// from the purge -- leaving a live credential behind while logout reported
// success. A struct key has no separator to collide on.
type credentialPurgeKey struct {
	keyring   bool
	primary   string
	secondary string
}

// appendCredentialPurgeTargets adds the locations one credential can be
// removed from -- the address it was actually captured at, then the currently
// configured address -- skipping anything already queued under seen.
func appendCredentialPurgeTargets(targets *[]credentialPurgeTarget, seen map[credentialPurgeKey]bool, current CredentialResult, keyringAllowed bool) {
	addFilePurgeTarget(targets, seen, current.filePath)
	if keyringAllowed {
		addKeyringPurgeTarget(targets, seen, current.keyringService, current.keyringAccount)
	}
	addFilePurgeTarget(targets, seen, configuredTokenFilePath())
	if keyringAllowed {
		service, account, configured := credentialKeyringAddress()
		if configured {
			addKeyringPurgeTarget(targets, seen, service, account)
		}
	}
}

func addFilePurgeTarget(targets *[]credentialPurgeTarget, seen map[credentialPurgeKey]bool, path string) {
	key := credentialPurgeKey{primary: path}
	if path == "" || seen[key] {
		return
	}
	seen[key] = true
	*targets = append(*targets, credentialPurgeTarget{location: path, remove: func() error {
		return removeACRCredentialFile(path)
	}})
}

// removeACRCredentialFile deletes path only after proving it is an ACR
// credential target through the same boundaries that admit one for reading:
// no-follow open, regular-file fstat, group/world permission denial, and the
// ACR API bearer token shape (loadCredentialFile). ACR_API_TOKEN_FILE is an
// operator-supplied path, and cleanup used to unlink whatever regular file
// was sitting at it, so a mistyped or hostile value turned logout into an
// arbitrary-file delete. An absent, unreadable, or non-ACR file is left
// exactly as it is; a missing one reports success so cleanup stays
// idempotent.
//
// The parent directory is checked too. Every property proven about the file
// is proven before the unlink, and on a group- or world-writable parent any
// local user can replace the target in that window, so a verified ACR
// credential is not enough on its own: the removal would be of whatever was
// swapped in. Such a parent is refused and reported rather than removed,
// because the safe action there is for an operator to fix the directory.
func removeACRCredentialFile(path string) error {
	if err := rejectSharedWritableCredentialParent(path); err != nil {
		return err
	}
	if _, err := loadCredentialFile(path); err != nil {
		if errors.Is(err, ErrCredentialMissing) {
			return nil
		}
		return fmt.Errorf("verify ACR credential file before removal: %w", err)
	}
	if err := removeCredentialFile(path); err != nil {
		return fmt.Errorf("remove ACR credential fallback file: %w", err)
	}
	return nil
}

func addKeyringPurgeTarget(targets *[]credentialPurgeTarget, seen map[credentialPurgeKey]bool, service, account string) {
	key := credentialPurgeKey{keyring: true, primary: service, secondary: account}
	if service == "" || account == "" || seen[key] {
		return
	}
	seen[key] = true
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
