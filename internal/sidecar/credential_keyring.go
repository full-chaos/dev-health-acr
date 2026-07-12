package sidecar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
)

// KeyringLookup resolves a credential from an OS-native secret store. It
// returns ok=false without an error when the platform has no reachable
// keyring backend or the entry does not exist, so LoadCredential can fall
// through to the next precedence source. A non-nil error indicates the
// lookup itself failed unexpectedly (for example the backend refused the
// query); LoadCredential still falls through rather than failing hard,
// since the keyring is an optional convenience seam, not a required
// credential source.
type KeyringLookup func(ctx context.Context, service, account string) (token string, ok bool, err error)

// currentKeyringLookup is the active keyring implementation. It is a
// package-level seam so platform-specific behavior can be swapped out in
// tests without touching real OS secret stores.
var currentKeyringLookup KeyringLookup = defaultKeyringLookup

// defaultKeyringLookup shells out to the platform's native secret-store CLI.
// This intentionally avoids adding a third-party keyring dependency: macOS
// ships `security`, and most Linux desktop environments with libsecret ship
// `secret-tool`. Platforms without a known, dependency-free lookup command
// report the keyring as unavailable (ok=false, err=nil) rather than fail.
func defaultKeyringLookup(ctx context.Context, service, account string) (string, bool, error) {
	switch runtime.GOOS {
	case "darwin":
		return runKeyringCommand(ctx, "security", "find-generic-password", "-a", account, "-s", service, "-w")
	case "linux":
		return runKeyringCommand(ctx, "secret-tool", "lookup", "service", service, "account", account)
	default:
		return "", false, nil
	}
}

// maxKeyringOutputBytes bounds how many bytes of stdout runKeyringCommand
// will read from a keyring backend's lookup command. A resolved ACR API
// token is a fixed 49 bytes (auth.TokenPrefix plus a 32-byte base64url
// secret); this ceiling exists to bound memory and I/O if the backend (or
// an attacker-controlled PATH substitute standing in for it) returns a
// pathologically large or endless stream, not to accommodate legitimate
// secret growth.
const maxKeyringOutputBytes = 4096 // 4 KiB

// maxKeyringStderrBytes bounds how many bytes of the keyring command's
// stderr runKeyringCommand retains for a failure diagnostic. Stderr only
// ever carries backend diagnostic text, never the looked-up secret (which
// travels exclusively over stdout and is never included in an error
// returned by this function), but it is still bounded so a misbehaving or
// hostile binary cannot force unbounded memory growth there either.
const maxKeyringStderrBytes = 4096 // 4 KiB

// errKeyringOutputTooLarge is returned by runKeyringCommand when the
// backend's stdout exceeds maxKeyringOutputBytes. It carries no captured
// output, so no partial secret can ever reach an error message or log
// line.
var errKeyringOutputTooLarge = errors.New("keyring lookup output exceeded the maximum size")

// runKeyringCommand executes a bounded external secret-store lookup.
// Binary absence and "not found" exits are reported as unavailable, not
// errors; any other failure (permission denial, locked keychain,
// malformed output) is surfaced as an error so LoadCredential can still
// fall through while leaving room for callers that want to distinguish
// the cases.
//
// stdout is read through cmd.StdoutPipe() wrapped in
// io.LimitReader(stdout, maxKeyringOutputBytes+1): once that many bytes
// have been consumed, io.ReadAll stops -- via io.LimitReader's own EOF,
// not the child's -- regardless of whether the child process has closed
// its stdout or is still running. A backend that never stops writing (or
// that writes past the ceiling and then hangs without exiting) therefore
// cannot force an unbounded read or an indefinite wait the way the
// previous cmd.Output() call could: this bounds stdout even though the
// keyring lookup's own timeout (credential.go's keyringLookupTimeout,
// applied to ctx by the caller) already bounds wall-clock time
// independently. Whenever the read comes back oversized (or otherwise
// fails), the process is killed immediately rather than left to run out
// the timeout, since output past the ceiling means the backend is
// misbehaving, not merely slow. stderr is captured into a boundedBuffer
// for the same reason stdout is bounded: unbounded stderr buffering would
// reopen the same memory-exhaustion class this function exists to close.
//
// name is resolved via currentExecutableResolver (exec_resolver.go), never
// a bare argv0 left for exec.CommandContext to search PATH for on its own:
// production resolution (resolveTrustedExecutable) searches only a fixed
// system directory allowlist, never the process's PATH environment
// variable, so a backend that cannot be found there -- absent, or present
// only via a PATH entry -- is reported unavailable (ok=false, err=nil)
// exactly like a genuinely missing binary, never silently substituted
// with something workspace- or environment-controlled. The subprocess's
// environment is credentialSafeEnviron() (exec_resolver.go), not this
// process's own: every ACR_-prefixed variable, including the ACR API
// bearer credential this lookup exists to eventually supply, is stripped
// before the backend ever runs, since neither `security` nor
// `secret-tool` has any legitimate need to see it.
func runKeyringCommand(ctx context.Context, name string, args ...string) (string, bool, error) {
	path, err := currentExecutableResolver(name)
	if err != nil {
		return "", false, nil
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = credentialSafeEnviron()
	cmd.Stdin = nil

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	stderr := &boundedBuffer{limit: maxKeyringStderrBytes}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return "", false, err
	}

	output, readErr := io.ReadAll(io.LimitReader(stdout, maxKeyringOutputBytes+1))
	oversize := int64(len(output)) > maxKeyringOutputBytes
	if readErr != nil || oversize {
		// Kill immediately rather than waiting out the lookup timeout: a
		// backend producing pathological output is misbehaving, not
		// merely slow, and there is no reason to let it keep running.
		// Killing first is also what makes the subsequent Wait safe to
		// call even though stdout was abandoned before EOF: Wait's own
		// documentation warns that calling it before all reads from a
		// StdoutPipe complete can deadlock a still-writing child against
		// a full pipe buffer, and SIGKILL (which cannot be blocked or
		// caught) unconditionally unblocks any such pending write.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if oversize {
			return "", false, errKeyringOutputTooLarge
		}
		return "", false, fmt.Errorf("read keyring lookup output: %w", readErr)
	}

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Nonzero exit from a present binary conventionally means "not
			// found" for both `security` and `secret-tool`; treat it as an
			// unavailable entry rather than a hard failure.
			return "", false, nil
		}
		// err here (a process-management failure such as a wait error
		// unrelated to a clean or nonzero exit) is os/exec's own text,
		// never secret content: the secret only ever travels over stdout,
		// which is discarded on every non-success path. diag is capped,
		// backend-diagnostic stderr text, not the looked-up credential.
		if diag := stderr.String(); diag != "" {
			return "", false, fmt.Errorf("%w: %s", err, diag)
		}
		return "", false, err
	}

	return string(output), true, nil
}

// boundedBuffer is an io.Writer that retains at most limit bytes, silently
// discarding any bytes beyond that ceiling while still reporting every
// Write call as fully consumed. Used for a subprocess's stderr so a
// misbehaving backend cannot force unbounded memory growth there either;
// os/exec runs an internal goroutine copying the child's stderr into this
// writer, and that goroutine must never block on a full destination, so
// discarding past the limit (rather than returning an error, which would
// abort the copy and could itself surface in cmd.Wait's error) is
// required, not just convenient.
type boundedBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		b.buf.Write(p[:room])
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	return b.buf.String()
}

// defaultKeyringAccount derives a stable per-user account name for keyring
// lookups when ACR_API_TOKEN_KEYRING_ACCOUNT is not explicitly configured.
func defaultKeyringAccount() string {
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	if user := os.Getenv("USERNAME"); user != "" {
		return user
	}
	return "acr-agent"
}
