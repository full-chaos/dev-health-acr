//go:build darwin || linux

package sidecar

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// keyringBackendFixture installs a fake secret-store binary under `name` whose
// exit code and stderr are driven by the environment. It never touches a host
// keychain: resolution goes through the injected executable resolver seam, so
// the process PATH is irrelevant and no real backend is ever started.
func keyringBackendFixture(t *testing.T, name string, exitCode int, diagnostic string) {
	t.Helper()
	requireSh(t)
	script := filepath.Join(t.TempDir(), name)
	contents := "#!/bin/sh\nif [ -n \"$KEYRING_FIXTURE_STDERR\" ]; then printf '%s\\n' \"$KEYRING_FIXTURE_STDERR\" >&2; fi\nexit $KEYRING_FIXTURE_EXIT\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	injectExecutableResolver(t, name, script)
	t.Setenv("KEYRING_FIXTURE_STDERR", diagnostic)
	t.Setenv("KEYRING_FIXTURE_EXIT", strconv.Itoa(exitCode))
}

// secret-tool exits 1 both for a genuine miss and for an operational failure
// such as an unreachable D-Bus session; only the diagnostic on stderr tells
// them apart. Reading every exit 1 as absence let an unusable keyring look
// like an empty one, so a stale token file could quietly win instead.
func TestKeyringLookupSeparatesEntryAbsenceFromOperationalFailure(t *testing.T) {
	cases := []struct {
		name       string
		backend    string
		exitCode   int
		diagnostic string
		wantErr    bool
	}{
		{name: "secret-tool miss", backend: "secret-tool", exitCode: 1},
		{name: "secret-tool diagnostic", backend: "secret-tool", exitCode: 1, diagnostic: "Cannot autolaunch D-Bus without X11 $DISPLAY", wantErr: true},
		{name: "secret-tool other failure", backend: "secret-tool", exitCode: 2, wantErr: true},
		{name: "security miss", backend: "security", exitCode: 44},
		{name: "security diagnostic", backend: "security", exitCode: 1, diagnostic: "User interaction is not allowed.", wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			keyringBackendFixture(t, testCase.backend, testCase.exitCode, testCase.diagnostic)

			// When
			token, ok, err := runKeyringCommand(context.Background(), testCase.backend, "lookup", "service", defaultKeyringService, "account", "agent-a")

			// Then
			if testCase.wantErr {
				if err == nil {
					t.Fatal("an operational keyring failure was reported as entry absence, which permits a stale fallback file")
				}
				return
			}
			if err != nil {
				t.Fatalf("entry absence was reported as a failure: %v", err)
			}
			if ok || token != "" {
				t.Fatalf("lookup miss returned ok=%v token=%q, want an empty miss", ok, token)
			}
		})
	}
}

// Deleting an entry that is already gone is the goal state, so a clear miss
// must stay idempotent -- but only when the backend actually reported absence.
func TestKeyringDeleteIsIdempotentOnMissAndFailsClosedOnDiagnostic(t *testing.T) {
	cases := []struct {
		name       string
		backend    string
		clearArgs  []string
		exitCode   int
		diagnostic string
		wantErr    bool
	}{
		{name: "secret-tool clear miss", backend: "secret-tool", clearArgs: []string{"clear", "service", defaultKeyringService, "account", "agent-a"}, exitCode: 1},
		{name: "secret-tool clear diagnostic", backend: "secret-tool", clearArgs: []string{"clear", "service", defaultKeyringService, "account", "agent-a"}, exitCode: 1, diagnostic: "Cannot autolaunch D-Bus without X11 $DISPLAY", wantErr: true},
		{name: "security delete miss", backend: "security", clearArgs: []string{"delete-generic-password", "-a", "agent-a", "-s", defaultKeyringService}, exitCode: 44},
		{name: "security delete diagnostic", backend: "security", clearArgs: []string{"delete-generic-password", "-a", "agent-a", "-s", defaultKeyringService}, exitCode: 1, diagnostic: "User interaction is not allowed.", wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			keyringBackendFixture(t, testCase.backend, testCase.exitCode, testCase.diagnostic)

			// When
			err := runKeyringMutation(context.Background(), nil, true, testCase.backend, testCase.clearArgs...)

			// Then
			if testCase.wantErr && err == nil {
				t.Fatal("an operational keyring failure was accepted as a successful removal")
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("removing an entry that was already absent failed: %v", err)
			}
		})
	}
}

// A store must never inherit the delete path's tolerance: a secret-tool store
// that exits 1 without a diagnostic wrote nothing, and reporting success there
// would hand back a keyring credential result for an entry that does not exist.
func TestKeyringStoreNeverTreatsAMissingEntryExitAsSuccess(t *testing.T) {
	// Given
	keyringBackendFixture(t, "secret-tool", 1, "")

	// When
	err := runKeyringMutation(context.Background(), nil, false, "secret-tool", "store", "service", defaultKeyringService, "account", "agent-a")

	// Then
	if err == nil {
		t.Fatal("a failed keyring store was reported as success")
	}
}

func TestKeyringExitMeansEntryMissingRequiresAnEmptyDiagnostic(t *testing.T) {
	cases := []struct {
		name     string
		backend  string
		exitCode int
		stderr   string
		want     bool
	}{
		{name: "secret-tool silent one", backend: "secret-tool", exitCode: 1, want: true},
		{name: "secret-tool whitespace only", backend: "secret-tool", exitCode: 1, stderr: " \n\t", want: true},
		{name: "secret-tool with diagnostic", backend: "secret-tool", exitCode: 1, stderr: "dbus failure"},
		{name: "secret-tool other code", backend: "secret-tool", exitCode: 2},
		{name: "security missing item", backend: "security", exitCode: 44, want: true},
		{name: "security missing item ignores stderr", backend: "security", exitCode: 44, stderr: "note", want: true},
		{name: "security other code", backend: "security", exitCode: 1},
		{name: "unknown backend", backend: "pass", exitCode: 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := keyringExitMeansEntryMissing(testCase.backend, testCase.exitCode, testCase.stderr); got != testCase.want {
				t.Fatalf("keyringExitMeansEntryMissing(%q, %d, %q) = %v, want %v", testCase.backend, testCase.exitCode, testCase.stderr, got, testCase.want)
			}
		})
	}
}
