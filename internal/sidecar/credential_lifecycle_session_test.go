package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
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

func TestCredentialLifecyclePublicReaderReportsBusyDuringSession(t *testing.T) {
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := LoadCredential(); !errors.Is(err, errCredentialLifecycleBusy) {
		t.Fatalf("public load = %v, want busy", err)
	}
}

func TestCredentialLifecycleSessionVerifyDoesNotReenterTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	token := validTestToken(83)
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, path)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}

	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.VerifyCredential(current, token); err != nil {
		t.Fatalf("session verify = %v, want success", err)
	}
}

func TestCredentialLifecycleSessionCloseIsConcurrentSafe(t *testing.T) {
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	const closers = 8
	errs := make(chan error, closers)
	var group sync.WaitGroup
	for range closers {
		group.Go(func() {
			errs <- session.Close()
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent close = %v", err)
		}
	}

	next, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatalf("session after concurrent close = %v", err)
	}
	defer next.Close()
}
