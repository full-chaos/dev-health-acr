package sidecar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

const (
	TokenEnvironment               = "ACR_API_TOKEN"
	TokenFileEnvironment           = "ACR_API_TOKEN_FILE"
	TokenKeyringServiceEnvironment = "ACR_API_TOKEN_KEYRING_SERVICE"
	TokenKeyringAccountEnvironment = "ACR_API_TOKEN_KEYRING_ACCOUNT"

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

type CredentialResult struct {
	Token  string
	Source string
}

// LoadCredential resolves the ACR API bearer credential with a fixed
// precedence: the process environment always wins (agent-client
// compatibility), then an optional OS keyring entry, then a
// permission-restricted token file. There is intentionally no CLI-argument
// or plaintext-config-file source: both would make secrets visible in shell
// history, process listings, or unencrypted files by default.
func LoadCredential() (CredentialResult, error) {
	if token := strings.TrimSpace(os.Getenv(TokenEnvironment)); token != "" {
		if !auth.IsTokenShapeValid(token) {
			return CredentialResult{}, fmt.Errorf("%s: %w", TokenEnvironment, ErrCredentialShapeInvalid)
		}
		return CredentialResult{Token: token, Source: "environment"}, nil
	}
	if result, ok := loadFromKeyring(); ok {
		return result, nil
	}
	return loadFromFile()
}

// loadFromKeyring consults the optional OS keyring seam when
// ACR_API_TOKEN_KEYRING_SERVICE is configured. A miss, an unconfigured
// service, or an unexpected lookup error all report ok=false so the caller
// falls through to the token file; the keyring is a convenience source, not
// a required one.
func loadFromKeyring() (CredentialResult, bool) {
	service := strings.TrimSpace(os.Getenv(TokenKeyringServiceEnvironment))
	if service == "" || currentKeyringLookup == nil {
		return CredentialResult{}, false
	}
	account := strings.TrimSpace(os.Getenv(TokenKeyringAccountEnvironment))
	if account == "" {
		account = defaultKeyringAccount()
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
		return CredentialResult{}, ErrCredentialMissing
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
