package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
)

const (
	promptSynthesizeAnswer = "synthesize_answer"
	uriSynthesisOutput     = "acr://contract/synthesis-output"
)

// synthesizePromptMeta is the _meta block of a synthesize_answer result.
func synthesizePromptMeta(system, serviceVersion string) mcpsdk.Meta {
	return mcpsdk.Meta{
		"prompt_version":       synthesisprompt.PromptVersion,
		"model_output_version": synthesisprompt.OutputVersion,
		"system_sha256":        sha256Hex(system),
		"service_version":      serviceVersion,
	}
}

// registerSynthesizePrompt serves the synthesis system message the server
// itself sends and the schema of the object that message asks for, both taken
// from synthesisprompt, the one assembly the runtime also sends. Both
// register only with investigate_question, so a credential that cannot
// investigate cannot read them.
func registerSynthesizePrompt(server *mcpsdk.Server, cfg *ProcessConfig, caller *CallerContext, serviceVersion string) {
	if !hostedToolEnabled(caller, toolInvestigateQuestion) {
		return
	}
	meta := synthesizePromptMeta(synthesisprompt.System(), serviceVersion)
	server.AddPrompt(&mcpsdk.Prompt{
		Name:  promptSynthesizeAnswer,
		Title: "Synthesize an answer",
		Description: "The synthesis system message acr runs on the facts read for a question, byte for byte. " +
			"Run it as the system message with the synthesis_input.input of an investigation that asked synthesis client as the user message. It asks for a structured answer object; the schema of that object is the resource " + uriSynthesisOutput + ". Versions: prompt " +
			metaString(meta, "prompt_version") + ", model output " + metaString(meta, "model_output_version") +
			", system sha256 " + metaString(meta, "system_sha256") + ", service " + serviceVersion + ".",
		Meta: meta,
	}, func(ctx context.Context, _ *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
		if ok, reason := liveToolEnabled(ctx, caller, toolInvestigateQuestion); !ok {
			logSurfaceRefusal(ctx, cfg, "prompt", promptSynthesizeAnswer, reason)
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown prompt"}
		}
		system := synthesisprompt.System()
		return &mcpsdk.GetPromptResult{
			Description: "Synthesis prompt " + synthesisprompt.PromptVersion,
			Meta:        synthesizePromptMeta(system, serviceVersion),
			Messages:    []*mcpsdk.PromptMessage{{Role: "user", Content: &mcpsdk.TextContent{Text: system}}},
		}, nil
	})
	addStaticResource(server, cfg, caller, toolInvestigateQuestion, uriSynthesisOutput, "synthesis-output", "Synthesis output schema",
		"JSON schema of the object the synthesis prompt returns. Version "+synthesisprompt.OutputVersion+".",
		"application/schema+json", synthesisprompt.OutputSchema(), synthesisprompt.PromptVersion, serviceVersion)
}
