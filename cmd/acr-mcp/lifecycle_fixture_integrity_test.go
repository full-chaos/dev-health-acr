package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
//
// The stub is asserted by its named sentinel error, not by a bare nil return:
// TestMain used to install `func(string) error { return nil }`, which reads
// identically whether the stub ran or a real opener launched and happened to
// succeed. Deleting the stub line entirely left this test passing on macOS --
// `open` resolves, launches Safari, and returns nil -- so a nil check alone is
// not a guard against the stub being absent.
func TestPackageDefaultsKeepEveryTestFromLaunchingABrowser(t *testing.T) {
	err := lifecycleBrowserOpen("https://acr.example.com/device")
	if !errors.Is(err, errLifecycleBrowserStub) {
		t.Fatalf("lifecycleBrowserOpen returned %v, want the inert stub's sentinel error; a nil or different error means this call did not reach the test stub", err)
	}
	// A real opener would have had to resolve a binary. The stub cannot fail
	// with anything but its own sentinel and cannot launch, which is exactly the
	// property being pinned: this call would otherwise attempt a desktop launch
	// right here.
}

// TestMain's stub replaces the package variable before the first test runs, so
// every `original := lifecycleBrowserOpen` a test saves to restore later only
// ever captures that stub -- it cannot by itself prove production still wires
// a real opener. This asserts the wiring lifecycle.go actually ships:
// `lifecycleBrowserOpen = sidecar.OpenVerificationURI` at package init, before
// TestMain's replacement runs, captured into productionLifecycleBrowserOpen
// specifically so this test has something real to compare against.
func TestPackageDefaultsWireTheRealBrowserOpenerInProduction(t *testing.T) {
	want := reflect.ValueOf(sidecar.OpenVerificationURI).Pointer()
	got := reflect.ValueOf(productionLifecycleBrowserOpen).Pointer()
	if got != want {
		t.Fatal("lifecycleBrowserOpen's production value is not sidecar.OpenVerificationURI; login would never launch a real browser")
	}
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

// decodeStrictLifecycleFixtureRequest is what stands between every lifecycle
// fixture and silently accepting a client that got the wire contract wrong.
// This exercises it directly against httptest.NewRecorder with a fresh,
// unregistered *lifecycleFixtureState -- not one wired through
// registerLifecycleFixture, whose t.Cleanup would otherwise turn every
// recorded problem below (which this test deliberately provokes) into a
// spurious test failure of its own -- so only the HTTP status this function
// actually wrote is asserted.
//
// contractsv1.DeviceTokenRequest is used rather than DeviceAuthorizationRequest
// because the latter has its own custom UnmarshalJSON that already calls
// DisallowUnknownFields internally (device_types.go), which would make the
// unknown-field case below pass even if this helper's own check were
// deleted. DeviceTokenRequest has no such method, so it is decoded purely by
// this helper -- exactly the case a mutation to this helper needs to be able
// to break.
func TestDecodeStrictLifecycleFixtureRequestRejectsAMalformedDeviceTokenRequest(t *testing.T) {
	validDeviceCode := strings.Repeat("d", 32)
	cases := []struct {
		name        string
		method      string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "wrong method", method: http.MethodGet, contentType: "application/json", body: `{"schema_version":"device_token_request.v1","grant_type":"urn:ietf:params:oauth:grant-type:device_code","device_code":"` + validDeviceCode + `"}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "wrong content type", method: http.MethodPost, contentType: "text/plain", body: `{"schema_version":"device_token_request.v1","grant_type":"urn:ietf:params:oauth:grant-type:device_code","device_code":"` + validDeviceCode + `"}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "unknown field", method: http.MethodPost, contentType: "application/json", body: `{"schema_version":"device_token_request.v1","grant_type":"urn:ietf:params:oauth:grant-type:device_code","device_code":"` + validDeviceCode + `","unexpected_field":true}`, wantStatus: http.StatusBadRequest},
		{name: "missing device_code fails Validate", method: http.MethodPost, contentType: "application/json", body: `{"schema_version":"device_token_request.v1","grant_type":"urn:ietf:params:oauth:grant-type:device_code"}`, wantStatus: http.StatusBadRequest},
		{name: "trailing JSON value", method: http.MethodPost, contentType: "application/json", body: `{"schema_version":"device_token_request.v1","grant_type":"urn:ietf:params:oauth:grant-type:device_code","device_code":"` + validDeviceCode + `"}{}`, wantStatus: http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			state := &lifecycleFixtureState{}
			request := httptest.NewRequest(testCase.method, "/api/v1/oauth/token", strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", testCase.contentType)
			recorder := httptest.NewRecorder()
			var decoded contractsv1.DeviceTokenRequest

			// When
			ok := decodeStrictLifecycleFixtureRequest(t, state, recorder, request, &decoded)

			// Then
			if ok {
				t.Fatalf("decodeStrictLifecycleFixtureRequest accepted a %s request, want a refusal", testCase.name)
			}
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d for %s", recorder.Code, testCase.wantStatus, testCase.name)
			}
		})
	}
}
