package panelharness

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestLinebreakNewClientTrimsTheEdgesAndRefusesTheMiddle(t *testing.T) {
	token := testBearerToken(0x36)
	rows := []struct {
		name   string
		value  string
		accept bool
	}{
		{"trailing_lf", token + "\n", true},
		{"trailing_crlf", token + "\r\n", true},
		{"embedded_lf", token[:20] + "\n" + token[20:], false},
		{"embedded_cr", token[:20] + "\r" + token[20:], false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, err := NewClient("https://acr.example.com", row.value, time.Second)

			if row.accept && err != nil {
				t.Fatalf("NewClient refused an edge-padded credential: %v", err)
			}
			if !row.accept && err == nil {
				t.Fatal("NewClient accepted a credential with a line break inside")
			}
		})
	}
}

func TestLinebreakCustomCredentialSourceIsRefusedAtConstruction(t *testing.T) {
	token := testBearerToken(0x37)
	for name, value := range map[string]string{
		"trailing_lf":   token + "\n",
		"trailing_crlf": token + "\r\n",
		"embedded_lf":   token[:20] + "\n" + token[20:],
	} {
		source := func() (sidecar.CredentialResult, error) {
			return sidecar.CredentialResult{Token: value, Source: "test"}, nil
		}

		if _, err := NewClientWithCredentialSource("https://acr.example.com", source, time.Second); err == nil {
			t.Errorf("%s: NewClientWithCredentialSource accepted a credential with a line break", name)
		}
	}
}
