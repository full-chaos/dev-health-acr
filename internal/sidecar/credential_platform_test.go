package sidecar

import (
	"errors"
	"testing"
)

func TestCredentialPersistenceSupportedForPlatform(t *testing.T) {
	for _, testCase := range []struct {
		goos    string
		wantErr error
	}{
		{goos: "darwin"},
		{goos: "linux"},
		{goos: "windows", wantErr: ErrCredentialPersistenceUnsupported},
		{goos: "freebsd"},
		{goos: "openbsd"},
		{goos: "js"},
		{goos: "plan9"},
	} {
		t.Run(testCase.goos, func(t *testing.T) {
			if err := credentialPersistenceSupportedFor(testCase.goos); !errors.Is(err, testCase.wantErr) {
				t.Fatalf("credentialPersistenceSupportedFor(%q) error = %v, want %v", testCase.goos, err, testCase.wantErr)
			}
		})
	}
}
