package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// net/http recovers a handler panic at the connection boundary and drops the
// connection, so a fixture bug surfaced only as whatever the client made of a
// severed request: a transport error, a timeout, or -- on a best-effort path --
// nothing at all. The assertion that actually failed was lost, and the
// fixture's own defect read as a product failure or as a pass.
//
// The recorder is asserted directly rather than by planting a panic in a live
// fixture, because a fixture that correctly reports a panic would fail its own
// test by design.
func TestRecordFixturePanicTurnsAHandlerPanicIntoANamedProblem(t *testing.T) {
	// Given
	state := &lifecycleFixtureState{}
	recorder := httptest.NewRecorder()

	// When
	func() {
		defer recordFixturePanic(state, recorder)
		panic("scripted response index out of range")
	}()

	// Then
	state.mu.Lock()
	problems := append([]string(nil), state.problems...)
	state.mu.Unlock()
	if len(problems) != 1 {
		t.Fatalf("recorded problems = %v, want exactly one", problems)
	}
	if !strings.Contains(problems[0], "handler panicked") || !strings.Contains(problems[0], "out of range") {
		t.Fatalf("recorded problem = %q, want the panic named with its value", problems[0])
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("response status = %d, want %d so the client sees a definite failure", recorder.Code, http.StatusInternalServerError)
	}
}

// A handler that does not panic must be left completely alone: a recover-based
// guard that wrote a status unconditionally would break every fixture it was
// added to.
func TestRecordFixturePanicIsInertWithoutAPanic(t *testing.T) {
	// Given
	state := &lifecycleFixtureState{}
	recorder := httptest.NewRecorder()

	// When
	func() {
		defer recordFixturePanic(state, recorder)
		recorder.WriteHeader(http.StatusOK)
	}()

	// Then
	state.mu.Lock()
	problems := len(state.problems)
	state.mu.Unlock()
	if problems != 0 {
		t.Fatalf("recorded %d problem(s) for a handler that did not panic", problems)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("response status = %d, want the handler's own %d", recorder.Code, http.StatusOK)
	}
}

// The keychain guarantee for this package is a TestMain default, which is
// invisible at every call site that depends on it. This asserts the default is
// actually in effect, so removing it fails here rather than silently putting a
// real `security` or `secret-tool` lookup into someone's suite.
func TestPackageDefaultsKeepEveryTestAwayFromTheHostKeychain(t *testing.T) {
	if os.Getenv(sidecar.TokenKeyringDisabledEnvironment) != "true" {
		t.Fatalf("%s = %q at test start, want \"true\": compiled subprocess tests inherit this and would otherwise query the host keychain", sidecar.TokenKeyringDisabledEnvironment, os.Getenv(sidecar.TokenKeyringDisabledEnvironment))
	}
	// The flag alone is a per-test choice any test can reverse. The seam is the
	// real guarantee, and it must not be a silent one: an in-memory store would
	// answer "no entry" and let an unintended keyring access read as a pass.
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("the default keyring seam answered instead of panicking; an unintended keyring access would pass silently")
		}
		if !strings.Contains(fmt.Sprint(recovered), "without installing a stub") {
			t.Fatalf("keyring seam panic = %v, want the opt-in instruction", recovered)
		}
	}()
	_ = sidecar.ProbeKeyringSeamForTesting()
}

// No ACR_ variable exported by whoever runs the suite may reach a test. An
// exported ACR_API_URL alone gives the keyring lookup a non-empty default
// account, which turns a doctor or diagnostics test that never mentions the
// keyring into a real query against that developer's login keychain.
func TestPackageDefaultsClearAmbientACRConfiguration(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(name, "ACR_") {
			continue
		}
		if isSubprocessEntryPointMarker(name) || name == "ACR_LOCAL_INDEX_PROVIDER" || name == sidecar.TokenKeyringDisabledEnvironment {
			continue
		}
		t.Fatalf("ambient %s survived into the test process; the host's configuration would decide this suite's outcome", name)
	}
}

// The browser guarantee is the same shape of invisible default: without it an
// in-process login test that reaches the launch starts a real browser on
// whoever runs the suite.
func TestPackageDefaultsKeepEveryTestFromLaunchingABrowser(t *testing.T) {
	if err := lifecycleBrowserOpen("https://acr.example.com/device"); err != nil {
		t.Fatalf("the default in-process opener seam returned %v; it must be an inert stub", err)
	}
	// A real opener would have had to resolve a binary. The stub cannot fail
	// and cannot launch, which is exactly the property being pinned: this call
	// would otherwise attempt a desktop launch right here.
}

// TestPackageDefaultsClearAmbientACRConfiguration above inspects the process
// state, which means it can only fail on a machine that actually exports an ACR_
// variable -- a clean CI runner has nothing for it to catch. A mutation removing
// the clear from TestMain therefore survived it.
//
// This asserts the mechanism instead, so the guard holds regardless of the
// environment the suite happens to run in: a configuration variable is removed,
// and a subprocess entry-point marker is not, because clearing the marker made
// every subprocess test run the Go harness and compare its output to "PASS".
func TestClearAmbientACREnvironmentRemovesConfigurationAndKeepsEntryPointMarkers(t *testing.T) {
	// Given
	// clearAmbientACREnvironment unsets ACR_ variables process-wide, including the
	// two TestMain set for the whole package. Naming them in t.Setenv first is
	// what makes the cleanup put them back: without this the clear leaks past this
	// test, the keyring becomes enabled for whatever runs next, and under
	// -shuffle=on an unrelated doctor test panics on the seam guard. Found exactly
	// that way.
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, os.Getenv(sidecar.TokenKeyringDisabledEnvironment))
	t.Setenv("ACR_LOCAL_INDEX_PROVIDER", os.Getenv("ACR_LOCAL_INDEX_PROVIDER"))
	t.Setenv(sidecar.APIURLEnvironment, "https://ambient.example.invalid")
	t.Setenv(sidecar.TokenEnvironment, validDoctorToken(99))
	t.Setenv("ACR_MCP_FIXTURE_PROCESS", "1")
	t.Setenv("PATH", os.Getenv("PATH"))

	// When
	clearAmbientACREnvironment()

	// Then
	for _, name := range []string{sidecar.APIURLEnvironment, sidecar.TokenEnvironment} {
		if value, present := os.LookupEnv(name); present {
			t.Fatalf("%s survived as %q; ambient configuration would decide this suite's outcome", name, value)
		}
	}
	if os.Getenv("ACR_MCP_FIXTURE_PROCESS") != "1" {
		t.Fatal("a subprocess entry-point marker was cleared; every subprocess test would run the Go harness instead of the command")
	}
	if os.Getenv("PATH") == "" {
		t.Fatal("clearing removed a non-ACR variable")
	}
}
