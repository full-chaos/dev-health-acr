package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

func startupLine(t *testing.T, env map[string]string) (map[string]any, string) {
	t.Helper()
	var out bytes.Buffer
	logStarting(slog.New(slog.NewJSONHandler(&out, nil)), version.Info{}, config.Config{}, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})
	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("startup line is not JSON: %v: %s", err, out.String())
	}
	return line, out.String()
}

func TestStartupLineDisclosesTheEmbedProviderState(t *testing.T) {
	off, _ := startupLine(t, nil)
	if off["embed_provider_configured"] != false || off["embed_provider"] != "" || off["embed_model"] != "" || off["embed_dimension"] != float64(0) || off["embed_key_present"] != false || off["embed_config_valid"] != true {
		t.Fatalf("unset state is not explicit: %v", off)
	}
	on, raw := startupLine(t, map[string]string{
		"ACR_CONTEXT_FABRIC_EMBED_BASE_URL":  "https://user:urlsecret@embed.example/v1",
		"ACR_CONTEXT_FABRIC_EMBED_PROVIDER":  "lmstudio",
		"ACR_CONTEXT_FABRIC_EMBED_MODEL":     "qwen3-embedding",
		"ACR_CONTEXT_FABRIC_EMBED_DIMENSION": "1024",
		"ACR_CONTEXT_FABRIC_EMBED_API_KEY":   "keysecret",
	})
	if on["embed_provider_configured"] != true || on["embed_provider"] != "lmstudio" || on["embed_model"] != "qwen3-embedding" || on["embed_dimension"] != float64(1024) || on["embed_key_present"] != true {
		t.Fatalf("configured state is not disclosed: %v", on)
	}
	for _, secret := range []string{"keysecret", "urlsecret", "embed.example"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("startup line leaks %q: %s", secret, raw)
		}
	}
}

func TestStartupLineReadsTheKeyFileAndTrimsValues(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "embed-key")
	if err := os.WriteFile(keyFile, []byte("filesecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	line, raw := startupLine(t, map[string]string{
		"ACR_CONTEXT_FABRIC_EMBED_BASE_URL":     " https://embed.example/v1 ",
		"ACR_CONTEXT_FABRIC_EMBED_PROVIDER":     "  lmstudio ",
		"ACR_CONTEXT_FABRIC_EMBED_MODEL":        " qwen3-embedding ",
		"ACR_CONTEXT_FABRIC_EMBED_DIMENSION":    " 1024 ",
		"ACR_CONTEXT_FABRIC_EMBED_API_KEY_FILE": keyFile,
	})
	if line["embed_key_present"] != true || line["embed_provider"] != "lmstudio" || line["embed_model"] != "qwen3-embedding" || line["embed_dimension"] != float64(1024) || line["embed_config_valid"] != true {
		t.Fatalf("resolved state not disclosed: %v", line)
	}
	for _, secret := range []string{"filesecret", keyFile} {
		if strings.Contains(raw, secret) {
			t.Fatalf("startup line leaks %q: %s", secret, raw)
		}
	}
	rejected, _ := startupLine(t, map[string]string{
		"ACR_CONTEXT_FABRIC_EMBED_BASE_URL":  "http://embed.example/v1",
		"ACR_CONTEXT_FABRIC_EMBED_PROVIDER":  "lmstudio",
		"ACR_CONTEXT_FABRIC_EMBED_MODEL":     "m",
		"ACR_CONTEXT_FABRIC_EMBED_DIMENSION": "8",
	})
	if rejected["embed_provider_configured"] != true || rejected["embed_config_valid"] != false {
		t.Fatalf("a rejected configuration is not flagged: %v", rejected)
	}
}

func TestStartupLineNeverCarriesFreeTextNames(t *testing.T) {
	line, raw := startupLine(t, map[string]string{
		"ACR_CONTEXT_FABRIC_EMBED_BASE_URL":  "https://embed.example/v1",
		"ACR_CONTEXT_FABRIC_EMBED_PROVIDER":  "provider secret=Bearer review-secret",
		"ACR_CONTEXT_FABRIC_EMBED_MODEL":     "sk-secret-token-12345-review-secret",
		"ACR_CONTEXT_FABRIC_EMBED_DIMENSION": "8",
		"ACR_CONTEXT_FABRIC_EMBED_API_KEY":   "k",
	})
	if strings.Contains(raw, "review-secret") || line["embed_provider"] != "unlisted" || line["embed_model"] != "unlisted" {
		t.Fatalf("free-text names reached the startup line: %s", raw)
	}
	hex, hexRaw := startupLine(t, map[string]string{
		"ACR_CONTEXT_FABRIC_EMBED_BASE_URL":  "https://embed.example/v1",
		"ACR_CONTEXT_FABRIC_EMBED_PROVIDER":  "lmstudio",
		"ACR_CONTEXT_FABRIC_EMBED_MODEL":     "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"ACR_CONTEXT_FABRIC_EMBED_DIMENSION": "8",
		"ACR_CONTEXT_FABRIC_EMBED_API_KEY":   "k",
	})
	if hex["embed_model"] != "unlisted" || hex["embed_model_configured"] != true || strings.Contains(hexRaw, "9f86d081") {
		t.Fatalf("a token-shaped model reached the startup line: %s", hexRaw)
	}
	ok, _ := startupLine(t, map[string]string{
		"ACR_CONTEXT_FABRIC_EMBED_BASE_URL":  "https://embed.example/v1",
		"ACR_CONTEXT_FABRIC_EMBED_PROVIDER":  "lmstudio",
		"ACR_CONTEXT_FABRIC_EMBED_MODEL":     "qwen3-embedding",
		"ACR_CONTEXT_FABRIC_EMBED_DIMENSION": "1024",
		"ACR_CONTEXT_FABRIC_EMBED_API_KEY":   "k",
	})
	if ok["embed_model"] != "qwen3-embedding" {
		t.Fatalf("a real model name was redacted: %v", ok["embed_model"])
	}
}
