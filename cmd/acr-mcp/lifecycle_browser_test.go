package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// newVerificationAddressServer issues a device authorization carrying exactly
// the supplied verification address and then answers every poll with a denial,
// so a test can assert what login did with that address without also having to
// script a full successful redemption.
func newVerificationAddressServer(t *testing.T, uri string) *httptest.Server {
	t.Helper()
	state := registerLifecycleFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer recordFixturePanic(state, w)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			state.countAuthorization()
			if r.Header.Get("Authorization") != "" {
				state.recordProblem("device authorization request unexpectedly had bearer authorization")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: strings.Repeat("d", 32), UserCode: "ABCDEFGH", VerificationURI: uri, ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			if r.Header.Get("Authorization") != "" {
				state.recordProblem("device token request unexpectedly had bearer authorization")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			// Every poll is answered with a denial, so the flow ends on the first
			w.WriteHeader(http.StatusBadRequest)
			writeLifecycleJSON(t, w, contractsv1.OAuthDeviceErrorResponse{SchemaVersion: contractsv1.OAuthDeviceErrorSchema, Error: contractsv1.OAuthDeviceErrorAccessDenied})
		default:
			state.recordProblem("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func withRecordedBrowserOpen(t *testing.T) *[]string {
	t.Helper()
	opened := []string{}
	original := lifecycleBrowserOpen
	lifecycleBrowserOpen = func(uri string) error {
		opened = append(opened, uri)
		return nil
	}
	t.Cleanup(func() { lifecycleBrowserOpen = original })
	return &opened
}

// withImmediateDevicePoll removes the fixture's five-second poll interval from
// the wall clock while leaving the grant context untouched, so a test's own
// duration never approaches a real timeout constant and cannot be mistaken for
// one. It replaces only the sleep: every context deadline the command builds
// is still the one production builds, and a cancelled context is still
// reported as cancelled.
func withImmediateDevicePoll(t *testing.T) {
	t.Helper()
	original := lifecycleWait
	lifecycleWait = func(ctx context.Context, _ time.Duration) error {
		return ctx.Err()
	}
	t.Cleanup(func() { lifecycleWait = original })
}

// captureLifecycleOutput redirects both standard streams for one command and
// returns accessors that read what was written. Both are needed together: the
// verification address is printed on stdout and every refusal is reported on
// stderr, so a test that captured only one could not tell "refused before
// printing" apart from "printed, then refused".
func captureLifecycleOutput(t *testing.T) (func() string, func() string) {
	t.Helper()
	return captureStream(t, &os.Stdout), captureStream(t, &os.Stderr)
}

func captureStream(t *testing.T, stream **os.File) func() string {
	t.Helper()
	original := *stream
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	*stream = writer
	t.Cleanup(func() { *stream = original })
	captured := ""
	read := false
	return func() string {
		if read {
			return captured
		}
		read = true
		*stream = original
		_ = writer.Close()
		contents, readErr := io.ReadAll(reader)
		if readErr != nil {
			t.Fatalf("read captured stream: %v", readErr)
		}
		_ = reader.Close()
		captured = string(contents)
		return captured
	}
}

func loginTestEnvironment(t *testing.T, serverURL string) {
	t.Helper()
	withImmediateDevicePoll(t)
	t.Setenv(sidecar.APIURLEnvironment, serverURL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
}

// --no-browser exists so a headless host, a remote shell, or a compiled QA run
// can complete a device login against a real, safe verification address without
// a desktop opener ever being resolved or executed. It must suppress only the
// launch: the address and code are still printed, so the operator can still
// complete the flow.
func TestLoginPrintsTheVerificationAddressAndSkipsTheLaunch_whenNoBrowserIsRequested(t *testing.T) {
	// Given
	server := newVerificationAddressServer(t, deviceVerificationURI)
	loginTestEnvironment(t, server.URL)
	opened := withRecordedBrowserOpen(t)
	stdout, stderr := captureLifecycleOutput(t)

	// When
	code := runCLI([]string{"login", "--no-browser"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d for a denied authorization", code, lifecycleExitFailure)
	}
	if !strings.Contains(stdout(), "Open "+deviceVerificationURI+" and enter code ABCDEFGH") {
		t.Fatalf("login did not print the verification address and code: %q", stdout())
	}
	if len(*opened) != 0 {
		t.Fatalf("--no-browser still launched an opener for %v", *opened)
	}
	if !strings.Contains(stderr(), "device authorization was denied") {
		t.Fatalf("login stderr = %q, want the denial reported", stderr())
	}
}

// The default remains a best-effort launch. Without this the flag could be
// implemented by simply never opening anything, and the test above would still
// pass.
func TestLoginAttemptsTheLaunch_whenNoBrowserIsNotRequested(t *testing.T) {
	// Given
	server := newVerificationAddressServer(t, deviceVerificationURI)
	loginTestEnvironment(t, server.URL)
	opened := withRecordedBrowserOpen(t)

	// When
	runCLI([]string{"login"})

	// Then
	if len(*opened) != 1 || (*opened)[0] != deviceVerificationURI {
		t.Fatalf("default login opened %v, want exactly the verification address once", *opened)
	}
}

// The verification address is server-supplied data that login renders into an
// operator's terminal. Validating it only inside the opener left that channel
// unguarded: the address was printed regardless, ready to be copied into a
// browser by hand, and --no-browser skipped the check entirely. Nothing may be
// printed, and no opener may be consulted, for an address this client refuses.
func TestLoginRefusesAnUnsafeVerificationAddressBeforePrintingIt(t *testing.T) {
	cases := []struct {
		name string
		uri  string
	}{
		{name: "non loopback http", uri: "http://device.acr.invalid/acr/device"},
		{name: "userinfo", uri: "https://user:secret@acr.example.com/device"},
		// A control character is refused by the contract decoder before it can
		// reach this client's own check, so the whitespace case is used here: it
		// survives contract validation and still must not be printed or opened.
		{name: "embedded whitespace", uri: "https://acr.example.com/dev ice"},
		{name: "token shaped", uri: "https://acr.example.com/device?hint=" + validDoctorToken(96)},
		// The schemes a hostile server would actually choose. Each has a
		// registered local handler on some desktop, so handing one to an opener
		// is arbitrary local execution rather than a browser navigation.
		{name: "javascript scheme", uri: "javascript://acr.example.com/#alert(1)"},
		{name: "file scheme", uri: "file://localhost/etc/passwd"},
		{name: "data scheme", uri: "data://acr.example.com/text/html,<script>alert(1)</script>"},
		{name: "custom handler scheme", uri: "vscode://acr.example.com/extension/install?id=evil"},
	}
	for _, testCase := range cases {
		for _, flags := range [][]string{{"login"}, {"login", "--no-browser"}} {
			t.Run(testCase.name+"/"+strings.Join(flags, "_"), func(t *testing.T) {
				// Given
				server := newVerificationAddressServer(t, testCase.uri)
				loginTestEnvironment(t, server.URL)
				opened := withRecordedBrowserOpen(t)
				stdout, stderr := captureLifecycleOutput(t)

				// When
				code := runCLI(flags)

				// Then
				if code != lifecycleExitFailure {
					t.Fatalf("login exit code = %d, want %d for a refused verification address", code, lifecycleExitFailure)
				}
				if printed := stdout(); printed != "" {
					t.Fatalf("login printed %q for a refused verification address, want nothing", printed)
				}
				if len(*opened) != 0 {
					t.Fatalf("login opened %v for a refused verification address", *opened)
				}
				if strings.Contains(stderr(), testCase.uri) {
					t.Fatalf("login stderr echoed the refused address: %q", stderr())
				}
				if !strings.Contains(stderr(), "will not display or open") {
					t.Fatalf("login stderr = %q, want the refusal named", stderr())
				}
			})
		}
	}
}

// --refresh replaces an existing credential and never starts a device flow, so
// pairing it with --no-browser describes a launch that cannot happen. Accepting
// the combination would let an operator believe they had suppressed something.
func TestLoginRejectsNoBrowserCombinedWithRefreshAndRepeatedFlags(t *testing.T) {
	cases := [][]string{
		{"login", "--refresh", "--no-browser"},
		{"login", "--no-browser", "--refresh"},
		{"login", "--no-browser", "--no-browser"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			if code := runCLI(args); code != 2 {
				t.Fatalf("%v exit code = %d, want 2", args, code)
			}
		})
	}
}

func TestLoginUsageNamesTheNoBrowserFlag(t *testing.T) {
	if !strings.Contains(loginUsageLine, "--no-browser") {
		t.Fatalf("login usage = %q, want --no-browser documented", loginUsageLine)
	}
}
