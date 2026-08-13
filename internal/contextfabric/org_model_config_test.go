package contextfabric

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func sampleResolvedOrgModelConfig() ResolvedOrgModelConfig {
	return ResolvedOrgModelConfig{
		Provider:      "acme-gateway",
		BaseURL:       "https://llm.acme-gateway.example/v1/",
		Model:         "acme-large",
		FallbackModel: "acme-large-fallback",
		Credential:    "sk-acme-live-super-secret-value",
		Generation:    42,
		UpdatedAt:     time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
	}
}

// TestResolvedOrgModelConfig_redactsCredentialAcrossEveryFormatVerb is the
// Codex round-1 F7 probe, permanently locked: %v, %s, %+v, and %#v must ALL
// redact Credential. Before adding GoString, %#v bypassed String/LogValue/
// MarshalJSON entirely -- fmt's GoStringer interface is a distinct seam
// from Stringer, and Go's default %#v rendering of a struct with no
// GoString method prints every exported field verbatim, Credential
// included.
func TestResolvedOrgModelConfig_redactsCredentialAcrossEveryFormatVerb(t *testing.T) {
	config := sampleResolvedOrgModelConfig()
	cases := map[string]string{
		"%v":  fmt.Sprintf("%v", config),
		"%s":  fmt.Sprintf("%s", config),
		"%+v": fmt.Sprintf("%+v", config),
		"%#v": fmt.Sprintf("%#v", config),
	}
	for verb, rendered := range cases {
		if strings.Contains(rendered, config.Credential) {
			t.Fatalf("%s rendering leaked the credential: %s", verb, rendered)
		}
		if !strings.Contains(rendered, redactedCredentialPlaceholder) {
			t.Fatalf("%s rendering did not contain the redaction placeholder: %s", verb, rendered)
		}
	}
}

func TestResolvedOrgModelConfig_LogValue_redactsCredential(t *testing.T) {
	config := sampleResolvedOrgModelConfig()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("resolved org model config", "config", config)
	output := buf.String()
	if strings.Contains(output, config.Credential) {
		t.Fatalf("slog output leaked the credential: %s", output)
	}
	if !strings.Contains(output, redactedCredentialPlaceholder) {
		t.Fatalf("slog output did not contain the redaction placeholder: %s", output)
	}
}

func TestResolvedOrgModelConfig_MarshalJSON_redactsCredential(t *testing.T) {
	config := sampleResolvedOrgModelConfig()
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if bytes.Contains(encoded, []byte(config.Credential)) {
		t.Fatalf("json.Marshal leaked the credential: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(redactedCredentialPlaceholder)) {
		t.Fatalf("json.Marshal output did not contain the redaction placeholder: %s", encoded)
	}
	// Belt-and-suspenders: the Credential field's own json:"-" tag means a
	// caller marshaling the RAW struct fields (bypassing MarshalJSON is not
	// actually possible from outside the package, but this proves the tag
	// itself is correctly placed) never emits a literal "credential" key
	// pointing at the plaintext value either.
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded["credential"] != redactedCredentialPlaceholder {
		t.Fatalf(`decoded["credential"] = %v, want %q`, decoded["credential"], redactedCredentialPlaceholder)
	}
}
