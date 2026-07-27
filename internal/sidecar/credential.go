package sidecar

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

const (
	TokenEnvironment                = "ACR_API_TOKEN"
	TokenFileEnvironment            = "ACR_API_TOKEN_FILE"
	TokenKeyringDisabledEnvironment = "ACR_API_TOKEN_KEYRING_DISABLED"
	TokenKeyringServiceEnvironment  = "ACR_API_TOKEN_KEYRING_SERVICE"
	TokenKeyringAccountEnvironment  = "ACR_API_TOKEN_KEYRING_ACCOUNT"
	defaultKeyringService           = "dev-health-acr"

	// keyringLookupTimeout bounds the optional OS keyring seam so a locked,
	// hung, or misbehaving secret-store backend cannot stall credential
	// resolution; LoadCredential falls through to the token file on timeout.
	keyringLookupTimeout = 2 * time.Second
)

// ErrCredentialShapeInvalid is returned whenever a nonblank credential
// value was found but does not match the ACR API bearer token shape
// (auth.IsTokenShapeValid: the auth.TokenPrefix prefix followed by a
// 32-byte base64url secret). This is the enforcement point for "never
// accept a Dev Health license key, or any other non-ACR credential, as an
// ACR API bearer": nothing that fails this shape check is ever returned
// from LoadCredential or sent as a bearer token, regardless of source.
var ErrCredentialShapeInvalid = errors.New("acr: credential does not match the ACR API token shape (expected an \"" + auth.TokenPrefix + "\"-prefixed token; license keys and other credentials are not accepted)")

// ErrCredentialMissing is returned when no credential source (environment
// variable, OS keyring, or token file) is configured at all -- distinct
// from ErrCredentialShapeInvalid, which means a credential value was
// found but rejected for not matching the ACR API token shape. Callers
// (see internal/mcp's tool-error classification) need to tell "nothing
// configured" apart from "something configured, but wrong".
var ErrCredentialMissing = errors.New("ACR API credential is not configured")

// ErrCredentialPersistenceUnsupported is returned before any login flow can
// issue a credential on Windows, where this sidecar has no supported secure
// persistence mechanism.
var ErrCredentialPersistenceUnsupported = errors.New("acr: credential persistence is unsupported on Windows")

var ErrCredentialPersistenceSourceUnsupported = errors.New("acr: credential source cannot be replaced securely")

// errCredentialWriteAmbiguous marks the one credential-file write failure
// whose on-disk outcome is genuinely unknown: the same-directory temporary
// file was already renamed over the target, so the new credential is
// visible at the configured path, but the directory fsync that would make
// that replacement durable failed. Returning a bare error here would tell
// a caller "nothing was written" while a readable credential sits on disk.
// PersistCredential therefore pairs this sentinel with the actual candidate
// locator so login can revoke the server-side credential and then purge the
// exact file it may have just written.
var errCredentialWriteAmbiguous = errors.New("acr: credential file replacement could not be confirmed durable")

type CredentialResult struct {
	Token          string
	Source         string
	keyringService string
	keyringAccount string
	filePath       string
}

type CredentialCleanupError struct {
	Location string
	cause    error
}

func (e *CredentialCleanupError) Error() string {
	return "remove ACR credential from " + e.Location
}

func (e *CredentialCleanupError) Unwrap() error { return e.cause }

// LoadCredential resolves the ACR API bearer credential with a fixed
// precedence: the process environment always wins (agent-client
// compatibility), then the explicit or default OS keyring entry, then the
// explicit or default permission-restricted token file. There is intentionally
// no CLI-argument or plaintext-config-file source: both would make secrets
// visible in shell history, process listings, or unencrypted files by default.
//
// Precedence is evaluated in that order, which means the environment is
// resolved before ACR_API_TOKEN_KEYRING_DISABLED is even read. Reading the
// keyring flag first inverted the precedence for its own failure mode: a
// malformed disable flag rejected a perfectly valid ACR_API_TOKEN, taking
// down a source the flag does not govern.
func LoadCredential() (CredentialResult, error) {
	if result, configured, err := loadFromEnvironment(); configured {
		return result, err
	}
	keyringAllowed, err := keyringEnabled()
	if err != nil {
		return CredentialResult{}, err
	}
	return loadCredential(keyringAllowed)
}

// loadFromEnvironment reports whether ACR_API_TOKEN is set at all as its
// second result, so a configured-but-malformed environment credential stays
// an error instead of silently falling through to a lower-precedence source.
func loadFromEnvironment() (CredentialResult, bool, error) {
	token := strings.TrimSpace(os.Getenv(TokenEnvironment))
	if token == "" {
		return CredentialResult{}, false, nil
	}
	if !auth.IsTokenShapeValid(token) {
		return CredentialResult{}, true, fmt.Errorf("%s: %w", TokenEnvironment, ErrCredentialShapeInvalid)
	}
	return CredentialResult{Token: token, Source: "environment"}, true, nil
}

func loadCredential(keyringAllowed bool) (CredentialResult, error) {
	if result, configured, err := loadFromEnvironment(); configured {
		return result, err
	}
	if keyringAllowed {
		result, ok, err := loadFromKeyring()
		if err != nil {
			return CredentialResult{}, fmt.Errorf("load ACR keyring credential: %w", err)
		}
		if ok {
			return result, nil
		}
	}
	return loadFromFile()
}

// loadFromKeyring consults the explicit or default OS keyring seam. Only an
// exact entry miss or unavailable trusted executable permits file fallback;
// all other lookup failures are returned so the caller fails closed.
func loadFromKeyring() (CredentialResult, bool, error) {
	if currentKeyringLookup == nil {
		return CredentialResult{}, false, ErrExecutableUnavailable
	}
	service, account, configured := credentialKeyringAddress()
	if !configured {
		return CredentialResult{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyringLookupTimeout)
	defer cancel()
	token, ok, err := currentKeyringLookup(ctx, service, account)
	if errors.Is(err, ErrExecutableUnavailable) || !ok && err == nil {
		return CredentialResult{}, false, nil
	}
	if err != nil {
		return CredentialResult{}, false, err
	}
	token = strings.TrimSpace(token)
	if token == "" || !auth.IsTokenShapeValid(token) {
		return CredentialResult{}, false, ErrCredentialShapeInvalid
	}
	return CredentialResult{Token: token, Source: "keyring", keyringService: service, keyringAccount: account}, true, nil
}

// maxTokenFileBytes bounds how many bytes of a configured token file
// loadFromFile will read. The ACR API token shape (auth.TokenPrefix plus
// a 32-byte base64url secret) is a fixed 49 bytes; this ceiling exists to
// bound memory and I/O if TokenFileEnvironment is misconfigured to point
// at an oversized or pathological file, not to accommodate legitimate
// token growth.
const maxTokenFileBytes = 4096 // 4 KiB

// loadFromFile reads a permission-restricted, regular token file via the
// shared readBoundedRegularFile (see boundedfile.go): the path is opened
// with platform no-follow/non-blocking flags set atomically as part of
// the single open(2) syscall that obtains the descriptor (boundedfile_unix.go),
// so there is no separate pre-open check for an attacker to race against
// a path swap -- the kernel itself refuses to open a path whose last
// component is a symlink, and refuses to block waiting for a writer on a
// FIFO. A single fstat(2) on that already-open descriptor then verifies
// the result is a regular file -- never a directory, FIFO, device,
// socket, or symlink, even one that would otherwise resolve to a
// legitimate regular file. The read itself is bounded to
// maxTokenFileBytes so an oversized or pathological file cannot be read
// into memory in full. The file must also deny group and world access on
// POSIX platforms so a shared or misconfigured host cannot leak the
// credential to other local users; that check runs against the fstat(2)
// result on the open descriptor, not a separate stat(2) on the path, so
// it cannot be fooled by a path swapped after the type check above.
func loadFromFile() (CredentialResult, error) {
	path := strings.TrimSpace(os.Getenv(TokenFileEnvironment))
	if path == "" {
		path = defaultTokenFilePath()
		if path == "" {
			return CredentialResult{}, ErrCredentialMissing
		}
	}
	return loadCredentialFile(path)
}

func loadCredentialFile(path string) (CredentialResult, error) {
	contents, info, err := readBoundedRegularFile(path, maxTokenFileBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CredentialResult{}, fmt.Errorf("read ACR credential file: %w", ErrCredentialMissing)
		}
		return CredentialResult{}, fmt.Errorf("read ACR credential file: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return CredentialResult{}, fmt.Errorf("ACR credential file permissions must not grant group or world access: %s", info.Mode().Perm())
	}
	token := strings.TrimSpace(string(contents))
	if token == "" {
		return CredentialResult{}, fmt.Errorf("ACR credential file is empty: %w", ErrCredentialMissing)
	}
	if !auth.IsTokenShapeValid(token) {
		return CredentialResult{}, fmt.Errorf("%s: %w", TokenFileEnvironment, ErrCredentialShapeInvalid)
	}
	return CredentialResult{Token: token, Source: "file", filePath: path}, nil
}

// CredentialPersistenceSupported exposes the stable, preflightable platform
// boundary for login before it asks the server to issue a one-time secret.
func CredentialPersistenceSupported() error {
	if runtime.GOOS == "windows" {
		return ErrCredentialPersistenceUnsupported
	}
	return nil
}

// PersistCredential stores a shape-valid credential in the configured or
// default keyring when the platform can write it safely. Unavailable or failed
// keyring writes atomically fall back to the configured or default token file.
func PersistCredential(token string) (CredentialResult, error) {
	keyringAllowed, err := keyringEnabled()
	if err != nil {
		return CredentialResult{}, err
	}
	if err := CredentialPersistenceSupported(); err != nil {
		return CredentialResult{}, err
	}
	token = strings.TrimSpace(token)
	if !auth.IsTokenShapeValid(token) {
		return CredentialResult{}, ErrCredentialShapeInvalid
	}
	if keyringAllowed {
		service, account, keyringConfigured := credentialKeyringAddress()
		if keyringConfigured && currentKeyringWriter != nil {
			ctx, cancel := context.WithTimeout(context.Background(), keyringLookupTimeout)
			defer cancel()
			if err := currentKeyringWriter(ctx, service, account, token); err == nil {
				return CredentialResult{Token: token, Source: "keyring", keyringService: service, keyringAccount: account}, nil
			} else if !errors.Is(err, errKeyringWriteUnavailable) {
				// Anything past the attempt itself is ambiguous. A secret-store
				// backend can commit the entry and still fail afterwards -- the
				// mutation succeeds and the collection write-out, the D-Bus
				// reply, or the process exit does not -- so returning a bare
				// error told the caller "nothing was stored" while a readable
				// credential sat in the keyring with no locator to purge it by.
				// Hand back the candidate address alongside the failure, exactly
				// as the ambiguous file write below does, so login can revoke
				// the server-side credential and then purge precisely this entry.
				return CredentialResult{Token: token, Source: "keyring", keyringService: service, keyringAccount: account},
					fmt.Errorf("persist ACR keyring credential: %w", err)
			}
		}
	}
	path := configuredTokenFilePath()
	if path == "" {
		return CredentialResult{}, ErrCredentialMissing
	}
	if err := writeCredentialFile(path, token); err != nil {
		failure := fmt.Errorf("persist ACR credential fallback file: %w", err)
		if errors.Is(err, errCredentialWriteAmbiguous) {
			// The credential may already be readable at path. Report the
			// failure, but hand back the candidate locator so the caller can
			// revoke the server-side credential and purge exactly this file
			// instead of guessing at a source it was never told about.
			return CredentialResult{Token: token, Source: "file", filePath: path}, failure
		}
		return CredentialResult{}, failure
	}
	return CredentialResult{Token: token, Source: "file", filePath: path}, nil
}

// ReplaceCredential updates the credential source already selected by the
// sidecar. Refresh must never silently change sources: doing so can leave an
// older higher-precedence token active.
func ReplaceCredential(current CredentialResult, token string) error {
	if err := CredentialPersistenceSupported(); err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if !auth.IsTokenShapeValid(token) {
		return ErrCredentialShapeInvalid
	}
	switch current.Source {
	case "keyring":
		keyringAllowed, err := keyringEnabled()
		if err != nil {
			return err
		}
		if !keyringAllowed {
			return ErrCredentialPersistenceSourceUnsupported
		}
		if current.keyringService == "" || current.keyringAccount == "" || currentKeyringWriter == nil {
			return ErrCredentialPersistenceSourceUnsupported
		}
		ctx, cancel := context.WithTimeout(context.Background(), keyringLookupTimeout)
		defer cancel()
		if err := currentKeyringWriter(ctx, current.keyringService, current.keyringAccount, token); err != nil {
			return fmt.Errorf("replace ACR keyring credential: %w", err)
		}
		return nil
	case "file":
		if current.filePath == "" {
			return ErrCredentialPersistenceSourceUnsupported
		}
		if err := writeCredentialFile(current.filePath, token); err != nil {
			return fmt.Errorf("replace ACR credential file: %w", err)
		}
		return nil
	default:
		return ErrCredentialPersistenceSourceUnsupported
	}
}

func RestoreCredential(current CredentialResult) error {
	return ReplaceCredential(current, current.Token)
}

func DeleteCredential() error {
	keyringAllowed, err := keyringEnabled()
	if err != nil {
		return err
	}
	if err := CredentialPersistenceSupported(); err != nil {
		return err
	}
	credential, err := loadCredential(keyringAllowed)
	if errors.Is(err, ErrCredentialMissing) {
		return nil
	}
	if err != nil {
		return &CredentialCleanupError{Location: configuredTokenFilePath(), cause: fmt.Errorf("load ACR credential for deletion: %w", err)}
	}
	switch credential.Source {
	case "environment":
		return &CredentialCleanupError{Location: TokenEnvironment, cause: ErrCredentialPersistenceSourceUnsupported}
	case "keyring":
		if !keyringAllowed {
			return &CredentialCleanupError{Location: "ACR keyring", cause: ErrCredentialPersistenceSourceUnsupported}
		}
		service, account, keyringConfigured := credentialKeyringAddress()
		if !keyringConfigured || currentKeyringDeleter == nil {
			return &CredentialCleanupError{Location: credentialKeyringLocation(service, account), cause: errors.New("delete ACR keyring credential: keyring unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), keyringLookupTimeout)
		defer cancel()
		if err := currentKeyringDeleter(ctx, service, account); err != nil {
			return &CredentialCleanupError{Location: credentialKeyringLocation(service, account), cause: fmt.Errorf("delete ACR keyring credential: %w", err)}
		}
		return nil
	case "file":
		path := configuredTokenFilePath()
		if path == "" {
			return &CredentialCleanupError{Location: TokenFileEnvironment, cause: ErrCredentialPersistenceSourceUnsupported}
		}
		if err := removeACRCredentialFile(path); err != nil {
			return &CredentialCleanupError{Location: path, cause: err}
		}
		return nil
	default:
		return &CredentialCleanupError{Location: "ACR credential", cause: ErrCredentialPersistenceSourceUnsupported}
	}
}

func credentialKeyringLocation(service, account string) string {
	return "keyring service " + service + " account " + account
}

// credentialKeyringAddress derives the keyring service and account, and
// reports whether the pair addresses anything at all.
//
// This is the single derivation for every keyring operation -- lookup,
// verification, persistence, deletion, and purge. It used to be duplicated
// inside loadFromKeyring, which is a latent divergence with no symptom until
// the two disagree: a read that resolves one account while a delete resolves
// another leaves a live credential in the store that logout reported as
// removed, and neither copy's own tests would notice.
//
// The account falls back to defaultKeyringAccount (the OS user) only when the
// service was set explicitly. Without an explicit service, an unset
// ACR_API_URL leaves no address at all, rather than pointing every ACR install
// on the host at one shared per-user entry under the default service.
func credentialKeyringAddress() (string, string, bool) {
	return deriveCredentialKeyringAddress(os.Getenv)
}

func deriveCredentialKeyringAddress(getenv func(string) string) (string, string, bool) {
	service := strings.TrimSpace(getenv(TokenKeyringServiceEnvironment))
	explicitService := service != ""
	if service == "" {
		service = defaultKeyringService
	}
	account := strings.TrimSpace(getenv(TokenKeyringAccountEnvironment))
	if account == "" {
		account = normalizedKeyringAccountForAPIURL(strings.TrimSpace(getenv(APIURLEnvironment)))
		if account == "" && explicitService {
			account = defaultKeyringAccountFrom(getenv)
		}
	}
	return service, account, account != ""
}

func keyringEnabled() (bool, error) {
	disabled, err := strictBoolOrDefault(os.LookupEnv, TokenKeyringDisabledEnvironment, false)
	if err != nil {
		return false, err
	}
	return !disabled, nil
}

func configuredTokenFilePath() string {
	if path := strings.TrimSpace(os.Getenv(TokenFileEnvironment)); path != "" {
		return path
	}
	return defaultTokenFilePath()
}

func defaultTokenFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".acr", "token")
}

// normalizedKeyringAccountForAPIURL renders the configured API origin as a
// keyring account: scheme and host only, lowercased, with no path, query, port
// stripping, or userinfo. A malformed or non-absolute value yields no account
// rather than a partial one, so a broken ACR_API_URL cannot address a keyring
// entry belonging to some other origin.
func normalizedKeyringAccountForAPIURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}
