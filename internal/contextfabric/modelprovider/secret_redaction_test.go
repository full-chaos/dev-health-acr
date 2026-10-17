package modelprovider

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-go/llm"
)

type heldClients struct {
	clients genkitruntime.LLMClients
}

type heldConfig struct {
	config llm.Config
}

func TestModelClientsNeverPrintTheAPIKey(t *testing.T) {
	const key = "sk-redaction-canary-7f3a91c2d8e4"
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x"}
	for _, provider := range []string{"openai", "anthropic", "gemini", "qwen"} {
		t.Run(provider, func(t *testing.T) {
			cfg := Config{Provider: provider, Model: "m1", APIKey: key, BaseURL: "https://example.invalid"}
			clients, err := newLLMClients(cfg, "m1")
			if err != nil {
				t.Fatalf("newLLMClients: %v", err)
			}
			runtime, err := genkitruntime.NewWithLLM(runtimeConfig(cfg, "m1", nil), clients)
			if err != nil {
				t.Fatalf("NewWithLLM: %v", err)
			}
			values := map[string]any{
				"clients":      clients,
				"provider":     clients["m1"],
				"held clients": heldClients{clients: clients},
				"held ptr":     &heldClients{clients: clients},
				"runtime":      runtime,
				"runtime val":  *runtime,
				"llm config":   heldConfig{config: llm.Config{Kind: llm.KindLocal, APIKey: llm.NewSecret(key), Model: "m1"}},
			}
			needles := []string{key, fmt.Sprintf("%x", key)}
			for name, value := range values {
				for _, verb := range verbs {
					out := fmt.Sprintf(verb, value)
					for _, needle := range needles {
						if strings.Contains(out, needle) {
							t.Errorf("%s printed with %s leaks the API key", name, verb)
						}
					}
				}
				var buf bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&buf, nil))
				logger.Info("held", "value", value)
				logger.Info("held", slog.Any("any", value), slog.String("str", fmt.Sprint(value)))
				textBuf := &bytes.Buffer{}
				slog.New(slog.NewTextHandler(textBuf, nil)).Info("held", "value", value)
				for _, needle := range needles {
					if strings.Contains(buf.String(), needle) || strings.Contains(textBuf.String(), needle) {
						t.Errorf("%s logged through slog leaks the API key", name)
					}
				}
			}
		})
	}
}
