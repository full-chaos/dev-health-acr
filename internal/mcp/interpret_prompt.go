package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	promptInterpretQuestion = "interpret_question"
	interpretQuestionArg    = "question"
	interpretPromptMaxBytes = genkitruntime.DefaultExchangeMaxInputBytes
)

// interpretPromptMeta is the _meta block of an interpret_question result.
func interpretPromptMeta(system, serviceVersion string) mcpsdk.Meta {
	sum := sha256.Sum256([]byte(system))
	return mcpsdk.Meta{
		"prompt_version":       genkitruntime.DefaultInterpretationPromptVersion,
		"model_output_version": genkitruntime.DefaultSchemaVersion,
		"system_sha256":        hex.EncodeToString(sum[:]),
		"service_version":      serviceVersion,
	}
}

// registerInterpretPrompt serves the interpretation system message the server
// itself sends, taken from the one assembly in genkitruntime. It registers
// only with investigate_question, so a credential that cannot investigate
// cannot read the prompt.
func registerInterpretPrompt(server *mcpsdk.Server, caller *CallerContext, serviceVersion string) {
	if !hostedToolEnabled(caller, toolInvestigateQuestion) {
		return
	}
	meta := interpretPromptMeta(genkitruntime.InterpretationSystemPrompt(), serviceVersion)
	server.AddPrompt(&mcpsdk.Prompt{
		Name:  promptInterpretQuestion,
		Title: "Interpret a question",
		Description: "Message 1 is the interpretation system message acr runs on a question, byte for byte; message 2 carries the question. " +
			"Run it on your own model; the reply is the interpretation object. Versions: prompt " +
			metaString(meta, "prompt_version") + ", model output " + metaString(meta, "model_output_version") +
			", system sha256 " + metaString(meta, "system_sha256") + ", service " + serviceVersion + ".",
		Arguments: []*mcpsdk.PromptArgument{{
			Name: interpretQuestionArg, Title: "Question", Required: true,
			Description: "The engineering question to interpret, in plain words.",
		}},
		Meta: meta,
	}, func(_ context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
		var question string
		if req != nil && req.Params != nil {
			question = req.Params.Arguments[interpretQuestionArg]
		}
		return interpretPromptResult(question, serviceVersion)
	})
}

func metaString(m mcpsdk.Meta, key string) string {
	s, _ := m[key].(string)
	return s
}

func interpretPromptResult(question, serviceVersion string) (*mcpsdk.GetPromptResult, error) {
	if strings.TrimSpace(question) == "" {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "question is required"}
	}
	user, err := genkitruntime.BuildInterpretationPrompt(contextfabric.InvestigationRequest{
		Question:    question,
		TimeContext: contextfabric.TimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
	}, interpretPromptMaxBytes)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "question is too large"}
	}
	system := genkitruntime.InterpretationSystemPrompt()
	return &mcpsdk.GetPromptResult{
		Description: "Interpretation prompt " + genkitruntime.DefaultInterpretationPromptVersion,
		Meta:        interpretPromptMeta(system, serviceVersion),
		Messages: []*mcpsdk.PromptMessage{
			{Role: "user", Content: &mcpsdk.TextContent{Text: system}},
			{Role: "user", Content: &mcpsdk.TextContent{Text: user}},
		},
	}, nil
}
