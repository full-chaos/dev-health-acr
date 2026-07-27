package main

import (
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
