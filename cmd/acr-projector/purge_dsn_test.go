package main

import (
	"errors"
	"os"
	"testing"
)

// The purge refuses to run without its own DSN, before it reads any other
// configuration, with a typed error that names the variable.
func TestPurgeRefusesWithoutItsOwnDSN(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"unset":                {},
		"blank":                {"ACR_POSTGRES_PURGE_DSN": "   "},
		"runtime DSN set only": {"ACR_POSTGRES_DSN": "postgres://runtime:pw@db/acr"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"ACR_POSTGRES_PURGE_DSN", "ACR_POSTGRES_PURGE_DSN_FILE"} {
				t.Setenv(key, "")
				os.Unsetenv(key)
			}
			for key, value := range env {
				t.Setenv(key, value)
			}
			err := purge([]string{"--org", "0f7c2d4e-8a31-4b6d-9c15-2e7a6b9d1f03", "--dry-run"})
			if !errors.Is(err, errPurgeDSNRequired) {
				t.Fatalf("err = %v, want errPurgeDSNRequired", err)
			}
			if got := err.Error(); !contains(got, "ACR_POSTGRES_PURGE_DSN") {
				t.Fatalf("the refusal does not name the variable: %q", got)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
