//go:build !darwin && !linux

package sidecar

import (
	"errors"
	"testing"
)

// On every platform other than darwin and linux, openNoFollowNonBlocking
// refuses every bounded local file read unconditionally (see
// boundedfile_unsupported.go) -- this sidecar can never have written,
// verified, or deleted a credential file there either
// (credential_persistence_other.go). loadCredentialFile must therefore treat
// that refusal as ErrCredentialMissing, not as an unexplained failure:
// otherwise CollectCredentialMaterial aborts its entire enumeration -- and
// logout with it -- over a file location this sidecar could never have
// populated, even when the operator's only configured credential is the
// environment variable.
//
// This file cannot run on this repository's darwin/linux development and CI
// hosts; it exists so a Windows build of this test binary exercises the path
// the //go:build tag above targets.
func TestLoadFromFileTreatsUnsupportedPlatformAsMissingNotAnAbort(t *testing.T) {
	t.Setenv(TokenFileEnvironment, "")
	_, err := loadFromFile()
	if err == nil {
		t.Fatal("loadFromFile succeeded with no configured or default token file; want ErrCredentialMissing or a platform-unsupported path")
	}
	if !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("loadFromFile error = %v, want it to satisfy errors.Is(err, ErrCredentialMissing) on a platform where bounded file reads are structurally unsupported", err)
	}
}

func TestLoadCredentialFileMapsPlatformUnsupportedToMissing(t *testing.T) {
	_, err := loadCredentialFile("C:\\Users\\example\\.acr\\token")
	if !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("loadCredentialFile error = %v, want errors.Is(err, ErrCredentialMissing); the platform-unsupported bounded-read error must not abort enumeration", err)
	}
	if !errors.Is(err, ErrBoundedFileReadsUnsupported) {
		t.Fatalf("loadCredentialFile error = %v, want it to also still satisfy errors.Is(err, ErrBoundedFileReadsUnsupported), so a caller that cares about the distinction can still observe it", err)
	}
}
