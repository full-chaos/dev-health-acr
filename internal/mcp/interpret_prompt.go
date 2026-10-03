package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	promptInterpretQuestion = "interpret_question"
	interpretQuestionArg    = "question"
	interpretPromptMaxBytes = 512 << 10
)

// interpretPromptMeta is the _meta block of an interpret_question result.
func interpretPromptMeta(system, serviceVersion string) mcpsdk.Meta {
	sum := sha256.Sum256([]byte(system))
	return mcpsdk.Meta{
		"prompt_version":       interpretprompt.PromptVersion,
		"model_output_version": interpretprompt.OutputVersion,
		"system_sha256":        hex.EncodeToString(sum[:]),
		"service_version":      serviceVersion,
	}
}

// registerInterpretPrompt serves the interpretation system message the server
// itself sends, taken from interpretprompt, the one assembly the runtime also sends. It registers
// only with investigate_question, so a credential that cannot investigate
// cannot read the prompt.
func registerInterpretPrompt(server *mcpsdk.Server, cfg *ProcessConfig, caller *CallerContext, serviceVersion string) {
	if !hostedToolEnabled(caller, toolInvestigateQuestion) {
		return
	}
	meta := interpretPromptMeta(interpretprompt.System(), serviceVersion)
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
	}, func(ctx context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
		if ok, reason := liveToolEnabled(ctx, caller, toolInvestigateQuestion); !ok {
			logSurfaceRefusal(ctx, cfg, "prompt", promptInterpretQuestion, reason)
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown prompt"}
		}
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
	if utf8.RuneCountInString(question) > contractsv1.MCPInvestigationQuestionMaxLength {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "question is too long"}
	}
	userJSON, err := interpretprompt.UserPayload(contextfabric.InvestigationRequest{
		Question:    question,
		TimeContext: contextfabric.TimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
	}, interpretPromptMaxBytes)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "question is too large"}
	}
	user := string(userJSON)
	system := interpretprompt.System()
	return &mcpsdk.GetPromptResult{
		Description: "Interpretation prompt " + interpretprompt.PromptVersion,
		Meta:        interpretPromptMeta(system, serviceVersion),
		Messages: []*mcpsdk.PromptMessage{
			{Role: "user", Content: &mcpsdk.TextContent{Text: system}},
			{Role: "user", Content: &mcpsdk.TextContent{Text: user}},
		},
	}, nil
}

// liveToolEnabled re-reads the caller's capabilities from the hosted API, so a
// revocation after the server was built (a long-lived STDIO session) takes
// effect on the next read. It fails closed. The returned reason is a closed
// vocabulary ("ok", "no_client", "capability_check_failed", "tool_revoked")
// so an operator can tell an outage from a revocation. With several tools it
// passes when any one is enabled, from one capabilities read.
func liveToolEnabled(ctx context.Context, caller *CallerContext, tools ...string) (bool, string) {
	client := caller.Client()
	if client == nil {
		return false, "no_client"
	}
	caps, err := client.Capabilities(ctx)
	if err != nil {
		return false, "capability_check_failed"
	}
	for _, tool := range tools {
		if slices.Contains(caps.EnabledTools, tool) {
			return true, "ok"
		}
	}
	return false, "tool_revoked"
}

func logSurfaceRefusal(ctx context.Context, cfg *ProcessConfig, surface, name, reason string) {
	if cfg == nil || cfg.diagnostics == nil {
		return
	}
	cfg.diagnostics.WarnContext(ctx, "mcp read refused", "surface", surface, "name", name, "reason", reason)
}
