package sidecar

import (
	"errors"
	"testing"
)

func TestCredentialLifecycleSessionRejectsConcurrentSessions(t *testing.T) {
	first, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := BeginCredentialLifecycleSession()
	if second != nil || !errors.Is(err, errCredentialLifecycleBusy) {
		t.Fatalf("second session = %v, %v; want busy", second, err)
	}
}

func TestCredentialLifecycleSessionMethodsDoNotReenterTheLock(t *testing.T) {
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenFileEnvironment, t.TempDir()+"/missing")
	if _, err := session.LoadCredential(); !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("session load = %v, want missing credential", err)
	}
}
