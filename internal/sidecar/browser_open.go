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
)

// ErrVerificationURIUnsupported is returned when a device-flow verification
// URI is not something this sidecar will hand to a desktop opener. It carries
// no part of the rejected value: the URI is server-supplied data, and echoing
// it would put untrusted text on an operator-facing error.
var ErrVerificationURIUnsupported = errors.New("acr: verification URI is not a launchable https or loopback http address")

// maxVerificationURIBytes matches the contract bound on the device
// authorization response's verification_uri.
const maxVerificationURIBytes = 2048

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
// Three things are enforced before anything is executed. The URI must be
// https, or http to a validated loopback address (the local development and
// fixture case); every other scheme -- file, javascript, a custom handler a
// hostile server could register -- is refused, as is any URI carrying control
// characters or whitespace. The opener binary is resolved through
// currentExecutableResolver, which searches a fixed system directory allowlist
// and never the process's PATH, so a PATH entry cannot substitute an attacker's
// "open". And the child receives browserLaunchEnvironment, not this process's
// environment, so no ACR credential or client-supplied variable is inherited by
// whatever browser the opener launches.
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
	if err := command.Start(); err != nil {
		return fmt.Errorf("start browser opener: %w", err)
	}
	// The opener is expected to hand off and exit immediately. Reap it in the
	// background so a launcher that lingers cannot block login's polling loop
	// and cannot be left as a zombie either.
	go func() { _ = command.Wait() }()
	return nil
}

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
