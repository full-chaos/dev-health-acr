package sidecar

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// ErrVerificationURIUnsupported is returned when a device-flow verification
// URI is not something this sidecar will hand to a desktop opener. It carries
// no part of the rejected value: the URI is server-supplied data, and echoing
// it would put untrusted text on an operator-facing error.
var ErrVerificationURIUnsupported = errors.New("acr: verification URI is not a launchable https or loopback http address")

// maxVerificationURIBytes matches the contract bound on the device
// authorization response's verification_uri.
const maxVerificationURIBytes = 2048

// browserOpenerDeadline bounds how long a launched opener may hold this
// process's attention. A desktop opener is expected to hand the address to an
// already-running browser and exit immediately; one that instead blocks --
// a misconfigured handler waiting on a lock, an xdg-open that falls through to
// a foreground text browser, a hostile substitute -- previously stayed alive
// for the whole login session, because the only thing waiting on it was an
// unbounded background Wait.
//
// It is a var rather than a const solely so the reap test can drive the
// deadline branch in bounded time; production never reassigns it.
var browserOpenerDeadline = 20 * time.Second

// browserOpenerReaped, when non-nil, is invoked after the opener has been
// waited for exactly once. It exists so a test can observe the reap itself
// rather than infer it from elapsed time, which would pass whether or not the
// child was ever collected.
var browserOpenerReaped func()

// browserLaunchEnvironment is the complete set of variables a desktop opener
// is allowed to inherit. This is an allowlist rather than credentialSafeEnviron's
// ACR_-prefix strip because an opener hands control to an arbitrary,
// user-configured browser: every unlisted variable in this process's
// environment -- proxies, tool configuration, anything an MCP client set --
// stops here instead of reaching that browser.
var browserLaunchEnvironment = []string{
	"HOME",
	"USER",
	"LOGNAME",
	"LANG",
	"LC_ALL",
	"TMPDIR",
	"DISPLAY",
	"WAYLAND_DISPLAY",
	"XAUTHORITY",
	"XDG_RUNTIME_DIR",
	"XDG_SESSION_TYPE",
	"XDG_CURRENT_DESKTOP",
	"DBUS_SESSION_BUS_ADDRESS",
}

// OpenVerificationURI best-effort opens a device-flow verification URI in the
// operator's desktop browser. Failure is never fatal: login always prints the
// URI and user code first, so an opener that is absent, untrusted, or refuses
// the address only costs the convenience of the automatic launch.
//
// Four things are enforced before anything is executed. The URI must be
// https, or http to a validated loopback address (the local development and
// fixture case); every other scheme -- file, javascript, a custom handler a
// hostile server could register -- is refused, as is any URI carrying control
// characters, whitespace, userinfo, or ACR-token-shaped text. The opener
// binary is resolved through
// currentExecutableResolver, which searches a fixed system directory allowlist
// and never the process's PATH, so a PATH entry cannot substitute an attacker's
// "open". The child receives browserLaunchEnvironment, not this process's
// environment, so no ACR credential or client-supplied variable is inherited by
// whatever browser the opener launches. And the child is placed in its own
// process group and reaped under a fixed deadline, so an opener that hangs
// takes neither this process's login session nor an orphaned process tree with
// it.
func OpenVerificationURI(uri string) error {
	if err := validateVerificationURI(uri); err != nil {
		return err
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	path, err := currentExecutableResolver(name)
	if err != nil {
		return fmt.Errorf("resolve trusted browser opener: %w", err)
	}
	command := exec.Command(path, uri)
	command.Env = browserLaunchEnviron()
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	// configureKeyringProcessGroup is this package's generic child
	// process-group helper (credential_keyring_procgroup_unix.go), already
	// shared with codegraph_runner_exec.go. An opener needs it for the same
	// reason a keyring backend does: xdg-open is a shell script that forks a
	// handler, so killing only the immediate child leaves that handler running.
	configureKeyringProcessGroup(command)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start browser opener: %w", err)
	}
	// The deadline and the reap hook are captured here, at launch, not read at
	// reap time: an opener launched before a test installed its own values must
	// not be able to signal that test's reap -- both a false pass and a data
	// race on the seams.
	go reapBrowserOpener(command, browserOpenerDeadline, browserOpenerReaped)
	return nil
}

// reapBrowserOpener waits for the opener exactly once and bounds how long it
// may run.
//
// Wait is called from exactly one goroutine, and the process group is killed
// only on the branch where the deadline actually won -- while the child is
// still un-reaped. Arming a timer that kills after Wait may already have
// returned would signal a process group whose leader PID the kernel is free to
// have reused, turning a hung-opener guard into a stray SIGKILL at an
// unrelated process tree.
func reapBrowserOpener(command *exec.Cmd, deadline time.Duration, reaped func()) {
	processGroup := captureKeyringProcessGroup(command)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = command.Wait()
	}()
	bound := time.NewTimer(deadline)
	defer bound.Stop()
	select {
	case <-done:
	case <-bound.C:
		_ = killKeyringProcessGroupID(processGroup)
		<-done
	}
	if reaped != nil {
		reaped()
	}
}

// ValidateVerificationURI reports whether a device-flow verification address
// is safe to show an operator and hand to a desktop opener. Login calls it
// before printing, so a hostile address is never rendered into a terminal an
// operator may copy from, and never reaches an opener whether or not the
// automatic launch is enabled.
func ValidateVerificationURI(raw string) error { return validateVerificationURI(raw) }

// validateVerificationURI accepts only what a desktop opener may be handed.
func validateVerificationURI(raw string) error {
	if raw == "" || len(raw) > maxVerificationURIBytes {
		return ErrVerificationURIUnsupported
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f {
			return ErrVerificationURIUnsupported
		}
	}
	if containsTokenShapedText(raw) {
		return ErrVerificationURIUnsupported
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil {
		return ErrVerificationURIUnsupported
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHostname(parsed.Hostname()) {
			return nil
		}
	}
	return ErrVerificationURIUnsupported
}

// containsTokenShapedText refuses any address carrying ACR bearer-credential
// text. A verification address has no legitimate reason to contain one, and
// this URI is both printed to the operator's terminal and passed as an argv
// element to a user-configured browser -- two places a credential must never
// appear. The whole prefix is refused rather than only well-formed tokens:
// distinguishing "a real token" from "a truncated one" buys nothing here, and
// a partial secret is still a secret.
func containsTokenShapedText(raw string) bool {
	return strings.Contains(strings.ToLower(raw), strings.ToLower(auth.TokenPrefix))
}

func isLoopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// browserLaunchEnviron builds the opener's environment from the allowlist plus
// a PATH fixed to the same trusted system directories executable resolution
// uses, so a desktop opener that shells out resolves its helpers there too.
func browserLaunchEnviron() []string {
	environment := make([]string, 0, len(browserLaunchEnvironment)+1)
	for _, key := range browserLaunchEnvironment {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	if directories := trustedExecutableSearchDirs(); len(directories) != 0 {
		environment = append(environment, "PATH="+strings.Join(directories, string(os.PathListSeparator)))
	}
	return environment
}
