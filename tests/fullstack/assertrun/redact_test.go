package main

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantContain string
		wantAbsent  []string
	}{
		{
			name:        "acr credential token",
			in:          "token=fcacr_ab12CD34-ok_",
			wantContain: "REDACTED",
			wantAbsent:  []string{"fcacr_ab12CD34"},
		},
		{
			name:        "ops credential token",
			in:          "operator svc_acr_deadbeef123 issued the credential",
			wantContain: "REDACTED",
			wantAbsent:  []string{"svc_acr_deadbeef123"},
		},
		{
			name:        "postgres DSN",
			in:          "connecting to postgres://user:pass@localhost:5432/acr",
			wantContain: "REDACTED_DSN",
			wantAbsent:  []string{"user:pass", "5432/acr"},
		},
		{
			name:        "postgresql DSN scheme",
			in:          "postgresql://u:p@host/db failed",
			wantContain: "REDACTED_DSN",
			wantAbsent:  []string{"u:p@host"},
		},
		{
			name:        "clickhouse DSN",
			in:          "dsn=clickhouse://default:ch@clickhouse:9000/acr_e2e",
			wantContain: "REDACTED_DSN",
			wantAbsent:  []string{"default:ch@clickhouse"},
		},
		{
			name:        "authorization header transport form",
			in:          "Authorization: Bearer fcacr_secrettoken",
			wantContain: "Authorization: REDACTED",
			wantAbsent:  []string{"secrettoken"},
		},
		{
			name:        "authorization JSON field",
			in:          `{"headers":{"authorization":"Bearer abcdef123456"}}`,
			wantContain: "REDACTED",
			wantAbsent:  []string{"abcdef123456"},
		},
		{
			name:        "safe values pass through untouched",
			in:          "context_packet_id=cp_01H example-org/widget-service",
			wantContain: "example-org/widget-service",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redact(tc.in)
			if !strings.Contains(got, tc.wantContain) {
				t.Fatalf("redact(%q) = %q, want to contain %q", tc.in, got, tc.wantContain)
			}
			for _, forbidden := range tc.wantAbsent {
				if strings.Contains(got, forbidden) {
					t.Fatalf("redact(%q) = %q, must not contain %q", tc.in, got, forbidden)
				}
			}
		})
	}
}

func TestRedactBytes(t *testing.T) {
	in := []byte("svc_acr_topsecret")
	got := string(redactBytes(in))
	if strings.Contains(got, "topsecret") {
		t.Fatalf("redactBytes leaked secret: %q", got)
	}
}
