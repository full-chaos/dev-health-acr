package sidecar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

var errKeyringWriteUnavailable = errors.New("acr: keyring write unavailable")
var errKeyringWriteFailed = errors.New("acr: keyring write failed")

// KeyringWriter stores a token without exposing it in argv or the child
// environment. Tests replace this seam instead of touching OS secret stores.
type KeyringWriter func(ctx context.Context, service, account, token string) error

// KeyringDeleter removes a previously stored token. It accepts only stable
// identifiers, never the secret itself.
type KeyringDeleter func(ctx context.Context, service, account string) error

var currentKeyringWriter KeyringWriter = defaultKeyringWriter
var currentKeyringDeleter KeyringDeleter = defaultKeyringDeleter

func defaultKeyringWriter(ctx context.Context, service, account, token string) error {
	if runtime.GOOS != "linux" {
		return errKeyringWriteUnavailable
	}
	// A store must never treat a nonzero exit as success: tolerateMissingEntry
	// is false so an exit 1 from `secret-tool store` stays a write failure.
	return runKeyringMutation(ctx, strings.NewReader(token), false, "secret-tool", "store", "--label=Dev Health ACR credential", "service", service, "account", account)
}

// defaultKeyringDeleter removes an entry idempotently: a delete whose target
// is already gone is the goal state, not a failure. Absence is recognized
// through keyringExitMeansEntryMissing, so `secret-tool clear` reporting an
// operational problem on stderr still fails closed instead of being read as
// "there was nothing to remove".
func defaultKeyringDeleter(ctx context.Context, service, account string) error {
	switch runtime.GOOS {
	case "darwin":
		return runKeyringMutation(ctx, nil, true, "security", "delete-generic-password", "-a", account, "-s", service)
	case "linux":
		return runKeyringMutation(ctx, nil, true, "secret-tool", "clear", "service", service, "account", account)
	default:
		return errKeyringWriteUnavailable
	}
}

// runKeyringMutation shares the lookup subprocess hardening: trusted binary
// resolution, sanitized environment, bounded completion, and process-group
// cancellation. The token reaches secret-tool only through stdin.
//
// tolerateMissingEntry is set only by delete callers. Stderr is bounded and
// inspected, never returned: it carries backend diagnostic text, and the
// secret itself only ever travels over stdin.
func runKeyringMutation(ctx context.Context, stdin io.Reader, tolerateMissingEntry bool, name string, args ...string) error {
	path, err := currentExecutableResolver(name)
	if err != nil {
		if errors.Is(err, ErrExecutableUnavailable) {
			return errKeyringWriteUnavailable
		}
		return fmt.Errorf("resolve trusted keyring executable: %w", err)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = credentialSafeEnviron()
	cmd.Stdin = stdin
	cmd.Stdout = &boundedBuffer{limit: maxKeyringOutputBytes}
	stderr := &boundedBuffer{limit: maxKeyringStderrBytes}
	cmd.Stderr = stderr
	configureKeyringProcessGroup(cmd)
	cmd.Cancel = func() error { return killKeyringProcessGroup(cmd) }
	cmd.WaitDelay = keyringCancelWaitDelay
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exitErr *exec.ExitError
		if tolerateMissingEntry && errors.As(err, &exitErr) && keyringExitMeansEntryMissing(name, exitErr.ExitCode(), stderr.String()) {
			return nil
		}
		return fmt.Errorf("%w: %w", errKeyringWriteFailed, err)
	}
	return nil
}
