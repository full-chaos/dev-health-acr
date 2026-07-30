package main

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestDoctorDistinguishesMissingCredentialFromLoadFailures(t *testing.T) {
	t.Setenv(sidecar.APIURLEnvironment, "https://acr.example.test")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenFileEnvironment, t.TempDir()+"/missing-token")

	report := runDoctor()
	check := doctorCheck(t, report, "credential")
	if report.Status != "incomplete_configuration" || report.CredentialSet || report.CredentialShapeValid {
		t.Fatalf("unexpected missing-credential report: %#v", report)
	}
	if check.Status != "warning" || check.Detail != "ACR API credential is not configured" {
		t.Fatalf("unexpected missing-credential check: %#v", check)
	}
}

func TestDoctorDistinguishesMalformedCredentialFromLoadFailures(t *testing.T) {
	const malformed = "fcacr_doctor-malformed-canary"
	t.Setenv(sidecar.APIURLEnvironment, "https://acr.example.test")
	t.Setenv(sidecar.TokenEnvironment, malformed)

	report := runDoctor()
	check := doctorCheck(t, report, "credential")
	if report.Status != "invalid_configuration" || !report.CredentialSet || report.CredentialShapeValid {
		t.Fatalf("unexpected malformed-credential report: %#v", report)
	}
	if check.Status != "error" || check.Detail != "ACR API credential is configured but malformed" {
		t.Fatalf("unexpected malformed-credential check: %#v", check)
	}
	if strings.Contains(check.Detail, malformed) {
		t.Fatalf("doctor leaked a malformed credential: %#v", check)
	}
}

func TestDoctorReportsCredentialLifecycleContentionAsUnavailable(t *testing.T) {
	t.Setenv(sidecar.APIURLEnvironment, "https://acr.example.test")
	t.Setenv(sidecar.TokenEnvironment, "")

	session, err := sidecar.BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatalf("begin credential lifecycle session: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("close credential lifecycle session: %v", closeErr)
		}
	})

	report := runDoctor()
	check := doctorCheck(t, report, "credential")
	if report.Status != "credential_unavailable" || report.CredentialSet || report.CredentialShapeValid {
		t.Fatalf("unexpected lifecycle-busy report: %#v", report)
	}
	if check.Status != "error" || check.Detail != "ACR API credential could not be checked because another credential lifecycle operation is active" {
		t.Fatalf("unexpected lifecycle-busy check: %#v", check)
	}
	if strings.Contains(strings.ToLower(check.Detail), "not configured") {
		t.Fatalf("lifecycle contention was mislabeled as a missing credential: %#v", check)
	}
}

func TestDoctorReportsOtherCredentialLoadErrorsWithoutLeakingValues(t *testing.T) {
	const canary = "doctor-secret-canary"
	t.Setenv(sidecar.APIURLEnvironment, "https://acr.example.test")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, canary)

	report := runDoctor()
	check := doctorCheck(t, report, "credential")
	if report.Status != "credential_unavailable" || report.CredentialSet || report.CredentialShapeValid {
		t.Fatalf("unexpected credential-load-error report: %#v", report)
	}
	if check.Status != "error" || check.Detail != "ACR API credential could not be checked safely" {
		t.Fatalf("unexpected credential-load-error check: %#v", check)
	}
	for _, diagnostic := range report.Checks {
		if strings.Contains(diagnostic.Detail, canary) {
			t.Fatalf("doctor leaked a credential configuration value: %#v", diagnostic)
		}
	}
}

func doctorCheck(t *testing.T, report doctorReport, name string) diagnostic {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("doctor report does not contain %q check: %#v", name, report)
	return diagnostic{}
}
