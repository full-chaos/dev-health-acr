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
	"sync"
	"time"
	"unicode"

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

// browserOpenerWaited, when non-nil, is invoked immediately after this
// package's single command.Wait() call on the launched opener returns. It is
// distinct from browserOpenerReaped: reaped fires once after the whole reap
// sequence completes regardless of how many times Wait was actually called,
// so a mutation that added a second Wait call would still satisfy a test that
// only hooks reaped. Hooking the Wait call itself is the only way to pin
// "waited for exactly once" as a property of the call site, not of the
// sequence around it.
var browserOpenerWaited func(error)

// activeBrowserOpener tracks the most recently launched opener that has not
// yet finished reaping, so CloseVerificationBrowserOpener can synchronously
// tear it down. OpenVerificationURI hands off and reaps in a background
// goroutine bounded by browserOpenerDeadline; that goroutine, like every other
// goroutine in this process, is killed the instant the process exits. A login
// that succeeds (or is cancelled) before a slow or hung opener returns would
// otherwise orphan both the opener and any process it forked, unreaped, for
// however long they keep running after this process is gone -- silently,
// since process exit reports nothing about what it left behind.
var (
	activeBrowserOpenerMu sync.Mutex
	activeBrowserOpener   *browserOpenerHandle
)

// browserOpenerHandle lets the launching goroutine and the reaping goroutine
// coordinate a forced, synchronous teardown without either one guessing at
// the other's state.
type browserOpenerHandle struct {
	processGroup int
	done         chan struct{}
	forceKill    chan struct{}
	forceOnce    sync.Once
}

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
//
// The launch hands off immediately; the caller must call
// CloseVerificationBrowserOpener before the process may exit, so a fast
// success does not orphan a still-running opener that the background deadline
// has not yet caught up with.
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
	// The process group is captured here, at launch, so a synchronous
	// CloseVerificationBrowserOpener call racing the reaper goroutine's own
	// startup always has a real target to kill rather than an empty one.
	processGroup := captureKeyringProcessGroup(command)
	handle := &browserOpenerHandle{processGroup: processGroup, done: make(chan struct{}), forceKill: make(chan struct{})}
	activeBrowserOpenerMu.Lock()
	activeBrowserOpener = handle
	activeBrowserOpenerMu.Unlock()
	// The deadline and the reap hooks are captured here, at launch, not read at
	// reap time: an opener launched before a test installed its own values must
	// not be able to signal that test's reap -- both a false pass and a data
	// race on the seams.
	go reapBrowserOpener(command, processGroup, handle, browserOpenerDeadline, browserOpenerReaped, browserOpenerWaited)
	return nil
}

// CloseVerificationBrowserOpener synchronously tears down the most recently
// launched browser opener, if one has not already finished on its own. Login
// calls this once it no longer needs the opener -- on every return path from
// the attempt that launched it, success or failure alike -- so the process
// never exits while a reap is still only bounded by the 20-second background
// deadline: that deadline exists to bound an unattended login, not to be the
// only thing standing between a fast, successful login and an orphaned
// browser process (and everything it may have forked) left running after this
// process is gone.
//
// A second call, or a call with no opener outstanding, is a safe no-op:
// forceOnce and the done channel together make teardown idempotent regardless
// of how many return paths converge on it.
func CloseVerificationBrowserOpener() {
	activeBrowserOpenerMu.Lock()
	handle := activeBrowserOpener
	activeBrowserOpenerMu.Unlock()
	if handle == nil {
		return
	}
	handle.forceOnce.Do(func() { close(handle.forceKill) })
	<-handle.done
}

// reapBrowserOpener waits for the opener exactly once and bounds how long it
// may run, or tears it down immediately if handle.forceKill fires first --
// CloseVerificationBrowserOpener's synchronous teardown path.
//
// Wait is called from exactly one goroutine, and the process group is killed
// only on a branch where the child is confirmed still un-reaped (the deadline
// firing, or a forced close): arming a timer that kills after Wait may
// already have returned would signal a process group whose leader PID the
// kernel is free to have reused, turning a hung-opener guard into a stray
// SIGKILL at an unrelated process tree.
func reapBrowserOpener(command *exec.Cmd, processGroup int, handle *browserOpenerHandle, deadline time.Duration, reaped func(), waited func(error)) {
	defer func() {
		activeBrowserOpenerMu.Lock()
		if activeBrowserOpener == handle {
			activeBrowserOpener = nil
		}
		activeBrowserOpenerMu.Unlock()
		close(handle.done)
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := command.Wait()
		if waited != nil {
			waited(err)
		}
	}()
	bound := time.NewTimer(deadline)
	defer bound.Stop()
	select {
	case <-done:
	case <-bound.C:
		_ = killKeyringProcessGroupID(processGroup)
		<-done
	case <-handle.forceKill:
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
		// r <= ' ' catches every ASCII control character and the space itself;
		// 0x7f is DEL, the one ASCII control point above the printable range.
		// Neither test reaches the much larger set of Unicode characters that
		// read as invisible or as whitespace to a terminal or a browser's
		// address handling without being ASCII: C1 controls (0x80-0x9F),
		// Unicode space separators (Zs: NBSP, the en/em spaces, the ideographic
		// space...), and the format category (Cf: zero-width space/joiner,
		// left-to-right and right-to-left marks, the BOM) that a renderer
		// treats as zero-width rather than printing. A verification address
		// carrying one of these could visually hide or rearrange itself for an
		// operator asked to copy it, so unicode.IsControl and unicode.IsSpace
		// plus the Cf format category are rejected alongside the ASCII check
		// rather than instead of it.
		if r <= ' ' || r == 0x7f || unicode.IsControl(r) || unicode.IsSpace(r) || unicode.In(r, unicode.Cf) {
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
