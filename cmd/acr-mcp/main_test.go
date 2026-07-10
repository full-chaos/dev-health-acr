package main

import "testing"

func TestDoctorReportsCredentialPresenceWithoutValue(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", "fcacr_super_secret")
	result := runDoctor()
	if !result.CredentialSet || result.Status != "ok" {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
	for _, check := range result.Checks {
		if check.Detail == "fcacr_super_secret" {
			t.Fatal("doctor leaked the credential")
		}
	}
}
