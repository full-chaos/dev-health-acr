package contextfabric_test

import (
	"bytes"
	"fmt"
	"github.com/full-chaos/dev-health-go/llm"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

type heldResolvedOrgModelConfig struct {
	config contextfabric.ResolvedOrgModelConfig
}

func TestResolvedOrgModelConfigHeldInAnUnexportedFieldNeverPrintsTheCredential(t *testing.T) {
	const key = "sk-org-model-canary-9c41d7a3"
	config := contextfabric.ResolvedOrgModelConfig{Provider: "anthropic", Model: "m", Credential: llm.NewSecret(key), Generation: 3}
	values := map[string]any{
		"value":      config,
		"pointer":    &config,
		"held":       heldResolvedOrgModelConfig{config: config},
		"held ptr":   &heldResolvedOrgModelConfig{config: config},
		"held slice": []heldResolvedOrgModelConfig{{config: config}},
	}
	needles := []string{key, fmt.Sprintf("%x", key)}
	for name, value := range values {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			out := fmt.Sprintf(verb, value)
			for _, needle := range needles {
				if strings.Contains(out, needle) {
					t.Errorf("%s printed with %s leaks the credential", name, verb)
				}
			}
		}
		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("held", "value", value)
		slog.New(slog.NewTextHandler(&buf, nil)).Info("held", "value", value)
		for _, needle := range needles {
			if strings.Contains(buf.String(), needle) {
				t.Errorf("%s logged through slog leaks the credential", name)
			}
		}
	}
}
