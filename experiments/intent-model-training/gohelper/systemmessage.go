package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/firebase/genkit/go/ai"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// recorderAnswer is a minimal schema-valid interpretation so the production
// call completes; its content is irrelevant to the recorded request.
const recorderAnswer = `{"shape":"open","requested_judgment":"recorder","time_context":{"axis":"current"},"fact_requirements":[],"clarification_needed":false}`

type systemMessageOutput struct {
	Text              string           `json:"text"`
	SHA256            string           `json:"sha256"`
	Bytes             int              `json:"bytes"`
	Parts             int              `json:"parts"`
	PromptSHA256      string           `json:"prompt_sha256"`
	InstructionSHA256 string           `json:"instruction_sha256"`
	InstructionBytes  int              `json:"instruction_bytes"`
	UserContent       string           `json:"-"`
	Request           *ai.ModelRequest `json:"-"`
}

// recordProductionModelRequest runs the production interpretation call
// (genkitruntime.Runtime.InterpretQuestion -> sdkGenerator.Interpret ->
// genkit.GenerateData[interpretationOutput] with WithSystem, WithPrompt,
// WithCustomConstrainedOutput and the seed config) against the in-process
// recorder model (genkitharness.go). genkit's own code builds and injects
// the output instruction.
func recordProductionModelRequest(request contextfabric.InvestigationRequest) (systemMessageOutput, error) {
	harness, err := sharedHarness()
	if err != nil {
		return systemMessageOutput{}, err
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	harness.begin(recorderAnswer)
	principal := storage.Principal{OrgID: "offline-helper", Subject: "offline-helper"}
	if _, _, err := harness.runtime.InterpretQuestion(context.Background(), principal, request); err != nil {
		return systemMessageOutput{}, fmt.Errorf("production interpret call: %w", err)
	}
	if harness.lastRequest == nil {
		return systemMessageOutput{}, errors.New("recorder model was not called")
	}
	return summarizeModelRequest(harness.lastRequest)
}

// summarizeModelRequest reduces the recorded request to the system text an
// OpenAI-compatible endpoint receives. compat_oai sends a system message
// as the concatenation of its parts' Text with no separator
// (plugins/compat_oai/generate.go concatenateContent); the differential
// test proves this equals the HTTP body compat_oai actually sends.
func summarizeModelRequest(req *ai.ModelRequest) (systemMessageOutput, error) {
	var system, user *ai.Message
	for _, message := range req.Messages {
		switch message.Role {
		case ai.RoleSystem:
			if system != nil {
				return systemMessageOutput{}, errors.New("recorded request has more than one system message")
			}
			system = message
		case ai.RoleUser:
			if user != nil {
				return systemMessageOutput{}, errors.New("recorded request has more than one user message")
			}
			user = message
		default:
			return systemMessageOutput{}, fmt.Errorf("recorded request has an unexpected %q message", message.Role)
		}
	}
	if system == nil || user == nil {
		return systemMessageOutput{}, errors.New("recorded request lacks a system or user message")
	}
	var text, instruction strings.Builder
	for _, part := range system.Content {
		if !part.IsText() {
			return systemMessageOutput{}, errors.New("recorded system message has a non-text part")
		}
		text.WriteString(part.Text)
		if part.Metadata != nil && part.Metadata["purpose"] == "output" {
			instruction.WriteString(part.Text)
		}
	}
	var userText strings.Builder
	for _, part := range user.Content {
		if part.IsText() {
			userText.WriteString(part.Text)
		}
	}
	prompt := genkitruntime.InterpretationSystemPrompt()
	full := text.String()
	if !strings.HasPrefix(full, prompt) {
		return systemMessageOutput{}, errors.New("recorded system message does not start with the interpretation system prompt")
	}
	return systemMessageOutput{
		Text:              full,
		SHA256:            sha256Hex([]byte(full)),
		Bytes:             len(full),
		Parts:             len(system.Content),
		PromptSHA256:      sha256Hex([]byte(prompt)),
		InstructionSHA256: sha256Hex([]byte(instruction.String())),
		InstructionBytes:  instruction.Len(),
		UserContent:       userText.String(),
		Request:           req,
	}, nil
}

// systemMessageRequest is a fixed fictional request. The system message
// does not depend on the request; the test checks that across requests.
const systemMessageRequest = `{"question":"How is the Harbor Lights team doing?","time_context":{"axis":"current"}}`

func systemMessageInfo() (systemMessageOutput, error) {
	decoded, err := decodeRequest([]byte(systemMessageRequest))
	if err != nil {
		return systemMessageOutput{}, err
	}
	return recordProductionModelRequest(decoded.Request)
}
