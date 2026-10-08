package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fileCredentialEnvironment(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	token := validTestToken(61)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, path)
	return token
}

func setSharedWait(t *testing.T, wait time.Duration) {
	t.Helper()
	original := credentialLifecycleSharedWait
	credentialLifecycleSharedWait = wait
	t.Cleanup(func() { credentialLifecycleSharedWait = original })
}

func TestLoadCredentialFromEnvironmentTakesNoLockDuringMutation(t *testing.T) {
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	t.Setenv(TokenEnvironment, envToken)

	result, err := LoadCredential()
	if err != nil || result.Token != envToken || result.Source != "environment" {
		t.Fatalf("load during mutation = %+v, %v; want the environment token", result, err)
	}
}

func TestLoadCredentialFromFileWaitsForMutationToFinish(t *testing.T) {
	token := fileCredentialEnvironment(t)
	setSharedWait(t, 5*time.Second)
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = session.Close()
		close(released)
	}()
	start := time.Now()

	result, err := LoadCredential()
	if err != nil || result.Token != token {
		t.Fatalf("load = %+v, %v; want the file token after the mutation ended", result, err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatalf("load returned after %s; it must have waited for the mutation", time.Since(start))
	}
	<-released
}

func TestLoadCredentialFromFileReportsTimeoutBeyondTheBound(t *testing.T) {
	fileCredentialEnvironment(t)
	setSharedWait(t, 100*time.Millisecond)
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = LoadCredential()
	if !errors.Is(err, ErrCredentialLifecycleWaitTimeout) {
		t.Fatalf("load = %v, want the wait timeout", err)
	}
	if errors.Is(err, ErrCredentialLifecycleBusy) {
		t.Fatal("a bounded wait that expired must not read as an immediate busy refusal")
	}
}

func TestLoadCredentialReadersDoNotExcludeEachOther(t *testing.T) {
	fileCredentialEnvironment(t)
	setSharedWait(t, 100*time.Millisecond)
	release, err := acquireSharedCredentialLifecycle(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if _, err := LoadCredential(); err != nil {
		t.Fatalf("second reader = %v, want success while another reader is active", err)
	}
}

func TestMutationRefusesWhileAReaderIsActive(t *testing.T) {
	release, err := acquireSharedCredentialLifecycle(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	session, err := BeginCredentialLifecycleSession()
	if session != nil || !errors.Is(err, ErrCredentialLifecycleBusy) {
		t.Fatalf("mutation = %v, %v; want busy while a reader is active", session, err)
	}
}
