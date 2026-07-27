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
	return "ACR credential cleanup failed"
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

func credentialCleanupLocations(err error) []string {
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
	locations := credentialCleanupLocations(err)
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
//
// auth.TokenPrefix is matched by ASCII-only case folding against value's own
// byte offsets, never against a separately allocated strings.ToLower(value).
// strings.ToLower does not merely reinterpret bytes: for certain runes (for
// example U+0130, LATIN CAPITAL LETTER I WITH DOT ABOVE, 2 UTF-8 bytes) it
// produces a lowercased rune whose UTF-8 encoding is a DIFFERENT byte length
// (1 byte for plain "i"), so a lowercased copy of a string containing enough
// such runes is shorter, in bytes, than the original. This loop walks index
// over the ORIGINAL value, so once the lowered copy has fallen behind by even
// one byte, lower[index:] eventually slices past the shorter string's length
// and panics with "slice bounds out of range" -- turning an operator-facing
// redaction helper into a crash triggered by attacker- or operator-supplied
// Unicode in a credential location (ACR_API_TOKEN_FILE, a keyring account).
// hasASCIIFoldPrefix below never allocates a case-folded copy of value and
// therefore cannot desynchronize value's byte offsets from themselves; it is
// exact for this purpose because auth.TokenPrefix is itself pure ASCII.
func redactTokenText(value string) string {
	prefix := auth.TokenPrefix
	var builder strings.Builder
	for index := 0; index < len(value); {
		if !hasASCIIFoldPrefix(value[index:], prefix) {
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

// hasASCIIFoldPrefix reports whether s starts with prefix under ASCII-only
// case folding, without allocating a case-folded copy of s. It compares byte
// by byte and only ever folds 'A'-'Z' to 'a'-'z', so it cannot change s's
// length or byte alignment the way strings.ToLower can -- see redactTokenText
// above for why that distinction is load-bearing here, not cosmetic.
func hasASCIIFoldPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if asciiFoldByte(s[i]) != asciiFoldByte(prefix[i]) {
			return false
		}
	}
	return true
}

func asciiFoldByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
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
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		return err
	}
	defer session.Close()
	return session.PurgeCredentialMaterial(current)
}

func (s *CredentialLifecycleSession) PurgeCredentialMaterial(current CredentialResult) error {
	return s.PurgeAllCredentialMaterial([]CredentialResult{current})
}

// PurgeAllCredentialMaterial removes only locations captured with the supplied
// credential material, as a single deduplicated pass.
//
// Purging one credential at a time reported the same configured location as a
// separate failure once per credential, and, worse, could remove a location
// belonging to a later credential before that credential's own remote
// revocation had been attempted. Callers therefore hand the whole set here,
// after every remote revocation has succeeded.
func PurgeAllCredentialMaterial(material []CredentialResult) error {
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		return err
	}
	defer session.Close()
	return session.PurgeAllCredentialMaterial(material)
}

func (s *CredentialLifecycleSession) PurgeAllCredentialMaterial(material []CredentialResult) error {
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
	// expectedPurgeTokens binds every physical location this purge might touch
	// to the exact token CollectCredentialMaterial captured there, built once
	// up front across the whole material set (not per credential) so the
	// "currently configured" fallback targets below resolve to the same
	// location key -- and therefore the same expected token -- as whichever
	// material entry actually observed a credential there, regardless of which
	// order the two are visited in.
	//
	// This is what closes the TOCTOU a location-only purge target cannot:
	// logout revokes the token it enumerated, then removes by address some
	// time later. Without an expected value, a target that only proves "the
	// current occupant has the right shape" deletes whatever a concurrent
	// login or refresh wrote to that same file or keyring entry in between --
	// a credential this purge never revoked -- while reporting cleanup as
	// successful. A mismatch below retains the location and reports it rather
	// than deleting.
	expectedPurgeTokens := map[credentialPurgeKey]string{}
	for _, current := range material {
		if current.filePath != "" {
			key := credentialPurgeKey{primary: current.filePath}
			if prior, ok := expectedPurgeTokens[key]; ok && prior != current.Token {
				return &CredentialPurgeError{Failures: []*CredentialCleanupError{{Location: current.filePath, cause: errCredentialPurgeDuplicateLocator}}}
			}
			expectedPurgeTokens[key] = current.Token
		}
		if current.keyringService != "" && current.keyringAccount != "" {
			key := credentialPurgeKey{keyring: true, primary: current.keyringService, secondary: current.keyringAccount}
			if prior, ok := expectedPurgeTokens[key]; ok && prior != current.Token {
				return &CredentialPurgeError{Failures: []*CredentialCleanupError{{Location: credentialKeyringLocation(current.keyringService, current.keyringAccount), cause: errCredentialPurgeDuplicateLocator}}}
			}
			expectedPurgeTokens[key] = current.Token
		}
	}
	seen := map[credentialPurgeKey]bool{}
	targets := make([]credentialPurgeTarget, 0, 4)
	for _, current := range material {
		appendCredentialPurgeTargets(&targets, seen, expectedPurgeTokens, current, keyringAllowed)
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

func appendCredentialPurgeTargets(targets *[]credentialPurgeTarget, seen map[credentialPurgeKey]bool, expected map[credentialPurgeKey]string, current CredentialResult, keyringAllowed bool) {
	addFilePurgeTarget(targets, seen, expected, current.filePath)
	if keyringAllowed {
		addKeyringPurgeTarget(targets, seen, expected, current.keyringService, current.keyringAccount)
	}
}

// errCredentialPurgeTargetChanged marks a purge target whose expected token
// -- the value CollectCredentialMaterial captured, and that this logout has
// since revoked -- no longer matches what is actually stored at that
// location. The location is retained, not deleted: the value now there was
// written after enumeration, by a login or refresh this purge knows nothing
// about, and is therefore not something this purge has any business
// removing, whether or not it happens to look like a valid ACR credential.
var errCredentialPurgeTargetChanged = errors.New("acr: credential at this location changed since it was enumerated for removal; it was left in place")

var errCredentialPurgeDuplicateLocator = errors.New("acr: conflicting credentials were captured at one cleanup location; it was left in place")

func addFilePurgeTarget(targets *[]credentialPurgeTarget, seen map[credentialPurgeKey]bool, expected map[credentialPurgeKey]string, path string) {
	key := credentialPurgeKey{primary: path}
	if path == "" || seen[key] {
		return
	}
	seen[key] = true
	expectedToken, tokenKnown := expected[key]
	*targets = append(*targets, credentialPurgeTarget{location: path, remove: func() error {
		return removeACRCredentialFile(path, expectedToken, tokenKnown)
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
// When expectedTokenKnown, the file's actual token must also match
// expectedToken -- the value this purge enumerated and already revoked --
// or the file is retained and reported via errCredentialPurgeTargetChanged
// rather than deleted. Without this, a shape check alone proves only "this
// file currently holds *some* ACR credential", which a concurrent login or
// refresh can make true again for a *different*, still-live token in the
// window between enumeration and this call, turning a successful-looking
// purge into the deletion of a credential nobody revoked.
//
// The parent directory is checked too, but note what that does and does not
// buy, and note separately what the compare-and-remove above does and does
// not buy. Every property -- parent, shape, and now expected value -- is
// proven about the file and the unlink then happens by name, so a window
// still exists between the last check and the unlink regardless: this
// narrows what a concurrent writer in that window can make this function do,
// it does not make the sequence atomic, and this comment does not claim it
// does. A group- or world-writable parent is refused and reported rather than
// removed, because the safe action there is for an operator to fix the
// directory.
func removeACRCredentialFile(path, expectedToken string, expectedTokenKnown bool) error {
	if err := rejectSharedWritableCredentialParent(path); err != nil {
		return err
	}
	credential, err := loadCredentialFile(path)
	if err != nil {
		if errors.Is(err, ErrCredentialMissing) {
			return nil
		}
		return fmt.Errorf("verify ACR credential file before removal: %w", err)
	}
	if expectedTokenKnown && credential.Token != expectedToken {
		return errCredentialPurgeTargetChanged
	}
	if err := removeCredentialFile(path); err != nil {
		return fmt.Errorf("remove ACR credential fallback file: %w", err)
	}
	return nil
}

func addKeyringPurgeTarget(targets *[]credentialPurgeTarget, seen map[credentialPurgeKey]bool, expected map[credentialPurgeKey]string, service, account string) {
	key := credentialPurgeKey{keyring: true, primary: service, secondary: account}
	if service == "" || account == "" || seen[key] {
		return
	}
	seen[key] = true
	expectedToken, tokenKnown := expected[key]
	location := credentialKeyringLocation(service, account)
	*targets = append(*targets, credentialPurgeTarget{location: location, remove: func() error {
		if currentKeyringDeleter == nil {
			return errors.New("delete ACR keyring credential: keyring unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), keyringLookupTimeout)
		defer cancel()
		// See removeACRCredentialFile's comment for what this comparison closes
		// and what it does not: it narrows, rather than eliminates, the window
		// between proving a value and deleting it by address.
		if tokenKnown {
			if currentKeyringLookup == nil {
				return errors.New("verify ACR keyring credential before removal: keyring unavailable")
			}
			actual, ok, lookupErr := currentKeyringLookup(ctx, service, account)
			if lookupErr != nil {
				return fmt.Errorf("verify ACR keyring credential before removal: %w", lookupErr)
			}
			if !ok {
				// Already gone; deletion is idempotent.
				return nil
			}
			if strings.TrimSpace(actual) != expectedToken {
				return errCredentialPurgeTargetChanged
			}
		}
		if err := currentKeyringDeleter(ctx, service, account); err != nil {
			return fmt.Errorf("delete ACR keyring credential: %w", err)
		}
		return nil
	}})
}
