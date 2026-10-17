package entitlements_test

import (
	"bytes"
	"fmt"
	"github.com/full-chaos/dev-health-go/llm"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

type heldOrgLLMConfig struct {
	config entitlements.OrgLLMConfig
}

func TestOrgLLMConfigHeldInAnUnexportedFieldNeverPrintsTheAPIKey(t *testing.T) {
	const key = "sk-org-llm-canary-5b0e72d1"
	config := entitlements.OrgLLMConfig{Provider: "anthropic", Model: "m", APIKey: llm.NewSecret(key), Version: "v"}
	values := map[string]any{
		"value":      config,
		"pointer":    &config,
		"held":       heldOrgLLMConfig{config: config},
		"held ptr":   &heldOrgLLMConfig{config: config},
		"held slice": []heldOrgLLMConfig{{config: config}},
	}
	needles := []string{key, fmt.Sprintf("%x", key)}
	for name, value := range values {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			out := fmt.Sprintf(verb, value)
			for _, needle := range needles {
				if strings.Contains(out, needle) {
					t.Errorf("%s printed with %s leaks the API key", name, verb)
				}
			}
		}
		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("held", "value", value)
		slog.New(slog.NewTextHandler(&buf, nil)).Info("held", "value", value)
		for _, needle := range needles {
			if strings.Contains(buf.String(), needle) {
				t.Errorf("%s logged through slog leaks the API key", name)
			}
		}
	}
}
