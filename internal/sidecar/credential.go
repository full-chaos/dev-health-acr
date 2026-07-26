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

type CredentialResult struct {
	Token  string
	Source string
}

// LoadCredential resolves the ACR API bearer credential with a fixed
// precedence: the process environment always wins (agent-client
// compatibility), then the explicit or default OS keyring entry, then the
// explicit or default permission-restricted token file. There is intentionally
// no CLI-argument or plaintext-config-file source: both would make secrets
// visible in shell history, process listings, or unencrypted files by default.
func LoadCredential() (CredentialResult, error) {
	if token := strings.TrimSpace(os.Getenv(TokenEnvironment)); token != "" {
		if !auth.IsTokenShapeValid(token) {
			return CredentialResult{}, fmt.Errorf("%s: %w", TokenEnvironment, ErrCredentialShapeInvalid)
		}
		return CredentialResult{Token: token, Source: "environment"}, nil
	}
	if !keyringDisabled() {
		if result, ok := loadFromKeyring(); ok {
			return result, nil
		}
	}
	return loadFromFile()
}

// loadFromKeyring consults the explicit or default OS keyring seam. A miss or
// an unexpected lookup error reports ok=false so the caller falls through to
// the token file; the keyring is a convenience source, not a required one.
func loadFromKeyring() (CredentialResult, bool) {
	service := strings.TrimSpace(os.Getenv(TokenKeyringServiceEnvironment))
	explicitService := service != ""
	if service == "" {
		service = defaultKeyringService
	}
	if currentKeyringLookup == nil {
		return CredentialResult{}, false
	}
	account := strings.TrimSpace(os.Getenv(TokenKeyringAccountEnvironment))
	if account == "" {
		account = defaultKeyringAccountForAPIURL()
		if account == "" && explicitService {
			account = defaultKeyringAccount()
		}
	}
	if account == "" {
		return CredentialResult{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyringLookupTimeout)
	defer cancel()
	token, ok, err := currentKeyringLookup(ctx, service, account)
	if err != nil || !ok {
		return CredentialResult{}, false
	}
	token = strings.TrimSpace(token)
	if token == "" || !auth.IsTokenShapeValid(token) {
		// A blank entry or one that does not match the ACR token shape is
		// treated as unusable rather than a hard failure: the keyring is a
		// convenience source, so callers fall through to the token file
		// instead of being blocked by a stray/garbage keyring entry.
		return CredentialResult{}, false
	}
	return CredentialResult{Token: token, Source: "keyring"}, true
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
	contents, info, err := readBoundedRegularFile(path, maxTokenFileBytes)
	if err != nil {
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
	return CredentialResult{Token: token, Source: "file"}, nil
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
	if err := CredentialPersistenceSupported(); err != nil {
		return CredentialResult{}, err
	}
	token = strings.TrimSpace(token)
	if !auth.IsTokenShapeValid(token) {
		return CredentialResult{}, ErrCredentialShapeInvalid
	}
	if !keyringDisabled() {
		service, account, keyringConfigured := credentialKeyringAddress()
		if keyringConfigured && currentKeyringWriter != nil {
			ctx, cancel := context.WithTimeout(context.Background(), keyringLookupTimeout)
			defer cancel()
			if err := currentKeyringWriter(ctx, service, account, token); err == nil {
				return CredentialResult{Token: token, Source: "keyring"}, nil
			}
		}
	}
	path := configuredTokenFilePath()
	if path == "" {
		return CredentialResult{}, ErrCredentialMissing
	}
	if err := writeCredentialFile(path, token); err != nil {
		return CredentialResult{}, fmt.Errorf("persist ACR credential fallback file: %w", err)
	}
	return CredentialResult{Token: token, Source: "file"}, nil
}

func DeleteCredential() error {
	if err := CredentialPersistenceSupported(); err != nil {
		return err
	}
	credential, err := LoadCredential()
	if errors.Is(err, ErrCredentialMissing) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load ACR credential for deletion: %w", err)
	}
	if credential.Source == "keyring" && !keyringDisabled() {
		service, account, keyringConfigured := credentialKeyringAddress()
		if !keyringConfigured || currentKeyringDeleter == nil {
			return errors.New("delete ACR keyring credential: keyring unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), keyringLookupTimeout)
		defer cancel()
		if err := currentKeyringDeleter(ctx, service, account); err != nil {
			return fmt.Errorf("delete ACR keyring credential: %w", err)
		}
	}
	path := configuredTokenFilePath()
	if path == "" {
		return nil
	}
	if err := removeCredentialFile(path); err != nil {
		return fmt.Errorf("remove ACR credential fallback file: %w", err)
	}
	return nil
}

func CredentialCleanupLocation(credential CredentialResult) string {
	if credential.Source == "file" {
		if path := configuredTokenFilePath(); path != "" {
			return path
		}
	}
	return "the configured ACR keyring entry"
}

func credentialKeyringAddress() (string, string, bool) {
	service := strings.TrimSpace(os.Getenv(TokenKeyringServiceEnvironment))
	explicitService := service != ""
	if service == "" {
		service = defaultKeyringService
	}
	account := strings.TrimSpace(os.Getenv(TokenKeyringAccountEnvironment))
	if account == "" {
		account = defaultKeyringAccountForAPIURL()
		if account == "" && explicitService {
			account = defaultKeyringAccount()
		}
	}
	return service, account, account != ""
}

func keyringDisabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(TokenKeyringDisabledEnvironment)), "true")
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

func defaultKeyringAccountForAPIURL() string {
	raw := strings.TrimSpace(os.Getenv(APIURLEnvironment))
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}
