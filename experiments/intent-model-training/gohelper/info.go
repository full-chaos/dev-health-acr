package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"runtime/debug"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

type versionOutput struct {
	ACRRevision        string `json:"acr_revision"`
	ACRDirty           bool   `json:"acr_dirty"`
	ACRRevisionKnown   bool   `json:"acr_revision_known"`
	GoVersion          string `json:"go_version"`
	SystemPromptSHA256 string `json:"system_prompt_sha256"`
	// SystemMessageSHA256 is the training SYSTEM: the prompt plus the
	// genkit-injected output instruction (see systemmessage.go).
	SystemMessageSHA256 string `json:"system_message_sha256"`
	SchemaSHA256        string `json:"schema_sha256"`
	PromptVersion       string `json:"prompt_version"`
	FrameVersion        string `json:"frame_version"`
}

type systemPromptOutput struct {
	Text   string `json:"text"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type schemaOutput struct {
	Schema json.RawMessage `json:"schema"`
	SHA256 string          `json:"sha256"`
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// versionInfo reads the VCS stamp Go embeds at build time. vcs.modified is
// true whenever the worktree had uncommitted changes at build time, which
// includes this experiment directory itself.
func versionInfo() versionOutput {
	out := versionOutput{
		GoVersion:          runtime.Version(),
		SystemPromptSHA256: systemPromptInfo().SHA256,
		PromptVersion:      genkitruntime.DefaultInterpretationPromptVersion,
		FrameVersion:       contextfabric.QuestionFrameVersion,
	}
	if schema, err := schemaInfo(); err == nil {
		out.SchemaSHA256 = schema.SHA256
	}
	if message, err := systemMessageInfo(); err == nil {
		out.SystemMessageSHA256 = message.SHA256
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				out.ACRRevision = setting.Value
				out.ACRRevisionKnown = setting.Value != ""
			case "vcs.modified":
				out.ACRDirty = setting.Value == "true"
			}
		}
	}
	return out
}

func systemPromptInfo() systemPromptOutput {
	text := genkitruntime.InterpretationSystemPrompt()
	return systemPromptOutput{Text: text, SHA256: sha256Hex([]byte(text)), Bytes: len(text)}
}

// schemaInfo returns the production output schema exactly as
// genkitruntime.InterpretationOutputSchema renders it. The digest is taken
// over those bytes, so it changes only when production's schema changes.
func schemaInfo() (schemaOutput, error) {
	raw, err := genkitruntime.InterpretationOutputSchema()
	if err != nil {
		return schemaOutput{}, err
	}
	return schemaOutput{Schema: json.RawMessage(raw), SHA256: sha256Hex(raw)}, nil
}
