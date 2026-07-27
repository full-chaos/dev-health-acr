package sidecar

import (
	"context"
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

func TestCredentialLifecycleSessionRejectsForgedSessions(t *testing.T) {
	// Given
	var zero CredentialLifecycleSession
	forged := &CredentialLifecycleSession{}
	methods := []struct {
		name string
		call func(*CredentialLifecycleSession) error
	}{
		{"load", func(s *CredentialLifecycleSession) error { _, err := s.LoadCredential(); return err }},
		{"persist", func(s *CredentialLifecycleSession) error {
			_, err := s.PersistCredential(validTestToken(1))
			return err
		}},
		{"replace", func(s *CredentialLifecycleSession) error {
			return s.ReplaceCredential(CredentialResult{}, validTestToken(1))
		}},
		{"restore", func(s *CredentialLifecycleSession) error { return s.RestoreCredential(CredentialResult{}) }},
		{"delete", func(s *CredentialLifecycleSession) error { return s.DeleteCredential() }},
		{"collect", func(s *CredentialLifecycleSession) error { _, err := s.CollectCredentialMaterial(); return err }},
		{"verify", func(s *CredentialLifecycleSession) error {
			return s.VerifyCredential(CredentialResult{}, validTestToken(1))
		}},
		{"purge one", func(s *CredentialLifecycleSession) error { return s.PurgeCredentialMaterial(CredentialResult{}) }},
		{"purge all", func(s *CredentialLifecycleSession) error { return s.PurgeAllCredentialMaterial(nil) }},
	}

	for _, session := range []*CredentialLifecycleSession{&zero, forged} {
		for _, method := range methods {
			t.Run(method.name, func(t *testing.T) {
				// When
				err := method.call(session)

				// Then
				if !errors.Is(err, ErrCredentialLifecycleSessionInvalid) {
					t.Fatalf("forged session %s error = %v, want ErrCredentialLifecycleSessionInvalid", method.name, err)
				}
			})
		}
	}
}

func TestCredentialLifecycleSessionRejectsOperationsAfterClose(t *testing.T) {
	// Given
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	methods := []struct {
		name string
		call func() error
	}{
		{"load", func() error { _, err := session.LoadCredential(); return err }},
		{"persist", func() error { _, err := session.PersistCredential(validTestToken(2)); return err }},
		{"replace", func() error { return session.ReplaceCredential(CredentialResult{}, validTestToken(2)) }},
		{"restore", func() error { return session.RestoreCredential(CredentialResult{}) }},
		{"delete", session.DeleteCredential},
		{"collect", func() error { _, err := session.CollectCredentialMaterial(); return err }},
		{"verify", func() error { return session.VerifyCredential(CredentialResult{}, validTestToken(2)) }},
		{"purge one", func() error { return session.PurgeCredentialMaterial(CredentialResult{}) }},
		{"purge all", func() error { return session.PurgeAllCredentialMaterial(nil) }},
	}

	for _, method := range methods {
		t.Run(method.name, func(t *testing.T) {
			// When
			err := method.call()

			// Then
			if !errors.Is(err, ErrCredentialLifecycleSessionClosed) {
				t.Fatalf("post-close %s error = %v, want ErrCredentialLifecycleSessionClosed", method.name, err)
			}
		})
	}
}

func TestCredentialLifecycleSessionCloseWaitsForInFlightOperation(t *testing.T) {
	// Given
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenKeyringAccountEnvironment, "agent-a")
	t.Setenv(TokenFileEnvironment, filepath.Join(t.TempDir(), "missing"))
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	originalLookup := currentKeyringLookup
	currentKeyringLookup = func(context.Context, string, string) (string, bool, error) {
		close(lookupStarted)
		<-releaseLookup
		return "", false, nil
	}
	t.Cleanup(func() { currentKeyringLookup = originalLookup })
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	operationDone := make(chan error, 1)
	go func() {
		_, loadErr := session.LoadCredential()
		operationDone <- loadErr
	}()
	<-lookupStarted
	closeDone := make(chan error, 1)
	go func() { closeDone <- session.Close() }()
	<-session.state.closeStarted

	// When
	select {
	case closeErr := <-closeDone:
		t.Fatalf("Close returned before in-flight operation finished: %v", closeErr)
	default:
	}
	close(releaseLookup)

	// Then
	if loadErr := <-operationDone; !errors.Is(loadErr, ErrCredentialMissing) {
		t.Fatalf("in-flight load error = %v, want ErrCredentialMissing", loadErr)
	}
	if closeErr := <-closeDone; closeErr != nil {
		t.Fatalf("Close error = %v", closeErr)
	}
}

func TestCredentialLifecyclePublicWrappersReportBusyAcrossMutationSurfaces(t *testing.T) {
	// Given
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	wrappers := []struct {
		name string
		call func() error
	}{
		{"load", func() error { _, err := LoadCredential(); return err }},
		{"persist", func() error { _, err := PersistCredential(""); return err }},
		{"replace", func() error { return ReplaceCredential(CredentialResult{}, "") }},
		{"restore", func() error { return RestoreCredential(CredentialResult{}) }},
		{"delete", DeleteCredential},
		{"collect", func() error { _, err := CollectCredentialMaterial(); return err }},
		{"verify", func() error { return VerifyCredential(CredentialResult{}, "") }},
		{"purge one", func() error { return PurgeCredentialMaterial(CredentialResult{}) }},
		{"purge all", func() error { return PurgeAllCredentialMaterial(nil) }},
	}

	for _, wrapper := range wrappers {
		t.Run(wrapper.name, func(t *testing.T) {
			// When
			err := wrapper.call()

			// Then
			if !errors.Is(err, errCredentialLifecycleBusy) {
				t.Fatalf("public %s error = %v, want busy", wrapper.name, err)
			}
		})
	}
}
